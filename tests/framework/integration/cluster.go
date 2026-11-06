// Copyright 2016 The etcd Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package integration

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/soheilhy/cmux"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest"
	"golang.org/x/crypto/bcrypt"
	"google.golang.org/grpc"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/protobuf/testing/protocmp"

	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	"go.etcd.io/etcd/client/pkg/v3/testutil"
	"go.etcd.io/etcd/client/pkg/v3/tlsutil"
	"go.etcd.io/etcd/client/pkg/v3/transport"
	"go.etcd.io/etcd/client/pkg/v3/types"
	clientv3 "go.etcd.io/etcd/client/v3"
	"go.etcd.io/etcd/pkg/v3/featuregate"
	"go.etcd.io/etcd/pkg/v3/grpctesting"
	"go.etcd.io/etcd/server/v3/config"
	"go.etcd.io/etcd/server/v3/embed"
	"go.etcd.io/etcd/server/v3/etcdserver"
	"go.etcd.io/etcd/server/v3/etcdserver/api/etcdhttp"
	"go.etcd.io/etcd/server/v3/etcdserver/api/membership"
	"go.etcd.io/etcd/server/v3/etcdserver/api/rafthttp"
	"go.etcd.io/etcd/server/v3/etcdserver/api/v3client"
	"go.etcd.io/etcd/server/v3/etcdserver/api/v3election"
	epb "go.etcd.io/etcd/server/v3/etcdserver/api/v3election/v3electionpb"
	"go.etcd.io/etcd/server/v3/etcdserver/api/v3lock"
	lockpb "go.etcd.io/etcd/server/v3/etcdserver/api/v3lock/v3lockpb"
	"go.etcd.io/etcd/server/v3/etcdserver/api/v3rpc"
	"go.etcd.io/etcd/server/v3/features"
	"go.etcd.io/etcd/server/v3/verify"
	framecfg "go.etcd.io/etcd/tests/v3/framework/config"
	"go.etcd.io/etcd/tests/v3/framework/testutils"
	"go.etcd.io/raft/v3"
)

const (
	// RequestWaitTimeout is the time duration to wait for a request to go through or detect leader loss.
	RequestWaitTimeout = 5 * time.Second
	RequestTimeout     = 20 * time.Second

	ClusterName  = "etcd"
	BasePort     = 21000
	URLScheme    = "unix"
	URLSchemeTLS = "unixs"
	BaseGRPCPort = 30000
)

var (
	ElectionTicks = 10

	// UniqueCount integration test is used to set unique member ids
	UniqueCount = int32(0)

	TestTLSInfo = transport.TLSInfo{
		KeyFile:        testutils.MustAbsPath("../fixtures/server.key.insecure"),
		CertFile:       testutils.MustAbsPath("../fixtures/server.crt"),
		TrustedCAFile:  testutils.MustAbsPath("../fixtures/ca.crt"),
		ClientCertAuth: true,
	}

	TestTLSInfoWithSpecificUsage = transport.TLSInfo{
		KeyFile:        testutils.MustAbsPath("../fixtures/server-serverusage.key.insecure"),
		CertFile:       testutils.MustAbsPath("../fixtures/server-serverusage.crt"),
		ClientKeyFile:  testutils.MustAbsPath("../fixtures/client-clientusage.key.insecure"),
		ClientCertFile: testutils.MustAbsPath("../fixtures/client-clientusage.crt"),
		TrustedCAFile:  testutils.MustAbsPath("../fixtures/ca.crt"),
		ClientCertAuth: true,
	}

	TestTLSInfoIP = transport.TLSInfo{
		KeyFile:        testutils.MustAbsPath("../fixtures/server-ip.key.insecure"),
		CertFile:       testutils.MustAbsPath("../fixtures/server-ip.crt"),
		TrustedCAFile:  testutils.MustAbsPath("../fixtures/ca.crt"),
		ClientCertAuth: true,
	}

	TestTLSInfoExpired = transport.TLSInfo{
		KeyFile:        testutils.MustAbsPath("./fixtures-expired/server.key.insecure"),
		CertFile:       testutils.MustAbsPath("./fixtures-expired/server.crt"),
		TrustedCAFile:  testutils.MustAbsPath("./fixtures-expired/ca.crt"),
		ClientCertAuth: true,
	}

	TestTLSInfoExpiredIP = transport.TLSInfo{
		KeyFile:        testutils.MustAbsPath("./fixtures-expired/server-ip.key.insecure"),
		CertFile:       testutils.MustAbsPath("./fixtures-expired/server-ip.crt"),
		TrustedCAFile:  testutils.MustAbsPath("./fixtures-expired/ca.crt"),
		ClientCertAuth: true,
	}

	DefaultTokenJWT = fmt.Sprintf("jwt,pub-key=%s,priv-key=%s,sign-method=RS256,ttl=2s",
		testutils.MustAbsPath("../fixtures/server.crt"), testutils.MustAbsPath("../fixtures/server.key.insecure"))

	// UniqueNumber is used to generate unique port numbers
	// Should only be accessed via atomic package methods.
	UniqueNumber int32
)

type ClusterConfig struct {
	Size      int
	PeerTLS   *transport.TLSInfo
	ClientTLS *transport.TLSInfo

	AuthToken string

	QuotaBackendBytes    int64
	BackendBatchInterval time.Duration

	MaxTxnOps       uint
	MaxRequestBytes uint

	SnapshotCount          uint64
	SnapshotCatchUpEntries uint64

	GRPCKeepAliveMinTime        time.Duration
	GRPCKeepAliveInterval       time.Duration
	GRPCKeepAliveTimeout        time.Duration
	GRPCAdditionalServerOptions []grpc.ServerOption

	ClientMaxCallSendMsgSize int
	ClientMaxCallRecvMsgSize int

	// UseIP is true to use only IP for gRPC requests.
	UseIP bool
	// UseBridge adds bridge between client and grpc server. Should be used in tests that
	// want to manipulate connection or require connection not breaking despite server stop/restart.
	UseBridge bool
	// UseTCP configures server listen on tcp socket. If disabled unix socket is used.
	UseTCP bool

	EnableLeaseCheckpoint   bool
	LeaseCheckpointInterval time.Duration
	LeaseCheckpointPersist  bool

	EnableAutoPromoteLearners bool

	WatchProgressNotifyInterval time.Duration
	MaxLearners                 int
	DisableStrictReconfigCheck  bool
	CorruptCheckTime            time.Duration
	Metrics                     string
}

type Cluster struct {
	Cfg           *ClusterConfig
	Members       []*Member
	LastMemberNum int

	mu sync.Mutex
}

func SchemeFromTLSInfo(tls *transport.TLSInfo) string {
	if tls == nil {
		return URLScheme
	}
	return URLSchemeTLS
}

// fillClusterForMembers fills up Member.InitialPeerURLsMap from each member's [name, scheme and PeerListeners address]
func (c *Cluster) fillClusterForMembers() error {
	addrs := make([]string, 0)
	for _, m := range c.Members {
		scheme := SchemeFromTLSInfo(m.PeerTLSInfo)
		for _, l := range m.PeerListeners {
			addrs = append(addrs, fmt.Sprintf("%s=%s://%s", m.Name, scheme, l.Addr().String()))
		}
	}
	clusterStr := strings.Join(addrs, ",")
	var err error
	for _, m := range c.Members {
		m.InitialPeerURLsMap, err = types.NewURLsMap(clusterStr)
		if err != nil {
			return err
		}
	}
	return nil
}

