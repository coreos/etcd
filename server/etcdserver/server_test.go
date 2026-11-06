// Copyright 2015 The etcd Authors
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

package etcdserver

import (
	"context"
	"encoding/json"
	errorspkg "errors"
	"fmt"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/prometheus/client_golang/prometheus"
	ptestutil "github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest"
	"go.uber.org/zap/zaptest/observer"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/testing/protocmp"

	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	"go.etcd.io/etcd/api/v3/membershippb"
	"go.etcd.io/etcd/client/pkg/v3/fileutil"
	"go.etcd.io/etcd/client/pkg/v3/testutil"
	"go.etcd.io/etcd/client/pkg/v3/types"
	"go.etcd.io/etcd/client/pkg/v3/verify"
	"go.etcd.io/etcd/pkg/v3/featuregate"
	"go.etcd.io/etcd/pkg/v3/idutil"
	"go.etcd.io/etcd/pkg/v3/notify"
	"go.etcd.io/etcd/pkg/v3/pbutil"
	"go.etcd.io/etcd/pkg/v3/wait"
	"go.etcd.io/etcd/server/v3/auth"
	"go.etcd.io/etcd/server/v3/config"
	"go.etcd.io/etcd/server/v3/etcdserver/api/membership"
	"go.etcd.io/etcd/server/v3/etcdserver/api/rafthttp"
	"go.etcd.io/etcd/server/v3/etcdserver/api/snap"
	apply2 "go.etcd.io/etcd/server/v3/etcdserver/apply"
	"go.etcd.io/etcd/server/v3/etcdserver/cindex"
	"go.etcd.io/etcd/server/v3/etcdserver/errors"
	"go.etcd.io/etcd/server/v3/features"
	"go.etcd.io/etcd/server/v3/lease"
	"go.etcd.io/etcd/server/v3/mock/mockstorage"
	"go.etcd.io/etcd/server/v3/mock/mockstore"
	"go.etcd.io/etcd/server/v3/mock/mockwait"
	serverstorage "go.etcd.io/etcd/server/v3/storage"
	betesting "go.etcd.io/etcd/server/v3/storage/backend/testing"
	"go.etcd.io/etcd/server/v3/storage/mvcc"
	"go.etcd.io/etcd/server/v3/storage/schema"
	"go.etcd.io/raft/v3"
	"go.etcd.io/raft/v3/raftpb"
)

// TestApplyRepeat tests that server handles repeat raft messages gracefully
func TestApplyRepeat(t *testing.T) {
	lg := zaptest.NewLogger(t)
	n := newNodeConfChangeCommitterStream()
	n.readyc <- raft.Ready{
		SoftState: &raft.SoftState{RaftState: raft.StateLeader},
	}
	cl := newTestCluster(t)
	be, _ := betesting.NewDefaultTmpBackend(t)
	defer betesting.Close(t, be)
	cl.SetBackend(schema.NewMembershipBackend(lg, be))

	cl.AddMember(&membership.Member{ID: 1234}, true)
	r := newRaftNode(raftNodeConfig{
		lg:          zaptest.NewLogger(t),
		Node:        n,
		raftStorage: raft.NewMemoryStorage(),
		storage:     mockstorage.NewStorageRecorder(""),
		transport:   newNopTransporter(),
	})
	s := &EtcdServer{
		lgMu:         new(sync.RWMutex),
		lg:           zaptest.NewLogger(t),
		r:            *r,
		cluster:      cl,
		reqIDGen:     idutil.NewGenerator(0, time.Time{}),
		consistIndex: cindex.NewFakeConsistentIndex(0),
		uberApply:    uberApplierMock{},
	}
	s.start()
	req := &pb.InternalRaftRequest{
		Header: &pb.RequestHeader{ID: 1},
		Put:    &pb.PutRequest{Key: []byte("foo"), Value: []byte("bar")},
	}
	ents := []*raftpb.Entry{{Index: new(uint64(1)), Data: pbutil.MustMarshalMessage(req)}}
	n.readyc <- raft.Ready{CommittedEntries: ents}
	// dup msg
	n.readyc <- raft.Ready{CommittedEntries: ents}

	// use a conf change to block until dup msgs are all processed
	cc := &raftpb.ConfChange{Type: raftpb.ConfChangeRemoveNode.Enum(), NodeId: new(uint64(2))}
	ents = []*raftpb.Entry{{
		Index: new(uint64(2)),
		Type:  raftpb.EntryConfChange.Enum(),
		Data:  pbutil.MustMarshalMessage(cc),
	}}
	n.readyc <- raft.Ready{CommittedEntries: ents}
	// wait for conf change message
	act, err := n.Wait(1)
	// wait for stop message (async to avoid deadlock)
	stopc := make(chan error, 1)
	go func() {
		_, werr := n.Wait(1)
		stopc <- werr
	}()
	s.Stop()

	// only want to confirm etcdserver won't panic; no data to check

	if err != nil {
		t.Fatal(err)
	}
	require.NotEmptyf(t, act, "expected len(act)=0, got %d", len(act))

	err = <-stopc
	require.NoErrorf(t, err, "error on stop (%v)", err)
}

type uberApplierMock struct{}

func (uberApplierMock) Apply(r *apply2.InternalRaftRequestWrapper, shouldApplyV3 membership.ShouldApplyV3) *apply2.Result {
	return &apply2.Result{}
}

func TestApplyConfStateWithRestart(t *testing.T) {
	n := newNodeRecorder()
	srv := newServer(t, n)
	defer srv.Cleanup()

	assert.Equal(t, uint64(0), srv.consistIndex.ConsistentIndex())

	var nodeID uint64 = 1
	memberData, err := json.Marshal(&membership.Member{ID: types.ID(nodeID), RaftAttributes: membership.RaftAttributes{PeerURLs: []string{""}}})
	if err != nil {
		t.Fatal(err)
	}

	entries := []*raftpb.Entry{
		{
			Term:  new(uint64(1)),
			Index: new(uint64(1)),
			Type:  raftpb.EntryConfChange.Enum(),
			Data: pbutil.MustMarshalMessage(&raftpb.ConfChange{
				Type:    raftpb.ConfChangeAddNode.Enum(),
				NodeId:  new(uint64(1)),
				Context: memberData,
			}),
		},
		{
			Term:  new(uint64(1)),
			Index: new(uint64(2)),
			Type:  raftpb.EntryConfChange.Enum(),
			Data: pbutil.MustMarshalMessage(&raftpb.ConfChange{
				Type:   raftpb.ConfChangeRemoveNode.Enum(),
				NodeId: new(uint64(1)),
			}),
		},
		{
			Term:  new(uint64(1)),
			Index: new(uint64(3)),
			Type:  raftpb.EntryConfChange.Enum(),
			Data: pbutil.MustMarshalMessage(&raftpb.ConfChange{
				Type:    raftpb.ConfChangeUpdateNode.Enum(),
				NodeId:  new(uint64(1)),
				Context: memberData,
			}),
		},
	}

	want := []testutil.Action{
		{
			Name: "ApplyConfChange",
			Params: []any{&raftpb.ConfChange{
				Type:    raftpb.ConfChangeAddNode.Enum(),
				NodeId:  new(uint64(1)),
				Context: memberData,
			}},
		},
		{
			Name: "ApplyConfChange",
			Params: []any{&raftpb.ConfChange{
				Type:   raftpb.ConfChangeRemoveNode.Enum(),
				NodeId: new(uint64(1)),
			}},
		},
		// This action is expected to fail validation, thus NodeID is set to 0
		{
			Name: "ApplyConfChange",
			Params: []any{&raftpb.ConfChange{
				Type:    raftpb.ConfChangeUpdateNode.Enum(),
				Context: memberData,
				NodeId:  new(uint64(0)),
			}},
		},
	}

	ep := &etcdProgress{
		confState: &raftpb.ConfState{},
	}

	t.Log("Applying entries for the first time")
	srv.apply(entries, ep, nil)
	got, _ := n.Wait(len(want))
	if diff := cmp.Diff(want, got, protocmp.Transform(), cmpopts.EquateEmpty()); diff != "" {
		t.Errorf("actions don't match (-want +got):\n%s", diff)
	}

	t.Log("Simulating etcd restart by clearing v3 store")
	be, _ := betesting.NewDefaultTmpBackend(t)
	t.Cleanup(func() {
		betesting.Close(t, be)
	})
	lg := zaptest.NewLogger(t)
	srv.cluster.SetBackend(schema.NewMembershipBackend(lg, be))
	srv.beHooks = serverstorage.NewBackendHooks(lg, srv.consistIndex)
	srv.consistIndex.SetBackend(be)

	t.Log("Reapplying same entries after restart")
	srv.apply(entries, ep, nil)
	got, _ = n.Wait(2 * len(want))
	if diff := cmp.Diff(want, got[len(want):], protocmp.Transform(), cmpopts.EquateEmpty()); diff != "" {
		t.Errorf("actions don't match (-want +got):\n%s", diff)
	}
}

