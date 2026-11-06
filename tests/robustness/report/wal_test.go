// Copyright 2025 The etcd Authors
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

//nolint:govet
package report

import (
	"reflect"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"
	"google.golang.org/protobuf/proto"

	"go.etcd.io/etcd/server/v3/storage/wal"
	"go.etcd.io/etcd/server/v3/storage/wal/walpb"
	"go.etcd.io/raft/v3/raftpb"
)

func TestMergeMemberEntries(t *testing.T) {
	tcs := []struct {
		name           string
		minCommitIndex uint64
		memberEntries  [][]*raftpb.Entry
		expectErr      string
		expectEntries  []*raftpb.Entry
	}{
		{
			name:          "Error when empty data dir",
			memberEntries: [][]*raftpb.Entry{},
			expectErr:     "no WAL entries matched",
		},
		{
			name: "Success when no entries",
			memberEntries: [][]*raftpb.Entry{
				{},
			},
			expectErr: "no WAL entries matched",
		},
		{
			name: "Error when one member cluster didn't observe the index",
			memberEntries: [][]*raftpb.Entry{
				{
					&raftpb.Entry{Index: new(uint64(1)), Data: []byte("a")},
					&raftpb.Entry{Index: new(uint64(3)), Data: []byte("c")},
				},
			},
			expectErr: "no entry for raft index 2",
		},
		{
			name: "Error when entries index unordered",
			memberEntries: [][]*raftpb.Entry{
				{
					&raftpb.Entry{Index: new(uint64(3)), Data: []byte("c")},
					&raftpb.Entry{Index: new(uint64(1)), Data: []byte("a")},
				},
			},
			expectErr: "raft index should increase, got: 1, previous: 3",
		},
		{
			name: "Error when entries index duplicated",
			memberEntries: [][]*raftpb.Entry{
				{
					&raftpb.Entry{Index: new(uint64(1)), Data: []byte("a")},
					&raftpb.Entry{Index: new(uint64(1)), Data: []byte("a")},
				},
			},
			expectErr: "raft index should increase, got: 1, previous: 1",
		},
		{
			name: "Success when one member cluster",
			memberEntries: [][]*raftpb.Entry{
				{
					&raftpb.Entry{Index: new(uint64(1)), Data: []byte("a")},
					&raftpb.Entry{Index: new(uint64(2)), Data: []byte("b")},
					&raftpb.Entry{Index: new(uint64(3)), Data: []byte("c")},
				},
			},
			expectEntries: []*raftpb.Entry{
				{Index: new(uint64(1)), Data: []byte("a")},
				{Index: new(uint64(2)), Data: []byte("b")},
				{Index: new(uint64(3)), Data: []byte("c")},
			},
		},
		{
			name: "Success when three members agree on entries",
			memberEntries: [][]*raftpb.Entry{
				{
					&raftpb.Entry{Index: new(uint64(1)), Data: []byte("a")},
					&raftpb.Entry{Index: new(uint64(2)), Data: []byte("b")},
					&raftpb.Entry{Index: new(uint64(3)), Data: []byte("c")},
				},
				{
					&raftpb.Entry{Index: new(uint64(1)), Data: []byte("a")},
					&raftpb.Entry{Index: new(uint64(2)), Data: []byte("b")},
					&raftpb.Entry{Index: new(uint64(3)), Data: []byte("c")},
				},
				{
					&raftpb.Entry{Index: new(uint64(1)), Data: []byte("a")},
					&raftpb.Entry{Index: new(uint64(2)), Data: []byte("b")},
					&raftpb.Entry{Index: new(uint64(3)), Data: []byte("c")},
				},
			},
			expectEntries: []*raftpb.Entry{
				{Index: new(uint64(1)), Data: []byte("a")},
				{Index: new(uint64(2)), Data: []byte("b")},
				{Index: new(uint64(3)), Data: []byte("c")},
			},
		},
		{
			name: "Success when three members have no entries",
			memberEntries: [][]*raftpb.Entry{
				{}, {}, {},
			},
			expectErr: "no WAL entries matched",
		},
		{
			name: "Success when one member has no entries in three node cluster",
			memberEntries: [][]*raftpb.Entry{
				{},
				{
					&raftpb.Entry{Index: new(uint64(1)), Data: []byte("a")},
					&raftpb.Entry{Index: new(uint64(2)), Data: []byte("b")},
					&raftpb.Entry{Index: new(uint64(3)), Data: []byte("c")},
				},
				{
					&raftpb.Entry{Index: new(uint64(1)), Data: []byte("a")},
					&raftpb.Entry{Index: new(uint64(2)), Data: []byte("b")},
					&raftpb.Entry{Index: new(uint64(3)), Data: []byte("c")},
				},
			},
			expectEntries: []*raftpb.Entry{
				{Index: new(uint64(1)), Data: []byte("a")},
				{Index: new(uint64(2)), Data: []byte("b")},
				{Index: new(uint64(3)), Data: []byte("c")},
			},
		},
		{
			name: "Success if two members have no entries in three node cluster",
			memberEntries: [][]*raftpb.Entry{
				{},
				{},
				{
					&raftpb.Entry{Index: new(uint64(1)), Data: []byte("a")},
					&raftpb.Entry{Index: new(uint64(2)), Data: []byte("b")},
					&raftpb.Entry{Index: new(uint64(3)), Data: []byte("c")},
				},
			},
			expectEntries: []*raftpb.Entry{
				{Index: new(uint64(1)), Data: []byte("a")},
				{Index: new(uint64(2)), Data: []byte("b")},
				{Index: new(uint64(3)), Data: []byte("c")},
			},
		},
		{
			name: "Success if members didn't observe the whole history",
			memberEntries: [][]*raftpb.Entry{
				{
					&raftpb.Entry{Index: new(uint64(1)), Data: []byte("a")},
					&raftpb.Entry{Index: new(uint64(2)), Data: []byte("b")},
				},
				{
					&raftpb.Entry{Index: new(uint64(2)), Data: []byte("b")},
					&raftpb.Entry{Index: new(uint64(3)), Data: []byte("c")},
				},
				{
					&raftpb.Entry{Index: new(uint64(3)), Data: []byte("c")},
				},
			},
			expectEntries: []*raftpb.Entry{
				{Index: new(uint64(1)), Data: []byte("a")},
				{Index: new(uint64(2)), Data: []byte("b")},
				{Index: new(uint64(3)), Data: []byte("c")},
			},
		},
		{
			name: "Success if members observed only one part of history",
			memberEntries: [][]*raftpb.Entry{
				{
					&raftpb.Entry{Index: new(uint64(1)), Data: []byte("a")},
				},
				{
					&raftpb.Entry{Index: new(uint64(2)), Data: []byte("b")},
				},
				{
					&raftpb.Entry{Index: new(uint64(3)), Data: []byte("c")},
				},
			},
			expectEntries: []*raftpb.Entry{
				{Index: new(uint64(1)), Data: []byte("a")},
				{Index: new(uint64(2)), Data: []byte("b")},
				{Index: new(uint64(3)), Data: []byte("c")},
			},
		},
		{
			name: "Error when in three member cluster if no members observed index",
			memberEntries: [][]*raftpb.Entry{
				{
					&raftpb.Entry{Index: new(uint64(1)), Data: []byte("a")},
					&raftpb.Entry{Index: new(uint64(3)), Data: []byte("c")},
				},
				{
					&raftpb.Entry{Index: new(uint64(1)), Data: []byte("a")},
					&raftpb.Entry{Index: new(uint64(3)), Data: []byte("c")},
				},
				{
					&raftpb.Entry{Index: new(uint64(1)), Data: []byte("a")},
					&raftpb.Entry{Index: new(uint64(3)), Data: []byte("c")},
				},
			},
			expectErr: "no entry for raft index 2",
		},
		{
			name: "Success if only one member observed history",
			memberEntries: [][]*raftpb.Entry{
				{
					&raftpb.Entry{Index: new(uint64(1)), Data: []byte("a")},
					&raftpb.Entry{Index: new(uint64(2)), Data: []byte("b")},
					&raftpb.Entry{Index: new(uint64(3)), Data: []byte("c")},
				},
				{},
				{},
			},
			expectEntries: []*raftpb.Entry{
				{Index: new(uint64(1)), Data: []byte("a")},
				{Index: new(uint64(2)), Data: []byte("b")},
				{Index: new(uint64(3)), Data: []byte("c")},
			},
		},
		{
			name: "Success when one member observed different last entry",
			memberEntries: [][]*raftpb.Entry{
				{
					&raftpb.Entry{Index: new(uint64(1)), Data: []byte("a")},
					&raftpb.Entry{Index: new(uint64(2)), Data: []byte("b")},
					&raftpb.Entry{Index: new(uint64(3)), Data: []byte("c")},
				},
				{
					&raftpb.Entry{Index: new(uint64(1)), Data: []byte("a")},
					&raftpb.Entry{Index: new(uint64(2)), Data: []byte("b")},
					&raftpb.Entry{Index: new(uint64(3)), Data: []byte("c")},
				},
				{
					&raftpb.Entry{Index: new(uint64(1)), Data: []byte("a")},
					&raftpb.Entry{Index: new(uint64(2)), Data: []byte("b")},
					&raftpb.Entry{Index: new(uint64(3)), Data: []byte("x")},
				},
			},
			expectEntries: []*raftpb.Entry{
				{Index: new(uint64(1)), Data: []byte("a")},
				{Index: new(uint64(2)), Data: []byte("b")},
				{Index: new(uint64(3)), Data: []byte("c")},
			},
		},
		{
			name: "Error when one member didn't observe whole history and others observed different last entry",
			memberEntries: [][]*raftpb.Entry{
				{
					&raftpb.Entry{Index: new(uint64(1)), Data: []byte("a")},
					&raftpb.Entry{Index: new(uint64(2)), Data: []byte("b")},
				},
				{
					&raftpb.Entry{Index: new(uint64(1)), Data: []byte("a")},
					&raftpb.Entry{Index: new(uint64(2)), Data: []byte("b")},
					&raftpb.Entry{Index: new(uint64(3)), Data: []byte("c")},
				},
				{
					&raftpb.Entry{Index: new(uint64(1)), Data: []byte("a")},
					&raftpb.Entry{Index: new(uint64(2)), Data: []byte("b")},
					&raftpb.Entry{Index: new(uint64(3)), Data: []byte("x")},
				},
			},
			expectErr: "mismatching entries on raft index 3",
		},
		{
			name: "Error when three members observed different last entry",
			memberEntries: [][]*raftpb.Entry{
				{
					&raftpb.Entry{Index: new(uint64(1)), Data: []byte("a")},
					&raftpb.Entry{Index: new(uint64(2)), Data: []byte("x")},
					&raftpb.Entry{Index: new(uint64(3)), Data: []byte("c")},
				},
				{
					&raftpb.Entry{Index: new(uint64(1)), Data: []byte("a")},
					&raftpb.Entry{Index: new(uint64(2)), Data: []byte("y")},
					&raftpb.Entry{Index: new(uint64(3)), Data: []byte("c")},
				},
				{
					&raftpb.Entry{Index: new(uint64(1)), Data: []byte("a")},
					&raftpb.Entry{Index: new(uint64(2)), Data: []byte("z")},
					&raftpb.Entry{Index: new(uint64(3)), Data: []byte("c")},
				},
			},
			expectErr: "mismatching entries on raft index 2",
		},
		{
			name: "Error when one member observed empty history and others differ on last entry",
			memberEntries: [][]*raftpb.Entry{
				{},
				{
					&raftpb.Entry{Index: new(uint64(1)), Data: []byte("x")},
					&raftpb.Entry{Index: new(uint64(2)), Data: []byte("b")},
					&raftpb.Entry{Index: new(uint64(3)), Data: []byte("c")},
				},
				{
					&raftpb.Entry{Index: new(uint64(1)), Data: []byte("y")},
					&raftpb.Entry{Index: new(uint64(2)), Data: []byte("b")},
					&raftpb.Entry{Index: new(uint64(3)), Data: []byte("c")},
				},
			},
			expectErr: "mismatching entries on raft index 1",
		},
		{
			name:           "Error if entries mismatch on index before minCommitIndex",
			minCommitIndex: 2,
			memberEntries: [][]*raftpb.Entry{
				{
					&raftpb.Entry{Index: new(uint64(1)), Term: new(uint64(1)), Data: []byte("a")},
					&raftpb.Entry{Index: new(uint64(2)), Term: new(uint64(1)), Data: []byte("b")},
				},
				{
					&raftpb.Entry{Index: new(uint64(1)), Term: new(uint64(1)), Data: []byte("a")},
					&raftpb.Entry{Index: new(uint64(2)), Term: new(uint64(2)), Data: []byte("c")},
				},
			},
			expectErr: "mismatching entries on raft index 2",
		},
		{
			name:           "Select entry with higher term if they conflict on uncommitted index",
			minCommitIndex: 1,
			memberEntries: [][]*raftpb.Entry{
				{
					&raftpb.Entry{Index: new(uint64(1)), Term: new(uint64(1)), Data: []byte("a")},
					&raftpb.Entry{Index: new(uint64(2)), Term: new(uint64(1)), Data: []byte("b")},
				},
				{
					&raftpb.Entry{Index: new(uint64(1)), Term: new(uint64(1)), Data: []byte("a")},
					&raftpb.Entry{Index: new(uint64(2)), Term: new(uint64(2)), Data: []byte("x")},
				},
			},
			expectEntries: []*raftpb.Entry{
				{Index: new(uint64(1)), Term: new(uint64(1)), Data: []byte("a")},
				{Index: new(uint64(2)), Term: new(uint64(2)), Data: []byte("x")},
			},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			entries, err := mergeMembersEntries(tc.minCommitIndex, tc.memberEntries)
			if tc.expectErr == "" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, tc.expectErr)
			}
			if diff := cmp.Diff(tc.expectEntries, entries, cmpopts.IgnoreUnexported(raftpb.Entry{}, raftpb.HardState{})); diff != "" {
				t.Errorf("expectEntries mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestWriteReadWAL(t *testing.T) {
	type batch struct {
		state    *raftpb.HardState
		entries  []*raftpb.Entry
		snapshot *walpb.Snapshot
	}
	type want struct {
		wantState   raftpb.HardState
		wantEntries []*raftpb.Entry
		wantError   string
	}

	tcs := []struct {
		name           string
		operations     []batch
		readAt         *walpb.Snapshot
		walReadAll     want
		readAllEntries want
	}{
		{
			name: "single batch",
			operations: []batch{
				{
					state:   &raftpb.HardState{Commit: new(uint64(5))},
					entries: []*raftpb.Entry{{Index: new(uint64(1)), Data: []byte("a")}, {Index: new(uint64(2)), Data: []byte("b")}, {Index: new(uint64(3)), Data: []byte("c")}, {Index: new(uint64(4)), Data: []byte("d")}, {Index: new(uint64(5)), Data: []byte("e")}},
				},
			},
			walReadAll: want{
				wantState:   raftpb.HardState{Commit: new(uint64(5))},
				wantEntries: []*raftpb.Entry{{Index: new(uint64(1)), Data: []byte("a")}, {Index: new(uint64(2)), Data: []byte("b")}, {Index: new(uint64(3)), Data: []byte("c")}, {Index: new(uint64(4)), Data: []byte("d")}, {Index: new(uint64(5)), Data: []byte("e")}},
			},
			readAllEntries: want{
				wantState:   raftpb.HardState{Commit: new(uint64(5))},
				wantEntries: []*raftpb.Entry{{Index: new(uint64(1)), Data: []byte("a")}, {Index: new(uint64(2)), Data: []byte("b")}, {Index: new(uint64(3)), Data: []byte("c")}, {Index: new(uint64(4)), Data: []byte("d")}, {Index: new(uint64(5)), Data: []byte("e")}},
			},
		},
		{
			name: "multiple committed batches",
			operations: []batch{
				{
					state:   &raftpb.HardState{Commit: new(uint64(2))},
					entries: []*raftpb.Entry{{Index: new(uint64(1)), Data: []byte("a")}, {Index: new(uint64(2)), Data: []byte("b")}},
				},
				{
					state:   &raftpb.HardState{Commit: new(uint64(4))},
					entries: []*raftpb.Entry{{Index: new(uint64(3)), Data: []byte("c")}, {Index: new(uint64(4)), Data: []byte("d")}},
				},
				{
					state:   &raftpb.HardState{Commit: new(uint64(5))},
					entries: []*raftpb.Entry{{Index: new(uint64(5)), Data: []byte("e")}},
				},
			},
			walReadAll: want{
				wantState:   raftpb.HardState{Commit: new(uint64(5))},
				wantEntries: []*raftpb.Entry{{Index: new(uint64(1)), Data: []byte("a")}, {Index: new(uint64(2)), Data: []byte("b")}, {Index: new(uint64(3)), Data: []byte("c")}, {Index: new(uint64(4)), Data: []byte("d")}, {Index: new(uint64(5)), Data: []byte("e")}},
			},
			readAllEntries: want{
				wantState:   raftpb.HardState{Commit: new(uint64(5))},
				wantEntries: []*raftpb.Entry{{Index: new(uint64(1)), Data: []byte("a")}, {Index: new(uint64(2)), Data: []byte("b")}, {Index: new(uint64(3)), Data: []byte("c")}, {Index: new(uint64(4)), Data: []byte("d")}, {Index: new(uint64(5)), Data: []byte("e")}},
			},
		},
		{
			name: "uncommitted ovewritten entries",
			operations: []batch{
				{
					state:   &raftpb.HardState{Commit: new(uint64(1))},
					entries: []*raftpb.Entry{{Index: new(uint64(1)), Data: []byte("a")}, {Index: new(uint64(2)), Data: []byte("a")}},
				},
				{
					state:   &raftpb.HardState{Commit: new(uint64(3))},
					entries: []*raftpb.Entry{{Index: new(uint64(2)), Data: []byte("b")}, {Index: new(uint64(3)), Data: []byte("b")}, {Index: new(uint64(4)), Data: []byte("b")}},
				},
				{
					state:   &raftpb.HardState{Commit: new(uint64(4))},
					entries: []*raftpb.Entry{{Index: new(uint64(4)), Data: []byte("c")}, {Index: new(uint64(5)), Data: []byte("c")}},
				},
			},
			walReadAll: want{
				wantState:   raftpb.HardState{Commit: new(uint64(4))},
				wantEntries: []*raftpb.Entry{{Index: new(uint64(1)), Data: []byte("a")}, {Index: new(uint64(2)), Data: []byte("b")}, {Index: new(uint64(3)), Data: []byte("b")}, {Index: new(uint64(4)), Data: []byte("c")}, {Index: new(uint64(5)), Data: []byte("c")}},
			},
			readAllEntries: want{
				wantState:   raftpb.HardState{Commit: new(uint64(4))},
				wantEntries: []*raftpb.Entry{{Index: new(uint64(1)), Data: []byte("a")}, {Index: new(uint64(2)), Data: []byte("b")}, {Index: new(uint64(3)), Data: []byte("b")}, {Index: new(uint64(4)), Data: []byte("c")}, {Index: new(uint64(5)), Data: []byte("c")}},
			},
		},
		{
			name: "entries in bad order",
			operations: []batch{
				{
					state:   &raftpb.HardState{Commit: new(uint64(2))},
					entries: []*raftpb.Entry{{Index: new(uint64(1)), Data: []byte("a")}, {Index: new(uint64(2)), Data: []byte("b")}},
				},
				{
					state:   &raftpb.HardState{Commit: new(uint64(6))},
					entries: []*raftpb.Entry{{Index: new(uint64(5)), Data: []byte("e")}, {Index: new(uint64(6)), Data: []byte("f")}},
				},
				{
					state:   &raftpb.HardState{Commit: new(uint64(4))},
					entries: []*raftpb.Entry{{Index: new(uint64(3)), Data: []byte("c")}, {Index: new(uint64(4)), Data: []byte("d")}},
				},
			},
			walReadAll: want{
				wantError:   "slice bounds out of range",
				wantState:   raftpb.HardState{Commit: new(uint64(2))},
				wantEntries: []*raftpb.Entry{{Index: new(uint64(1)), Data: []byte("a")}, {Index: new(uint64(2)), Data: []byte("b")}},
			},
			readAllEntries: want{
				wantState:   raftpb.HardState{Commit: new(uint64(4))},
				wantEntries: []*raftpb.Entry{{Index: new(uint64(1)), Data: []byte("a")}, {Index: new(uint64(2)), Data: []byte("b")}, {Index: new(uint64(3)), Data: []byte("c")}, {Index: new(uint64(4)), Data: []byte("d")}},
			},
		},
		{
			name: "read before snapshot",
			operations: []batch{
				{
					state:   &raftpb.HardState{Commit: new(uint64(1))},
					entries: []*raftpb.Entry{{Index: new(uint64(1)), Data: []byte("a")}, {Index: new(uint64(2)), Data: []byte("b")}},
				},
				{
					snapshot: &walpb.Snapshot{Index: new(uint64(3)), Term: new(uint64(0)), ConfState: &raftpb.ConfState{}},
				},
				{
					state:   &raftpb.HardState{Commit: new(uint64(5))},
					entries: []*raftpb.Entry{{Index: new(uint64(4)), Data: []byte("d")}, {Index: new(uint64(5)), Data: []byte("e")}},
				},
			},
			walReadAll: want{
				wantError:   "slice bounds out of range",
				wantState:   raftpb.HardState{Commit: new(uint64(1))},
				wantEntries: []*raftpb.Entry{{Index: new(uint64(1)), Data: []byte("a")}, {Index: new(uint64(2)), Data: []byte("b")}},
			},
			readAllEntries: want{
				wantState:   raftpb.HardState{Commit: new(uint64(5))},
				wantEntries: []*raftpb.Entry{{Index: new(uint64(1)), Data: []byte("a")}, {Index: new(uint64(2)), Data: []byte("b")}, {Index: new(uint64(4)), Data: []byte("d")}, {Index: new(uint64(5)), Data: []byte("e")}},
			},
		},
		{
			name: "read at snapshot",
			operations: []batch{
				{
					state:   &raftpb.HardState{Commit: new(uint64(1))},
					entries: []*raftpb.Entry{{Index: new(uint64(1)), Data: []byte("a")}, {Index: new(uint64(2)), Data: []byte("b")}},
				},
				{
					snapshot: &walpb.Snapshot{Index: new(uint64(3)), Term: new(uint64(0)), ConfState: &raftpb.ConfState{}},
				},
				{
					state:   &raftpb.HardState{Commit: new(uint64(5))},
					entries: []*raftpb.Entry{{Index: new(uint64(4)), Data: []byte("d")}, {Index: new(uint64(5)), Data: []byte("e")}},
				},
			},
			readAt: &walpb.Snapshot{Index: new(uint64(3))},
			walReadAll: want{
				wantState:   raftpb.HardState{Commit: new(uint64(5))},
				wantEntries: []*raftpb.Entry{{Index: new(uint64(4)), Data: []byte("d")}, {Index: new(uint64(5)), Data: []byte("e")}},
			},
			readAllEntries: want{
				wantState:   raftpb.HardState{Commit: new(uint64(5))},
				wantEntries: []*raftpb.Entry{{Index: new(uint64(1)), Data: []byte("a")}, {Index: new(uint64(2)), Data: []byte("b")}, {Index: new(uint64(4)), Data: []byte("d")}, {Index: new(uint64(5)), Data: []byte("e")}},
			},
		},
		{
			name: "uncommitted entries before snapshot",
			operations: []batch{
				{
					state:   &raftpb.HardState{Commit: new(uint64(1))},
					entries: []*raftpb.Entry{{Index: new(uint64(1)), Data: []byte("a")}, {Index: new(uint64(2)), Data: []byte("b")}},
				},
				{
					state:   &raftpb.HardState{Commit: new(uint64(3))},
					entries: []*raftpb.Entry{{Index: new(uint64(3)), Data: []byte("c")}, {Index: new(uint64(4)), Data: []byte("d")}},
				},
				{
					snapshot: &walpb.Snapshot{Index: new(uint64(3)), Term: new(uint64(0)), ConfState: &raftpb.ConfState{}},
				},
				{
					state:   &raftpb.HardState{Commit: new(uint64(4))},
					entries: []*raftpb.Entry{{Index: new(uint64(4)), Data: []byte("e")}, {Index: new(uint64(5)), Data: []byte("f")}},
				},
			},
			walReadAll: want{
				wantState:   raftpb.HardState{Commit: new(uint64(4))},
				wantEntries: []*raftpb.Entry{{Index: new(uint64(1)), Data: []byte("a")}, {Index: new(uint64(2)), Data: []byte("b")}, {Index: new(uint64(3)), Data: []byte("c")}, {Index: new(uint64(4)), Data: []byte("e")}, {Index: new(uint64(5)), Data: []byte("f")}},
			},
			readAllEntries: want{
				wantState:   raftpb.HardState{Commit: new(uint64(4))},
				wantEntries: []*raftpb.Entry{{Index: new(uint64(1)), Data: []byte("a")}, {Index: new(uint64(2)), Data: []byte("b")}, {Index: new(uint64(3)), Data: []byte("c")}, {Index: new(uint64(4)), Data: []byte("e")}, {Index: new(uint64(5)), Data: []byte("f")}},
			},
		},
		{
			name: "entries preceding snapshot",
			operations: []batch{
				{
					snapshot: &walpb.Snapshot{Index: new(uint64(4)), Term: new(uint64(0)), ConfState: &raftpb.ConfState{}},
				},
				{
					state:   &raftpb.HardState{Commit: new(uint64(2))},
					entries: []*raftpb.Entry{{Index: new(uint64(1)), Data: []byte("a")}, {Index: new(uint64(2)), Data: []byte("b")}},
				},
				{
					state:   &raftpb.HardState{Commit: new(uint64(4))},
					entries: []*raftpb.Entry{{Index: new(uint64(3)), Data: []byte("c")}, {Index: new(uint64(4)), Data: []byte("d")}},
				},
				{
					state:   &raftpb.HardState{Commit: new(uint64(6))},
					entries: []*raftpb.Entry{{Index: new(uint64(5)), Data: []byte("e")}, {Index: new(uint64(6)), Data: []byte("f")}},
				},
			},
			readAt: &walpb.Snapshot{Index: new(uint64(4))},
			walReadAll: want{
				wantState:   raftpb.HardState{Commit: new(uint64(6))},
				wantEntries: []*raftpb.Entry{{Index: new(uint64(5)), Data: []byte("e")}, {Index: new(uint64(6)), Data: []byte("f")}},
			},
			readAllEntries: want{
				wantState:   raftpb.HardState{Commit: new(uint64(6))},
				wantEntries: []*raftpb.Entry{{Index: new(uint64(1)), Data: []byte("a")}, {Index: new(uint64(2)), Data: []byte("b")}, {Index: new(uint64(3)), Data: []byte("c")}, {Index: new(uint64(4)), Data: []byte("d")}, {Index: new(uint64(5)), Data: []byte("e")}, {Index: new(uint64(6)), Data: []byte("f")}},
			},
		},
		{
			name: "read after snapshot",
			operations: []batch{
				{
					state:   &raftpb.HardState{Commit: new(uint64(1))},
					entries: []*raftpb.Entry{{Index: new(uint64(1)), Data: []byte("a")}, {Index: new(uint64(2)), Data: []byte("b")}},
				},
				{
					snapshot: &walpb.Snapshot{Index: new(uint64(3)), Term: new(uint64(0)), ConfState: &raftpb.ConfState{}},
				},
				{
					state:   &raftpb.HardState{Commit: new(uint64(5))},
					entries: []*raftpb.Entry{{Index: new(uint64(4)), Data: []byte("d")}, {Index: new(uint64(5)), Data: []byte("e")}},
				},
			},
			readAt: &walpb.Snapshot{Index: new(uint64(4))},
			walReadAll: want{
				wantError:   "snapshot not found",
				wantState:   raftpb.HardState{Commit: new(uint64(5))},
				wantEntries: []*raftpb.Entry{{Index: new(uint64(5)), Data: []byte("e")}},
			},
			readAllEntries: want{
				wantState:   raftpb.HardState{Commit: new(uint64(5))},
				wantEntries: []*raftpb.Entry{{Index: new(uint64(1)), Data: []byte("a")}, {Index: new(uint64(2)), Data: []byte("b")}, {Index: new(uint64(4)), Data: []byte("d")}, {Index: new(uint64(5)), Data: []byte("e")}},
			},
		},
	}
	// Iterate by index: the test cases embed raftpb.HardState, which contains
	// a mutex since the raft v3.7 protobuf regeneration and must not be copied.
	for i := range tcs {
		tc := &tcs[i]
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			lg := zaptest.NewLogger(t)
			w, err := wal.Create(lg, dir, nil)
			require.NoError(t, err)
			for _, op := range tc.operations {
				if op.state != nil {
					err = w.Save(op.state, op.entries)
					require.NoError(t, err)
				}
				if op.snapshot != nil {
					err = w.SaveSnapshot(op.snapshot)
					require.NoError(t, err)
				}
			}
			w.Close()

			w2, err := wal.OpenForRead(lg, dir, tc.readAt)
			require.NoError(t, err)
			defer w2.Close()
			t.Run("wal.ReadAll", func(t *testing.T) {
				_, state, entries, err := w2.ReadAll()
				if tc.walReadAll.wantError != "" {
					require.ErrorContains(t, err, tc.walReadAll.wantError)
				} else {
					require.NoError(t, err)
				}
				if !proto.Equal(&tc.walReadAll.wantState, state) {
					t.Errorf("wantState mismatch\n got  %+v\n want %+v", state, &tc.walReadAll.wantState)
				}
				if diff := cmp.Diff(tc.walReadAll.wantEntries, entries, cmpopts.IgnoreUnexported(raftpb.Entry{}, raftpb.HardState{})); diff != "" {
					t.Errorf("wantEntries mismatch (-want +got):\n%s", diff)
				}
			})
			t.Run("ReadAllEntries", func(t *testing.T) {
				state, entries, err := ReadAllWALEntries(lg, dir)
				if tc.readAllEntries.wantError != "" {
					require.ErrorContains(t, err, tc.walReadAll.wantError)
				} else {
					require.NoError(t, err)
				}
				if !proto.Equal(&tc.readAllEntries.wantState, state) {
					t.Errorf("wantState mismatch\n got  %+v\n want %+v", state, &tc.readAllEntries.wantState)
				}
				if diff := cmp.Diff(tc.readAllEntries.wantEntries, entries, cmpopts.IgnoreUnexported(raftpb.Entry{}, raftpb.HardState{})); diff != "" {
					t.Errorf("wantEntries mismatch (-want +got):\n%s", diff)
				}
			})
		})
	}
}