func (c *Cluster) Launch(t testutil.TB) {
	t.Logf("Launching new cluster...")
	errc := make(chan error)
	for _, m := range c.Members {
		// Members are launched in separate goroutines because if they boot
		// using discovery url, they have to wait for others to register to continue.
		go func(m *Member) {
			errc <- m.Launch()
		}(m)
	}
	for range c.Members {
		if err := <-errc; err != nil {
			c.Terminate(t)
			t.Fatalf("error setting up member: %v", err)
		}
	}
	// wait Cluster to be stable to receive future client requests
	c.WaitMembersMatch(t, c.ProtoMembers())
	c.waitVersion()
	for _, m := range c.Members {
		t.Logf(" - %v -> %v (%v)", m.Name, m.ID(), m.GRPCURL)
	}
}

// ProtoMembers returns a list of all active members as etcdserverpb.Member
func (c *Cluster) ProtoMembers() []*pb.Member {
	var ms []*pb.Member
	for _, m := range c.Members {
		pScheme := SchemeFromTLSInfo(m.PeerTLSInfo)
		cScheme := SchemeFromTLSInfo(m.ClientTLSInfo)
		cm := &pb.Member{Name: m.Name}
		for _, ln := range m.PeerListeners {
			cm.PeerURLs = append(cm.PeerURLs, pScheme+"://"+ln.Addr().String())
		}
		for _, ln := range m.ClientListeners {
			cm.ClientURLs = append(cm.ClientURLs, cScheme+"://"+ln.Addr().String())
		}
		ms = append(ms, cm)
	}
	return ms
}

func (c *Cluster) MustNewMember(t testutil.TB) *Member {
	memberNumber := c.LastMemberNum
	c.LastMemberNum++

	m := MustNewMember(t,
		MemberConfig{
			Name:                        fmt.Sprintf("m%v", memberNumber),
			MemberNumber:                memberNumber,
			AuthToken:                   c.Cfg.AuthToken,
			PeerTLS:                     c.Cfg.PeerTLS,
			ClientTLS:                   c.Cfg.ClientTLS,
			QuotaBackendBytes:           c.Cfg.QuotaBackendBytes,
			BackendBatchInterval:        c.Cfg.BackendBatchInterval,
			MaxTxnOps:                   c.Cfg.MaxTxnOps,
			MaxRequestBytes:             c.Cfg.MaxRequestBytes,
			SnapshotCount:               c.Cfg.SnapshotCount,
			SnapshotCatchUpEntries:      c.Cfg.SnapshotCatchUpEntries,
			GRPCKeepAliveMinTime:        c.Cfg.GRPCKeepAliveMinTime,
			GRPCKeepAliveInterval:       c.Cfg.GRPCKeepAliveInterval,
			GRPCKeepAliveTimeout:        c.Cfg.GRPCKeepAliveTimeout,
			GRPCAdditionalServerOptions: c.Cfg.GRPCAdditionalServerOptions,
			ClientMaxCallSendMsgSize:    c.Cfg.ClientMaxCallSendMsgSize,
			ClientMaxCallRecvMsgSize:    c.Cfg.ClientMaxCallRecvMsgSize,
			UseIP:                       c.Cfg.UseIP,
			UseBridge:                   c.Cfg.UseBridge,
			UseTCP:                      c.Cfg.UseTCP,
			EnableLeaseCheckpoint:       c.Cfg.EnableLeaseCheckpoint,
			LeaseCheckpointInterval:     c.Cfg.LeaseCheckpointInterval,
			LeaseCheckpointPersist:      c.Cfg.LeaseCheckpointPersist,
			EnableAutoPromoteLearners:   c.Cfg.EnableAutoPromoteLearners,
			WatchProgressNotifyInterval: c.Cfg.WatchProgressNotifyInterval,
			MaxLearners:                 c.Cfg.MaxLearners,
			DisableStrictReconfigCheck:  c.Cfg.DisableStrictReconfigCheck,
			CorruptCheckTime:            c.Cfg.CorruptCheckTime,
			Metrics:                     c.Cfg.Metrics,
		})
	return m
}

// addMember return PeerURLs of the added member.
func (c *Cluster) addMember(t testutil.TB) types.URLs {
	m := c.MustNewMember(t)

	scheme := SchemeFromTLSInfo(c.Cfg.PeerTLS)

	// send add request to the Cluster
	var err error
	for i := 0; i < len(c.Members); i++ {
		peerURL := scheme + "://" + m.PeerListeners[0].Addr().String()
		if err = c.AddMemberByURL(t, c.Members[i].Client, peerURL); err == nil {
			break
		}
	}
	if err != nil {
		t.Fatalf("add member failed on all members error: %v", err)
	}

	m.InitialPeerURLsMap = types.URLsMap{}
	for _, mm := range c.Members {
		m.InitialPeerURLsMap[mm.Name] = mm.PeerURLs
	}
	m.InitialPeerURLsMap[m.Name] = m.PeerURLs
	m.NewCluster = false
	if err := m.Launch(); err != nil {
		t.Fatal(err)
	}
	c.Members = append(c.Members, m)
	// wait Cluster to be stable to receive future client requests
	c.WaitMembersMatch(t, c.ProtoMembers())
	return m.PeerURLs
}

func (c *Cluster) AddMemberByURL(t testutil.TB, cc *clientv3.Client, peerURL string) error {
	ctx, cancel := context.WithTimeout(context.Background(), RequestTimeout)
	_, err := cc.MemberAdd(ctx, []string{peerURL})
	cancel()
	if err != nil {
		return err
	}

	// wait for the add node entry applied in the Cluster
	members := append(c.ProtoMembers(), &pb.Member{PeerURLs: []string{peerURL}, ClientURLs: []string{}})
	c.WaitMembersMatch(t, members)
	return nil
}

// AddMember return PeerURLs of the added member.
func (c *Cluster) AddMember(t testutil.TB) types.URLs {
	return c.addMember(t)
}

func (c *Cluster) RemoveMember(t testutil.TB, cc *clientv3.Client, id uint64) error {
	// send remove request to the Cluster

	ctx, cancel := context.WithTimeout(context.Background(), RequestTimeout)
	_, err := cc.MemberRemove(ctx, id)
	cancel()
	if err != nil {
		return err
	}
	newMembers := make([]*Member, 0)
	for _, m := range c.Members {
		if uint64(m.Server.MemberID()) != id {
			newMembers = append(newMembers, m)
		} else {
			m.Client.Close()
			select {
			case <-m.Server.StopNotify():
				m.Terminate(t)
			// 1s stop delay + election timeout + 1s disk and network delay + connection write timeout
			// TODO: remove connection write timeout by selecting on http response closeNotifier
			// blocking on https://github.com/golang/go/issues/9524
			case <-time.After(time.Second + time.Duration(ElectionTicks)*framecfg.TickDuration + time.Second + rafthttp.ConnWriteTimeout):
				t.Fatalf("failed to remove member %s in time", m.Server.MemberID())
			}
		}
	}

	c.Members = newMembers
	c.WaitMembersMatch(t, c.ProtoMembers())
	return nil
}

func (c *Cluster) WaitMembersMatch(t testutil.TB, membs []*pb.Member) {
	ctx, cancel := context.WithTimeout(context.Background(), RequestTimeout)
	defer cancel()
	for _, m := range c.Members {
		cc := ToGRPC(m.Client)
		select {
		case <-m.Server.StopNotify():
			continue
		default:
		}
		for {
			select {
			case <-ctx.Done():
				t.Fatalf("WaitMembersMatch failed: %v", ctx.Err())
			default:
			}

			resp, err := cc.Cluster.MemberList(ctx, &pb.MemberListRequest{Linearizable: false})
			if err != nil {
				continue
			}
			if isMembersEqual(resp.Members, membs) {
				break
			}
			time.Sleep(framecfg.TickDuration)
		}
	}
}

