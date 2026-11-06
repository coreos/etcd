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

package mvcc

import (
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"

	"go.etcd.io/etcd/pkg/v3/traceutil"
	"go.etcd.io/etcd/server/v3/lease"
	betesting "go.etcd.io/etcd/server/v3/storage/backend/testing"
)

func liveSize(k, v string) int64 { return int64(len(k) + len(v)) }

// TestStoreLogicalBytes verifies that the mvcc logical-size counter tracks the
// live keyspace exactly across creates, in-place updates, deletes, and re-puts,
// and that it is rebuilt correctly on restore. It must be independent of
// revision history and reclaimable storage-engine slack.
func TestStoreLogicalBytes(t *testing.T) {
	b, _ := betesting.NewDefaultTmpBackend(t)
	s := NewStore(zaptest.NewLogger(t), b, &lease.FakeLessor{}, StoreConfig{})
	defer b.Close()

	require.Equal(t, int64(0), s.LogicalBytes(), "empty store")

	// creates
	s.Put([]byte("foo"), []byte("bar"), lease.NoLease) // 6
	require.Equal(t, liveSize("foo", "bar"), s.LogicalBytes())
	s.Put([]byte("hello"), []byte("world!"), lease.NoLease) // +11
	want := liveSize("foo", "bar") + liveSize("hello", "world!")
	require.Equal(t, want, s.LogicalBytes())

	// in-place update replaces the value size, not accumulates history
	s.Put([]byte("foo"), []byte("bazzz"), lease.NoLease) // foo now 8
	want = liveSize("foo", "bazzz") + liveSize("hello", "world!")
	require.Equal(t, want, s.LogicalBytes())

	// re-writing the identical value is a no-op for the counter
	s.Put([]byte("foo"), []byte("bazzz"), lease.NoLease)
	require.Equal(t, want, s.LogicalBytes())

	// delete removes the whole live entry
	s.DeleteRange([]byte("hello"), nil)
	want = liveSize("foo", "bazzz")
	require.Equal(t, want, s.LogicalBytes())

	// deleting an absent key changes nothing
	s.DeleteRange([]byte("absent"), nil)
	require.Equal(t, want, s.LogicalBytes())

	// re-put after delete starts a fresh generation (prev live size is 0)
	s.Put([]byte("hello"), []byte("x"), lease.NoLease)
	want = liveSize("foo", "bazzz") + liveSize("hello", "x")
	require.Equal(t, want, s.LogicalBytes())

	// restore from the backend must reproduce the same logical size
	beforeRestore := s.LogicalBytes()
	s.Close()

	s2 := NewStore(zaptest.NewLogger(t), b, &lease.FakeLessor{}, StoreConfig{})
	defer s2.Close()
	require.Equal(t, beforeRestore, s2.LogicalBytes(), "restore should rebuild logical size")
}

// TestStoreLogicalBytesCompactionStable verifies that compacting away revision
// history does not change the logical-size counter: it reflects live values
// only, never the accumulated history an LSM would carry until compaction.
func TestStoreLogicalBytesCompactionStable(t *testing.T) {
	b, _ := betesting.NewDefaultTmpBackend(t)
	s := NewStore(zaptest.NewLogger(t), b, &lease.FakeLessor{}, StoreConfig{})
	defer b.Close()
	defer s.Close()

	// Churn a single key many times: history grows, live size stays constant.
	var lastRev int64
	for i := 0; i < 50; i++ {
		lastRev = s.Put([]byte("k"), []byte("vvvvv"), lease.NoLease)
	}
	want := liveSize("k", "vvvvv")
	require.Equal(t, want, s.LogicalBytes(), "50 overwrites, one live value")

	ch, err := s.Compact(traceutil.TODO(), lastRev)
	require.NoError(t, err)
	<-ch
	require.Equal(t, want, s.LogicalBytes(), "compaction must not change logical size")
}
