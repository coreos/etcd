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

	"go.uber.org/zap/zaptest"

	"go.etcd.io/etcd/server/v3/lease"
	betesting "go.etcd.io/etcd/server/v3/storage/backend/testing"
)

// BenchmarkWatchStreamWatchCritical measures the duration of the
// watchStream.Watch() critical section. serverWatchStream.recvLoop holds its
// own mu across this call so that a watch ID is registered as awaiting its
// created response before any event for it can be classified, so the duration
// measured here bounds how long event handling on that stream can be stalled
// by a watch creation.
func BenchmarkWatchStreamWatchCritical(b *testing.B) {
	be, _ := betesting.NewDefaultTmpBackend(b)
	s := New(zaptest.NewLogger(b), be, &lease.FakeLessor{}, StoreConfig{})
	defer cleanup(s, be)

	k := []byte("testkey")
	w := s.NewWatchStream()
	defer w.Close()

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := w.Watch(b.Context(), 0, k, nil, 0); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkWatchStreamWatchCriticalWithLoad measures the same critical section
// while watchers are already registered on the stream and the store is being
// written to, so that Watch() has to contend on watchableStore.mu with
// notify(). Each iteration adds a watcher, so notify() walks a growing set and
// the reported figure is an overestimate of the lock wait alone.
func BenchmarkWatchStreamWatchCriticalWithLoad(b *testing.B) {
	be, _ := betesting.NewDefaultTmpBackend(b)
	s := New(zaptest.NewLogger(b), be, &lease.FakeLessor{}, StoreConfig{})
	defer cleanup(s, be)

	k := []byte("testkey")
	v := []byte("testval")

	w := s.NewWatchStream()
	defer w.Close()
	for range 1000 {
		if _, err := w.Watch(b.Context(), 0, k, nil, 0); err != nil {
			b.Fatal(err)
		}
	}

	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			default:
				s.Put(k, v, lease.NoLease)
			}
		}
	}()
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		for {
			select {
			case <-stop:
				return
			case <-w.Chan():
			}
		}
	}()

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := w.Watch(b.Context(), 0, k, nil, 0); err != nil {
			b.Fatal(err)
		}
	}
	b.StopTimer()
	close(stop)
	<-done
	<-drained
}