// WaitLeader returns index of the member in c.Members that is leader
// or fails the test (if not established in 30s).
func (c *Cluster) WaitLeader(tb testing.TB) int {
	return c.WaitMembersForLeader(tb, c.Members)
}

// WaitMembersForLeader waits until given members agree on the same leader,
// and returns its 'index' in the 'membs' list
func (c *Cluster) WaitMembersForLeader(tb testing.TB, membs []*Member) int {
	tb.Logf("WaitMembersForLeader")
	ctx, cancel := context.WithTimeout(tb.Context(), 30*time.Second)
	defer cancel()
	l := 0
	for l = c.waitMembersForLeader(ctx, tb, membs); l < 0; {
		if ctx.Err() != nil {
			tb.Fatalf("WaitLeader FAILED: %v", ctx.Err())
		}
	}
	tb.Logf("WaitMembersForLeader succeeded. Cluster leader index: %v", l)

	// TODO: Consider second pass check as sometimes leadership is lost
	// soon after election:
	//
	// We perform multiple attempts, as some-times just after successful WaitLLeader
	// there is a race and leadership is quickly lost:
	//   - MsgAppResp message with higher term from 2acc3d3b521981 [term: 3]	{"member": "m0"}
	//   - 9903a56eaf96afac became follower at term 3	{"member": "m0"}
	//   - 9903a56eaf96afac lost leader 9903a56eaf96afac at term 3	{"member": "m0"}

	return l
}

// WaitMembersForLeader waits until given members agree on the same leader,
// and returns its 'index' in the 'membs' list
func (c *Cluster) waitMembersForLeader(ctx context.Context, tb testing.TB, membs []*Member) int {
	possibleLead := make(map[uint64]bool)
	var lead uint64
	for _, m := range membs {
		possibleLead[uint64(m.Server.MemberID())] = true
	}
	cc, err := c.ClusterClient(tb)
	if err != nil {
		tb.Fatal(err)
	}
	// ensure leader is up via linearizable get
	for {
		fctx, fcancel := context.WithTimeout(ctx, 10*framecfg.TickDuration+time.Second)
		_, err := cc.Get(fctx, "0")
		fcancel()
		if err == nil || strings.Contains(err.Error(), "Key not found") {
			break
		}
	}

	for lead == 0 || !possibleLead[lead] {
		lead = 0
		for _, m := range membs {
			select {
			case <-m.Server.StopNotify():
				continue
			default:
			}
			if lead != 0 && lead != m.Server.Lead() {
				lead = 0
				time.Sleep(10 * framecfg.TickDuration)
				break
			}
			lead = m.Server.Lead()
		}
	}

	for i, m := range membs {
		if uint64(m.Server.MemberID()) == lead {
			tb.Logf("waitMembersForLeader found leader. Member: %v lead: %x", i, lead)
			return i
		}
	}

	tb.Logf("waitMembersForLeader failed (-1)")
	return -1
}

func (c *Cluster) WaitNoLeader() { c.WaitMembersNoLeader(c.Members) }

// WaitMembersNoLeader waits until given members lose leader.
func (c *Cluster) WaitMembersNoLeader(membs []*Member) {
	noLeader := false
	for !noLeader {
		noLeader = true
		for _, m := range membs {
			select {
			case <-m.Server.StopNotify():
				continue
			default:
			}
			if m.Server.Lead() != 0 {
				noLeader = false
				time.Sleep(10 * framecfg.TickDuration)
				break
			}
		}
	}
}

func (c *Cluster) waitVersion() {
	for _, m := range c.Members {
		for {
			if m.Server.ClusterVersion() != nil {
				break
			}
			time.Sleep(framecfg.TickDuration)
		}
	}
}

// isMembersEqual checks whether two members equal except ID field.
// The given wmembs should always set ID field to empty string.
func isMembersEqual(membs []*pb.Member, wmembs []*pb.Member) bool {
	sort.Sort(SortableMemberSliceByPeerURLs(membs))
	sort.Sort(SortableMemberSliceByPeerURLs(wmembs))
	return cmp.Equal(membs, wmembs,
		protocmp.Transform(),
		protocmp.IgnoreFields(&pb.Member{}, "ID"))
}

func NewLocalListener(t testutil.TB) net.Listener {
	c := atomic.AddInt32(&UniqueCount, 1)
	// Go 1.8+ allows only numbers in port
	addr := fmt.Sprintf("127.0.0.1:%05d%05d", c+BasePort, os.Getpid())
	return NewListenerWithAddr(t, addr)
}

func NewListenerWithAddr(t testutil.TB, addr string) net.Listener {
	t.Logf("Creating listener with addr: %v", addr)
	l, err := transport.NewUnixListener(addr)
	if err != nil {
		t.Fatal(err)
	}
	return l
}

type Member struct {
	config.ServerConfig
	UniqNumber                     int
	MemberNumber                   int
	Port                           string
	PeerListeners, ClientListeners []net.Listener
	GRPCListener                   net.Listener
	// PeerTLSInfo enables peer TLS when set
	PeerTLSInfo *transport.TLSInfo
	// ClientTLSInfo enables client TLS when set
	ClientTLSInfo *transport.TLSInfo
	DialOptions   []grpc.DialOption

	RaftHandler   *testutil.PauseableHandler
	Server        *etcdserver.EtcdServer
	ServerClosers []func()

	GRPCServerOpts []grpc.ServerOption
	GRPCServer     *grpc.Server
	GRPCURL        string
	GRPCBridge     Bridge

	// ServerClient is a clientv3 that directly calls the etcdserver.
	ServerClient *clientv3.Client
	// Client is a clientv3 that communicates via socket, either UNIX or TCP.
	Client *clientv3.Client

	KeepDataDirTerminate     bool
	ClientMaxCallSendMsgSize int
	ClientMaxCallRecvMsgSize int
	UseIP                    bool
	UseBridge                bool
	UseTCP                   bool

	IsLearner bool
	Closed    bool

	GRPCServerRecorder *grpctesting.GRPCRecorder

	LogObserver *testutils.LogObserver
}

type MemberConfig struct {
	Name                        string
	UniqNumber                  int64
	MemberNumber                int
	PeerTLS                     *transport.TLSInfo
	ClientTLS                   *transport.TLSInfo
	AuthToken                   string
	QuotaBackendBytes           int64
	BackendBatchInterval        time.Duration
	MaxTxnOps                   uint
	MaxRequestBytes             uint
	SnapshotCount               uint64
	SnapshotCatchUpEntries      uint64
	GRPCKeepAliveMinTime        time.Duration
	GRPCKeepAliveInterval       time.Duration
	GRPCKeepAliveTimeout        time.Duration
	GRPCAdditionalServerOptions []grpc.ServerOption
	ClientMaxCallSendMsgSize    int
	ClientMaxCallRecvMsgSize    int
	UseIP                       bool
	UseBridge                   bool
	UseTCP                      bool
	EnableLeaseCheckpoint       bool
	LeaseCheckpointInterval     time.Duration
	LeaseCheckpointPersist      bool
	EnableAutoPromoteLearners   bool
	WatchProgressNotifyInterval time.Duration
	MaxLearners                 int
	DisableStrictReconfigCheck  bool
	CorruptCheckTime            time.Duration
	Metrics                     string
}

