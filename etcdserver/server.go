package etcdserver

import (
	"encoding/json"
	"errors"
	"log"
	"math/rand"
	"os"
	"path"
	"sync/atomic"
	"time"

	"github.com/coreos/etcd/discovery"
	pb "github.com/coreos/etcd/etcdserver/etcdserverpb"
	"github.com/coreos/etcd/pkg/types"
	"github.com/coreos/etcd/raft"
	"github.com/coreos/etcd/raft/raftpb"
	"github.com/coreos/etcd/snap"
	"github.com/coreos/etcd/store"
	"github.com/coreos/etcd/third_party/code.google.com/p/go.net/context"
	"github.com/coreos/etcd/wait"
	"github.com/coreos/etcd/wal"
)

const (
	// owner can make/remove files inside the directory
	privateDirMode = 0700

	defaultSyncTimeout = time.Second
	DefaultSnapCount   = 10000
	// TODO: calculate based on heartbeat interval
	defaultPublishRetryInterval = 5 * time.Second
)

var (
	ErrUnknownMethod = errors.New("etcdserver: unknown method")
	ErrStopped       = errors.New("etcdserver: server stopped")
)

func init() {
	rand.Seed(time.Now().UnixNano())
}

type sendFunc func(m []raftpb.Message)

type Response struct {
	Event   *store.Event
	Watcher store.Watcher
	err     error
}

type Storage interface {
	// Save function saves ents and state to the underlying stable storage.
	// Save MUST block until st and ents are on stable storage.
	Save(st raftpb.HardState, ents []raftpb.Entry)
	// SaveSnap function saves snapshot to the underlying stable storage.
	SaveSnap(snap raftpb.Snapshot)

	// TODO: WAL should be able to control cut itself. After implement self-controled cut,
	// remove it in this interface.
	// Cut cuts out a new wal file for saving new state and entries.
	Cut() error
}

type Server interface {
	// Start performs any initialization of the Server necessary for it to
	// begin serving requests. It must be called before Do or Process.
	// Start must be non-blocking; any long-running server functionality
	// should be implemented in goroutines.
	Start()
	// Stop terminates the Server and performs any necessary finalization.
	// Do and Process cannot be called after Stop has been invoked.
	Stop()
	// Do takes a request and attempts to fulfil it, returning a Response.
	Do(ctx context.Context, r pb.Request) (Response, error)
	// Process takes a raft message and applies it to the server's raft state
	// machine, respecting any timeout of the given context.
	Process(ctx context.Context, m raftpb.Message) error
}

type RaftTimer interface {
	Index() int64
	Term() int64
}