func newServer(t *testing.T, recorder *nodeRecorder) *EtcdServer {
	lg := zaptest.NewLogger(t)
	be, _ := betesting.NewDefaultTmpBackend(t)
	t.Cleanup(func() {
		betesting.Close(t, be)
	})
	srv := &EtcdServer{
		lgMu:         new(sync.RWMutex),
		lg:           zaptest.NewLogger(t),
		r:            *newRaftNode(raftNodeConfig{lg: lg, Node: recorder, storage: mockstorage.NewStorageRecorder("")}),
		cluster:      membership.NewCluster(lg),
		consistIndex: cindex.NewConsistentIndex(be),
	}
	srv.cluster.SetBackend(schema.NewMembershipBackend(lg, be))
	srv.beHooks = serverstorage.NewBackendHooks(lg, srv.consistIndex)
	srv.r.transport = newNopTransporter()
	srv.w = mockwait.NewNop()
	return srv
}

func TestApplyConfChangeError(t *testing.T) {
	lg := zaptest.NewLogger(t)
	be, _ := betesting.NewDefaultTmpBackend(t)
	defer betesting.Close(t, be)

	cl := membership.NewCluster(lg)
	cl.SetBackend(schema.NewMembershipBackend(lg, be))

	for i := 1; i <= 4; i++ {
		cl.AddMember(&membership.Member{ID: types.ID(i)}, true)
	}
	cl.RemoveMember(4, true)

	attr := membership.RaftAttributes{PeerURLs: []string{fmt.Sprintf("http://127.0.0.1:%d", 1)}}
	ctx, err := json.Marshal(&membership.Member{ID: types.ID(1), RaftAttributes: attr})
	if err != nil {
		t.Fatal(err)
	}

	attr = membership.RaftAttributes{PeerURLs: []string{fmt.Sprintf("http://127.0.0.1:%d", 4)}}
	ctx4, err := json.Marshal(&membership.Member{ID: types.ID(1), RaftAttributes: attr})
	if err != nil {
		t.Fatal(err)
	}

	attr = membership.RaftAttributes{PeerURLs: []string{fmt.Sprintf("http://127.0.0.1:%d", 5)}}
	ctx5, err := json.Marshal(&membership.Member{ID: types.ID(1), RaftAttributes: attr})
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		cc   *raftpb.ConfChange
		werr error
	}{
		{
			&raftpb.ConfChange{
				Type:    raftpb.ConfChangeAddNode.Enum(),
				NodeId:  new(uint64(4)),
				Context: ctx4,
			},
			membership.ErrIDRemoved,
		},
		{
			&raftpb.ConfChange{
				Type:    raftpb.ConfChangeUpdateNode.Enum(),
				NodeId:  new(uint64(4)),
				Context: ctx4,
			},
			membership.ErrIDRemoved,
		},
		{
			&raftpb.ConfChange{
				Type:    raftpb.ConfChangeAddNode.Enum(),
				NodeId:  new(uint64(1)),
				Context: ctx,
			},
			membership.ErrIDExists,
		},
		{
			&raftpb.ConfChange{
				Type:    raftpb.ConfChangeRemoveNode.Enum(),
				NodeId:  new(uint64(5)),
				Context: ctx5,
			},
			membership.ErrIDNotFound,
		},
	}
	for i, tt := range tests {
		n := newNodeRecorder()
		srv := &EtcdServer{
			lgMu:    new(sync.RWMutex),
			lg:      zaptest.NewLogger(t),
			r:       *newRaftNode(raftNodeConfig{lg: zaptest.NewLogger(t), Node: n, storage: mockstorage.NewStorageRecorder("")}),
			cluster: cl,
		}
		_, err := srv.applyConfChange(tt.cc, nil, true)
		if !errorspkg.Is(err, tt.werr) {
			t.Errorf("#%d: applyConfChange error = %v, want %v", i, err, tt.werr)
		}
		cc := raftpb.ConfChange{Type: tt.cc.Type, NodeId: new(raft.None), Context: tt.cc.Context}
		w := []testutil.Action{
			{
				Name:   "ApplyConfChange",
				Params: []any{&cc},
			},
		}
		g, _ := n.Wait(1)
		if diff := cmp.Diff(w, g, protocmp.Transform(), cmpopts.EquateEmpty()); diff != "" {
			t.Errorf("#%d: action mismatch (-want +got):\n%s", i, diff)
		}
	}
}

func TestApplyConfChangeShouldStop(t *testing.T) {
	lg := zaptest.NewLogger(t)
	be, _ := betesting.NewDefaultTmpBackend(t)
	defer betesting.Close(t, be)

	cl := membership.NewCluster(lg)
	cl.SetBackend(schema.NewMembershipBackend(lg, be))

	for i := 1; i <= 3; i++ {
		cl.AddMember(&membership.Member{ID: types.ID(i)}, true)
	}
	r := newRaftNode(raftNodeConfig{
		lg:        zaptest.NewLogger(t),
		Node:      newNodeNop(),
		storage:   mockstorage.NewStorageRecorder(""),
		transport: newNopTransporter(),
	})
	srv := &EtcdServer{
		lgMu:     new(sync.RWMutex),
		lg:       lg,
		memberID: 1,
		r:        *r,
		cluster:  cl,
		beHooks:  serverstorage.NewBackendHooks(lg, nil),
	}
	cc := raftpb.ConfChange{
		Type:   raftpb.ConfChangeRemoveNode.Enum(),
		NodeId: new(uint64(2)),
	}
	ep := &etcdProgress{
		confState: &raftpb.ConfState{},
	}
	// remove non-local member
	shouldStop, err := srv.applyConfChange(&cc, ep, true)
	if err != nil {
		t.Fatalf("unexpected error %v", err)
	}
	if shouldStop {
		t.Errorf("shouldStop = %t, want %t", shouldStop, false)
	}

	// remove local member
	cc.NodeId = new(uint64(1))
	shouldStop, err = srv.applyConfChange(&cc, ep, true)
	if err != nil {
		t.Fatalf("unexpected error %v", err)
	}
	if !shouldStop {
		t.Errorf("shouldStop = %t, want %t", shouldStop, true)
	}
}