// MustNewMember return an inited member with the given name. If peerTLS is
// set, it will use https scheme to communicate between peers.
func MustNewMember(t testutil.TB, mcfg MemberConfig) *Member {
	var err error
	m := &Member{
		MemberNumber: mcfg.MemberNumber,
		UniqNumber:   int(atomic.AddInt32(&UniqueCount, 1)),
	}

	peerScheme := SchemeFromTLSInfo(mcfg.PeerTLS)
	clientScheme := SchemeFromTLSInfo(mcfg.ClientTLS)

	pln := NewLocalListener(t)
	m.PeerListeners = []net.Listener{pln}
	m.PeerURLs, err = types.NewURLs([]string{peerScheme + "://" + pln.Addr().String()})
	if err != nil {
		t.Fatal(err)
	}
	m.PeerTLSInfo = mcfg.PeerTLS

	cln := NewLocalListener(t)
	m.ClientListeners = []net.Listener{cln}
	m.ClientURLs, err = types.NewURLs([]string{clientScheme + "://" + cln.Addr().String()})
	if err != nil {
		t.Fatal(err)
	}
	m.ClientTLSInfo = mcfg.ClientTLS

	m.Name = mcfg.Name

	m.DataDir, err = os.MkdirTemp(t.TempDir(), "etcd")
	if err != nil {
		t.Fatal(err)
	}
	clusterStr := fmt.Sprintf("%s=%s://%s", mcfg.Name, peerScheme, pln.Addr().String())
	m.InitialPeerURLsMap, err = types.NewURLsMap(clusterStr)
	if err != nil {
		t.Fatal(err)
	}
	m.InitialClusterToken = ClusterName
	m.NewCluster = true
	m.BootstrapTimeout = 10 * time.Millisecond
	if m.PeerTLSInfo != nil {
		m.ServerConfig.PeerTLSInfo = *m.PeerTLSInfo
	}
	m.ElectionTicks = ElectionTicks
	m.InitialElectionTickAdvance = true
	m.TickMs = uint(framecfg.TickDuration / time.Millisecond)
	m.PreVote = true
	m.QuotaBackendBytes = mcfg.QuotaBackendBytes
	m.BackendBatchInterval = mcfg.BackendBatchInterval
	m.MaxTxnOps = mcfg.MaxTxnOps
	if m.MaxTxnOps == 0 {
		m.MaxTxnOps = embed.DefaultMaxTxnOps
	}
	m.MaxRequestBytes = mcfg.MaxRequestBytes
	if m.MaxRequestBytes == 0 {
		m.MaxRequestBytes = embed.DefaultMaxRequestBytes
	}
	m.SnapshotCount = etcdserver.DefaultSnapshotCount
	if mcfg.SnapshotCount != 0 {
		m.SnapshotCount = mcfg.SnapshotCount
	}
	m.SnapshotCatchUpEntries = etcdserver.DefaultSnapshotCatchUpEntries
	if mcfg.SnapshotCatchUpEntries != 0 {
		m.SnapshotCatchUpEntries = mcfg.SnapshotCatchUpEntries
	}

	// for the purpose of integration testing, simple token is enough
	m.AuthToken = "simple"
	if mcfg.AuthToken != "" {
		m.AuthToken = mcfg.AuthToken
	}

	m.BcryptCost = uint(bcrypt.MinCost) // use min bcrypt cost to speedy up integration testing

	m.GRPCServerOpts = []grpc.ServerOption{}
	if mcfg.GRPCKeepAliveMinTime > time.Duration(0) {
		m.GRPCServerOpts = append(m.GRPCServerOpts, grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{
			MinTime:             mcfg.GRPCKeepAliveMinTime,
			PermitWithoutStream: false,
		}))
	}
	if mcfg.GRPCKeepAliveInterval > time.Duration(0) &&
		mcfg.GRPCKeepAliveTimeout > time.Duration(0) {
		m.GRPCServerOpts = append(m.GRPCServerOpts, grpc.KeepaliveParams(keepalive.ServerParameters{
			Time:    mcfg.GRPCKeepAliveInterval,
			Timeout: mcfg.GRPCKeepAliveTimeout,
		}))
	}
	m.GRPCServerOpts = append(m.GRPCServerOpts, mcfg.GRPCAdditionalServerOptions...)
	m.ClientMaxCallSendMsgSize = mcfg.ClientMaxCallSendMsgSize
	m.ClientMaxCallRecvMsgSize = mcfg.ClientMaxCallRecvMsgSize
	m.UseIP = mcfg.UseIP
	m.UseBridge = mcfg.UseBridge
	m.UseTCP = mcfg.UseTCP
	m.LeaseCheckpointInterval = mcfg.LeaseCheckpointInterval

	m.WatchProgressNotifyInterval = mcfg.WatchProgressNotifyInterval

	m.InitialCorruptCheck = true
	if mcfg.CorruptCheckTime > time.Duration(0) {
		m.CorruptCheckTime = mcfg.CorruptCheckTime
	}
	m.WarningApplyDuration = embed.DefaultWarningApplyDuration
	m.WarningUnaryRequestDuration = embed.DefaultWarningUnaryRequestDuration
	m.MaxLearners = membership.DefaultMaxLearners
	if mcfg.MaxLearners != 0 {
		m.MaxLearners = mcfg.MaxLearners
	}
	m.Metrics = mcfg.Metrics
	m.GRPCServerRecorder = &grpctesting.GRPCRecorder{}

	m.Logger, m.LogObserver = memberLogger(t, mcfg.Name)
	m.ServerFeatureGate = features.NewDefaultServerFeatureGate(m.Name, m.Logger)
	featureGates := fmt.Sprintf("LeaseCheckpoint=%v,LeaseCheckpointPersist=%v,AutoPromoteLearners=%v", mcfg.EnableLeaseCheckpoint, mcfg.LeaseCheckpointPersist, mcfg.EnableAutoPromoteLearners)
	if err := m.ServerFeatureGate.(featuregate.MutableFeatureGate).Set(featureGates); err != nil {
		t.Fatalf("Set FeatureGate FAILED: %v", err)
	}

	m.StrictReconfigCheck = !mcfg.DisableStrictReconfigCheck
	if err := m.listenGRPC(); err != nil {
		t.Fatalf("listenGRPC FAILED: %v", err)
	}
	t.Cleanup(func() {
		// if we didn't cleanup the logger, the consecutive test
		// might reuse this (t).
		raft.ResetDefaultLogger()
	})
	return m
}

func memberLogger(t testutil.TB, name string) (*zap.Logger, *testutils.LogObserver) {
	level := zapcore.InfoLevel
	if os.Getenv("CLUSTER_DEBUG") != "" {
		level = zapcore.DebugLevel
	}

	obCore, logOb := testutils.NewLogObserver(level)

	options := zaptest.WrapOptions(
		zap.Fields(zap.String("member", name)),

		// copy logged entities to log observer
		zap.WrapCore(func(oldCore zapcore.Core) zapcore.Core {
			return zapcore.NewTee(oldCore, obCore)
		}),
	)
	return zaptest.NewLogger(t, zaptest.Level(level), options).Named(name), logOb
}

// listenGRPC starts a grpc server over a unix domain socket on the member
func (m *Member) listenGRPC() error {
	// prefix with localhost so cert has right domain
	network, host, port := m.grpcAddr()
	grpcAddr := net.JoinHostPort(host, port)
	wd, err := os.Getwd()
	if err != nil {
		return err
	}
	m.Logger.Info("LISTEN GRPC", zap.String("grpcAddr", grpcAddr), zap.String("m.Name", m.Name), zap.String("workdir", wd))
	grpcListener, err := net.Listen(network, grpcAddr)
	if err != nil {
		return fmt.Errorf("listen failed on grpc socket %s (%w)", grpcAddr, err)
	}

	addr := grpcListener.Addr().String()
	_, port, err = net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("failed to parse grpc listen port from address %s (%w)", addr, err)
	}
	m.Port = port
	m.GRPCURL = fmt.Sprintf("%s://%s", m.clientScheme(), addr)
	m.Logger.Info("LISTEN GRPC SUCCESS", zap.String("grpcAddr", m.GRPCURL), zap.String("m.Name", m.Name),
		zap.String("workdir", wd), zap.String("port", m.Port))

	if m.UseBridge {
		_, err = m.addBridge()
		if err != nil {
			grpcListener.Close()
			return err
		}
	}
	m.GRPCListener = grpcListener
	return nil
}

