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

package backend_test

import (
	"encoding/binary"
	"path/filepath"
	"testing"
	"time"

	"go.uber.org/zap"

	"go.etcd.io/etcd/server/v3/storage/backend"
	"go.etcd.io/etcd/server/v3/storage/schema"
)

func BenchmarkCompactionDeleteApplyWait(b *testing.B) {
	for _, tc := range []struct {
		name       string
		compaction bool
	}{
		{name: "DeleteCommitOnUnlock"},
		{name: "CompactionDeleteCommitAfterUnlock", compaction: true},
	} {
		b.Run(tc.name, func(b *testing.B) {
			be, keys := newCompactionBenchmarkBackend(b)
			defer be.Close()
			tx := be.BatchTx()

			b.StopTimer()
			for i := range b.N {
				tx.Lock()
				if tc.compaction {
					tx.UnsafeDeleteForCompaction(schema.Test, keys[i])
				} else {
					tx.UnsafeDelete(schema.Test, keys[i])
				}

				started := make(chan struct{})
				done := make(chan struct{})
				go func() {
					close(started)
					tx.Lock()
					tx.Unlock()
					close(done)
				}()
				<-started
				// A real apply waiter can spend milliseconds behind a compaction
				// batch. Ensure the waiter is queued before compaction unlocks.
				time.Sleep(2 * time.Millisecond)

				b.StartTimer()
				tx.Unlock()
				<-done
				b.StopTimer()

				if tc.compaction {
					be.ForceCommit()
				}
			}
		})
	}
}

func BenchmarkCompactionDeleteBatch(b *testing.B) {
	for _, tc := range []struct {
		name       string
		compaction bool
	}{
		{name: "DeleteCommitOnUnlock"},
		{name: "CompactionDeleteCommitAfterUnlock", compaction: true},
	} {
		b.Run(tc.name, func(b *testing.B) {
			be, keys := newCompactionBenchmarkBackend(b)
			defer be.Close()
			tx := be.BatchTx()

			for i := range b.N {
				tx.Lock()
				if tc.compaction {
					tx.UnsafeDeleteForCompaction(schema.Test, keys[i])
				} else {
					tx.UnsafeDelete(schema.Test, keys[i])
				}
				tx.Unlock()
				if tc.compaction {
					be.ForceCommit()
				}
			}
		})
	}
}

func newCompactionBenchmarkBackend(b *testing.B) (backend.Backend, [][]byte) {
	cfg := backend.DefaultBackendConfig(zap.NewNop())
	cfg.Path = filepath.Join(b.TempDir(), "database")
	cfg.BatchInterval = time.Hour
	cfg.BatchLimit = 10000
	be := backend.New(cfg)
	tx := be.BatchTx()
	tx.Lock()
	tx.UnsafeCreateBucket(schema.Test)
	keys := make([][]byte, b.N)
	for i := range b.N {
		keys[i] = make([]byte, 8)
		binary.BigEndian.PutUint64(keys[i], uint64(i))
		tx.UnsafePut(schema.Test, keys[i], []byte("value"))
	}
	tx.Unlock()
	be.ForceCommit()
	return be, keys
}