// TestApplyConfigChangeUpdatesConsistIndex ensures a config change also updates the consistIndex
// where consistIndex equals to applied index.
func TestApplyConfigChangeUpdatesConsistIndex(t *testing.T) {
	lg := zaptest.NewLogger(t)
	be, _ := betesting.NewDefaultTmpBackend(t)
	defer betesting.Close(t, be)

	cl := membership.NewCluster(zaptest.NewLogger(t))
	cl.SetBackend(schema.NewMembershipBackend(lg, be))

	cl.AddMember(&membership.Member{ID: types.ID(1)}, true)

	schema.CreateMetaBucket(be.BatchTx())

	ci := cindex.NewConsistentIndex(be)
	srv := &EtcdServer{
		lgMu:         new(sync.RWMutex),
		lg:           lg,
		memberID:     1,
		r:            *realisticRaftNode(lg, 1, nil),
		cluster:      cl,
		w:            wait.New(),
		consistIndex: ci,
		beHooks:      serverstorage.NewBackendHooks(lg, ci),
	}
	defer srv.r.raftNodeConfig.Stop()

	// create EntryConfChange entry
	now := time.Now()
	urls, err := types.NewURLs([]string{"http://whatever:123"})
	if err != nil {
		t.Fatal(err)
	}
	m := membership.NewMember("", urls, "", &now)
	m.ID = types.ID(2)
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	cc := &raftpb.ConfChange{Type: raftpb.ConfChangeAddNode.Enum(), NodeId: new(uint64(2)), Context: b}
	ents := []*raftpb.Entry{{
		Index: new(uint64(2)),
		Term:  new(uint64(4)),
		Type:  raftpb.EntryConfChange.Enum(),
		Data:  pbutil.MustMarshalMessage(cc),
	}}
	ep := &etcdProgress{
		confState: &raftpb.ConfState{},
	}

	raftAdvancedC := make(chan struct{}, 1)
	raftAdvancedC <- struct{}{}
	_, appliedi, _ := srv.apply(ents, ep, raftAdvancedC)
	consistIndex := srv.consistIndex.ConsistentIndex()
	assert.Equal(t, uint64(2), appliedi)

	t.Run("verify-backend", func(t *testing.T) {
		tx := be.BatchTx()
		tx.Lock()
		defer tx.Unlock()
		srv.beHooks.OnPreCommitUnsafe(tx)
		assert.Equal(t, raftpb.ConfState{Voters: []uint64{2}, AutoLeave: new(false)}, *schema.UnsafeConfStateFromBackend(lg, tx))
	})
	rindex, _ := schema.ReadConsistentIndex(be.ReadTx())
	assert.Equal(t, consistIndex, rindex)
}

func realisticRaftNode(lg *zap.Logger, id uint64, snap *raftpb.Snapshot) *raftNode {
	storage := raft.NewMemoryStorage()
	storage.SetHardState(&raftpb.HardState{Commit: new(uint64(0)), Term: new(uint64(0))})
	if snap != nil {
		err := storage.ApplySnapshot(snap)
		if err != nil {
			panic(err)
		}
	}
	c := &raft.Config{
		ID:              id,
		ElectionTick:    10,
		HeartbeatTick:   1,
		Storage:         storage,
		MaxSizePerMsg:   math.MaxUint64,
		MaxInflightMsgs: 256,
	}
	n := raft.RestartNode(c)
	r := newRaftNode(raftNodeConfig{
		lg:        lg,
		Node:      n,
		storage:   mockstorage.NewStorageRecorder(""),
		transport: newNopTransporter(),
	})
	return r
}

// TestApplyMultiConfChangeShouldStop ensures that toApply will return shouldStop
// if the local member is removed along with other conf updates.
func TestApplyMultiConfChangeShouldStop(t *testing.T) {
	lg := zaptest.NewLogger(t)
	cl := membership.NewCluster(lg)
	be, _ := betesting.NewDefaultTmpBackend(t)
	defer betesting.Close(t, be)
	cl.SetBackend(schema.NewMembershipBackend(lg, be))

	for i := 1; i <= 5; i++ {
		cl.AddMember(&membership.Member{ID: types.ID(i)}, true)
	}
	r := newRaftNode(raftNodeConfig{
		lg:        lg,
		Node:      newNodeNop(),
		storage:   mockstorage.NewStorageRecorder(""),
		transport: newNopTransporter(),
	})
	ci := cindex.NewFakeConsistentIndex(0)
	srv := &EtcdServer{
		lgMu:         new(sync.RWMutex),
		lg:           lg,
		memberID:     2,
		r:            *r,
		cluster:      cl,
		w:            wait.New(),
		consistIndex: ci,
		beHooks:      serverstorage.NewBackendHooks(lg, ci),
	}
	var ents []*raftpb.Entry
	for i := 1; i <= 4; i++ {
		ents = append(ents, &raftpb.Entry{
			Term:  new(uint64(1)),
			Index: new(uint64(i)),
			Type:  raftpb.EntryConfChange.Enum(),
			Data: pbutil.MustMarshalMessage(
				&raftpb.ConfChange{
					Type:   raftpb.ConfChangeRemoveNode.Enum(),
					NodeId: new(uint64(i)),
				}),
		})
	}
	ep := &etcdProgress{
		confState: &raftpb.ConfState{},
	}

	raftAdvancedC := make(chan struct{}, 1)
	raftAdvancedC <- struct{}{}
	_, _, shouldStop := srv.apply(ents, ep, raftAdvancedC)
	if !shouldStop {
		t.Errorf("shouldStop = %t, want %t", shouldStop, true)
	}
}

// TestSnapshotDisk should save the snapshot to disk and release old snapshots
func TestSnapshotDisk(t *testing.T) {
	revertFunc := verify.DisableVerifications()
	defer revertFunc()

	be, _ := betesting.NewDefaultTmpBackend(t)
	defer betesting.Close(t, be)

	s := raft.NewMemoryStorage()
	s.Append([]*raftpb.Entry{{Index: new(uint64(1))}})
	st := mockstore.NewRecorderStream()
	p := mockstorage.NewStorageRecorderStream("")
	r := newRaftNode(raftNodeConfig{
		lg:          zaptest.NewLogger(t),
		Node:        newNodeNop(),
		raftStorage: s,
		storage:     p,
	})
	srv := &EtcdServer{
		lgMu:         new(sync.RWMutex),
		lg:           zaptest.NewLogger(t),
		r:            *r,
		consistIndex: cindex.NewConsistentIndex(be),
	}
	srv.kv = mvcc.New(zaptest.NewLogger(t), be, &lease.FakeLessor{}, mvcc.StoreConfig{})
	defer func() {
		assert.NoError(t, srv.kv.Close())
	}()
	srv.be = be

	cl := membership.NewCluster(zaptest.NewLogger(t))
	srv.cluster = cl

	ch := make(chan struct{}, 1)

	go func() {
		gaction, _ := p.Wait(2)
		defer func() { ch <- struct{}{} }()

		assert.Len(t, gaction, 2)
		assert.Equal(t, testutil.Action{Name: "SaveSnap"}, gaction[0])
		assert.Equal(t, testutil.Action{Name: "Release"}, gaction[1])
	}()
	ep := etcdProgress{appliedi: 1, confState: &raftpb.ConfState{Voters: []uint64{1}}}
	srv.snapshot(&ep, true)
	<-ch
	assert.Empty(t, st.Action())
	assert.Equal(t, uint64(1), ep.diskSnapshotIndex)
	assert.Equal(t, uint64(1), ep.memorySnapshotIndex)
}

func TestSnapshotMemory(t *testing.T) {
	revertFunc := verify.DisableVerifications()
	defer revertFunc()

	be, _ := betesting.NewDefaultTmpBackend(t)
	defer betesting.Close(t, be)

	s := raft.NewMemoryStorage()
	s.Append([]*raftpb.Entry{{Index: new(uint64(1))}})
	st := mockstore.NewRecorderStream()
	p := mockstorage.NewStorageRecorderStream("")
	r := newRaftNode(raftNodeConfig{
		lg:          zaptest.NewLogger(t),
		Node:        newNodeNop(),
		raftStorage: s,
		storage:     p,
	})
	srv := &EtcdServer{
		lgMu:         new(sync.RWMutex),
		lg:           zaptest.NewLogger(t),
		r:            *r,
		consistIndex: cindex.NewConsistentIndex(be),
	}
	srv.kv = mvcc.New(zaptest.NewLogger(t), be, &lease.FakeLessor{}, mvcc.StoreConfig{})
	defer func() {
		assert.NoError(t, srv.kv.Close())
	}()
	srv.be = be

	cl := membership.NewCluster(zaptest.NewLogger(t))
	srv.cluster = cl

	ch := make(chan struct{}, 1)

	go func() {
		gaction, _ := p.Wait(1)
		defer func() { ch <- struct{}{} }()

		assert.Empty(t, gaction)
	}()
	ep := etcdProgress{appliedi: 1, confState: &raftpb.ConfState{Voters: []uint64{1}}}
	srv.snapshot(&ep, false)
	<-ch
	assert.Empty(t, st.Action())
	assert.Equal(t, uint64(0), ep.diskSnapshotIndex)
	assert.Equal(t, uint64(1), ep.memorySnapshotIndex)
}