// NewServer creates a new EtcdServer from the supplied configuration. The
// configuration is considered static for the lifetime of the EtcdServer.
func NewServer(cfg *ServerConfig) *EtcdServer {
	err := cfg.Verify()
	if err != nil {
		log.Fatalln(err)
	}
	snapdir := path.Join(cfg.DataDir, "snap")
	if err := os.MkdirAll(snapdir, privateDirMode); err != nil {
		log.Fatalf("etcdserver: cannot create snapshot directory: %v", err)
	}
	ss := snap.New(snapdir)
	st := store.New()
	var w *wal.WAL
	var n raft.Node
	m := cfg.Cluster.FindName(cfg.Name)
	waldir := path.Join(cfg.DataDir, "wal")
	if !wal.Exist(waldir) {
		if cfg.DiscoveryURL != "" {
			d, err := discovery.New(cfg.DiscoveryURL, m.ID, cfg.Cluster.String())
			if err != nil {
				log.Fatalf("etcd: cannot init discovery %v", err)
			}
			s, err := d.Discover()
			if err != nil {
				log.Fatalf("etcd: %v", err)
			}
			if err = cfg.Cluster.Set(s); err != nil {
				log.Fatalf("etcd: %v", err)
			}
		} else if (cfg.ClusterState) != ClusterStateValueNew {
			log.Fatalf("etcd: initial cluster state unset and no wal or discovery URL found")
		}
		if w, err = wal.Create(waldir); err != nil {
			log.Fatal(err)
		}
		// TODO: add context for PeerURLs
		// TODO: generate cluster id
		n = raft.StartNode(m.ID, 0xBAD0, cfg.Cluster.IDs(), 10, 1)
	} else {
		var index int64
		snapshot, err := ss.Load()
		if err != nil && err != snap.ErrNoSnapshot {
			log.Fatal(err)
		}
		if snapshot != nil {
			log.Printf("etcdserver: restart from snapshot at index %d", snapshot.Index)
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
		n = raft.RestartNode(m.ID, cfg.Cluster.IDs(), 10, 1, snapshot, st, ents)
	}

	cls := NewClusterStore(st, *cfg.Cluster)

	s := &EtcdServer{
		store: st,
		node:  n,
		name:  cfg.Name,
		storage: struct {
			*wal.WAL
			*snap.Snapshotter
		}{w, ss},
		send:         Sender(cfg.Transport, cls),
		clientURLs:   cfg.ClientURLs,
		ticker:       time.Tick(100 * time.Millisecond),
		syncTicker:   time.Tick(500 * time.Millisecond),
		snapCount:    cfg.SnapCount,
		ClusterStore: cls,
	}
	return s
}

// EtcdServer is the production implementation of the Server interface
type EtcdServer struct {
	w          wait.Wait
	done       chan struct{}
	name       string
	clientURLs types.URLs

	ClusterStore ClusterStore

	node  raft.Node
	store store.Store

	// send specifies the send function for sending msgs to members. send
	// MUST NOT block. It is okay to drop messages, since clients should
	// timeout and reissue their messages.  If send is nil, server will
	// panic.
	send sendFunc

	storage Storage

	ticker     <-chan time.Time
	syncTicker <-chan time.Time

	snapCount int64 // number of entries to trigger a snapshot

	// Cache of the latest raft index and raft term the server has seen
	raftIndex int64
	raftTerm  int64
}

// Start prepares and starts server in a new goroutine. It is no longer safe to
// modify a server's fields after it has been sent to Start.
// It also starts a goroutine to publish its server information.
func (s *EtcdServer) Start() {
	s.start()
	go s.publish(defaultPublishRetryInterval)
}

// start prepares and starts server in a new goroutine. It is no longer safe to
// modify a server's fields after it has been sent to Start.
// This function is just used for testing.
func (s *EtcdServer) start() {
	if s.snapCount == 0 {
		log.Printf("etcdserver: set snapshot count to default %d", DefaultSnapCount)
		s.snapCount = DefaultSnapCount
	}
	s.w = wait.New()
	s.done = make(chan struct{})
	// TODO: if this is an empty log, writes all peer infos
	// into the first entry
	go s.run()
}

func (s *EtcdServer) Process(ctx context.Context, m raftpb.Message) error {
	return s.node.Step(ctx, m)
}

func (s *EtcdServer) run() {
	var syncC <-chan time.Time
	// snapi indicates the index of the last submitted snapshot request
	var snapi, appliedi int64
	var nodes []int64
	for {
		select {
		case <-s.ticker:
			s.node.Tick()
		case rd := <-s.node.Ready():
			s.storage.Save(rd.HardState, rd.Entries)
			s.storage.SaveSnap(rd.Snapshot)
			s.send(rd.Messages)

			// TODO(bmizerany): do this in the background, but take
			// care to apply entries in a single goroutine, and not
			// race them.
			// TODO: apply configuration change into ClusterStore.
			for _, e := range rd.CommittedEntries {
				switch e.Type {
				case raftpb.EntryNormal:
					var r pb.Request
					if err := r.Unmarshal(e.Data); err != nil {
						panic("TODO: this is bad, what do we do about it?")
					}
					s.w.Trigger(r.ID, s.apply(r))
				case raftpb.EntryConfChange:
					var cc raftpb.ConfChange
					if err := cc.Unmarshal(e.Data); err != nil {
						panic("TODO: this is bad, what do we do about it?")
					}
					s.node.ApplyConfChange(cc)
					s.w.Trigger(cc.ID, nil)
				default:
					panic("unexpected entry type")
				}
				atomic.StoreInt64(&s.raftIndex, e.Index)
				atomic.StoreInt64(&s.raftTerm, e.Term)
				appliedi = e.Index
			}

			if rd.SoftState != nil {
				nodes = rd.SoftState.Nodes
				if rd.RaftState == raft.StateLeader {
					syncC = s.syncTicker
				} else {
					syncC = nil
				}
				if rd.SoftState.ShouldStop {
					s.Stop()
					return
				}
			}

			if rd.Snapshot.Index > snapi {
				snapi = rd.Snapshot.Index
			}

			// recover from snapshot if it is more updated than current applied
			if rd.Snapshot.Index > appliedi {
				if err := s.store.Recovery(rd.Snapshot.Data); err != nil {
					panic("TODO: this is bad, what do we do about it?")
				}
				appliedi = rd.Snapshot.Index
			}

			if appliedi-snapi > s.snapCount {
				s.snapshot(appliedi, nodes)
				snapi = appliedi
			}
		case <-syncC:
			s.sync(defaultSyncTimeout)
		case <-s.done:
			return
		}
	}
}

// Stop stops the server, and shuts down the running goroutine. Stop should be
// called after a Start(s), otherwise it will block forever.
func (s *EtcdServer) Stop() {
	s.node.Stop()
	close(s.done)
}

// Do interprets r and performs an operation on s.store according to r.Method
// and other fields. If r.Method is "POST", "PUT", "DELETE", or a "GET" with
// Quorum == true, r will be sent through consensus before performing its
// respective operation. Do will block until an action is performed or there is
// an error.
func (s *EtcdServer) Do(ctx context.Context, r pb.Request) (Response, error) {
	if r.ID == 0 {
		panic("r.Id cannot be 0")
	}
	if r.Method == "GET" && r.Quorum {
		r.Method = "QGET"
	}
	switch r.Method {
	case "POST", "PUT", "DELETE", "QGET":
		data, err := r.Marshal()
		if err != nil {
			return Response{}, err
		}
		ch := s.w.Register(r.ID)
		s.node.Propose(ctx, data)
		select {
		case x := <-ch:
			resp := x.(Response)
			return resp, resp.err
		case <-ctx.Done():
			s.w.Trigger(r.ID, nil) // GC wait
			return Response{}, ctx.Err()
		case <-s.done:
			return Response{}, ErrStopped
		}
	case "GET":
		switch {
		case r.Wait:
			wc, err := s.store.Watch(r.Path, r.Recursive, r.Stream, r.Since)
			if err != nil {
				return Response{}, err
			}
			return Response{Watcher: wc}, nil
		default:
			ev, err := s.store.Get(r.Path, r.Recursive, r.Sorted)
			if err != nil {
				return Response{}, err
			}
			return Response{Event: ev}, nil
		}
	default:
		return Response{}, ErrUnknownMethod
	}
}

func (s *EtcdServer) AddNode(ctx context.Context, id int64, context []byte) error {
	cc := raftpb.ConfChange{
		ID:      GenID(),
		Type:    raftpb.ConfChangeAddNode,
		NodeID:  id,
		Context: context,
	}
	return s.configure(ctx, cc)
}

func (s *EtcdServer) RemoveNode(ctx context.Context, id int64) error {
	cc := raftpb.ConfChange{
		ID:     GenID(),
		Type:   raftpb.ConfChangeRemoveNode,
		NodeID: id,
	}
	return s.configure(ctx, cc)
}

// Implement the RaftTimer interface
func (s *EtcdServer) Index() int64 {
	return atomic.LoadInt64(&s.raftIndex)
}

func (s *EtcdServer) Term() int64 {
	return atomic.LoadInt64(&s.raftTerm)
}

// configure sends configuration change through consensus then performs it.
// It will block until the change is performed or there is an error.
func (s *EtcdServer) configure(ctx context.Context, cc raftpb.ConfChange) error {
	ch := s.w.Register(cc.ID)
	if err := s.node.ProposeConfChange(ctx, cc); err != nil {
		log.Printf("configure error: %v", err)
		s.w.Trigger(cc.ID, nil)
		return err
	}
	select {
	case <-ch:
		return nil
	case <-ctx.Done():
		s.w.Trigger(cc.ID, nil) // GC wait
		return ctx.Err()
	case <-s.done:
		return ErrStopped
	}
}

// sync proposes a SYNC request and is non-blocking.
// This makes no guarantee that the request will be proposed or performed.
// The request will be cancelled after the given timeout.
func (s *EtcdServer) sync(timeout time.Duration) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	req := pb.Request{
		Method: "SYNC",
		ID:     GenID(),
		Time:   time.Now().UnixNano(),
	}
	data, err := req.Marshal()
	if err != nil {
		log.Printf("marshal request %#v error: %v", req, err)
		return
	}
	// There is no promise that node has leader when do SYNC request,
	// so it uses goroutine to propose.
	go func() {
		s.node.Propose(ctx, data)
		cancel()
	}()
}

