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
	"os"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest"

	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	"go.etcd.io/etcd/client/pkg/v3/types"
	"go.etcd.io/etcd/pkg/v3/pbutil"
	"go.etcd.io/etcd/raft/v3"
	"go.etcd.io/etcd/raft/v3/raftpb"
	"go.etcd.io/etcd/server/v3/config"
	"go.etcd.io/etcd/server/v3/datadir"
	"go.etcd.io/etcd/server/v3/etcdserver/api/membership"
	"go.etcd.io/etcd/server/v3/etcdserver/cindex"
	"go.etcd.io/etcd/server/v3/mock/mockstorage"
	"go.etcd.io/etcd/server/v3/wal"
	"go.etcd.io/etcd/server/v3/wal/walpb"
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
		idSet := getIDs(testLogger, &snap, tt.ents)
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
		gents := createConfigChangeEnts(testLogger, tt.ids, tt.self, tt.term, tt.index)
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
	defer srv.r.stop()

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

// TestExpvarWithNoRaftStatus to test that none of the expvars that get added during init panic.
// This matters if another package imports etcdserver, doesn't use it, but does use expvars.
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

func TestShouldWaitWALSync(t *testing.T) {
	testcases := []struct {
		name            string
		unstableEntries []raftpb.Entry
		commitedEntries []raftpb.Entry
		expectedResult  bool
	}{
		{
			name:            "both entries are nil",
			unstableEntries: nil,
			commitedEntries: nil,
			expectedResult:  false,
		},
		{
			name:            "both entries are empty slices",
			unstableEntries: []raftpb.Entry{},
			commitedEntries: []raftpb.Entry{},
			expectedResult:  false,
		},
		{
			name:            "one nil and the other empty",
			unstableEntries: nil,
			commitedEntries: []raftpb.Entry{},
			expectedResult:  false,
		},
		{
			name:            "one nil and the other has data",
			unstableEntries: nil,
			commitedEntries: []raftpb.Entry{{Term: 4, Index: 10, Type: raftpb.EntryNormal, Data: []byte{0x11, 0x22, 0x33}}},
			expectedResult:  false,
		},
		{
			name:            "one empty and the other has data",
			unstableEntries: []raftpb.Entry{},
			commitedEntries: []raftpb.Entry{{Term: 4, Index: 10, Type: raftpb.EntryNormal, Data: []byte{0x11, 0x22, 0x33}}},
			expectedResult:  false,
		},
		{
			name:            "has different term and index",
			unstableEntries: []raftpb.Entry{{Term: 5, Index: 11, Type: raftpb.EntryNormal, Data: []byte{0x11, 0x22, 0x33}}},
			commitedEntries: []raftpb.Entry{{Term: 4, Index: 10, Type: raftpb.EntryNormal, Data: []byte{0x11, 0x22, 0x33}}},
			expectedResult:  false,
		},
		{
			name:            "has identical data",
			unstableEntries: []raftpb.Entry{{Term: 4, Index: 10, Type: raftpb.EntryNormal, Data: []byte{0x11, 0x22, 0x33}}},
			commitedEntries: []raftpb.Entry{{Term: 4, Index: 10, Type: raftpb.EntryNormal, Data: []byte{0x11, 0x22, 0x33}}},
			expectedResult:  true,
		},
		{
			name: "has overlapped entry",
			unstableEntries: []raftpb.Entry{
				{Term: 4, Index: 10, Type: raftpb.EntryNormal, Data: []byte{0x11, 0x22, 0x33}},
				{Term: 4, Index: 11, Type: raftpb.EntryNormal, Data: []byte{0x44, 0x55, 0x66}},
				{Term: 4, Index: 12, Type: raftpb.EntryNormal, Data: []byte{0x77, 0x88, 0x99}},
			},
			commitedEntries: []raftpb.Entry{
				{Term: 4, Index: 8, Type: raftpb.EntryNormal, Data: []byte{0x07, 0x08, 0x09}},
				{Term: 4, Index: 9, Type: raftpb.EntryNormal, Data: []byte{0x10, 0x11, 0x12}},
				{Term: 4, Index: 10, Type: raftpb.EntryNormal, Data: []byte{0x11, 0x22, 0x33}},
			},
			expectedResult: true,
		},
	}

	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			shouldWALSync := shouldWaitWALSync(raft.Ready{
				Entries:          tc.unstableEntries,
				CommittedEntries: tc.commitedEntries,
			})
			assert.Equal(t, tc.expectedResult, shouldWALSync)
		})
	}
}

func TestRestartAsStandaloneNodeBackendAheadOfWAL(t *testing.T) {
	dataDir := t.TempDir()
	require.NoError(t, os.MkdirAll(datadir.ToWalDir(dataDir), 0o700))

	cfg := config.ServerConfig{
		Logger:          zaptest.NewLogger(t),
		DataDir:         dataDir,
		ForceNewCluster: true,
	}

	var ents []raftpb.Entry
	for idx := uint64(28); idx <= 31; idx++ {
		ents = append(ents, raftpb.Entry{
			Type:  raftpb.EntryNormal,
			Data:  []byte("foo"),
			Term:  2,
			Index: idx,
		})
	}

	metadata := pbutil.MustMarshal(
		&pb.Metadata{
			NodeID:    1,
			ClusterID: 1,
		},
	)
	w, err := wal.Create(cfg.Logger, cfg.WALDir(), metadata)
	require.NoError(t, err)
	snapshot := &raftpb.Snapshot{
		Metadata: raftpb.SnapshotMetadata{
			ConfState: raftpb.ConfState{Voters: []uint64{1, 2, 3}},
			Index:     27,
			Term:      2,
		},
	}
	require.NoError(t, w.SaveSnapshot(walpb.Snapshot{
		Index:     snapshot.Metadata.Index,
		Term:      snapshot.Metadata.Term,
		ConfState: &snapshot.Metadata.ConfState,
	}))
	err = w.Save(raftpb.HardState{Term: 2, Commit: 28}, ents)
	require.NoError(t, err)
	require.NoError(t, w.Close())

	_, _, _, _, _, err = restartAsStandaloneNode(cfg, snapshot, cindex.NewFakeConsistentIndex(49))
	require.Error(t, err)
	require.Contains(t, err.Error(), "exceeds the last WAL entry index")

	// the WAL must not have been touched: no entries past the gap are
	// appended, and the hard state is left as it was
	w2, err := wal.Open(cfg.Logger, cfg.WALDir(), walpb.Snapshot{
		Index: snapshot.Metadata.Index,
		Term:  snapshot.Metadata.Term,
	})
	require.NoError(t, err)
	defer w2.Close()
	_, _, ents2, err := w2.ReadAll()
	require.NoError(t, err)
	require.Len(t, ents2, 4)
	require.Equal(t, uint64(31), ents2[len(ents2)-1].Index)
}