func (m *Member) clientScheme() string {
	switch {
	case m.UseTCP && m.ClientTLSInfo != nil:
		return "https"
	case m.UseTCP && m.ClientTLSInfo == nil:
		return "http"
	case !m.UseTCP && m.ClientTLSInfo != nil:
		return "unixs"
	case !m.UseTCP && m.ClientTLSInfo == nil:
		return "unix"
	}
	m.Logger.Panic("Failed to determine client schema")
	return ""
}

func (m *Member) addBridge() (Bridge, error) {
	network, host, port := m.grpcAddr()
	grpcAddr := net.JoinHostPort(host, m.Port)
	bridgePort := fmt.Sprintf("%s%s", port, "0")
	if m.UseTCP {
		bridgePort = "0"
	}
	bridgeAddr := net.JoinHostPort(host, bridgePort)
	m.Logger.Info("LISTEN BRIDGE", zap.String("grpc-address", bridgeAddr), zap.String("member", m.Name))
	bridgeListener, err := transport.NewUnixListener(bridgeAddr)
	if err != nil {
		return nil, fmt.Errorf("listen failed on bridge socket %s (%w)", bridgeAddr, err)
	}
	m.GRPCBridge = newBridge(dialer{network: network, addr: grpcAddr}, bridgeListener)

	addr := bridgeListener.Addr().String()
	m.Logger.Info("LISTEN BRIDGE SUCCESS", zap.String("grpc-address", addr), zap.String("member", m.Name))
	m.GRPCURL = m.clientScheme() + "://" + addr
	return m.GRPCBridge, nil
}

func (m *Member) Bridge() Bridge {
	if !m.UseBridge {
		m.Logger.Panic("Bridge not available. Please configure using bridge before creating Cluster.")
	}
	return m.GRPCBridge
}

func (m *Member) grpcAddr() (network, host, port string) {
	// prefix with localhost so cert has right domain
	host = "localhost"
	if m.UseIP { // for IP-only TLS certs
		host = "127.0.0.1"
	}
	network = "unix"
	if m.UseTCP {
		network = "tcp"
	}

	if m.Port != "" {
		return network, host, m.Port
	}

	port = m.Name
	if m.UseTCP {
		// let net.Listen choose the port automatically
		port = fmt.Sprintf("%d", 0)
	}
	return network, host, port
}

func (m *Member) GRPCPortNumber() string {
	return m.Port
}

type dialer struct {
	network string
	addr    string
}

func (d dialer) Dial() (net.Conn, error) {
	return net.Dial(d.network, d.addr)
}

func (m *Member) ElectionTimeout() time.Duration {
	return time.Duration(m.Server.Cfg.ElectionTicks*int(m.Server.Cfg.TickMs)) * time.Millisecond
}

func (m *Member) ID() types.ID { return m.Server.MemberID() }

// NewClientV3 creates a new grpc client connection to the member
func NewClientV3(m *Member) (*clientv3.Client, error) {
	if m.GRPCURL == "" {
		return nil, fmt.Errorf("member not configured for grpc")
	}

	cfg := clientv3.Config{
		Endpoints:          []string{m.GRPCURL},
		DialTimeout:        5 * time.Second,
		MaxCallSendMsgSize: m.ClientMaxCallSendMsgSize,
		MaxCallRecvMsgSize: m.ClientMaxCallRecvMsgSize,
		Logger:             m.Logger.Named("client"),
	}

	if m.ClientTLSInfo != nil {
		tls, err := m.ClientTLSInfo.ClientConfig()
		if err != nil {
			return nil, err
		}
		cfg.TLS = tls
	}
	if m.DialOptions != nil {
		cfg.DialOptions = append(cfg.DialOptions, m.DialOptions...)
	}
	return newClientV3(cfg)
}

// Clone returns a member with the same server configuration. The returned
// member will not set PeerListeners and ClientListeners.
func (m *Member) Clone(t testutil.TB) *Member {
	mm := &Member{}
	mm.ServerConfig = m.ServerConfig

	var err error
	clientURLStrs := m.ClientURLs.StringSlice()
	mm.ClientURLs, err = types.NewURLs(clientURLStrs)
	if err != nil {
		// this should never fail
		panic(err)
	}
	peerURLStrs := m.PeerURLs.StringSlice()
	mm.PeerURLs, err = types.NewURLs(peerURLStrs)
	if err != nil {
		// this should never fail
		panic(err)
	}
	clusterStr := m.InitialPeerURLsMap.String()
	mm.InitialPeerURLsMap, err = types.NewURLsMap(clusterStr)
	if err != nil {
		// this should never fail
		panic(err)
	}
	mm.InitialClusterToken = m.InitialClusterToken
	mm.ElectionTicks = m.ElectionTicks
	mm.PeerTLSInfo = m.PeerTLSInfo
	mm.ClientTLSInfo = m.ClientTLSInfo
	mm.Logger, mm.LogObserver = memberLogger(t, mm.Name+"c")
	return mm
}