func TestAreEntriesEqualVerifyAllFields(t *testing.T) {
	base := &raftpb.Entry{
		Index: new(uint64(1)),
		Term:  new(uint64(1)),
		Type:  raftpb.EntryNormal.Enum(),
		Data:  []byte("data"),
	}

	val := reflect.ValueOf(base).Elem()
	typ := val.Type()

	for i := 0; i < val.NumField(); i++ {
		field := typ.Field(i)
		if !field.IsExported() {
			continue
		}

		t.Run(field.Name, func(t *testing.T) {
			modified := proto.Clone(base).(*raftpb.Entry)
			modVal := reflect.ValueOf(modified).Elem()

			f := modVal.Field(i)
			switch f.Kind() {
			case reflect.Pointer:
				if f.Type().Elem().Kind() == reflect.Uint64 {
					v := uint64(999)
					f.Set(reflect.ValueOf(&v))
				} else if f.Type().Elem().Kind() == reflect.Int32 {
					v := int32(999)
					f.Set(reflect.ValueOf(&v).Convert(f.Type()))
				}
			case reflect.Slice:
				if f.Type().Elem().Kind() == reflect.Uint8 {
					f.SetBytes([]byte("different-data-payload"))
				}
			default:
				t.Fatalf("Unhandled field type for reflection: %s", f.Type())
			}

			if areEntriesEqual(base, modified) {
				t.Errorf("areEntriesEqual returned true after mutating field %q! This means changes to %q are not being compared.", field.Name, field.Name)
			}
		})
	}
}