// TestSnapshotOrdering ensures raft persists snapshot onto disk before
// snapshot db is applied.
func TestSnapshotOrdering(t *testing.T) {
	// Ignore the snapshot index verification in unit test, because
	// it doesn't follow the e2e applying logic.
	revertFunc := verify.DisableVerifications()
	defer revertFunc()

	lg := zaptest.NewLogger(t)
	n := newNopReadyNode()
	cl := membership.NewCluster(lg)
	be, _ := betesting.NewDefaultTmpBackend(t)
	cl.SetBackend(schema.NewMembershipBackend(lg, be))

	testdir := t.TempDir()

	snapdir := filepath.Join(testdir, "member", "snap")
	if err := os.MkdirAll(snapdir, 0o755); err != nil {
		t.Fatalf("couldn't make snap dir (%v)", err)
	}

	rs := raft.NewMemoryStorage()
	p := mockstorage.NewStorageRecorderStream(testdir)
	tr, snapDoneC := newSnapTransporter(lg, snapdir)
	r := newRaftNode(raftNodeConfig{
		lg:          lg,
		isIDRemoved: func(id uint64) bool { return cl.IsIDRemoved(types.ID(id)) },
		Node:        n,
		transport:   tr,
		storage:     p,
		raftStorage: rs,
	})
	ci := cindex.NewConsistentIndex(be)
	cfg := config.ServerConfig{
		Logger:                 lg,
		DataDir:                testdir,
		SnapshotCatchUpEntries: DefaultSnapshotCatchUpEntries,
		ServerFeatureGate:      features.NewDefaultServerFeatureGate("test", lg),
	}

	s := &EtcdServer{
		lgMu:         new(sync.RWMutex),
		lg:           lg,
		Cfg:          cfg,
		r:            *r,
		snapshotter:  snap.New(lg, snapdir),
		cluster:      cl,
		consistIndex: ci,
		beHooks:      serverstorage.NewBackendHooks(lg, ci),
	}

	s.kv = mvcc.New(lg, be, &lease.FakeLessor{}, mvcc.StoreConfig{})
	s.be = be

	s.start()
	defer s.Stop()

	n.readyc <- raft.Ready{Messages: []*raftpb.Message{{Type: raftpb.MsgSnap.Enum()}}}
	go func() {
		// get the snapshot sent by the transport
		snapMsg := <-snapDoneC
		// Snapshot first triggers raftnode to persists the snapshot onto disk
		// before renaming db snapshot file to db
		snapMsg.Snapshot.Metadata.Index = new(uint64(1))
		n.readyc <- raft.Ready{Snapshot: snapMsg.Snapshot}
	}()

	ac := <-p.Chan()
	if ac.Name != "Save" {
		t.Fatalf("expected Save, got %+v", ac)
	}

	if ac := <-p.Chan(); ac.Name != "SaveSnap" {
		t.Fatalf("expected SaveSnap, got %+v", ac)
	}

	if ac := <-p.Chan(); ac.Name != "Save" {
		t.Fatalf("expected Save, got %+v", ac)
	}

	// confirm snapshot file still present before calling SaveSnap
	snapPath := filepath.Join(snapdir, fmt.Sprintf("%016x.snap.db", 1))
	if !fileutil.Exist(snapPath) {
		t.Fatalf("expected file %q, got missing", snapPath)
	}

	// unblock SaveSnapshot, etcdserver now permitted to move snapshot file
	if ac := <-p.Chan(); ac.Name != "Sync" {
		t.Fatalf("expected Sync, got %+v", ac)
	}

	if ac := <-p.Chan(); ac.Name != "Release" {
		t.Fatalf("expected Release, got %+v", ac)
	}
}

// TestConcurrentApplyAndSnapshotV3 will send out snapshots concurrently with
// proposals.
func TestConcurrentApplyAndSnapshotV3(t *testing.T) {
	// Ignore the snapshot index verification in unit test, because
	// it doesn't follow the e2e applying logic.
	revertFunc := verify.DisableVerifications()
	defer revertFunc()

	lg := zaptest.NewLogger(t)
	n := newNopReadyNode()
	cl := membership.NewCluster(lg)
	be, _ := betesting.NewDefaultTmpBackend(t)
	cl.SetBackend(schema.NewMembershipBackend(lg, be))

	testdir := t.TempDir()
	if err := os.MkdirAll(testdir+"/member/snap", 0o755); err != nil {
		t.Fatalf("Couldn't make snap dir (%v)", err)
	}

	rs := raft.NewMemoryStorage()
	tr, snapDoneC := newSnapTransporter(lg, testdir)
	r := newRaftNode(raftNodeConfig{
		lg:          lg,
		isIDRemoved: func(id uint64) bool { return cl.IsIDRemoved(types.ID(id)) },
		Node:        n,
		transport:   tr,
		storage:     mockstorage.NewStorageRecorder(testdir),
		raftStorage: rs,
	})
	ci := cindex.NewConsistentIndex(be)
	s := &EtcdServer{
		lgMu: new(sync.RWMutex),
		lg:   lg,
		Cfg: config.ServerConfig{
			Logger:                 lg,
			DataDir:                testdir,
			SnapshotCatchUpEntries: DefaultSnapshotCatchUpEntries,
			ServerFeatureGate:      features.NewDefaultServerFeatureGate("test", lg),
		},
		r:                 *r,
		snapshotter:       snap.New(lg, testdir),
		cluster:           cl,
		consistIndex:      ci,
		beHooks:           serverstorage.NewBackendHooks(lg, ci),
		firstCommitInTerm: notify.NewNotifier(),
		lessor:            &lease.FakeLessor{},
		uberApply:         uberApplierMock{},
		authStore:         auth.NewAuthStore(lg, schema.NewAuthBackend(lg, be), nil, 1),
	}

	s.kv = mvcc.New(lg, be, &lease.FakeLessor{}, mvcc.StoreConfig{})
	s.be = be

	s.start()
	defer s.Stop()

	// submit applied entries and snap entries
	idx := uint64(0)
	outdated := 0
	accepted := 0
	for k := 1; k <= 101; k++ {
		idx++
		ch := s.w.Register(idx)
		req := &pb.InternalRaftRequest{
			Header: &pb.RequestHeader{ID: idx},
			Put:    &pb.PutRequest{Key: []byte("foo"), Value: []byte("bar")},
		}
		ent := raftpb.Entry{Index: new(idx), Data: pbutil.MustMarshalMessage(req)}
		ready := raft.Ready{Entries: []*raftpb.Entry{&ent}}
		n.readyc <- ready

		ready = raft.Ready{CommittedEntries: []*raftpb.Entry{&ent}}
		n.readyc <- ready

		// "idx" applied
		<-ch

		// one snapshot for every two messages
		if k%2 != 0 {
			continue
		}

		n.readyc <- raft.Ready{Messages: []*raftpb.Message{{Type: raftpb.MsgSnap.Enum()}}}
		// get the snapshot sent by the transport
		snapMsg := <-snapDoneC
		// If the snapshot trails applied records, recovery will panic
		// since there's no allocated snapshot at the place of the
		// snapshot record. This only happens when the applier and the
		// snapshot sender get out of sync.
		if snapMsg.Snapshot.Metadata.GetIndex() == idx {
			idx++
			snapMsg.Snapshot.Metadata.Index = new(idx)
			ready = raft.Ready{Snapshot: snapMsg.Snapshot}
			n.readyc <- ready
			accepted++
		} else {
			outdated++
		}
		// don't wait for the snapshot to complete, move to next message
	}
	if accepted != 50 {
		t.Errorf("accepted=%v, want 50", accepted)
	}
	if outdated != 0 {
		t.Errorf("outdated=%v, want 0", outdated)
	}
}

