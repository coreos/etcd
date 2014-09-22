package integration

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"testing"
	"time"

	"github.com/coreos/etcd/etcdserver"
	"github.com/coreos/etcd/etcdserver/etcdhttp"
	"github.com/coreos/etcd/raft"
	"github.com/coreos/etcd/raft/raftpb"
	"github.com/coreos/etcd/store"
	"github.com/coreos/etcd/third_party/code.google.com/p/go.net/context"
)

func nopSave(st raftpb.HardState, ents []raftpb.Entry) {}
func nopSend(m []raftpb.Message)                       {}

func TestSet(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	n := raft.StartNode(1, []int64{1}, 0, 0)
	n.Campaign(ctx)

	srv := &etcdserver.EtcdServer{
		Store:   store.New(),
		Node:    n,
		Storage: nopStorage{},
		Send:    etcdserver.SendFunc(nopSend),
	}
	srv.Start()
	defer srv.Stop()

	h := etcdhttp.NewClientHandler(srv, nil, time.Hour)
	s := httptest.NewServer(h)
	defer s.Close()

	resp, err := http.PostForm(s.URL+"/v2/keys/foo", url.Values{"value": {"bar"}})
	if err != nil {
		t.Fatal(err)
	}

	if resp.StatusCode != 201 {
		t.Errorf("StatusCode = %d, expected %d", resp.StatusCode, 201)
	}

	g := new(store.Event)
	if err := json.NewDecoder(resp.Body).Decode(&g); err != nil {
		t.Fatal(err)
	}

	w := &store.NodeExtern{
		Key:           "/foo/1",
		Value:         stringp("bar"),
		ModifiedIndex: 1,
		CreatedIndex:  1,
	}
	if !reflect.DeepEqual(g.Node, w) {
		t.Errorf("g = %+v, want %+v", g.Node, w)
	}
}

func stringp(s string) *string { return &s }

type nopStorage struct{}

func (np nopStorage) Save(st raftpb.HardState, ents []raftpb.Entry) {}
func (np nopStorage) Cut() error                                    { return nil }
func (np nopStorage) SaveSnap(st raftpb.Snapshot)                   {}