func BenchmarkMergeMemberEntries(b *testing.B) {
	const numEntries = 1000
	memberEntries := make([][]*raftpb.Entry, 3)
	for i := 0; i < 3; i++ {
		memberEntries[i] = make([]*raftpb.Entry, numEntries)
		for j := 0; j < numEntries; j++ {
			var entryType *raftpb.EntryType
			if i == 0 {
				entryType = nil
			} else {
				entryType = raftpb.EntryNormal.Enum()
			}
			memberEntries[i][j] = &raftpb.Entry{
				Index: new(uint64(j + 1)),
				Term:  new(uint64(1)),
				Type:  entryType,
				Data:  []byte("some realistic data payload for entry"),
			}
		}
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := mergeMembersEntries(0, memberEntries)
		if err != nil {
			b.Fatal(err)
		}
	}
}

func TestParseEntryNormal_IgnoreV2(t *testing.T) {
	tcs := []struct {
		name string
		data []byte
	}{
		{
			name: "invalid wire format (proto: cannot parse invalid wire-format data)",
			// 0x1a is field number 3 (RangeRequest), wire type 2 (length-delimited).
			// Followed by length 5 and bytes that do not form a valid RangeRequest message.
			data: []byte{0x1a, 0x05, 'a', 'b', 'c', 'd', 'e'},
		},
	}

	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			ent := &raftpb.Entry{
				Data: tc.data,
			}
			req, err := parseEntryNormal(ent)
			require.NoError(t, err)
			require.Nil(t, req)
		})
	}
}

