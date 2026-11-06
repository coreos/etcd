// Copyright 2026 The etcd Authors
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

package wal

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"

	"go.etcd.io/etcd/server/v3/storage/wal/walpb"
	"go.etcd.io/raft/v3/raftpb"
)

// TestReadAllDropsOverwrittenSuffixBelowSnapshot is a regression test for
// WAL.ReadAll resurrecting entries that raft had already truncated.
//
// When a follower appends entries that conflict with its local uncommitted
// suffix, raft truncates the suffix in MemoryStorage.Append and the WAL
// records the overwriting entries after the stale ones. ReadAll replays that
// truncation with "ents = append(ents[:offset], e)", but only for entries
// whose index is strictly greater than the snapshot the WAL was opened with.
// An overwriting entry whose index is <= the snapshot index is skipped along
// with the truncation it implies, so the stale entries it superseded (which
// have a higher index and are therefore kept) come back after a restart.
func TestReadAllDropsOverwrittenSuffixBelowSnapshot(t *testing.T) {
	cases := []struct {
		name string
		// steps is the sequence of Save calls made by raft before the
		// snapshot, in order.
		steps []struct {
			hs   *raftpb.HardState
			ents []*raftpb.Entry
		}
		snap *walpb.Snapshot
		// wantEnts is the logical raft log above snap after replay.
		wantEnts []*raftpb.Entry
	}{
		{
			// Local node appended 1..5 at term 1; only 1..2 are
			// committed. A new leader (term 2) overwrites 3..4 with
			// term-2 entries and commits 4. The local 5/1 is truncated
			// by raft. After a snapshot at 4/2, replay must yield no
			// entries: the real log ends at 4/2.
			//
			// The resurrected entry (5/1) has a term lower than the
			// snapshot term.
			name: "resurrected suffix has lower term than snapshot",
			steps: []struct {
				hs   *raftpb.HardState
				ents []*raftpb.Entry
			}{
				{
					hs:   &raftpb.HardState{Term: new(uint64(1)), Commit: new(uint64(2))},
					ents: entries(1, 1, 2, 3, 4, 5),
				},
				{
					hs:   &raftpb.HardState{Term: new(uint64(2)), Commit: new(uint64(4))},
					ents: entries(2, 3, 4),
				},
			},
			snap:     &walpb.Snapshot{Index: new(uint64(4)), Term: new(uint64(2)), ConfState: &confState},
			wantEnts: nil,
		},
		{
			// Local node was briefly leader at term 3 and appended
			// 3..5 at term 3, uncommitted. It then steps down and the
			// current leader backfills 3..4 at term 2 and commits 4.
			// Raft truncates 5/3. After a snapshot at 4/2, replay must
			// yield no entries.
			//
			// Here the resurrected entry (5/3) has a term HIGHER than
			// the snapshot term, so checking ents[0].Term < snap.Term
			// alone would not catch it.
			name: "resurrected suffix has higher term than snapshot",
			steps: []struct {
				hs   *raftpb.HardState
				ents []*raftpb.Entry
			}{
				{
					hs:   &raftpb.HardState{Term: new(uint64(2)), Commit: new(uint64(2))},
					ents: entries(2, 1, 2),
				},
				{
					hs:   &raftpb.HardState{Term: new(uint64(3)), Commit: new(uint64(2))},
					ents: entries(3, 3, 4, 5),
				},
				{
					hs:   &raftpb.HardState{Term: new(uint64(3)), Commit: new(uint64(4))},
					ents: entries(2, 3, 4),
				},
			},
			snap:     &walpb.Snapshot{Index: new(uint64(4)), Term: new(uint64(2)), ConfState: &confState},
			wantEnts: nil,
		},
		{
			// Same overwrite as the first case, but the snapshot is
			// taken before the overwriting entries: 3..4 at term 2 are
			// above the snapshot, so the truncation is replayed and
			// the log correctly ends at 4/2. This case passes today
			// and pins the behaviour the fix must preserve.
			name: "overwriting entries above snapshot are truncated",
			steps: []struct {
				hs   *raftpb.HardState
				ents []*raftpb.Entry
			}{
				{
					hs:   &raftpb.HardState{Term: new(uint64(1)), Commit: new(uint64(2))},
					ents: entries(1, 1, 2, 3, 4, 5),
				},
				{
					hs:   &raftpb.HardState{Term: new(uint64(2)), Commit: new(uint64(4))},
					ents: entries(2, 3, 4),
				},
			},
			snap:     &walpb.Snapshot{Index: new(uint64(2)), Term: new(uint64(1)), ConfState: &confState},
			wantEnts: entries(2, 3, 4),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := t.TempDir()

			w, err := Create(zaptest.NewLogger(t), p, []byte("metadata"))
			require.NoError(t, err)
			require.NoError(t, w.SaveSnapshot(&walpb.Snapshot{Index: new(uint64(0)), Term: new(uint64(0))}))
			for _, step := range tc.steps {
				require.NoError(t, w.Save(step.hs, step.ents))
			}
			require.NoError(t, w.SaveSnapshot(tc.snap))
			require.NoError(t, w.Close())

			w, err = Open(zaptest.NewLogger(t), p, tc.snap)
			require.NoError(t, err)
			defer w.Close()

			_, _, ents, err := w.ReadAll()
			require.NoError(t, err)

			require.Equalf(t, formatEntries(tc.wantEnts), formatEntries(ents),
				"ReadAll(snapshot %d/%d) resurrected entries that raft had truncated: got %s, want %s",
				tc.snap.GetIndex(), tc.snap.GetTerm(), formatEntries(ents), formatEntries(tc.wantEnts))
			if len(ents) > 0 {
				require.Equalf(t, tc.snap.GetIndex()+1, ents[0].GetIndex(),
					"first replayed entry %s must directly follow snapshot index %d", formatEntries(ents[:1]), tc.snap.GetIndex())
			}
		})
	}
}

// entries builds raft entries with the given term and indexes.
func entries(term uint64, indexes ...uint64) []*raftpb.Entry {
	ents := make([]*raftpb.Entry, 0, len(indexes))
	for _, idx := range indexes {
		ents = append(ents, &raftpb.Entry{
			Term:  new(term),
			Index: new(idx),
			Data:  []byte(fmt.Sprintf("entry-%d-%d", idx, term)),
		})
	}
	return ents
}

// formatEntries renders entries as "[index/term ...]" for readable failures.
func formatEntries(ents []*raftpb.Entry) string {
	s := "["
	for i, e := range ents {
		if i > 0 {
			s += " "
		}
		s += fmt.Sprintf("%d/%d", e.GetIndex(), e.GetTerm())
	}
	return s + "]"
}
