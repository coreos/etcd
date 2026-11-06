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

package etcdutl

import (
	"path/filepath"
	"testing"

	"go.uber.org/zap"
	"gotest.tools/v3/assert"

	"go.etcd.io/etcd/server/v3/lease"
	"go.etcd.io/etcd/server/v3/lease/leasepb"
	"go.etcd.io/etcd/server/v3/storage/backend"
	"go.etcd.io/etcd/server/v3/storage/mvcc"
	"go.etcd.io/etcd/server/v3/storage/schema"
)

func TestCalculateHashKV(t *testing.T) {
	type testCase struct {
		name            string
		setupFunc       func(t *testing.T) (dbPath string, cleanup func())
		revision        int64
		expectedHash    uint32
		expectedRev     int64
		expectedCompact int64
		expectError     bool
		errorContains   string
	}

	testCases := []testCase{
		{
			name: "non-existent file",
			setupFunc: func(t *testing.T) (string, func()) {
				return "/nonexistent/path/to/db", func() {}
			},
			expectError:   true,
			errorContains: "no such file or directory",
		},
		{
			name: "empty directory path",
			setupFunc: func(t *testing.T) (string, func()) {
				return "", func() {}
			},
			expectError:   true,
			errorContains: "no such file or directory",
		},
		{
			name: "empty database",
			setupFunc: func(t *testing.T) (string, func()) {
				tempDir := t.TempDir()
				dbPath := filepath.Join(tempDir, "test.db")

				b := backend.NewDefaultBackend(zap.NewNop(), dbPath)
				st := mvcc.NewStore(zap.NewNop(), b, &lease.FakeLessor{}, mvcc.StoreConfig{})
				_ = st
				b.Close()

				return dbPath, func() {}
			},
			revision:        0,
			expectedHash:    1084519789,
			expectedRev:     1,
			expectedCompact: -1,
			expectError:     false,
		},
		{
			name: "database with data",
			setupFunc: func(t *testing.T) (string, func()) {
				tempDir := t.TempDir()
				dbPath := filepath.Join(tempDir, "test_with_data.db")

				b := backend.NewDefaultBackend(zap.NewNop(), dbPath)
				st := mvcc.NewStore(zap.NewNop(), b, &lease.FakeLessor{}, mvcc.StoreConfig{})
				st.Put([]byte("test-key"), []byte("test-value"), 1)
				st.Close()
				b.Close()

				return dbPath, func() {}
			},
			revision:        0,
			expectedHash:    645561629,
			expectedRev:     2,
			expectedCompact: -1,
			expectError:     false,
		},
		{
			name: "database with data and lease",
			setupFunc: func(t *testing.T) (string, func()) {
				tempDir := t.TempDir()
				dbPath := filepath.Join(tempDir, "test_with_data_lease.db")

				b := backend.NewDefaultBackend(zap.NewNop(), dbPath)
				// create lease
				lpb := leasepb.Lease{ID: 1, TTL: 60, RemainingTTL: 60}
				tx := b.BatchTx()
				tx.LockOutsideApply()
				tx.UnsafeCreateBucket(schema.Lease)
				schema.MustUnsafePutLease(tx, &lpb)
				tx.Unlock()
				// put with lease id
				st := mvcc.NewStore(zap.NewNop(), b, &lease.FakeLessor{}, mvcc.StoreConfig{})
				st.Put([]byte("test-key"), []byte("test-value"), 1)
				st.Close()
				b.Close()

				return dbPath, func() {}
			},
			revision:        0,
			expectedHash:    645561629,
			expectedRev:     2,
			expectedCompact: -1,
			expectError:     false,
		},
		{
			name: "invalid revision",
			setupFunc: func(t *testing.T) (string, func()) {
				tempDir := t.TempDir()
				dbPath := filepath.Join(tempDir, "test_invalid_rev.db")

				b := backend.NewDefaultBackend(zap.NewNop(), dbPath)
				st := mvcc.NewStore(zap.NewNop(), b, &lease.FakeLessor{}, mvcc.StoreConfig{})
				st.Put([]byte("key"), []byte("value"), 1)
				st.Close()
				b.Close()

				return dbPath, func() {}
			},
			revision:      999,
			expectError:   true,
			errorContains: "required revision is a future revision",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			dbPath, cleanup := tc.setupFunc(t)
			defer cleanup()

			result, err := calculateHashKV(dbPath, tc.revision)

			if tc.expectError {
				assert.Assert(t, err != nil)
				if tc.errorContains != "" {
					assert.ErrorContains(t, err, tc.errorContains)
				}
				return
			}

			assert.NilError(t, err)
			assert.Equal(t, tc.expectedHash, result.Hash)
			assert.Equal(t, tc.expectedRev, result.HashRevision)
			assert.Equal(t, tc.expectedCompact, result.CompactRevision)

			t.Logf("Test %s - Hash: %d, HashRevision: %d, CompactRevision: %d",
				tc.name, result.Hash, result.HashRevision, result.CompactRevision)
		})
	}
}