// TestAddMember tests AddMember can propose and perform node addition.
func TestAddMember(t *testing.T) {
	lg := zaptest.NewLogger(t)
	n := newNodeConfChangeCommitterRecorder()
	n.readyc <- raft.Ready{
		SoftState: &raft.SoftState{RaftState: raft.StateLeader},
	}
	cl := newTestCluster(t)
	be, _ := betesting.NewDefaultTmpBackend(t)
	defer betesting.Close(t, be)
	cl.SetBackend(schema.NewMembershipBackend(lg, be))

	r := newRaftNode(raftNodeConfig{
		lg:          lg,
		Node:        n,
		raftStorage: raft.NewMemoryStorage(),
		storage:     mockstorage.NewStorageRecorder(""),
		transport:   newNopTransporter(),
	})
	s := &EtcdServer{
		lgMu:         new(sync.RWMutex),
		lg:           lg,
		r:            *r,
		cluster:      cl,
		reqIDGen:     idutil.NewGenerator(0, time.Time{}),
		consistIndex: cindex.NewFakeConsistentIndex(0),
		beHooks:      serverstorage.NewBackendHooks(lg, nil),
	}
	s.start()
	m := membership.Member{ID: 1234, RaftAttributes: membership.RaftAttributes{PeerURLs: []string{"foo"}}}
	_, err := s.AddMember(t.Context(), m)
	gaction := n.Action()
	s.Stop()

	if err != nil {
		t.Fatalf("AddMember error: %v", err)
	}
	wactions := []testutil.Action{{Name: "ProposeConfChange:ConfChangeAddNode"}, {Name: "ApplyConfChange:ConfChangeAddNode"}}
	if !reflect.DeepEqual(gaction, wactions) {
		t.Errorf("action = %v, want %v", gaction, wactions)
	}
	if cl.Member(1234) == nil {
		t.Errorf("member with id 1234 is not added")
	}
}

// TestProcessIgnoreMismatchMessage tests Process must ignore messages to
// mismatch member.
func TestProcessIgnoreMismatchMessage(t *testing.T) {
	lg := zaptest.NewLogger(t)
	cl := newTestCluster(t)
	be, _ := betesting.NewDefaultTmpBackend(t)
	defer betesting.Close(t, be)
	cl.SetBackend(schema.NewMembershipBackend(lg, be))

	// Bootstrap a 3-node cluster, member IDs: 1 2 3.
	cl.AddMember(&membership.Member{ID: types.ID(1)}, true)
	cl.AddMember(&membership.Member{ID: types.ID(2)}, true)
	cl.AddMember(&membership.Member{ID: types.ID(3)}, true)
	// r is initialized with ID 1.
	r := realisticRaftNode(lg, 1, &raftpb.Snapshot{
		Metadata: &raftpb.SnapshotMetadata{
			Index: new(uint64(11)),
			Term:  new(uint64(11)), // Magic number.
			ConfState: &raftpb.ConfState{
				// Member ID list.
				Voters: []uint64{1, 2, 3},
			},
		},
	})
	defer r.raftNodeConfig.Stop()
	s := &EtcdServer{
		lgMu:         new(sync.RWMutex),
		lg:           lg,
		memberID:     1,
		r:            *r,
		cluster:      cl,
		reqIDGen:     idutil.NewGenerator(0, time.Time{}),
		consistIndex: cindex.NewFakeConsistentIndex(0),
		beHooks:      serverstorage.NewBackendHooks(lg, nil),
	}
	// Mock a mad switch dispatching messages to wrong node.
	m := raftpb.Message{
		Type:   raftpb.MsgHeartbeat.Enum(),
		To:     new(uint64(2)), // Wrong ID, s.MemberID() is 1.
		From:   new(uint64(3)),
		Term:   new(uint64(11)),
		Commit: new(uint64(42)), // Commit is larger than the last index 11.
	}
	if types.ID(m.GetTo()) == s.MemberID() {
		t.Fatalf("m.To (%d) is expected to mismatch s.MemberID (%d)", m.GetTo(), s.MemberID())
	}
	err := s.Process(t.Context(), &m)
	if err == nil {
		t.Fatalf("Must ignore the message and return an error")
	}
}

// TestRemoveMember tests RemoveMember can propose and perform node removal.
func TestRemoveMember(t *testing.T) {
	lg := zaptest.NewLogger(t)
	n := newNodeConfChangeCommitterRecorder()
	n.readyc <- raft.Ready{
		SoftState: &raft.SoftState{RaftState: raft.StateLeader},
	}
	cl := newTestCluster(t)
	be, _ := betesting.NewDefaultTmpBackend(t)
	defer betesting.Close(t, be)
	cl.SetBackend(schema.NewMembershipBackend(lg, be))

	cl.AddMember(&membership.Member{ID: 1234}, true)
	r := newRaftNode(raftNodeConfig{
		lg:          lg,
		Node:        n,
		raftStorage: raft.NewMemoryStorage(),
		storage:     mockstorage.NewStorageRecorder(""),
		transport:   newNopTransporter(),
	})
	s := &EtcdServer{
		lgMu:         new(sync.RWMutex),
		lg:           zaptest.NewLogger(t),
		r:            *r,
		cluster:      cl,
		reqIDGen:     idutil.NewGenerator(0, time.Time{}),
		consistIndex: cindex.NewFakeConsistentIndex(0),
		beHooks:      serverstorage.NewBackendHooks(lg, nil),
	}
	s.start()
	_, err := s.RemoveMember(t.Context(), 1234)
	gaction := n.Action()
	s.Stop()

	if err != nil {
		t.Fatalf("RemoveMember error: %v", err)
	}
	wactions := []testutil.Action{{Name: "ProposeConfChange:ConfChangeRemoveNode"}, {Name: "ApplyConfChange:ConfChangeRemoveNode"}}
	if !reflect.DeepEqual(gaction, wactions) {
		t.Errorf("action = %v, want %v", gaction, wactions)
	}
	if cl.Member(1234) != nil {
		t.Errorf("member with id 1234 is not removed")
	}
}

// TestUpdateMember tests RemoveMember can propose and perform node update.
func TestUpdateMember(t *testing.T) {
	lg := zaptest.NewLogger(t)
	be, _ := betesting.NewDefaultTmpBackend(t)
	defer betesting.Close(t, be)
	n := newNodeConfChangeCommitterRecorder()
	n.readyc <- raft.Ready{
		SoftState: &raft.SoftState{RaftState: raft.StateLeader},
	}
	cl := newTestCluster(t)
	cl.SetBackend(schema.NewMembershipBackend(lg, be))
	cl.AddMember(&membership.Member{ID: 1234}, true)
	r := newRaftNode(raftNodeConfig{
		lg:          lg,
		Node:        n,
		raftStorage: raft.NewMemoryStorage(),
		storage:     mockstorage.NewStorageRecorder(""),
		transport:   newNopTransporter(),
	})
	s := &EtcdServer{
		lgMu:         new(sync.RWMutex),
		lg:           lg,
		r:            *r,
		cluster:      cl,
		reqIDGen:     idutil.NewGenerator(0, time.Time{}),
		consistIndex: cindex.NewFakeConsistentIndex(0),
		beHooks:      serverstorage.NewBackendHooks(lg, nil),
	}
	s.start()
	wm := membership.Member{ID: 1234, RaftAttributes: membership.RaftAttributes{PeerURLs: []string{"http://127.0.0.1:1"}}}
	_, err := s.UpdateMember(t.Context(), wm)
	gaction := n.Action()
	s.Stop()

	if err != nil {
		t.Fatalf("UpdateMember error: %v", err)
	}
	wactions := []testutil.Action{{Name: "ProposeConfChange:ConfChangeUpdateNode"}, {Name: "ApplyConfChange:ConfChangeUpdateNode"}}
	if !reflect.DeepEqual(gaction, wactions) {
		t.Errorf("action = %v, want %v", gaction, wactions)
	}
	if !reflect.DeepEqual(cl.Member(1234), &wm) {
		t.Errorf("member = %v, want %v", cl.Member(1234), &wm)
	}
}