// publish registers server information into the cluster. The information
// is the JSON representation of this server's member struct, updated with the
// static clientURLs of the server.
// The function keeps attempting to register until it succeeds,
// or its server is stopped.
// TODO: take care of info fetched from cluster store after having reconfig.
func (s *EtcdServer) publish(retryInterval time.Duration) {
	m := *s.ClusterStore.Get().FindName(s.name)
	m.ClientURLs = s.clientURLs.StringSlice()
	b, err := json.Marshal(m)
	if err != nil {
		log.Printf("etcdserver: json marshal error: %v", err)
		return
	}
	req := pb.Request{
		ID:     GenID(),
		Method: "PUT",
		Path:   m.storeKey(),
		Val:    string(b),
	}

	for {
		ctx, cancel := context.WithTimeout(context.Background(), retryInterval)
		_, err := s.Do(ctx, req)
		cancel()
		switch err {
		case nil:
			log.Printf("etcdserver: published %+v to the cluster", m)
			return
		case ErrStopped:
			log.Printf("etcdserver: aborting publish because server is stopped")
			return
		default:
			log.Printf("etcdserver: publish error: %v", err)
		}
	}
}

func getExpirationTime(r *pb.Request) time.Time {
	var t time.Time
	if r.Expiration != 0 {
		t = time.Unix(0, r.Expiration)
	}
	return t
}

