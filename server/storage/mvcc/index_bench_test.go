// Copyright 2018 The etcd Authors
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
	"bytes"
	"fmt"
	"math"
	"slices"
	"sync"
	"sync/atomic"
	"testing"

	"go.uber.org/zap"
)

func BenchmarkIndexCompact1(b *testing.B)       { benchmarkIndexCompact(b, 1) }
func BenchmarkIndexCompact100(b *testing.B)     { benchmarkIndexCompact(b, 100) }
func BenchmarkIndexCompact10000(b *testing.B)   { benchmarkIndexCompact(b, 10000) }
func BenchmarkIndexCompact100000(b *testing.B)  { benchmarkIndexCompact(b, 100000) }
func BenchmarkIndexCompact1000000(b *testing.B) { benchmarkIndexCompact(b, 1000000) }

func benchmarkIndexCompact(b *testing.B, size int) {
	log := zap.NewNop()
	kvindex := newTreeIndex(log)

	bytesN := 64
	keys := createBytesSlice(bytesN, size)
	for i := 1; i < size; i++ {
		kvindex.Put(keys[i], Revision{Main: int64(i), Sub: int64(i)})
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 1; i < b.N; i++ {
		kvindex.Compact(int64(i))
	}
}

func BenchmarkIndexPut(b *testing.B) {
	for _, txnSize := range []int{1, 8} {
		for _, dataset := range indexBenchDatasets {
			b.Run(fmt.Sprintf("Update/Keys=%d,KeyLength=%d,TxnSize=%d", dataset.keySpace, dataset.keyLen, txnSize), func(b *testing.B) {
				benchmarkIndexPut(b, dataset.keys(), txnSize, true)
			})
		}

		b.Run(fmt.Sprintf("Create/Keys=b.N,KeyLength=64,TxnSize=%d", txnSize), func(b *testing.B) {
			benchmarkIndexPut(b, createBytesSlice(64, b.N), txnSize, false)
		})
	}
}

// benchmarkIndexPut puts keys in order, cycling through them as needed.
// If prepopulate is true, the index is populated before timing starts.
func benchmarkIndexPut(b *testing.B, keys [][]byte, txnSize int, prepopulate bool) {
	kvindex := newTreeIndex(zap.NewNop())
	var baseRev int64
	if prepopulate {
		for i, key := range keys {
			kvindex.Put(key, Revision{Main: int64(i + 1)})
		}
		baseRev = int64(len(keys))
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := range b.N {
		kvindex.Put(keys[i%len(keys)], Revision{
			Main: baseRev + int64(i/txnSize) + 1,
			Sub:  int64(i % txnSize),
		})
	}
}

func BenchmarkIndexPutTombstone(b *testing.B) {
	for _, dataset := range indexBenchDatasets {
		b.Run(fmt.Sprintf("Keys=%d,KeyLength=%d", dataset.keySpace, dataset.keyLen), func(b *testing.B) {
			keys := dataset.keys()
			benchmarkIndexPutTombstone(b, keys, keys)
		})
		if dataset.keySpace == 100_000 {
			const activeKeys = 5_000
			b.Run(fmt.Sprintf("Keys=%d,KeyLength=%d,ActiveKeys=%d", dataset.keySpace, dataset.keyLen, activeKeys), func(b *testing.B) {
				keys, sortedKeys := dataset.keys(), dataset.sortedKeys()
				first := (len(sortedKeys) - activeKeys) / 2
				benchmarkIndexPutTombstone(b, keys, sortedKeys[first:first+activeKeys])
			})
		}
	}
}

func benchmarkIndexPutTombstone(b *testing.B, keys, activeKeys [][]byte) {
	kvindex := newTreeIndex(zap.NewNop())
	for i, key := range keys {
		kvindex.Put(key, Revision{Main: int64(i + 1)})
	}
	var putIndex, tombstoneIndex int
	b.ReportAllocs()
	b.ResetTimer()
	for i := range b.N {
		rev := Revision{Main: int64(len(keys) + i + 1)}
		if i%3 == 1 {
			if err := kvindex.Tombstone(activeKeys[tombstoneIndex%len(activeKeys)], rev); err != nil {
				b.Fatal(err)
			}
			tombstoneIndex++
		} else {
			kvindex.Put(activeKeys[putIndex%len(activeKeys)], rev)
			putIndex++
		}
	}
	b.StopTimer()
	b.ReportMetric(float64(tombstoneIndex)/float64(b.N), "tombstones/op")
}

func BenchmarkIndexPutCount(b *testing.B) {
	for _, dataset := range indexBenchDatasets {
		if dataset.keySpace != 100_000 {
			continue
		}
		for _, countKeys := range []int{1, 10, 1000} {
			b.Run(fmt.Sprintf("Keys=%d,KeyLength=%d,CountKeys=%d", dataset.keySpace, dataset.keyLen, countKeys), func(b *testing.B) {
				benchmarkIndexPutCount(b, dataset, countKeys)
			})
		}
	}
}

func benchmarkIndexPutCount(b *testing.B, dataset *indexBenchDataset, countKeys int) {
	keys, sortedKeys := dataset.keys(), dataset.sortedKeys()
	first := (len(sortedKeys) - countKeys) / 2
	start := sortedKeys[first]
	var end []byte
	if first+countKeys < len(sortedKeys) {
		end = sortedKeys[first+countKeys]
	} else {
		end = append(slices.Clone(sortedKeys[len(sortedKeys)-1]), 0)
	}
	benchmarkIndexPutWithRead(b, keys, func(kvindex index) bool {
		if count := kvindex.CountRevisions(start, end, math.MaxInt64); count != countKeys {
			b.Errorf("range count = %d, want %d", count, countKeys)
			return false
		}
		return true
	})
}

func BenchmarkIndexPutRangeLimit(b *testing.B) {
	const limit = 100
	for _, dataset := range indexBenchDatasets {
		if dataset.keySpace != 100_000 {
			continue
		}
		for _, withTotalCount := range []bool{true, false} {
			b.Run(fmt.Sprintf("Keys=%d,KeyLength=%d,Limit=%d,WithTotalCount=%t", dataset.keySpace, dataset.keyLen, limit, withTotalCount), func(b *testing.B) {
				benchmarkIndexPutRangeLimit(b, dataset.keys(), limit, withTotalCount)
			})
		}
	}
}

func benchmarkIndexPutRangeLimit(b *testing.B, keys [][]byte, limit int, withTotalCount bool) {
	end := []byte{}
	wantTotal := limit
	if withTotalCount {
		wantTotal = len(keys)
	}
	benchmarkIndexPutWithRead(b, keys, func(kvindex index) bool {
		rangeKeys, _, _, _, total := kvindex.Range(nil, end, math.MaxInt64, limit, withTotalCount)
		if len(rangeKeys) != limit || total != wantTotal {
			b.Errorf("range result = %d keys, total %d; want %d keys, total %d", len(rangeKeys), total, limit, wantTotal)
			return false
		}
		return true
	})
}

func benchmarkIndexPutWithRead(b *testing.B, keys [][]byte, read func(index) bool) {
	kvindex := newTreeIndex(zap.NewNop())
	for i, key := range keys {
		kvindex.Put(key, Revision{Main: int64(i + 1)})
	}
	var stop atomic.Bool
	var wg sync.WaitGroup
	readerReady := make(chan struct{})
	wg.Go(func() {
		close(readerReady)
		for !stop.Load() {
			if !read(kvindex) {
				return
			}
		}
	})

	// Wait for the reader goroutine to start before timing Puts.
	<-readerReady
	// Allocation statistics include concurrent reads and are normalized by the Put count.
	b.ReportAllocs()
	b.ResetTimer()
	for i := range b.N {
		kvindex.Put(keys[i%len(keys)], Revision{Main: int64(len(keys) + i + 1)})
	}
	stop.Store(true)
	b.StopTimer()
	wg.Wait()
}

func BenchmarkIndexGet(b *testing.B) {
	log := zap.NewNop()
	kvindex := newTreeIndex(log)

	bytesN := 64
	keys := createBytesSlice(bytesN, b.N)
	for i := 1; i < b.N; i++ {
		kvindex.Put(keys[i], Revision{Main: int64(i), Sub: int64(i)})
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 1; i < b.N; i++ {
		kvindex.Get(keys[i], int64(i))
	}
}

// indexBenchDataset lazily creates random keys in allocation order and caches
// a sorted view for range boundaries. Both views are shared read-only.
//
// Based on the current etcd design, the key index uses key bytes from the
// request directly and assumes they will not be modified, so we can share the
// benchmark dataset as long as we don't modify it after creation.
type indexBenchDataset struct {
	once sync.Once

	keySpace      int
	keyLen        int
	keyData       [][]byte
	sortedKeyData [][]byte
}

func (dataset *indexBenchDataset) keys() [][]byte {
	dataset.once.Do(func() {
		dataset.keyData = createBytesSlice(dataset.keyLen, dataset.keySpace)
		sortedKeys := slices.Clone(dataset.keyData)
		slices.SortFunc(sortedKeys, bytes.Compare)
		for i := 1; i < len(sortedKeys); i++ {
			if bytes.Equal(sortedKeys[i-1], sortedKeys[i]) {
				panic("mvcc: index benchmark dataset contains duplicate keys")
			}
		}
		dataset.sortedKeyData = sortedKeys
	})
	return dataset.keyData
}

// sortedKeys returns the cached sorted view, sharing the immutable key bytes.
func (dataset *indexBenchDataset) sortedKeys() [][]byte {
	dataset.keys()
	return dataset.sortedKeyData
}

// indexBenchDatasets uses 32B, 64B and 128B keys based on Kubernetes storage key
// lengths, which vary with the resource prefix, namespace and object name.
var indexBenchDatasets = []*indexBenchDataset{
	{keySpace: 10_000, keyLen: 32},
	{keySpace: 10_000, keyLen: 64},
	{keySpace: 10_000, keyLen: 128},
	{keySpace: 100_000, keyLen: 32},
	{keySpace: 100_000, keyLen: 64},
	{keySpace: 100_000, keyLen: 128},
	{keySpace: 1_000_000, keyLen: 32},
	{keySpace: 1_000_000, keyLen: 64},
	{keySpace: 1_000_000, keyLen: 128},
}
