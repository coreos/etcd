// Copyright 2021 The etcd Authors
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

package schema

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"go.etcd.io/etcd/raft/v3/raftpb"
	betesting "go.etcd.io/etcd/server/v3/storage/backend/testing"
	"go.uber.org/zap/zaptest"
)

func TestConfStateFromBackendInOneTx(t *testing.T) {
	lg := zaptest.NewLogger(t)
	be, _ := betesting.NewDefaultTmpBackend(t)
	defer betesting.Close(t, be)

	tx := be.BatchTx()
	CreateMetaBucket(tx)
	tx.Lock()
	defer tx.Unlock()
	assert.Nil(t, UnsafeConfStateFromBackend(lg, tx))

	confState := raftpb.ConfState{Learners: []uint64{1, 2}, Voters: []uint64{3}, AutoLeave: false}
	MustUnsafeSaveConfStateToBackend(lg, tx, &confState)

	assert.Equal(t, confState, *UnsafeConfStateFromBackend(lg, tx))
}

func TestMustUnsafeSaveConfStateToBackend(t *testing.T) {
	lg := zaptest.NewLogger(t)
	be, _ := betesting.NewDefaultTmpBackend(t)
	defer betesting.Close(t, be)

	{
		tx := be.BatchTx()
		CreateMetaBucket(tx)
		tx.Commit()
	}

	t.Run("missing", func(t *testing.T) {
		tx := be.ReadTx()
		tx.Lock()
		defer tx.Unlock()
		assert.Nil(t, UnsafeConfStateFromBackend(lg, tx))
	})

	confState := raftpb.ConfState{Learners: []uint64{1, 2}, Voters: []uint64{3}, AutoLeave: false}

	t.Run("save", func(t *testing.T) {
		tx := be.BatchTx()
		tx.Lock()
		MustUnsafeSaveConfStateToBackend(lg, tx, &confState)
		tx.Unlock()
		tx.Commit()
	})

	t.Run("read", func(t *testing.T) {
		tx := be.ReadTx()
		tx.Lock()
		defer tx.Unlock()
		assert.Equal(t, confState, *UnsafeConfStateFromBackend(lg, tx))
	})
}