// Launch starts a member based on ServerConfig, PeerListeners
// and ClientListeners.
func (m *Member) Launch() error {
	m.Logger.Info(
		"launching a member",
		zap.String("name", m.Name),
		zap.Strings("advertise-peer-urls", m.PeerURLs.StringSlice()),
		zap.Strings("listen-client-urls", m.ClientURLs.StringSlice()),
		zap.String("grpc-url", m.GRPCURL),
	)
	var err error
	if m.Server, err = etcdserver.NewServer(m.ServerConfig); err != nil {
		return fmt.Errorf("failed to initialize the etcd server: %w", err)
	}
	m.Server.Start()

	var peerTLScfg *tls.Config
	if m.PeerTLSInfo != nil && !m.PeerTLSInfo.Empty() {
		if peerTLScfg, err = m.PeerTLSInfo.ServerConfig(); err != nil {
			return err
		}
	}

	if m.GRPCListener != nil {
		var tlscfg *tls.Config
		if m.ClientTLSInfo != nil && !m.ClientTLSInfo.Empty() {
			tlscfg, err = m.ClientTLSInfo.ServerConfig()
			if err != nil {
				return err
			}
		}
		m.GRPCServer = v3rpc.Server(m.Server, tlscfg, m.GRPCServerRecorder.UnaryInterceptor(), m.GRPCServerOpts...)
		m.ServerClient = v3client.New(m.Server)
		lockpb.RegisterLockServer(m.GRPCServer, v3lock.NewLockServer(m.ServerClient))
		epb.RegisterElectionServer(m.GRPCServer, v3election.NewElectionServer(m.ServerClient))
		go m.GRPCServer.Serve(m.GRPCListener)
	}

	m.RaftHandler = &testutil.PauseableHandler{Next: etcdhttp.NewPeerHandler(m.Logger, m.Server)}

	h := (http.Handler)(m.RaftHandler)
	if m.GRPCListener != nil {
		h = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			m.RaftHandler.ServeHTTP(w, r)
		})
	}

	for _, ln := range m.PeerListeners {
		cm := cmux.New(ln)
		// don't hang on matcher after closing listener
		cm.SetReadTimeout(time.Second)

		// serve http1/http2 rafthttp/grpc
		ll := cm.Match(cmux.Any())
		if peerTLScfg != nil {
			if ll, err = transport.NewTLSListener(ll, m.PeerTLSInfo); err != nil {
				return err
			}
		}
		hs := &httptest.Server{
			Listener: ll,
			Config: &http.Server{
				Handler:   h,
				TLSConfig: peerTLScfg,
				ErrorLog:  log.New(io.Discard, "net/http", 0),
			},
			TLS: peerTLScfg,
		}
		hs.Start()

		donec := make(chan struct{})
		go func() {
			defer close(donec)
			cm.Serve()
		}()
		closer := func() {
			ll.Close()
			hs.CloseClientConnections()
			hs.Close()
			<-donec
		}
		m.ServerClosers = append(m.ServerClosers, closer)
	}
	for _, ln := range m.ClientListeners {
		handler := http.NewServeMux()
		etcdhttp.HandleDebug(handler)
		etcdhttp.HandleVersion(handler, m.Server)
		etcdhttp.HandleMetrics(handler)
		etcdhttp.HandleHealth(m.Logger, handler, m.Server)
		hs := &httptest.Server{
			Listener: ln,
			Config: &http.Server{
				Handler:  handler,
				ErrorLog: log.New(io.Discard, "net/http", 0),
			},
		}
		if m.ClientTLSInfo == nil {
			hs.Start()
		} else {
			info := m.ClientTLSInfo
			hs.TLS, err = info.ServerConfig()
			if err != nil {
				return err
			}

			// baseConfig is called on initial TLS handshake start.
			//
			// Previously,
			// 1. Server has non-empty (*tls.Config).Certificates on client hello
			// 2. Server calls (*tls.Config).GetCertificate iff:
			//    - Server'Server (*tls.Config).Certificates is not empty, or
			//    - Client supplies SNI; non-empty (*tls.ClientHelloInfo).ServerName
			//
			// When (*tls.Config).Certificates is always populated on initial handshake,
			// client is expected to provide a valid matching SNI to pass the TLS
			// verification, thus trigger server (*tls.Config).GetCertificate to reload
			// TLS assets. However, a cert whose SAN field does not include domain names
			// but only IP addresses, has empty (*tls.ClientHelloInfo).ServerName, thus
			// it was never able to trigger TLS reload on initial handshake; first
			// ceritifcate object was being used, never being updated.
			//
			// Now, (*tls.Config).Certificates is created empty on initial TLS client
			// handshake, in order to trigger (*tls.Config).GetCertificate and populate
			// rest of the certificates on every new TLS connection, even when client
			// SNI is empty (e.g. cert only includes IPs).
			//
			// This introduces another problem with "httptest.Server":
			// when server initial certificates are empty, certificates
			// are overwritten by Go'Server internal test certs, which have
			// different SAN fields (e.g. example.com). To work around,
			// re-overwrite (*tls.Config).Certificates before starting
			// test server.
			tlsCert, nerr := tlsutil.NewCert(info.CertFile, info.KeyFile, nil)
			if nerr != nil {
				return nerr
			}
			hs.TLS.Certificates = []tls.Certificate{*tlsCert}

			hs.StartTLS()
		}
		closer := func() {
			ln.Close()
			hs.CloseClientConnections()
			hs.Close()
		}
		m.ServerClosers = append(m.ServerClosers, closer)
	}
	if m.GRPCURL != "" && m.Client == nil {
		m.Client, err = NewClientV3(m)
		if err != nil {
			return err
		}
	}

	m.Logger.Info(
		"launched a member",
		zap.String("name", m.Name),
		zap.Strings("advertise-peer-urls", m.PeerURLs.StringSlice()),
		zap.Strings("listen-client-urls", m.ClientURLs.StringSlice()),
		zap.String("grpc-url", m.GRPCURL),
	)
	return nil
}

func (m *Member) RecordedRequests() []grpctesting.RequestInfo {
	return m.GRPCServerRecorder.RecordedRequests()
}

func (m *Member) WaitOK(t testutil.TB) {
	m.WaitStarted(t)
	for m.Server.Leader() == 0 {
		time.Sleep(framecfg.TickDuration)
	}
}

func (m *Member) WaitStarted(t testutil.TB) {
	for {
		ctx, cancel := context.WithTimeout(context.Background(), RequestTimeout)
		_, err := m.Client.Get(ctx, "/", clientv3.WithSerializable())
		if err != nil {
			time.Sleep(framecfg.TickDuration)
			continue
		}
		cancel()
		break
	}
}

func WaitClientV3(t testutil.TB, kv clientv3.KV) {
	WaitClientV3WithKey(t, kv, "/")
}

func WaitClientV3WithKey(t testutil.TB, kv clientv3.KV, key string) {
	timeout := time.Now().Add(RequestTimeout)
	var err error
	for time.Now().Before(timeout) {
		ctx, cancel := context.WithTimeout(context.Background(), RequestTimeout)
		_, err = kv.Get(ctx, key)
		cancel()
		if err == nil {
			return
		}
		time.Sleep(framecfg.TickDuration)
	}
	if err != nil {
		t.Fatalf("timed out waiting for client: %v", err)
	}
}

func (m *Member) URL() string { return m.ClientURLs[0].String() }

func (m *Member) Pause() {
	m.RaftHandler.Pause()
	m.Server.PauseSending()
}

func (m *Member) Resume() {
	m.RaftHandler.Resume()
	m.Server.ResumeSending()
}

// Close stops the member'Server etcdserver and closes its connections
func (m *Member) Close() {
	if m.GRPCBridge != nil {
		m.GRPCBridge.Close()
		m.GRPCBridge = nil
	}
	if m.ServerClient != nil {
		m.ServerClient.Close()
		m.ServerClient = nil
	}
	if m.GRPCServer != nil {
		ch := make(chan struct{})
		go func() {
			defer close(ch)
			// close listeners to stop accepting new connections,
			// will block on any existing transports
			m.GRPCServer.GracefulStop()
		}()
		// wait until all pending RPCs are finished
		select {
		case <-ch:
		case <-time.After(2 * time.Second):
			// took too long, manually close open transports
			// e.g. watch streams
			m.GRPCServer.Stop()
			<-ch
		}
		m.GRPCServer = nil
	}
	if m.Server != nil {
		m.Server.HardStop()
	}
	for _, f := range m.ServerClosers {
		f()
	}
	if !m.Closed {
		// Avoid verification of the same file multiple times
		// (that might not exist any longer)
		verify.MustVerifyIfEnabled(verify.Config{
			Logger:     m.Logger,
			DataDir:    m.DataDir,
			ExactIndex: false,
		})
	}
	m.Closed = true
}

// Stop stops the member, but the data dir of the member is preserved.
func (m *Member) Stop(_ testutil.TB) {
	m.Logger.Info(
		"stopping a member",
		zap.String("name", m.Name),
		zap.Strings("advertise-peer-urls", m.PeerURLs.StringSlice()),
		zap.Strings("listen-client-urls", m.ClientURLs.StringSlice()),
		zap.String("grpc-url", m.GRPCURL),
	)
	m.Close()
	m.ServerClosers = nil
	m.Logger.Info(
		"stopped a member",
		zap.String("name", m.Name),
		zap.Strings("advertise-peer-urls", m.PeerURLs.StringSlice()),
		zap.Strings("listen-client-urls", m.ClientURLs.StringSlice()),
		zap.String("grpc-url", m.GRPCURL),
	)
}

// CheckLeaderTransition waits for leader transition, returning the new leader ID.
func CheckLeaderTransition(m *Member, oldLead uint64) uint64 {
	interval := time.Duration(m.Server.Cfg.TickMs) * time.Millisecond
	for m.Server.Lead() == 0 || (m.Server.Lead() == oldLead) {
		time.Sleep(interval)
	}
	return m.Server.Lead()
}

// StopNotify unblocks when a member stop completes
func (m *Member) StopNotify() <-chan struct{} {
	return m.Server.StopNotify()
}

