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
	"encoding/json"
	"expvar"
	"reflect"
	"sync"
	"testing"
	"time"

	"go.etcd.io/etcd/client/pkg/v3/types"
	"go.etcd.io/etcd/pkg/v3/pbutil"
	"go.etcd.io/etcd/raft/v3"
	"go.etcd.io/etcd/raft/v3/raftpb"
	"go.etcd.io/etcd/server/v3/etcdserver/api/membership"
	"go.etcd.io/etcd/server/v3/mock/mockstorage"
	"go.uber.org/zap"
)

func TestGetIDs(t *testing.T) {
	addcc := &raftpb.ConfChange{Type: raftpb.ConfChangeAddNode, NodeID: 2}
	addEntry := raftpb.Entry{Type: raftpb.EntryConfChange, Data: pbutil.MustMarshal(addcc)}
	removecc := &raftpb.ConfChange{Type: raftpb.ConfChangeRemoveNode, NodeID: 2}
	removeEntry := raftpb.Entry{Type: raftpb.EntryConfChange, Data: pbutil.MustMarshal(removecc)}
	normalEntry := raftpb.Entry{Type: raftpb.EntryNormal}
	updatecc := &raftpb.ConfChange{Type: raftpb.ConfChangeUpdateNode, NodeID: 2}
	updateEntry := raftpb.Entry{Type: raftpb.EntryConfChange, Data: pbutil.MustMarshal(updatecc)}

	tests := []struct {
		confState *raftpb.ConfState
		ents      []raftpb.Entry

		widSet []uint64
	}{
		{nil, []raftpb.Entry{}, []uint64{}},
		{&raftpb.ConfState{Voters: []uint64{1}},
			[]raftpb.Entry{}, []uint64{1}},
		{&raftpb.ConfState{Voters: []uint64{1}},
			[]raftpb.Entry{addEntry}, []uint64{1, 2}},
		{&raftpb.ConfState{Voters: []uint64{1}},
			[]raftpb.Entry{addEntry, removeEntry}, []uint64{1}},
		{&raftpb.ConfState{Voters: []uint64{1}},
			[]raftpb.Entry{addEntry, normalEntry}, []uint64{1, 2}},
		{&raftpb.ConfState{Voters: []uint64{1}},
			[]raftpb.Entry{addEntry, normalEntry, updateEntry}, []uint64{1, 2}},
		{&raftpb.ConfState{Voters: []uint64{1}},
			[]raftpb.Entry{addEntry, removeEntry, normalEntry}, []uint64{1}},
	}

	for i, tt := range tests {
		var snap raftpb.Snapshot
		if tt.confState != nil {
			snap.Metadata.ConfState = *tt.confState
		}
		idSet, _ := getIDs(testLogger, &snap, tt.ents)
		if !reflect.DeepEqual(idSet, tt.widSet) {
			t.Errorf("#%d: idset = %#v, want %#v", i, idSet, tt.widSet)
		}
	}
}

func TestCreateConfigChangeEnts(t *testing.T) {
	m := membership.Member{
		ID:             types.ID(1),
		RaftAttributes: membership.RaftAttributes{PeerURLs: []string{"http://localhost:2380"}},
	}
	ctx, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	addcc1 := &raftpb.ConfChange{Type: raftpb.ConfChangeAddNode, NodeID: 1, Context: ctx}
	removecc2 := &raftpb.ConfChange{Type: raftpb.ConfChangeRemoveNode, NodeID: 2}
	removecc3 := &raftpb.ConfChange{Type: raftpb.ConfChangeRemoveNode, NodeID: 3}
	tests := []struct {
		ids         []uint64
		self        uint64
		term, index uint64

		wents []raftpb.Entry
	}{
		{
			[]uint64{1},
			1,
			1, 1,

			nil,
		},
		{
			[]uint64{1, 2},
			1,
			1, 1,

			[]raftpb.Entry{{Term: 1, Index: 2, Type: raftpb.EntryConfChange, Data: pbutil.MustMarshal(removecc2)}},
		},
		{
			[]uint64{1, 2},
			1,
			2, 2,

			[]raftpb.Entry{{Term: 2, Index: 3, Type: raftpb.EntryConfChange, Data: pbutil.MustMarshal(removecc2)}},
		},
		{
			[]uint64{1, 2, 3},
			1,
			2, 2,

			[]raftpb.Entry{
				{Term: 2, Index: 3, Type: raftpb.EntryConfChange, Data: pbutil.MustMarshal(removecc2)},
				{Term: 2, Index: 4, Type: raftpb.EntryConfChange, Data: pbutil.MustMarshal(removecc3)},
			},
		},
		{
			[]uint64{2, 3},
			2,
			2, 2,

			[]raftpb.Entry{
				{Term: 2, Index: 3, Type: raftpb.EntryConfChange, Data: pbutil.MustMarshal(removecc3)},
			},
		},
		{
			[]uint64{2, 3},
			1,
			2, 2,

			[]raftpb.Entry{
				{Term: 2, Index: 3, Type: raftpb.EntryConfChange, Data: pbutil.MustMarshal(addcc1)},
				{Term: 2, Index: 4, Type: raftpb.EntryConfChange, Data: pbutil.MustMarshal(removecc2)},
				{Term: 2, Index: 5, Type: raftpb.EntryConfChange, Data: pbutil.MustMarshal(removecc3)},
			},
		},
	}

	for i, tt := range tests {
		gents := createConfigChangeEnts(testLogger, tt.ids, make(map[uint64]bool), tt.self, tt.term, tt.index)
		if !reflect.DeepEqual(gents, tt.wents) {
			t.Errorf("#%d: ents = %v, want %v", i, gents, tt.wents)
		}
	}
}

