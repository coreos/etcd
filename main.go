package main

import (
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/coreos/etcd/etcdserver"
	"github.com/coreos/etcd/etcdserver/etcdhttp"
	"github.com/coreos/etcd/pkg"
	"github.com/coreos/etcd/pkg/transport"
	"github.com/coreos/etcd/proxy"
	"github.com/coreos/etcd/raft"
	"github.com/coreos/etcd/snap"
	"github.com/coreos/etcd/store"
	"github.com/coreos/etcd/wal"
)

const (
	// the owner can make/remove files inside the directory
	privateDirMode = 0700

	proxyFlagValueOff      = "off"
	proxyFlagValueReadonly = "readonly"
	proxyFlagValueOn       = "on"

	reconfigFlagValueOff    = "off"
	reconfigFlagValueAdd    = "add"
	reconfigFlagValueRemove = "remove"

	version = "0.5.0-alpha"
)

var (
	fid          = flag.String("id", "0x1", "ID of this server")
	timeout      = flag.Duration("timeout", 10*time.Second, "Request Timeout")
	paddr        = flag.String("peer-bind-addr", ":7001", "Peer service address (e.g., ':7001')")
	dir          = flag.String("data-dir", "", "Path to the data directory")
	snapCount    = flag.Int64("snapshot-count", etcdserver.DefaultSnapCount, "Number of committed transactions to trigger a snapshot")
	printVersion = flag.Bool("version", false, "Print the version and exit")

	peers        = &etcdhttp.Peers{}
	addrs        = &Addrs{}
	cors         = &pkg.CORSInfo{}
	proxyFlag    = new(ProxyFlag)
	reconfigFlag = new(ReconfigFlag)

	proxyFlagValues = []string{
		proxyFlagValueOff,
		proxyFlagValueReadonly,
		proxyFlagValueOn,
	}
	reconfigFlagValues = []string{
		reconfigFlagValueOff,
		reconfigFlagValueAdd,
		reconfigFlagValueRemove,
	}

	clientTLSInfo = transport.TLSInfo{}
	peerTLSInfo   = transport.TLSInfo{}

	deprecated = []string{
		"addr",
		"cluster-active-size",
		"cluster-remove-delay",
		"cluster-sync-interval",
		"config",
		"force",
		"max-result-buffer",
		"max-retry-attempts",
		"peer-addr",
		"peer-heartbeat-interval",
		"peer-election-timeout",
		"peers-file",
		"retry-interval",
		"snapshot",
		"v",
		"vv",
	}
)

func init() {
	flag.Var(peers, "peers", "your peers")
	flag.Var(addrs, "bind-addr", "List of HTTP service addresses (e.g., '127.0.0.1:4001,10.0.0.1:8080')")
	flag.Var(cors, "cors", "Comma-separated white list of origins for CORS (cross-origin resource sharing).")
	flag.Var(proxyFlag, "proxy", fmt.Sprintf("Valid values include %s", strings.Join(proxyFlagValues, ", ")))
	flag.Var(reconfigFlag, "reconfig", fmt.Sprintf("Valid values include %s", strings.Join(reconfigFlagValues, ", ")))
	peers.Set("0x1=localhost:8080")
	addrs.Set("127.0.0.1:4001")
	proxyFlag.Set(proxyFlagValueOff)
	reconfigFlag.Set(reconfigFlagValueOff)

	flag.StringVar(&clientTLSInfo.CAFile, "ca-file", "", "Path to the client server TLS CA file.")
	flag.StringVar(&clientTLSInfo.CertFile, "cert-file", "", "Path to the client server TLS cert file.")
	flag.StringVar(&clientTLSInfo.KeyFile, "key-file", "", "Path to the client server TLS key file.")

	flag.StringVar(&peerTLSInfo.CAFile, "peer-ca-file", "", "Path to the peer server TLS CA file.")
	flag.StringVar(&peerTLSInfo.CertFile, "peer-cert-file", "", "Path to the peer server TLS cert file.")
	flag.StringVar(&peerTLSInfo.KeyFile, "peer-key-file", "", "Path to the peer server TLS key file.")

	for _, f := range deprecated {
		flag.Var(&pkg.DeprecatedFlag{f}, f, "")
	}
}

