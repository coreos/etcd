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

package cache

import (
	"bytes"
	"fmt"
	"testing"
	"time"

	"go.etcd.io/etcd/api/v3/mvccpb"
	clientv3 "go.etcd.io/etcd/client/v3"
)

func BenchmarkStoreGetSingleKey100(b *testing.B)   { benchmarkStoreGetSingleKey(b, 100) }
func BenchmarkStoreGetSingleKey1000(b *testing.B)  { benchmarkStoreGetSingleKey(b, 1000) }
func BenchmarkStoreGetSingleKey10000(b *testing.B) { benchmarkStoreGetSingleKey(b, 10000) }

func benchmarkStoreGetSingleKey(b *testing.B, numKeys int) {
	s := newPopulatedStore(b, numKeys)
	target := []byte(fmt.Sprintf("/key/%05d", numKeys/2))

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, _, err := s.Get(target, nil, 0); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkStoreGetRange100(b *testing.B)   { benchmarkStoreGetRange(b, 100) }
func BenchmarkStoreGetRange1000(b *testing.B)  { benchmarkStoreGetRange(b, 1000) }
func BenchmarkStoreGetRange10000(b *testing.B) { benchmarkStoreGetRange(b, 10000) }

func benchmarkStoreGetRange(b *testing.B, numKeys int) {
	s := newPopulatedStore(b, numKeys)
	start := []byte("/key/")
	end := incrementLastByte(start)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, _, err := s.Get(start, end, 0); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkStoreGetPrefix100(b *testing.B)   { benchmarkStoreGetPrefix(b, 100) }
func BenchmarkStoreGetPrefix1000(b *testing.B)  { benchmarkStoreGetPrefix(b, 1000) }
func BenchmarkStoreGetPrefix10000(b *testing.B) { benchmarkStoreGetPrefix(b, 10000) }

func benchmarkStoreGetPrefix(b *testing.B, numKeys int) {
	s := newPopulatedStore(b, numKeys)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, _, err := s.Get([]byte("/key/"), []byte{0}, 0); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkStoreRestore100(b *testing.B)   { benchmarkStoreRestore(b, 100) }
func BenchmarkStoreRestore1000(b *testing.B)  { benchmarkStoreRestore(b, 1000) }
func BenchmarkStoreRestore10000(b *testing.B) { benchmarkStoreRestore(b, 10000) }

func benchmarkStoreRestore(b *testing.B, numKeys int) {
	kvs := generateKVs(numKeys, 100)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s := newStore(32, 2048)
		s.Restore(kvs, 100)
	}
}

func BenchmarkDemuxBroadcast10Watchers(b *testing.B)   { benchmarkDemuxBroadcast(b, 10, false) }
func BenchmarkDemuxBroadcast100Watchers(b *testing.B)  { benchmarkDemuxBroadcast(b, 100, false) }
func BenchmarkDemuxBroadcast1000Watchers(b *testing.B) { benchmarkDemuxBroadcast(b, 1000, false) }

func BenchmarkDemuxBroadcastWithPredicate10(b *testing.B)   { benchmarkDemuxBroadcast(b, 10, true) }
func BenchmarkDemuxBroadcastWithPredicate100(b *testing.B)  { benchmarkDemuxBroadcast(b, 100, true) }
func BenchmarkDemuxBroadcastWithPredicate1000(b *testing.B) { benchmarkDemuxBroadcast(b, 1000, true) }

func benchmarkDemuxBroadcast(b *testing.B, numWatchers int, withPredicate bool) {
	d := newDemux(b.N+numWatchers, 1*time.Hour)
	d.Init(1)

	watchers := make([]*watcher, numWatchers)
	for i := range watchers {
		var pred KeyPredicate
		if withPredicate {
			prefix := []byte(fmt.Sprintf("/key/%05d", i))
			pred = func(key []byte) bool { return bytes.HasPrefix(key, prefix) }
		}
		watchers[i] = newWatcher(b.N+1, pred)
		d.Register(watchers[i], 0)
	}

	events := make([]clientv3.WatchResponse, b.N)
	for i := range events {
		rev := int64(2 + i)
		events[i] = clientv3.WatchResponse{
			Events: []*clientv3.Event{
				{Type: clientv3.EventTypePut, Kv: &mvccpb.KeyValue{
					Key: []byte(fmt.Sprintf("/key/%05d", i%numWatchers)), Value: []byte("v"), ModRevision: rev,
				}},
			},
		}
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := d.Broadcast(events[i]); err != nil {
			b.Fatal(err)
		}
	}
	b.StopTimer()
	for _, w := range watchers {
		w.Stop()
	}
}

func newPopulatedStore(b *testing.B, numKeys int) *store {
	b.Helper()
	kvs := generateKVs(numKeys, 100)
	s := newStore(32, 2048)
	s.Restore(kvs, 100)
	return s
}

func generateKVs(n int, rev int64) []*mvccpb.KeyValue {
	kvs := make([]*mvccpb.KeyValue, n)
	for i := range kvs {
		kvs[i] = &mvccpb.KeyValue{
			Key:            []byte(fmt.Sprintf("/key/%05d", i)),
			Value:          []byte(fmt.Sprintf("value-%05d", i)),
			ModRevision:    rev,
			CreateRevision: rev,
			Version:        1,
		}
	}
	return kvs
}

func incrementLastByte(prefix []byte) []byte {
	end := make([]byte, len(prefix))
	copy(end, prefix)
	end[len(end)-1]++
	return end
}