// TODO: test server could stop itself when being removed

func TestPublishV3(t *testing.T) {
	n := newNodeRecorder()
	ch := make(chan any, 1)
	// simulate that request has gone through consensus
	ch <- &apply2.Result{}
	w := wait.NewWithResponse(ch)
	ctx, cancel := context.WithCancel(t.Context())
	lg := zaptest.NewLogger(t)
	be, _ := betesting.NewDefaultTmpBackend(t)
	defer betesting.Close(t, be)
	srv := &EtcdServer{
		lgMu:       new(sync.RWMutex),
		lg:         lg,
		readych:    make(chan struct{}),
		Cfg:        config.ServerConfig{Logger: lg, TickMs: 1, SnapshotCatchUpEntries: DefaultSnapshotCatchUpEntries, MaxRequestBytes: 1000, ServerFeatureGate: features.NewDefaultServerFeatureGate("test", lg)},
		memberID:   1,
		r:          *newRaftNode(raftNodeConfig{lg: lg, Node: n, storage: mockstorage.NewStorageRecorder("")}),
		attributes: membership.Attributes{Name: "node1", ClientURLs: []string{"http://a", "http://b"}},
		cluster:    &membership.RaftCluster{},
		w:          w,
		reqIDGen:   idutil.NewGenerator(0, time.Time{}),
		authStore:  auth.NewAuthStore(lg, schema.NewAuthBackend(lg, be), nil, 0),
		be:         be,
		ctx:        ctx,
		cancel:     cancel,
	}
	srv.publishV3(time.Hour)

	action := n.Action()
	if len(action) != 1 {
		t.Fatalf("len(action) = %d, want 1", len(action))
	}
	if action[0].Name != "Propose" {
		t.Fatalf("action = %s, want Propose", action[0].Name)
	}
	data := action[0].Params[0].([]byte)
	var r pb.InternalRaftRequest
	if err := proto.Unmarshal(data, &r); err != nil {
		t.Fatalf("unmarshal request error: %v", err)
	}
	assert.Equal(t, &membershippb.ClusterMemberAttrSetRequest{Member_ID: 0x1, MemberAttributes: &membershippb.Attributes{
		Name: "node1", ClientUrls: []string{"http://a", "http://b"},
	}}, r.ClusterMemberAttrSet)
}

// TestPublishV3Stopped tests that publish will be stopped if server is stopped.
func TestPublishV3Stopped(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	r := newRaftNode(raftNodeConfig{
		lg:        zaptest.NewLogger(t),
		Node:      newNodeNop(),
		storage:   mockstorage.NewStorageRecorder(""),
		transport: newNopTransporter(),
	})
	srv := &EtcdServer{
		lgMu:     new(sync.RWMutex),
		lg:       zaptest.NewLogger(t),
		Cfg:      config.ServerConfig{Logger: zaptest.NewLogger(t), TickMs: 1, SnapshotCatchUpEntries: DefaultSnapshotCatchUpEntries, ServerFeatureGate: features.NewDefaultServerFeatureGate("test", nil)},
		r:        *r,
		cluster:  &membership.RaftCluster{},
		w:        mockwait.NewNop(),
		done:     make(chan struct{}),
		stopping: make(chan struct{}),
		stop:     make(chan struct{}),
		reqIDGen: idutil.NewGenerator(0, time.Time{}),

		ctx:    ctx,
		cancel: cancel,
	}
	close(srv.stopping)
	srv.publishV3(time.Hour)
}

// TestPublishV3Retry tests that publish will keep retry until success.
func TestPublishV3Retry(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	n := newNodeRecorderStream()

	lg := zaptest.NewLogger(t)
	be, _ := betesting.NewDefaultTmpBackend(t)
	defer betesting.Close(t, be)
	srv := &EtcdServer{
		lgMu:       new(sync.RWMutex),
		lg:         lg,
		readych:    make(chan struct{}),
		Cfg:        config.ServerConfig{Logger: lg, TickMs: 1, SnapshotCatchUpEntries: DefaultSnapshotCatchUpEntries, MaxRequestBytes: 1000, ServerFeatureGate: features.NewDefaultServerFeatureGate("test", lg)},
		memberID:   1,
		r:          *newRaftNode(raftNodeConfig{lg: lg, Node: n, storage: mockstorage.NewStorageRecorder("")}),
		w:          mockwait.NewNop(),
		stopping:   make(chan struct{}),
		attributes: membership.Attributes{Name: "node1", ClientURLs: []string{"http://a", "http://b"}},
		cluster:    &membership.RaftCluster{},
		reqIDGen:   idutil.NewGenerator(0, time.Time{}),
		authStore:  auth.NewAuthStore(lg, schema.NewAuthBackend(lg, be), nil, 0),
		be:         be,
		ctx:        ctx,
		cancel:     cancel,
	}

	// expect multiple proposals from retrying
	ch := make(chan struct{})
	go func() {
		defer close(ch)
		if action, err := n.Wait(2); err != nil {
			t.Errorf("len(action) = %d, want >= 2 (%v)", len(action), err)
		}
		close(srv.stopping)
		// drain remaining actions, if any, so publish can terminate
		for {
			select {
			case <-ch:
				return
			default:
				n.Action()
			}
		}
	}()
	srv.publishV3(10 * time.Nanosecond)
	ch <- struct{}{}
	<-ch
}

// TestPublishV3WaitsForApplyAfterTooManyRequests ensures that a member whose
// applied index lags its committed index past the request limit does not
// retry its rejected publish in a tight loop, and proposes once it has
// applied enough entries.
func TestPublishV3WaitsForApplyAfterTooManyRequests(t *testing.T) {
	n := newNodeRecorder()
	ch := make(chan any, 1)
	ch <- &apply2.Result{}
	ctx, cancel := context.WithCancel(t.Context())
	core, logs := observer.New(zap.WarnLevel)
	lg := zap.New(core)
	be, _ := betesting.NewDefaultTmpBackend(t)
	defer betesting.Close(t, be)
	srv := &EtcdServer{
		lgMu:       new(sync.RWMutex),
		lg:         lg,
		readych:    make(chan struct{}),
		Cfg:        config.ServerConfig{Logger: lg, TickMs: 1, SnapshotCatchUpEntries: DefaultSnapshotCatchUpEntries, MaxRequestBytes: 1000, ServerFeatureGate: features.NewDefaultServerFeatureGate("test", lg)},
		memberID:   1,
		r:          *newRaftNode(raftNodeConfig{lg: lg, Node: n, storage: mockstorage.NewStorageRecorder("")}),
		w:          wait.NewWithResponse(ch),
		applyWait:  wait.NewTimeList(),
		stopping:   make(chan struct{}),
		attributes: membership.Attributes{Name: "node1", ClientURLs: []string{"http://a"}},
		cluster:    &membership.RaftCluster{},
		reqIDGen:   idutil.NewGenerator(0, time.Time{}),
		authStore:  auth.NewAuthStore(lg, schema.NewAuthBackend(lg, be), nil, 0),
		be:         be,
		ctx:        ctx,
		cancel:     cancel,
	}
	const committed = 3 * maxGapBetweenApplyAndCommitIndex
	srv.setCommittedIndex(committed)

	donec := make(chan struct{})
	go func() {
		defer close(donec)
		srv.publishV3(time.Hour)
	}()

	// The first attempt is rejected; publish must then wait for apply.
	require.Eventually(t, func() bool { return logs.Len() > 0 }, 5*time.Second, time.Millisecond)
	time.Sleep(100 * time.Millisecond)
	require.Equal(t, 1, logs.FilterMessage("failed to publish local member to cluster through raft").Len())
	require.Empty(t, n.Action())

	// Applying up to the limit lets the retry through.
	srv.setAppliedIndex(committed - maxGapBetweenApplyAndCommitIndex)
	srv.applyWait.Trigger(committed - maxGapBetweenApplyAndCommitIndex)
	select {
	case <-donec:
	case <-time.After(5 * time.Second):
		t.Fatal("publish did not finish after apply caught up")
	}
	action := n.Action()
	require.Len(t, action, 1)
	require.Equal(t, "Propose", action[0].Name)
	require.Equal(t, 1, logs.FilterMessage("failed to publish local member to cluster through raft").Len())
}

