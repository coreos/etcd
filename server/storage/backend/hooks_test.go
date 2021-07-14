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

package backend_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"go.etcd.io/etcd/server/v3/storage/backend"
	betesting "go.etcd.io/etcd/server/v3/storage/backend/testing"
	"go.etcd.io/etcd/server/v3/storage/schema"
)

var (
	bucket = schema.Test
	key    = []byte("key")
)

func TestBackendPreCommitHook(t *testing.T) {
	be := newTestHooksBackend(t, backend.DefaultBackendConfig())

	tx := be.BatchTx()
	prepareBuckenAndKey(tx)
	tx.Commit()

	// Empty commit.
	tx.Commit()

	write(tx, []byte("foo"), []byte("bar"))

	assert.Equal(t, ">cc", getCommitsKey(t, be), "expected 2 explict commits")
	tx.Commit()
	assert.Equal(t, ">ccc", getCommitsKey(t, be), "expected 3 explict commits")
}

func TestBackendAutoCommitLimitHook(t *testing.T) {
	cfg := backend.DefaultBackendConfig()
	cfg.BatchLimit = 3
	be := newTestHooksBackend(t, cfg)

	tx := be.BatchTx()
	prepareBuckenAndKey(tx) // writes 2 entries.

	for i := 3; i <= 9; i++ {
		write(tx, []byte("i"), []byte{byte(i)})
	}

	assert.Equal(t, ">ccc", getCommitsKey(t, be))
}

func write(tx backend.BatchTx, k, v []byte) {
	tx.Lock()
	defer tx.Unlock()
	tx.UnsafePut(bucket, k, v)
}

func TestBackendAutoCommitBatchIntervalHook(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	cfg := backend.DefaultBackendConfig()
	cfg.BatchInterval = 10 * time.Millisecond
	be := newTestHooksBackend(t, cfg)
	tx := be.BatchTx()
	prepareBuckenAndKey(tx)

	// Edits trigger an auto-commit
	waitUntil(ctx, t, func() bool { return getCommitsKey(t, be) == ">c" })

	time.Sleep(time.Second)
	// No additional auto-commits, as there were no more edits
	assert.Equal(t, ">c", getCommitsKey(t, be))

	write(tx, []byte("foo"), []byte("bar1"))

	waitUntil(ctx, t, func() bool { return getCommitsKey(t, be) == ">cc" })

	write(tx, []byte("foo"), []byte("bar1"))

	waitUntil(ctx, t, func() bool { return getCommitsKey(t, be) == ">ccc" })
}

func waitUntil(ctx context.Context, t testing.TB, f func() bool) {
	for !f() {
		select {
		case <-ctx.Done():
			t.Fatalf("Context cancelled/timedout without condition met: %v", ctx.Err())
		default:
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func prepareBuckenAndKey(tx backend.BatchTx) {
	tx.Lock()
	defer tx.Unlock()
	tx.UnsafeCreateBucket(bucket)
	tx.UnsafePut(bucket, key, []byte(">"))
}

func newTestHooksBackend(t testing.TB, baseConfig backend.BackendConfig) backend.Backend {
	cfg := baseConfig
	cfg.Hooks = backend.NewHooks(func(tx backend.BatchTx) {
		k, v := tx.UnsafeRange(bucket, key, nil, 1)
		t.Logf("OnPreCommit executed: %v %v", string(k[0]), string(v[0]))
		assert.Len(t, k, 1)
		assert.Len(t, v, 1)
		tx.UnsafePut(bucket, key, append(v[0], byte('c')))
	})

	be, _ := betesting.NewTmpBackendFromCfg(t, cfg)
	t.Cleanup(func() {
		betesting.Close(t, be)
	})
	return be
}

func getCommitsKey(t testing.TB, be backend.Backend) string {
	rtx := be.BatchTx()
	rtx.Lock()
	defer rtx.Unlock()
	_, v := rtx.UnsafeRange(bucket, key, nil, 1)
	assert.Len(t, v, 1)
	return string(v[0])
}