// apply interprets r as a call to store.X and returns a Response interpreted
// from store.Event
func (s *EtcdServer) apply(r pb.Request) Response {
	f := func(ev *store.Event, err error) Response {
		return Response{Event: ev, err: err}
	}
	expr := getExpirationTime(&r)
	switch r.Method {
	case "POST":
		return f(s.store.Create(r.Path, r.Dir, r.Val, true, expr))
	case "PUT":
		exists, existsSet := getBool(r.PrevExist)
		switch {
		case existsSet:
			if exists {
				return f(s.store.Update(r.Path, r.Val, expr))
			}
			return f(s.store.Create(r.Path, r.Dir, r.Val, false, expr))
		case r.PrevIndex > 0 || r.PrevValue != "":
			return f(s.store.CompareAndSwap(r.Path, r.PrevValue, r.PrevIndex, r.Val, expr))
		default:
			return f(s.store.Set(r.Path, r.Dir, r.Val, expr))
		}
	case "DELETE":
		switch {
		case r.PrevIndex > 0 || r.PrevValue != "":
			return f(s.store.CompareAndDelete(r.Path, r.PrevValue, r.PrevIndex))
		default:
			return f(s.store.Delete(r.Path, r.Dir, r.Recursive))
		}
	case "QGET":
		return f(s.store.Get(r.Path, r.Recursive, r.Sorted))
	case "SYNC":
		s.store.DeleteExpiredKeys(time.Unix(0, r.Time))
		return Response{}
	default:
		// This should never be reached, but just in case:
		return Response{err: ErrUnknownMethod}
	}
}

// TODO: non-blocking snapshot
func (s *EtcdServer) snapshot(snapi int64, snapnodes []int64) {
	d, err := s.store.Save()
	// TODO: current store will never fail to do a snapshot
	// what should we do if the store might fail?
	if err != nil {
		panic("TODO: this is bad, what do we do about it?")
	}
	s.node.Compact(snapi, snapnodes, d)
	s.storage.Cut()
}

// TODO: move the function to /id pkg maybe?
// GenID generates a random id that is not equal to 0.
func GenID() (n int64) {
	for n == 0 {
		n = rand.Int63()
	}
	return
}

func getBool(v *bool) (vv bool, set bool) {
	if v == nil {
		return false, false
	}
	return *v, true
}