func TestUpdateVersionV3(t *testing.T) {
	n := newNodeRecorder()
	ch := make(chan any, 1)
	// simulate that request has gone through consensus
	ch <- &apply2.Result{}
	w := wait.NewWithResponse(ch)
	ctx, cancel := context.WithCancel(t.Context())
	lg := zaptest.NewLogger(t)
	be, _ := betesting.NewDefaultTmpBackend(t)
	defer betesting.Close(t, be)
	srv := &EtcdServer{
		lgMu:       new(sync.RWMutex),
		lg:         zaptest.NewLogger(t),
		memberID:   1,
		Cfg:        config.ServerConfig{Logger: lg, TickMs: 1, SnapshotCatchUpEntries: DefaultSnapshotCatchUpEntries, MaxRequestBytes: 1000, ServerFeatureGate: features.NewDefaultServerFeatureGate("test", lg)},
		r:          *newRaftNode(raftNodeConfig{lg: zaptest.NewLogger(t), Node: n, storage: mockstorage.NewStorageRecorder("")}),
		attributes: membership.Attributes{Name: "node1", ClientURLs: []string{"http://node1.com"}},
		cluster:    &membership.RaftCluster{},
		w:          w,
		reqIDGen:   idutil.NewGenerator(0, time.Time{}),
		authStore:  auth.NewAuthStore(lg, schema.NewAuthBackend(lg, be), nil, 0),
		be:         be,

		ctx:    ctx,
		cancel: cancel,
	}
	ver := "2.0.0"
	srv.updateClusterVersionV3(ver)

	action := n.Action()
	if len(action) != 1 {
		t.Fatalf("len(action) = %d, want 1", len(action))
	}
	if action[0].Name != "Propose" {
		t.Fatalf("action = %s, want Propose", action[0].Name)
	}
	data := action[0].Params[0].([]byte)
	var r pb.InternalRaftRequest
	if err := proto.Unmarshal(data, &r); err != nil {
		t.Fatalf("unmarshal request error: %v", err)
	}
	assert.Equal(t, &membershippb.ClusterVersionSetRequest{Ver: ver}, r.ClusterVersionSet)
}

func TestStopNotify(t *testing.T) {
	s := &EtcdServer{
		lgMu: new(sync.RWMutex),
		lg:   zaptest.NewLogger(t),
		stop: make(chan struct{}),
		done: make(chan struct{}),
	}
	go func() {
		<-s.stop
		close(s.done)
	}()

	notifier := s.StopNotify()
	select {
	case <-notifier:
		t.Fatalf("received unexpected stop notification")
	default:
	}
	s.Stop()
	select {
	case <-notifier:
	default:
		t.Fatalf("cannot receive stop notification")
	}
}

func TestGetOtherPeerURLs(t *testing.T) {
	lg := zaptest.NewLogger(t)
	tests := []struct {
		membs []*membership.Member
		wurls []string
	}{
		{
			[]*membership.Member{
				membership.NewMember("1", types.MustNewURLs([]string{"http://10.0.0.1:1"}), "a", nil),
			},
			[]string{},
		},
		{
			[]*membership.Member{
				membership.NewMember("1", types.MustNewURLs([]string{"http://10.0.0.1:1"}), "a", nil),
				membership.NewMember("2", types.MustNewURLs([]string{"http://10.0.0.2:2"}), "a", nil),
				membership.NewMember("3", types.MustNewURLs([]string{"http://10.0.0.3:3"}), "a", nil),
			},
			[]string{"http://10.0.0.2:2", "http://10.0.0.3:3"},
		},
		{
			[]*membership.Member{
				membership.NewMember("1", types.MustNewURLs([]string{"http://10.0.0.1:1"}), "a", nil),
				membership.NewMember("3", types.MustNewURLs([]string{"http://10.0.0.3:3"}), "a", nil),
				membership.NewMember("2", types.MustNewURLs([]string{"http://10.0.0.2:2"}), "a", nil),
			},
			[]string{"http://10.0.0.2:2", "http://10.0.0.3:3"},
		},
	}
	for i, tt := range tests {
		cl := membership.NewClusterFromMembers(lg, types.ID(0), tt.membs)
		self := "1"
		urls := getRemotePeerURLs(cl, self)
		if !reflect.DeepEqual(urls, tt.wurls) {
			t.Errorf("#%d: urls = %+v, want %+v", i, urls, tt.wurls)
		}
	}
}

type nodeRecorder struct{ testutil.Recorder }

func newNodeRecorder() *nodeRecorder       { return &nodeRecorder{&testutil.RecorderBuffered{}} }
func newNodeRecorderStream() *nodeRecorder { return &nodeRecorder{testutil.NewRecorderStream()} }
func newNodeNop() raft.Node                { return newNodeRecorder() }

func (n *nodeRecorder) Tick() { n.Record(testutil.Action{Name: "Tick"}) }
func (n *nodeRecorder) Campaign(ctx context.Context) error {
	n.Record(testutil.Action{Name: "Campaign"})
	return nil
}

func (n *nodeRecorder) Propose(ctx context.Context, data []byte) error {
	n.Record(testutil.Action{Name: "Propose", Params: []any{data}})
	return nil
}

func (n *nodeRecorder) ProposeConfChange(ctx context.Context, conf raftpb.ConfChangeI) error {
	n.Record(testutil.Action{Name: "ProposeConfChange"})
	return nil
}

func (n *nodeRecorder) Step(ctx context.Context, msg *raftpb.Message) error {
	n.Record(testutil.Action{Name: "Step"})
	return nil
}
func (n *nodeRecorder) Status() raft.Status                                             { return raft.Status{} }
func (n *nodeRecorder) Ready() <-chan raft.Ready                                        { return nil }
func (n *nodeRecorder) TransferLeadership(ctx context.Context, lead, transferee uint64) {}
func (n *nodeRecorder) ReadIndex(ctx context.Context, rctx []byte) error                { return nil }
func (n *nodeRecorder) Advance()                                                        {}
func (n *nodeRecorder) ApplyConfChange(conf raftpb.ConfChangeI) *raftpb.ConfState {
	n.Record(testutil.Action{Name: "ApplyConfChange", Params: []any{conf}})
	return &raftpb.ConfState{}
}

func (n *nodeRecorder) Stop() {
	n.Record(testutil.Action{Name: "Stop"})
}

func (n *nodeRecorder) ReportUnreachable(id uint64) {}

func (n *nodeRecorder) ReportSnapshot(id uint64, status raft.SnapshotStatus) {}

func (n *nodeRecorder) Compact(index uint64, nodes []uint64, d []byte) {
	n.Record(testutil.Action{Name: "Compact"})
}

func (n *nodeRecorder) ForgetLeader(ctx context.Context) error {
	return nil
}

// readyNode is a nodeRecorder with a user-writeable ready channel
type readyNode struct {
	nodeRecorder
	readyc chan raft.Ready
}

func newReadyNode() *readyNode {
	return &readyNode{
		nodeRecorder{testutil.NewRecorderStream()},
		make(chan raft.Ready, 1),
	}
}

func newNopReadyNode() *readyNode {
	return &readyNode{*newNodeRecorder(), make(chan raft.Ready, 1)}
}

func (n *readyNode) Ready() <-chan raft.Ready { return n.readyc }

type nodeConfChangeCommitterRecorder struct {
	readyNode
	index uint64
}

func newNodeConfChangeCommitterRecorder() *nodeConfChangeCommitterRecorder {
	return &nodeConfChangeCommitterRecorder{*newNopReadyNode(), 0}
}

