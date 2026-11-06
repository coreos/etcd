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

package mvcc

import (
	"context"
	"fmt"
	"testing"

	"go.uber.org/zap/zaptest"

	"go.etcd.io/etcd/pkg/v3/traceutil"
	"go.etcd.io/etcd/server/v3/lease"
	betesting "go.etcd.io/etcd/server/v3/storage/backend/testing"
)

func BenchmarkKVStoreRangeLimit(b *testing.B) {
	testCases := []struct {
		name      string
		totalKeys int
		limit     int64
	}{
		{name: "10k_keys_limit_100", totalKeys: 10000, limit: 100},
		{name: "10k_keys_limit_1000", totalKeys: 10000, limit: 1000},
		{name: "100k_keys_limit_100", totalKeys: 100000, limit: 100},
		{name: "100k_keys_limit_1000", totalKeys: 100000, limit: 1000},
	}

	for _, tc := range testCases {
		b.Run(tc.name+"/Old_NoLimit", func(b *testing.B) {
			be, _ := betesting.NewDefaultTmpBackend(b)
			store := NewStore(zaptest.NewLogger(b), be, &lease.FakeLessor{}, StoreConfig{})
			defer cleanup(store, be)

			keys := createBytesSlice(32, tc.totalKeys)
			val := []byte("value")

			for i := 0; i < tc.totalKeys; i++ {
				store.Put(keys[i], val, lease.NoLease)
			}
			store.Commit()

			b.ResetTimer()
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				txnRead := store.Read(ConcurrentReadTxMode, traceutil.TODO())
				_, _ = txnRead.Range(context.Background(), []byte{}, []byte{}, RangeOptions{Limit: 0})
				txnRead.End()
			}
		})

		b.Run(tc.name+"/New_WithLimit", func(b *testing.B) {
			be, _ := betesting.NewDefaultTmpBackend(b)
			store := NewStore(zaptest.NewLogger(b), be, &lease.FakeLessor{}, StoreConfig{})
			defer cleanup(store, be)

			keys := createBytesSlice(32, tc.totalKeys)
			val := []byte("value")

			for i := 0; i < tc.totalKeys; i++ {
				store.Put(keys[i], val, lease.NoLease)
			}
			store.Commit()

			b.ResetTimer()
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				txnRead := store.Read(ConcurrentReadTxMode, traceutil.TODO())
				_, _ = txnRead.Range(context.Background(), []byte{}, []byte{}, RangeOptions{Limit: tc.limit + 1})
				txnRead.End()
			}
		})
	}
}

func BenchmarkKVStoreRangeWithPrefix(b *testing.B) {
	testCases := []struct {
		name         string
		totalKeys    int
		requestLimit int64
		prefix       string
	}{
		{name: "10k_keys_limit_100_prefix_a", totalKeys: 10000, requestLimit: 100, prefix: "a"},
		{name: "10k_keys_limit_100_prefix_z", totalKeys: 10000, requestLimit: 100, prefix: "z"},
		{name: "100k_keys_limit_100_prefix_a", totalKeys: 100000, requestLimit: 100, prefix: "a"},
	}

	for _, tc := range testCases {
		b.Run(tc.name+"/Old_NoLimit", func(b *testing.B) {
			be, _ := betesting.NewDefaultTmpBackend(b)
			store := NewStore(zaptest.NewLogger(b), be, &lease.FakeLessor{}, StoreConfig{})
			defer cleanup(store, be)

			keys := createBytesSlice(32, tc.totalKeys)
			val := []byte("value")

			for i := 0; i < tc.totalKeys; i++ {
				store.Put(keys[i], val, lease.NoLease)
			}
			store.Commit()

			startKey := []byte(tc.prefix)
			endKey := []byte(fmt.Sprintf("%s\x00", tc.prefix))

			b.ResetTimer()
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				txnRead := store.Read(ConcurrentReadTxMode, traceutil.TODO())
				_, _ = txnRead.Range(context.Background(), startKey, endKey, RangeOptions{Limit: 0})
				txnRead.End()
			}
		})

		b.Run(tc.name+"/New_WithLimit", func(b *testing.B) {
			be, _ := betesting.NewDefaultTmpBackend(b)
			store := NewStore(zaptest.NewLogger(b), be, &lease.FakeLessor{}, StoreConfig{})
			defer cleanup(store, be)

			keys := createBytesSlice(32, tc.totalKeys)
			val := []byte("value")

			for i := 0; i < tc.totalKeys; i++ {
				store.Put(keys[i], val, lease.NoLease)
			}
			store.Commit()

			startKey := []byte(tc.prefix)
			endKey := []byte(fmt.Sprintf("%s\x00", tc.prefix))

			b.ResetTimer()
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				txnRead := store.Read(ConcurrentReadTxMode, traceutil.TODO())
				_, _ = txnRead.Range(context.Background(), startKey, endKey, RangeOptions{Limit: tc.requestLimit + 1})
				txnRead.End()
			}
		})
	}
}

func BenchmarkKVStorePopulate(b *testing.B) {
	testCases := []struct {
		name      string
		totalKeys int
	}{
		{name: "10k_keys", totalKeys: 10000},
		{name: "100k_keys", totalKeys: 100000},
	}

	for _, tc := range testCases {
		b.Run(tc.name, func(b *testing.B) {
			be, _ := betesting.NewDefaultTmpBackend(b)
			store := NewStore(zaptest.NewLogger(b), be, &lease.FakeLessor{}, StoreConfig{})
			defer cleanup(store, be)

			keys := createBytesSlice(32, tc.totalKeys)
			val := []byte("value")

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				for j := 0; j < tc.totalKeys; j++ {
					store.Put(keys[j], val, lease.NoLease)
				}
				store.Commit()
			}
		})
	}
}
