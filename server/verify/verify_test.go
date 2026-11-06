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

package verify

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"

	betesting "go.etcd.io/etcd/server/v3/storage/backend/testing"
	"go.etcd.io/etcd/server/v3/storage/schema"
	"go.etcd.io/etcd/server/v3/storage/wal/walpb"
	"go.etcd.io/raft/v3/raftpb"
)

func uint64Ptr(v uint64) *uint64 {
	return &v
}

func TestValidateConsistentIndex(t *testing.T) {
	tcs := []struct {
		name          string
		cfg           Config
		backendIndex  uint64
		backendTerm   uint64
		hardState     *raftpb.HardState
		snapshot      *walpb.Snapshot
		expectedError error
	}{
		{
			name:         "ExactIndex: matches commit index and term",
			cfg:          Config{ExactIndex: true},
			backendIndex: 10,
			backendTerm:  1,
			hardState:    &raftpb.HardState{Commit: uint64Ptr(10), Term: uint64Ptr(1)},
			snapshot:     &walpb.Snapshot{Index: uint64Ptr(5)},
		},
		{
			name:          "ExactIndex: index mismatch",
			cfg:           Config{ExactIndex: true},
			backendIndex:  11,
			backendTerm:   1,
			hardState:     &raftpb.HardState{Commit: uint64Ptr(10), Term: uint64Ptr(1)},
			snapshot:      &walpb.Snapshot{Index: uint64Ptr(5)},
			expectedError: fmt.Errorf("backend.ConsistentIndex (11) expected == WAL.HardState.commit (10)"),
		},
		{
			name:          "ExactIndex: term mismatch",
			cfg:           Config{ExactIndex: true},
			backendIndex:  10,
			backendTerm:   2,
			hardState:     &raftpb.HardState{Commit: uint64Ptr(10), Term: uint64Ptr(1)},
			snapshot:      &walpb.Snapshot{Index: uint64Ptr(5)},
			expectedError: fmt.Errorf("backend.Term (2) expected == WAL.HardState.term, (1)"),
		},
		{
			name:         "Normal verification: index equals commit index",
			cfg:          Config{ExactIndex: false},
			backendIndex: 10,
			backendTerm:  1,
			hardState:    &raftpb.HardState{Commit: uint64Ptr(10), Term: uint64Ptr(1)},
			snapshot:     &walpb.Snapshot{Index: uint64Ptr(5)},
		},
		{
			name:         "Normal verification: index greater than commit index (issue #22028 scenario)",
			cfg:          Config{ExactIndex: false},
			backendIndex: 1049,
			backendTerm:  1,
			hardState:    &raftpb.HardState{Commit: uint64Ptr(1047), Term: uint64Ptr(1)},
			snapshot:     &walpb.Snapshot{Index: uint64Ptr(5)},
		},
		{
			name:         "Normal verification: term greater than hardstate term",
			cfg:          Config{ExactIndex: false},
			backendIndex: 10,
			backendTerm:  2,
			hardState:    &raftpb.HardState{Commit: uint64Ptr(10), Term: uint64Ptr(1)},
			snapshot:     &walpb.Snapshot{Index: uint64Ptr(5)},
		},
		{
			name:         "Normal verification: index less than commit index but >= snapshot index",
			cfg:          Config{ExactIndex: false},
			backendIndex: 8,
			backendTerm:  1,
			hardState:    &raftpb.HardState{Commit: uint64Ptr(10), Term: uint64Ptr(1)},
			snapshot:     &walpb.Snapshot{Index: uint64Ptr(5)},
		},
		{
			name:          "Normal verification: index less than snapshot index",
			cfg:           Config{ExactIndex: false},
			backendIndex:  4,
			backendTerm:   1,
			hardState:     &raftpb.HardState{Commit: uint64Ptr(10), Term: uint64Ptr(1)},
			snapshot:      &walpb.Snapshot{Index: uint64Ptr(5)},
			expectedError: fmt.Errorf("backend.ConsistentIndex (4) must be >= last snapshot index (5)"),
		},
		{
			name:         "Normal verification: zero index, term and snapshot",
			cfg:          Config{ExactIndex: false},
			backendIndex: 0,
			backendTerm:  0,
			hardState:    &raftpb.HardState{Commit: uint64Ptr(0), Term: uint64Ptr(0)},
			snapshot:     &walpb.Snapshot{Index: uint64Ptr(0)},
		},
	}

	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			be, _ := betesting.NewTmpBackend(t, time.Hour, 10000)
			tx := be.BatchTx()
			tx.Lock()
			schema.UnsafeCreateMetaBucket(tx)
			schema.UnsafeUpdateConsistentIndexForce(tx, tc.backendIndex, tc.backendTerm)
			tx.Unlock()
			be.ForceCommit()

			cfg := tc.cfg
			cfg.Logger = zaptest.NewLogger(t)

			err := validateConsistentIndex(cfg, tc.hardState, tc.snapshot, be)
			if tc.expectedError != nil {
				require.EqualError(t, err, tc.expectedError.Error())
			} else {
				require.NoError(t, err)
			}
		})
	}
}