func main() {
	flag.Usage = pkg.UsageWithIgnoredFlagsFunc(flag.CommandLine, deprecated)
	flag.Parse()

	if *printVersion {
		fmt.Println("etcd version", version)
		os.Exit(0)
	}

	pkg.SetFlagsFromEnv(flag.CommandLine)

	if string(*proxyFlag) == proxyFlagValueOff {
		if reconfigFlag.String() == reconfigFlagValueRemove {
			remove()
			return
		}
		startEtcd()
	} else {
		startProxy()
	}

	// Block indefinitely
	<-make(chan struct{})
}

func getID() int64 {
	id, err := strconv.ParseInt(*fid, 0, 64)
	if err != nil {
		log.Fatal(err)
	}
	if id == raft.None {
		log.Fatalf("etcd: cannot use None(%d) as etcdserver id", raft.None)
	}

	return id
}

// startEtcd launches the etcd server and HTTP handlers for client/server communication.
func startEtcd() {
	id := getID()

	if peers.Pick(id) == "" {
		log.Fatalf("%#x=<addr> must be specified in peers", id)
	}

	if *snapCount <= 0 {
		log.Fatalf("etcd: snapshot-count must be greater than 0: snapshot-count=%d", *snapCount)
	}

	if *dir == "" {
		*dir = fmt.Sprintf("%v_etcd_data", *fid)
		log.Printf("main: no data-dir is given, using default data-dir ./%s", *dir)
	}
	if err := os.MkdirAll(*dir, privateDirMode); err != nil {
		log.Fatalf("main: cannot create data directory: %v", err)
	}
	snapdir := path.Join(*dir, "snap")
	if err := os.MkdirAll(snapdir, privateDirMode); err != nil {
		log.Fatalf("etcd: cannot create snapshot directory: %v", err)
	}
	snapshotter := snap.New(snapdir)

	waldir := path.Join(*dir, "wal")
	var w *wal.WAL
	var n raft.Node
	st := store.New()

	var err error
	if !wal.Exist(waldir) {
		w, err = wal.Create(waldir)
		if err != nil {
			log.Fatal(err)
		}
		if reconfigFlag.String() == reconfigFlagValueAdd {
			add()
			// TODO: set peers to empty when bootstrap info could be recovered
			// from log
		}
		n = raft.StartNode(id, peers.IDs(), 10, 1)
	} else {
		var index int64
		snapshot, err := snapshotter.Load()
		if err != nil && err != snap.ErrNoSnapshot {
			log.Fatal(err)
		}
		if snapshot != nil {
			log.Printf("etcd: restart from snapshot at index %d", snapshot.Index)
			st.Recovery(snapshot.Data)
			index = snapshot.Index
		}

		// restart a node from previous wal
		if w, err = wal.OpenAtIndex(waldir, index); err != nil {
			log.Fatal(err)
		}
		wid, st, ents, err := w.ReadAll()
		if err != nil {
			log.Fatal(err)
		}
		// TODO(xiangli): save/recovery nodeID?
		if wid != 0 {
			log.Fatalf("unexpected nodeid %d: nodeid should always be zero until we save nodeid into wal", wid)
		}
		n = raft.RestartNode(id, peers.IDs(), 10, 1, snapshot, st, ents)
	}

	pt, err := transport.NewTransport(peerTLSInfo)
	if err != nil {
		log.Fatal(err)
	}

	s := &etcdserver.EtcdServer{
		Store: st,
		Node:  n,
		Storage: struct {
			*wal.WAL
			*snap.Snapshotter
		}{w, snapshotter},
		Send:       etcdhttp.Sender(pt, *peers),
		Ticker:     time.Tick(100 * time.Millisecond),
		SyncTicker: time.Tick(500 * time.Millisecond),
		SnapCount:  *snapCount,
		Peers:      *peers,
	}
	s.Start()

	ch := &pkg.CORSHandler{
		Handler: etcdhttp.NewClientHandler(s, *peers, *timeout),
		Info:    cors,
	}
	ph := etcdhttp.NewPeerHandler(s)

	l, err := transport.NewListener(*paddr, peerTLSInfo)
	if err != nil {
		log.Fatal(err)
	}

	// Start the peer server in a goroutine
	go func() {
		log.Print("Listening for peers on ", *paddr)
		log.Fatal(http.Serve(l, ph))
	}()

	// Start a client server goroutine for each listen address
	for _, addr := range *addrs {
		addr := addr
		l, err := transport.NewListener(addr, clientTLSInfo)
		if err != nil {
			log.Fatal(err)
		}

		go func() {
			log.Print("Listening for client requests on ", addr)
			log.Fatal(http.Serve(l, ch))
		}()
	}
}

