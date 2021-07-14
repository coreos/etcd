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

package mvcc

import (
	"math/rand"
	"os"
	"testing"

	"go.etcd.io/etcd/pkg/v3/traceutil"
	"go.etcd.io/etcd/server/v3/lease"
	betesting "go.etcd.io/etcd/server/v3/storage/backend/testing"

	"go.uber.org/zap"
)

func BenchmarkWatchableStorePut(b *testing.B) {
	be, tmpPath := betesting.NewDefaultTmpBackend(b)
	s := New(zap.NewExample(), be, &lease.FakeLessor{}, StoreConfig{})
	defer cleanup(s, be, tmpPath)

	// arbitrary number of bytes
	bytesN := 64
	keys := createBytesSlice(bytesN, b.N)
	vals := createBytesSlice(bytesN, b.N)

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		s.Put(keys[i], vals[i], lease.NoLease)
	}
}

// BenchmarkWatchableStoreTxnPut benchmarks the Put operation
// with transaction begin and end, where transaction involves
// some synchronization operations, such as mutex locking.
func BenchmarkWatchableStoreTxnPut(b *testing.B) {
	be, tmpPath := betesting.NewDefaultTmpBackend(b)
	s := New(zap.NewExample(), be, &lease.FakeLessor{}, StoreConfig{})
	defer cleanup(s, be, tmpPath)

	// arbitrary number of bytes
	bytesN := 64
	keys := createBytesSlice(bytesN, b.N)
	vals := createBytesSlice(bytesN, b.N)

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		txn := s.Write(traceutil.TODO())
		txn.Put(keys[i], vals[i], lease.NoLease)
		txn.End()
	}
}

// BenchmarkWatchableStoreWatchPutSync benchmarks the case of
// many synced watchers receiving a Put notification.
func BenchmarkWatchableStoreWatchPutSync(b *testing.B) {
	benchmarkWatchableStoreWatchPut(b, true)
}

// BenchmarkWatchableStoreWatchPutUnsync benchmarks the case of
// many unsynced watchers receiving a Put notification.
func BenchmarkWatchableStoreWatchPutUnsync(b *testing.B) {
	benchmarkWatchableStoreWatchPut(b, false)
}

func benchmarkWatchableStoreWatchPut(b *testing.B, synced bool) {
	be, tmpPath := betesting.NewDefaultTmpBackend(b)
	s := newWatchableStore(zap.NewExample(), be, &lease.FakeLessor{}, StoreConfig{})
	defer cleanup(s, be, tmpPath)

	k := []byte("testkey")
	v := []byte("testval")

	rev := int64(0)
	if !synced {
		// non-0 value to keep watchers in unsynced
		rev = 1
	}

	w := s.NewWatchStream()
	defer w.Close()
	watchIDs := make([]WatchID, b.N)
	for i := range watchIDs {
		watchIDs[i], _ = w.Watch(0, k, nil, rev)
	}

	b.ResetTimer()
	b.ReportAllocs()

	// trigger watchers
	s.Put(k, v, lease.NoLease)
	for range watchIDs {
		<-w.Chan()
	}
	select {
	case wc := <-w.Chan():
		b.Fatalf("unexpected data %v", wc)
	default:
	}
}

// Benchmarks on cancel function performance for unsynced watchers
// in a WatchableStore. It creates k*N watchers to populate unsynced
// with a reasonably large number of watchers. And measures the time it
// takes to cancel N watchers out of k*N watchers. The performance is
// expected to differ depending on the unsynced member implementation.
// TODO: k is an arbitrary constant. We need to figure out what factor
// we should put to simulate the real-world use cases.
func BenchmarkWatchableStoreUnsyncedCancel(b *testing.B) {
	be, tmpPath := betesting.NewDefaultTmpBackend(b)
	s := NewStore(zap.NewExample(), be, &lease.FakeLessor{}, StoreConfig{})

	// manually create watchableStore instead of newWatchableStore
	// because newWatchableStore periodically calls syncWatchersLoop
	// method to sync watchers in unsynced map. We want to keep watchers
	// in unsynced for this benchmark.
	ws := &watchableStore{
		store:    s,
		unsynced: newWatcherGroup(),

		// to make the test not crash from assigning to nil map.
		// 'synced' doesn't get populated in this test.
		synced: newWatcherGroup(),
	}

	defer func() {
		ws.store.Close()
		os.Remove(tmpPath)
	}()

	// Put a key so that we can spawn watchers on that key
	// (testKey in this test). This increases the rev to 1,
	// and later we can we set the watcher's startRev to 1,
	// and force watchers to be in unsynced.
	testKey := []byte("foo")
	testValue := []byte("bar")
	s.Put(testKey, testValue, lease.NoLease)

	w := ws.NewWatchStream()

	const k int = 2
	benchSampleN := b.N
	watcherN := k * benchSampleN

	watchIDs := make([]WatchID, watcherN)
	for i := 0; i < watcherN; i++ {
		// non-0 value to keep watchers in unsynced
		watchIDs[i], _ = w.Watch(0, testKey, nil, 1)
	}

	// random-cancel N watchers to make it not biased towards
	// data structures with an order, such as slice.
	ix := rand.Perm(watcherN)

	b.ResetTimer()
	b.ReportAllocs()

	// cancel N watchers
	for _, idx := range ix[:benchSampleN] {
		if err := w.Cancel(watchIDs[idx]); err != nil {
			b.Error(err)
		}
	}
}

func BenchmarkWatchableStoreSyncedCancel(b *testing.B) {
	be, tmpPath := betesting.NewDefaultTmpBackend(b)
	s := newWatchableStore(zap.NewExample(), be, &lease.FakeLessor{}, StoreConfig{})

	defer func() {
		s.store.Close()
		os.Remove(tmpPath)
	}()

	// Put a key so that we can spawn watchers on that key
	testKey := []byte("foo")
	testValue := []byte("bar")
	s.Put(testKey, testValue, lease.NoLease)

	w := s.NewWatchStream()

	// put 1 million watchers on the same key
	const watcherN = 1000000

	watchIDs := make([]WatchID, watcherN)
	for i := 0; i < watcherN; i++ {
		// 0 for startRev to keep watchers in synced
		watchIDs[i], _ = w.Watch(0, testKey, nil, 0)
	}

	// randomly cancel watchers to make it not biased towards
	// data structures with an order, such as slice.
	ix := rand.Perm(watcherN)

	b.ResetTimer()
	b.ReportAllocs()

	for _, idx := range ix {
		if err := w.Cancel(watchIDs[idx]); err != nil {
			b.Error(err)
		}
	}
}