func newNodeConfChangeCommitterStream() *nodeConfChangeCommitterRecorder {
	return &nodeConfChangeCommitterRecorder{*newReadyNode(), 0}
}

func confChangeActionName(conf raftpb.ConfChangeI) string {
	var s string
	if confV1, ok := conf.AsV1(); ok {
		s = confV1.Type.String()
	} else {
		for i, chg := range conf.AsV2().Changes {
			if i > 0 {
				s += "/"
			}
			s += chg.Type.String()
		}
	}
	return s
}

func (n *nodeConfChangeCommitterRecorder) ProposeConfChange(ctx context.Context, conf raftpb.ConfChangeI) error {
	typ, data, err := raftpb.MarshalConfChange(conf)
	if err != nil {
		return err
	}

	n.index++
	n.Record(testutil.Action{Name: "ProposeConfChange:" + confChangeActionName(conf)})
	n.readyc <- raft.Ready{CommittedEntries: []*raftpb.Entry{{Index: new(n.index), Type: &typ, Data: data}}}
	return nil
}

func (n *nodeConfChangeCommitterRecorder) Ready() <-chan raft.Ready {
	return n.readyc
}

func (n *nodeConfChangeCommitterRecorder) ApplyConfChange(conf raftpb.ConfChangeI) *raftpb.ConfState {
	n.Record(testutil.Action{Name: "ApplyConfChange:" + confChangeActionName(conf)})
	return &raftpb.ConfState{}
}

func newTestCluster(tb testing.TB) *membership.RaftCluster {
	return membership.NewCluster(zaptest.NewLogger(tb))
}

type nopTransporter struct{}

func newNopTransporter() rafthttp.Transporter {
	return &nopTransporter{}
}

func (s *nopTransporter) Start() error                        { return nil }
func (s *nopTransporter) Handler() http.Handler               { return nil }
func (s *nopTransporter) Send(m []*raftpb.Message)            {}
func (s *nopTransporter) SendSnapshot(m *snap.Message)        {}
func (s *nopTransporter) AddRemote(id types.ID, us []string)  {}
func (s *nopTransporter) AddPeer(id types.ID, us []string)    {}
func (s *nopTransporter) RemovePeer(id types.ID)              {}
func (s *nopTransporter) RemoveAllPeers()                     {}
func (s *nopTransporter) UpdatePeer(id types.ID, us []string) {}
func (s *nopTransporter) ActiveSince(id types.ID) time.Time   { return time.Time{} }
func (s *nopTransporter) ActivePeers() int                    { return 0 }
func (s *nopTransporter) Stop()                               {}
func (s *nopTransporter) Pause()                              {}
func (s *nopTransporter) Resume()                             {}

type snapTransporter struct {
	nopTransporter
	snapDoneC chan *snap.Message
	snapDir   string
	lg        *zap.Logger
}

func newSnapTransporter(lg *zap.Logger, snapDir string) (rafthttp.Transporter, <-chan *snap.Message) {
	ch := make(chan *snap.Message, 1)
	tr := &snapTransporter{snapDoneC: ch, snapDir: snapDir, lg: lg}
	return tr, ch
}

func (s *snapTransporter) SendSnapshot(m *snap.Message) {
	ss := snap.New(s.lg, s.snapDir)
	ss.SaveDBFrom(m.ReadCloser, m.Snapshot.Metadata.GetIndex()+1)
	m.CloseWithError(nil)
	s.snapDoneC <- m
}

type sendMsgAppRespTransporter struct {
	nopTransporter
	sendC chan int
}

func newSendMsgAppRespTransporter() (rafthttp.Transporter, <-chan int) {
	ch := make(chan int, 1)
	tr := &sendMsgAppRespTransporter{sendC: ch}
	return tr, ch
}

func (s *sendMsgAppRespTransporter) Send(m []*raftpb.Message) {
	var send int
	for _, msg := range m {
		if msg.GetTo() != 0 {
			send++
		}
	}
	s.sendC <- send
}

func TestWaitAppliedIndex(t *testing.T) {
	cases := []struct {
		name           string
		appliedIndex   uint64
		committedIndex uint64
		action         func(s *EtcdServer)
		ExpectedError  error
	}{
		{
			name:           "The applied Id is already equal to the commitId",
			appliedIndex:   10,
			committedIndex: 10,
			action: func(s *EtcdServer) {
				s.applyWait.Trigger(10)
			},
			ExpectedError: nil,
		},
		{
			name:           "The etcd server has already stopped",
			appliedIndex:   10,
			committedIndex: 12,
			action: func(s *EtcdServer) {
				s.stopping <- struct{}{}
			},
			ExpectedError: errors.ErrStopped,
		},
		{
			name:           "Timed out waiting for the applied index",
			appliedIndex:   10,
			committedIndex: 12,
			action:         nil,
			ExpectedError:  errors.ErrTimeoutWaitAppliedIndex,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := &EtcdServer{
				stopping:  make(chan struct{}, 1),
				applyWait: wait.NewTimeList(),
			}
			s.appliedIndex.Store(tc.appliedIndex)
			s.committedIndex.Store(tc.committedIndex)
			if tc.action != nil {
				go tc.action(s)
			}

			err := s.waitAppliedIndex()

			if !errorspkg.Is(err, tc.ExpectedError) {
				t.Errorf("Unexpected error, want (%v), got (%v)", tc.ExpectedError, err)
			}
		})
	}
}

func TestIsActive(t *testing.T) {
	cases := []struct {
		name                  string
		tickMs                uint
		durationSinceLastTick time.Duration
		expectActive          bool
	}{
		{
			name:                  "1.5*tickMs,active",
			tickMs:                100,
			durationSinceLastTick: 150 * time.Millisecond,
			expectActive:          true,
		},
		{
			name:                  "2*tickMs,active",
			tickMs:                200,
			durationSinceLastTick: 400 * time.Millisecond,
			expectActive:          true,
		},
		{
			name:                  "4*tickMs,not active",
			tickMs:                150,
			durationSinceLastTick: 600 * time.Millisecond,
			expectActive:          false,
		},
	}

	for _, tc := range cases {
		s := EtcdServer{
			Cfg: config.ServerConfig{
				TickMs: tc.tickMs,
			},
			r: raftNode{
				tickMu:       new(sync.RWMutex),
				latestTickTs: time.Now().Add(-tc.durationSinceLastTick),
			},
		}

		require.Equal(t, tc.expectActive, s.isActive())
	}
}

func TestAddFeatureGateMetrics(t *testing.T) {
	const testAlphaGate featuregate.Feature = "TestAlpha"
	const testBetaGate featuregate.Feature = "TestBeta"
	const testGAGate featuregate.Feature = "TestGA"

	featuremap := map[featuregate.Feature]featuregate.FeatureSpec{
		testGAGate:    {Default: true, PreRelease: featuregate.GA},
		testAlphaGate: {Default: true, PreRelease: featuregate.Alpha},
		testBetaGate:  {Default: false, PreRelease: featuregate.Beta},
	}
	fg := featuregate.New("test", zaptest.NewLogger(t))
	fg.Add(featuremap)

	addFeatureGateMetrics(fg, serverFeatureEnabled)

	expected := `# HELP etcd_server_feature_enabled Whether or not a feature is enabled. 1 is enabled, 0 is not.
	# TYPE etcd_server_feature_enabled gauge
	etcd_server_feature_enabled{name="AllAlpha",stage="ALPHA"} 0
	etcd_server_feature_enabled{name="AllBeta",stage="BETA"} 0
	etcd_server_feature_enabled{name="TestAlpha",stage="ALPHA"} 1
	etcd_server_feature_enabled{name="TestBeta",stage="BETA"} 0
	etcd_server_feature_enabled{name="TestGA",stage=""} 1
	`
	err := ptestutil.GatherAndCompare(prometheus.DefaultGatherer, strings.NewReader(expected), "etcd_server_feature_enabled")
	require.NoErrorf(t, err, "unexpected metric collection result: \n%s", err)
}