// Restart starts the member using the preserved data dir.
func (m *Member) Restart(t testutil.TB) error {
	m.Logger.Info(
		"restarting a member",
		zap.String("name", m.Name),
		zap.Strings("advertise-peer-urls", m.PeerURLs.StringSlice()),
		zap.Strings("listen-client-urls", m.ClientURLs.StringSlice()),
		zap.String("grpc-url", m.GRPCURL),
	)
	newPeerListeners := make([]net.Listener, 0)
	for _, ln := range m.PeerListeners {
		newPeerListeners = append(newPeerListeners, NewListenerWithAddr(t, ln.Addr().String()))
	}
	m.PeerListeners = newPeerListeners
	newClientListeners := make([]net.Listener, 0)
	for _, ln := range m.ClientListeners {
		newClientListeners = append(newClientListeners, NewListenerWithAddr(t, ln.Addr().String()))
	}
	m.ClientListeners = newClientListeners

	if m.GRPCListener != nil {
		if err := m.listenGRPC(); err != nil {
			t.Fatal(err)
		}
	}

	err := m.Launch()
	m.Logger.Info(
		"restarted a member",
		zap.String("name", m.Name),
		zap.Strings("advertise-peer-urls", m.PeerURLs.StringSlice()),
		zap.Strings("listen-client-urls", m.ClientURLs.StringSlice()),
		zap.String("grpc-url", m.GRPCURL),
		zap.Error(err),
	)
	return err
}

// Terminate stops the member and removes the data dir.
func (m *Member) Terminate(t testutil.TB) {
	m.Logger.Info(
		"terminating a member",
		zap.String("name", m.Name),
		zap.Strings("advertise-peer-urls", m.PeerURLs.StringSlice()),
		zap.Strings("listen-client-urls", m.ClientURLs.StringSlice()),
		zap.String("grpc-url", m.GRPCURL),
	)
	m.Close()
	if !m.KeepDataDirTerminate {
		if err := os.RemoveAll(m.ServerConfig.DataDir); err != nil {
			t.Fatal(err)
		}
	}
	m.Logger.Info(
		"terminated a member",
		zap.String("name", m.Name),
		zap.Strings("advertise-peer-urls", m.PeerURLs.StringSlice()),
		zap.Strings("listen-client-urls", m.ClientURLs.StringSlice()),
		zap.String("grpc-url", m.GRPCURL),
	)
}

// Metric gets the metric value for a member
func (m *Member) Metric(metricName string, expectLabels ...string) (string, error) {
	cfgtls := transport.TLSInfo{}
	tr, err := transport.NewTimeoutTransport(cfgtls, time.Second, time.Second, time.Second)
	if err != nil {
		return "", err
	}
	cli := &http.Client{Transport: tr}
	resp, err := cli.Get(m.ClientURLs[0].String() + "/metrics")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, rerr := io.ReadAll(resp.Body)
	if rerr != nil {
		return "", rerr
	}
	lines := strings.Split(string(b), "\n")
	for _, l := range lines {
		if !strings.HasPrefix(l, metricName) {
			continue
		}
		ok := true
		for _, lv := range expectLabels {
			if !strings.Contains(l, lv) {
				ok = false
				break
			}
		}
		if !ok {
			continue
		}
		return strings.Split(l, " ")[1], nil
	}
	return "", nil
}

// InjectPartition drops connections from m to others, vice versa.
func (m *Member) InjectPartition(t testutil.TB, others ...*Member) {
	for _, other := range others {
		m.Server.CutPeer(other.Server.MemberID())
		other.Server.CutPeer(m.Server.MemberID())
		t.Logf("network partition injected between: %v <-> %v", m.Server.MemberID(), other.Server.MemberID())
	}
}

// RecoverPartition recovers connections from m to others, vice versa.
func (m *Member) RecoverPartition(t testutil.TB, others ...*Member) {
	for _, other := range others {
		m.Server.MendPeer(other.Server.MemberID())
		other.Server.MendPeer(m.Server.MemberID())
		t.Logf("network partition between: %v <-> %v", m.Server.MemberID(), other.Server.MemberID())
	}
}

func (m *Member) ReadyNotify() <-chan struct{} {
	return m.Server.ReadyNotify()
}

type SortableMemberSliceByPeerURLs []*pb.Member

func (p SortableMemberSliceByPeerURLs) Len() int { return len(p) }
func (p SortableMemberSliceByPeerURLs) Less(i, j int) bool {
	return p[i].PeerURLs[0] < p[j].PeerURLs[0]
}
func (p SortableMemberSliceByPeerURLs) Swap(i, j int) { p[i], p[j] = p[j], p[i] }

// NewCluster returns a launched Cluster with a grpc client connection
// for each Cluster member.
func NewCluster(t testutil.TB, cfg *ClusterConfig) *Cluster {
	t.Helper()

	assertInTestContext(t)

	testutil.SkipTestIfShortMode(t, "Cannot start etcd Cluster in --short tests")

	c := &Cluster{Cfg: cfg}
	ms := make([]*Member, cfg.Size)
	for i := 0; i < cfg.Size; i++ {
		ms[i] = c.MustNewMember(t)
	}
	c.Members = ms
	if err := c.fillClusterForMembers(); err != nil {
		t.Fatalf("fillClusterForMembers failed: %v", err)
	}
	c.Launch(t)

	return c
}

func (c *Cluster) TakeClient(idx int) {
	c.mu.Lock()
	c.Members[idx].Client = nil
	c.mu.Unlock()
}

func (c *Cluster) Terminate(t testutil.TB) {
	if t != nil {
		t.Logf("========= Cluster termination started =====================")
	}
	for _, m := range c.Members {
		if m.Client != nil {
			m.Client.Close()
		}
	}
	var wg sync.WaitGroup
	wg.Add(len(c.Members))
	for _, m := range c.Members {
		go func(mm *Member) {
			defer wg.Done()
			mm.Terminate(t)
		}(m)
	}
	wg.Wait()
	if t != nil {
		t.Logf("========= Cluster termination succeeded ===================")
	}
}

func (c *Cluster) RandClient() *clientv3.Client {
	return c.Members[rand.Intn(len(c.Members))].Client
}

func (c *Cluster) Client(i int) *clientv3.Client {
	return c.Members[i].Client
}

func (c *Cluster) Endpoints() []string {
	var endpoints []string
	for _, m := range c.Members {
		endpoints = append(endpoints, m.GRPCURL)
	}
	return endpoints
}

func (c *Cluster) ClusterClient(tb testing.TB, opts ...framecfg.ClientOption) (client *clientv3.Client, err error) {
	cfg, err := c.newClientCfg()
	if err != nil {
		return nil, err
	}
	for _, opt := range opts {
		opt(cfg)
	}
	client, err = newClientV3(*cfg)
	if err != nil {
		return nil, err
	}
	tb.Cleanup(func() {
		client.Close()
	})
	return client, nil
}

func WithAuth(userName, password string) framecfg.ClientOption {
	return func(c any) {
		cfg := c.(*clientv3.Config)
		cfg.Username = userName
		cfg.Password = password
	}
}

func WithAuthToken(token string) framecfg.ClientOption {
	return func(c any) {
		cfg := c.(*clientv3.Config)
		cfg.Token = token
	}
}

func WithEndpoints(endpoints []string) framecfg.ClientOption {
	return func(c any) {
		cfg := c.(*clientv3.Config)
		cfg.Endpoints = endpoints
	}
}

func WithDialTimeout(tio time.Duration) framecfg.ClientOption {
	return func(c any) {
		cfg := c.(*clientv3.Config)
		cfg.DialTimeout = tio
	}
}