func TestStopRaftWhenWaitingForApplyDone(t *testing.T) {
	n := newNopReadyNode()
	r := newRaftNode(raftNodeConfig{
		lg:          zap.NewExample(),
		Node:        n,
		storage:     mockstorage.NewStorageRecorder(""),
		raftStorage: raft.NewMemoryStorage(),
		transport:   newNopTransporter(),
	})
	srv := &EtcdServer{lgMu: new(sync.RWMutex), lg: zap.NewExample(), r: *r}
	srv.r.start(nil)
	n.readyc <- raft.Ready{}
	select {
	case <-srv.r.applyc:
	case <-time.After(time.Second):
		t.Fatalf("failed to receive apply struct")
	}

	srv.r.stopped <- struct{}{}
	select {
	case <-srv.r.done:
	case <-time.After(time.Second):
		t.Fatalf("failed to stop raft loop")
	}
}

// TestConfigChangeBlocksApply ensures apply blocks if committed entries contain config-change.
func TestConfigChangeBlocksApply(t *testing.T) {
	n := newNopReadyNode()

	r := newRaftNode(raftNodeConfig{
		lg:          zap.NewExample(),
		Node:        n,
		storage:     mockstorage.NewStorageRecorder(""),
		raftStorage: raft.NewMemoryStorage(),
		transport:   newNopTransporter(),
	})
	srv := &EtcdServer{lgMu: new(sync.RWMutex), lg: zap.NewExample(), r: *r}

	srv.r.start(&raftReadyHandler{
		getLead:          func() uint64 { return 0 },
		updateLead:       func(uint64) {},
		updateLeadership: func(bool) {},
	})
	defer srv.r.Stop()

	n.readyc <- raft.Ready{
		SoftState:        &raft.SoftState{RaftState: raft.StateFollower},
		CommittedEntries: []raftpb.Entry{{Type: raftpb.EntryConfChange}},
	}
	ap := <-srv.r.applyc

	continueC := make(chan struct{})
	go func() {
		n.readyc <- raft.Ready{}
		<-srv.r.applyc
		close(continueC)
	}()

	select {
	case <-continueC:
		t.Fatalf("unexpected execution: raft routine should block waiting for apply")
	case <-time.After(time.Second):
	}

	// finish apply, unblock raft routine
	<-ap.notifyc

	select {
	case <-continueC:
	case <-time.After(time.Second):
		t.Fatalf("unexpected blocking on execution")
	}
}

func TestProcessDuplicatedAppRespMessage(t *testing.T) {
	n := newNopReadyNode()
	cl := membership.NewCluster(zap.NewExample())

	rs := raft.NewMemoryStorage()
	p := mockstorage.NewStorageRecorder("")
	tr, sendc := newSendMsgAppRespTransporter()
	r := newRaftNode(raftNodeConfig{
		lg:          zap.NewExample(),
		isIDRemoved: func(id uint64) bool { return cl.IsIDRemoved(types.ID(id)) },
		Node:        n,
		transport:   tr,
		storage:     p,
		raftStorage: rs,
	})

	s := &EtcdServer{
		lgMu:       new(sync.RWMutex),
		lg:         zap.NewExample(),
		r:          *r,
		cluster:    cl,
		SyncTicker: &time.Ticker{},
	}

	s.start()
	defer s.Stop()

	lead := uint64(1)

	n.readyc <- raft.Ready{Messages: []raftpb.Message{
		{Type: raftpb.MsgAppResp, From: 2, To: lead, Term: 1, Index: 1},
		{Type: raftpb.MsgAppResp, From: 2, To: lead, Term: 1, Index: 2},
		{Type: raftpb.MsgAppResp, From: 2, To: lead, Term: 1, Index: 3},
	}}

	got, want := <-sendc, 1
	if got != want {
		t.Errorf("count = %d, want %d", got, want)
	}
}

// Test that none of the expvars that get added during init panic.
// This matters if another package imports etcdserver,
// doesn't use it, but does use expvars.
func TestExpvarWithNoRaftStatus(t *testing.T) {
	defer func() {
		if err := recover(); err != nil {
			t.Fatal(err)
		}
	}()
	expvar.Do(func(kv expvar.KeyValue) {
		_ = kv.Value.String()
	})
}