func TestAreEntriesEqual_ProtobufSerializationDifference(t *testing.T) {
	// e1 has Id field absent (Id: nil)
	cc1 := &raftpb.ConfChange{
		Id:      nil,
		Type:    raftpb.ConfChangeAddNode.Enum(),
		NodeId:  proto.Uint64(12345),
		Context: []byte("test"),
	}
	data1, err := proto.Marshal(cc1)
	require.NoError(t, err)

	// e2 has Id field present with default zero (Id: 0)
	cc2 := &raftpb.ConfChange{
		Id:      proto.Uint64(0),
		Type:    raftpb.ConfChangeAddNode.Enum(),
		NodeId:  proto.Uint64(12345),
		Context: []byte("test"),
	}
	data2, err := proto.Marshal(cc2)
	require.NoError(t, err)

	e1 := &raftpb.Entry{
		Index: proto.Uint64(1),
		Term:  proto.Uint64(1),
		Type:  raftpb.EntryConfChange.Enum(),
		Data:  data1,
	}
	e2 := &raftpb.Entry{
		Index: proto.Uint64(1),
		Term:  proto.Uint64(1),
		Type:  raftpb.EntryConfChange.Enum(),
		Data:  data2,
	}

	// They must not be byte-identical (due to Id field presence)
	require.NotEqual(t, e1.Data, e2.Data)

	// But they must be semantically equal
	require.True(t, areEntriesEqual(e1, e2))
}