func (c *Cluster) newClientCfg() (*clientv3.Config, error) {
	cfg := &clientv3.Config{
		Endpoints:          c.Endpoints(),
		DialTimeout:        5 * time.Second,
		MaxCallSendMsgSize: c.Cfg.ClientMaxCallSendMsgSize,
		MaxCallRecvMsgSize: c.Cfg.ClientMaxCallRecvMsgSize,
	}
	if c.Cfg.ClientTLS != nil {
		tls, err := c.Cfg.ClientTLS.ClientConfig()
		if err != nil {
			return nil, err
		}
		cfg.TLS = tls
	}
	return cfg, nil
}

// NewClientV3 creates a new grpc client connection to the member
func (c *Cluster) NewClientV3(memberIndex int) (*clientv3.Client, error) {
	return NewClientV3(c.Members[memberIndex])
}

func makeClients(t testutil.TB, clus *Cluster, clients *[]*clientv3.Client, chooseMemberIndex func() int) func() *clientv3.Client {
	var mu sync.Mutex
	*clients = nil
	return func() *clientv3.Client {
		cli, err := clus.NewClientV3(chooseMemberIndex())
		if err != nil {
			t.Fatalf("cannot create client: %v", err)
		}
		mu.Lock()
		*clients = append(*clients, cli)
		mu.Unlock()
		return cli
	}
}

// MakeSingleNodeClients creates factory of clients that all connect to member 0.
// All the created clients are put on the 'clients' list. The factory is thread-safe.
func MakeSingleNodeClients(t testutil.TB, clus *Cluster, clients *[]*clientv3.Client) func() *clientv3.Client {
	return makeClients(t, clus, clients, func() int { return 0 })
}

// MakeMultiNodeClients creates factory of clients that all connect to random members.
// All the created clients are put on the 'clients' list. The factory is thread-safe.
func MakeMultiNodeClients(t testutil.TB, clus *Cluster, clients *[]*clientv3.Client) func() *clientv3.Client {
	return makeClients(t, clus, clients, func() int { return rand.Intn(len(clus.Members)) })
}

// CloseClients closes all the clients from the 'clients' list.
func CloseClients(t testutil.TB, clients []*clientv3.Client) {
	for _, cli := range clients {
		if err := cli.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

type GRPCAPI struct {
	// Cluster is the Cluster API for the client'Server connection.
	Cluster pb.ClusterClient
	// KV is the keyvalue API for the client'Server connection.
	KV pb.KVClient
	// Lease is the lease API for the client'Server connection.
	Lease pb.LeaseClient
	// Watch is the watch API for the client'Server connection.
	Watch pb.WatchClient
	// Maintenance is the maintenance API for the client'Server connection.
	Maintenance pb.MaintenanceClient
	// Auth is the authentication API for the client'Server connection.
	Auth pb.AuthClient
	// Lock is the lock API for the client'Server connection.
	Lock lockpb.LockClient
	// Election is the election API for the client'Server connection.
	Election epb.ElectionClient
}

// GetLearnerMembers returns the list of learner members in Cluster using MemberList API.
func (c *Cluster) GetLearnerMembers() ([]*pb.Member, error) {
	cli := c.Client(0)
	resp, err := cli.MemberList(context.Background())
	if err != nil {
		return nil, fmt.Errorf("failed to list member %w", err)
	}
	var learners []*pb.Member
	for _, m := range resp.Members {
		if m.IsLearner {
			learners = append(learners, m)
		}
	}
	return learners, nil
}

// AddAndLaunchLearnerMember creates a learner member, adds it to Cluster
// via v3 MemberAdd API, and then launches the new member.
func (c *Cluster) AddAndLaunchLearnerMember(t testutil.TB) {
	m := c.MustNewMember(t)
	m.IsLearner = true

	scheme := SchemeFromTLSInfo(c.Cfg.PeerTLS)
	peerURLs := []string{scheme + "://" + m.PeerListeners[0].Addr().String()}

	cli := c.Client(0)
	_, err := cli.MemberAddAsLearner(context.Background(), peerURLs)
	if err != nil {
		t.Fatalf("failed to add learner member %v", err)
	}

	m.InitialPeerURLsMap = types.URLsMap{}
	for _, mm := range c.Members {
		m.InitialPeerURLsMap[mm.Name] = mm.PeerURLs
	}
	m.InitialPeerURLsMap[m.Name] = m.PeerURLs
	m.NewCluster = false

	if err := m.Launch(); err != nil {
		t.Fatal(err)
	}

	c.Members = append(c.Members, m)

	c.waitMembersMatch(t)
}

// getMembers returns a list of members in Cluster, in format of etcdserverpb.Member
func (c *Cluster) getMembers() []*pb.Member {
	var mems []*pb.Member
	for _, m := range c.Members {
		mem := &pb.Member{
			Name:       m.Name,
			PeerURLs:   m.PeerURLs.StringSlice(),
			ClientURLs: m.ClientURLs.StringSlice(),
			IsLearner:  m.IsLearner,
		}
		mems = append(mems, mem)
	}
	return mems
}

// waitMembersMatch waits until v3rpc MemberList returns the 'same' members info as the
// local 'c.Members', which is the local recording of members in the testing Cluster. With
// the exception that the local recording c.Members does not have info on Member.ID, which
// is generated when the member is been added to Cluster.
//
// Note:
// A successful match means the Member.clientURLs are matched. This means member has already
// finished publishing its server attributes to Cluster. Publishing attributes is a Cluster-wide
// write request (in v2 server). Therefore, at this point, any raft log entries prior to this
// would have already been applied.
//
// If a new member was added to an existing Cluster, at this point, it has finished publishing
// its own server attributes to the Cluster. And therefore by the same argument, it has already
// applied the raft log entries (especially those of type raftpb.ConfChangeType). At this point,
// the new member has the correct view of the Cluster configuration.
//
// Special note on learner member:
// Learner member is only added to a Cluster via v3rpc MemberAdd API (as of v3.4). When starting
// the learner member, its initial view of the Cluster created by peerURLs map does not have info
// on whether or not the new member itself is learner. But at this point, a successful match does
// indicate that the new learner member has applied the raftpb.ConfChangeAddLearnerNode entry
// which was used to add the learner itself to the Cluster, and therefore it has the correct info
// on learner.
func (c *Cluster) waitMembersMatch(t testutil.TB) {
	wMembers := c.getMembers()
	sort.Sort(SortableProtoMemberSliceByPeerURLs(wMembers))
	cli := c.Client(0)
	for {
		resp, err := cli.MemberList(context.Background())
		if err != nil {
			t.Fatalf("failed to list member %v", err)
		}

		if len(resp.Members) != len(wMembers) {
			continue
		}
		sort.Sort(SortableProtoMemberSliceByPeerURLs(resp.Members))
		for _, m := range resp.Members {
			m.ID = 0
		}
		if reflect.DeepEqual(resp.Members, wMembers) {
			return
		}

		time.Sleep(framecfg.TickDuration)
	}
}

type SortableProtoMemberSliceByPeerURLs []*pb.Member

func (p SortableProtoMemberSliceByPeerURLs) Len() int { return len(p) }
func (p SortableProtoMemberSliceByPeerURLs) Less(i, j int) bool {
	return p[i].PeerURLs[0] < p[j].PeerURLs[0]
}
func (p SortableProtoMemberSliceByPeerURLs) Swap(i, j int) { p[i], p[j] = p[j], p[i] }

// InitializeMemberWithResponse initializes a member with the response
func (c *Cluster) InitializeMemberWithResponse(t testutil.TB, m *Member, resp *clientv3.MemberAddResponse) {
	m.IsLearner = resp.Member.IsLearner
	m.NewCluster = false

	m.InitialPeerURLsMap = types.URLsMap{}
	for _, mm := range c.Members {
		m.InitialPeerURLsMap[mm.Name] = mm.PeerURLs
	}
	m.InitialPeerURLsMap[m.Name] = types.MustNewURLs(resp.Member.PeerURLs)
	c.Members = append(c.Members, m)
}