// startProxy launches an HTTP proxy for client communication which proxies to other etcd nodes.
func startProxy() {
	pt, err := transport.NewTransport(clientTLSInfo)
	if err != nil {
		log.Fatal(err)
	}

	ph, err := proxy.NewHandler(pt, (*peers).Addrs())
	if err != nil {
		log.Fatal(err)
	}

	ph = &pkg.CORSHandler{
		Handler: ph,
		Info:    cors,
	}

	if string(*proxyFlag) == proxyFlagValueReadonly {
		ph = proxy.NewReadonlyHandler(ph)
	}

	// Start a proxy server goroutine for each listen address
	for _, addr := range *addrs {
		addr := addr
		l, err := transport.NewListener(addr, clientTLSInfo)
		if err != nil {
			log.Fatal(err)
		}

		go func() {
			log.Print("Listening for client requests on ", addr)
			log.Fatal(http.Serve(l, ph))
		}()
	}
}

// Addrs implements the flag.Value interface to allow users to define multiple
// listen addresses on the command-line
type Addrs []string

// Set parses a command line set of listen addresses, formatted like:
// 127.0.0.1:7001,10.1.1.2:80
func (as *Addrs) Set(s string) error {
	parsed := make([]string, 0)
	for _, in := range strings.Split(s, ",") {
		a := strings.TrimSpace(in)
		if err := validateAddr(a); err != nil {
			return err
		}
		parsed = append(parsed, a)
	}
	if len(parsed) == 0 {
		return errors.New("no valid addresses given!")
	}
	*as = parsed
	return nil
}

func (as *Addrs) String() string {
	return strings.Join(*as, ",")
}

// validateAddr ensures that the provided string is a valid address. Valid
// addresses are of the form IP:port.
// Returns an error if the address is invalid, else nil.
func validateAddr(s string) error {
	parts := strings.SplitN(s, ":", 2)
	if len(parts) != 2 {
		return errors.New("bad format in address specification")
	}
	if net.ParseIP(parts[0]) == nil {
		return errors.New("bad IP in address specification")
	}
	if _, err := strconv.Atoi(parts[1]); err != nil {
		return errors.New("bad port in address specification")
	}
	return nil
}

// ProxyFlag implements the flag.Value interface.
type ProxyFlag string

// Set verifies the argument to be a valid member of proxyFlagValues
// before setting the underlying flag value.
func (pf *ProxyFlag) Set(s string) error {
	for _, v := range proxyFlagValues {
		if s == v {
			*pf = ProxyFlag(s)
			return nil
		}
	}

	return errors.New("invalid value")
}

func (pf *ProxyFlag) String() string {
	return string(*pf)
}

// ReconfigFlag implements the flag.Value interface.
type ReconfigFlag string

func (rf *ReconfigFlag) Set(s string) error {
	for _, v := range reconfigFlagValues {
		if s == v {
			*rf = ReconfigFlag(s)
			return nil
		}
	}
	return errors.New("invalid value")
}

func (rf *ReconfigFlag) String() string {
	return string(*rf)
}

func add() {
	id := getID()
	f := func(endpoint string) error {
		if err := etcdhttp.SendAddMemberRequest(endpoint, id, (*peers)[id]); err != nil {
			return fmt.Errorf("main: add to %s error: %v", endpoint, err)
		}
		return nil
	}
	if err := endpointTraversal(peers.Endpoints(), f, 5, 0.5); err != nil {
		log.Fatalf("main: endpoint traversal error: %v", err)
	}
}

func remove() {
	id := getID()
	f := func(endpoint string) error {
		if err := etcdhttp.SendRemoveMemberRequest(endpoint, id); err != nil {
			return fmt.Errorf("main: remove to %s error: %v", endpoint, err)
		}
		return nil
	}
	if err := endpointTraversal(peers.Endpoints(), f, 5, 0.5); err != nil {
		log.Fatalf("main: endpoint traversal error: %v", err)
	}
}

func endpointTraversal(endpoints []string, f func(endpoint string) error, maxAttempt int, retryInterval float64) error {
	for attempt := 0; ; attempt++ {
		if attempt == maxAttempt {
			return fmt.Errorf("fail after %d attempts", maxAttempt)
		}
		for _, endpoint := range endpoints {
			if err := f(endpoint); err != nil {
				log.Printf("%v", err)
			} else {
				return nil
			}
		}
		time.Sleep(time.Millisecond * time.Duration(retryInterval*1000))
	}
}
