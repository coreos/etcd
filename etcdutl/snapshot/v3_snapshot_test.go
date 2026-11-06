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

package snapshot

import (
	"errors"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"crypto/fips140"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"go.etcd.io/bbolt"
	"go.etcd.io/etcd/api/v3/etcdserverpb"
	"go.etcd.io/etcd/server/v3/embed"
	"go.etcd.io/etcd/server/v3/etcdserver"
	"go.etcd.io/etcd/server/v3/storage/mvcc"
	"go.etcd.io/etcd/server/v3/storage/schema"
)

// TestSnapshotStatus is the happy case.
// It inserts pre-defined number of keys and asserts the output hash of status command.
// The expected hash value must not be changed.
// If it changes, there must be some backwards incompatible change introduced.
func TestSnapshotStatus(t *testing.T) {
	if fips140.Enabled() {
		t.Skip("TestSnapshotStatus uses SHA-1-derived expected hash, skipping in FIPS mode")
	}
	dbpath := createDB(t, insertKeys(t, 10, 100))

	status, err := NewV3(zap.NewNop()).Status(dbpath)
	require.NoError(t, err)

	assert.Equal(t, uint32(0x4b84b1be), status.Hash)
	assert.Equal(t, int64(11), status.Revision)
}

// TestSnapshotStatusCorruptRevision tests if snapshot status command fails when there is an unexpected revision in "key" bucket.
func TestSnapshotStatusCorruptRevision(t *testing.T) {
	dbpath := createDB(t, insertKeys(t, 1, 0))

	db, err := bbolt.Open(dbpath, 0o600, nil)
	require.NoError(t, err)
	defer db.Close()

	err = db.Update(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte("key"))
		if b == nil {
			return errors.New("key bucket not found")
		}
		return b.Put([]byte("0"), []byte{})
	})
	require.NoError(t, err)
	db.Close()

	_, err = NewV3(zap.NewNop()).Status(dbpath)
	require.ErrorContains(t, err, "invalid revision length")
}

// TestSnapshotStatusNegativeRevisionMain tests if snapshot status command fails when main revision number is negative.
func TestSnapshotStatusNegativeRevisionMain(t *testing.T) {
	dbpath := createDB(t, insertKeys(t, 1, 0))

	db, err := bbolt.Open(dbpath, 0o666, nil)
	require.NoError(t, err)
	defer db.Close()

	err = db.Update(func(tx *bbolt.Tx) error {
		b := tx.Bucket(schema.Key.Name())
		if b == nil {
			return errors.New("key bucket not found")
		}
		bytes := mvcc.NewRevBytes()
		mvcc.RevToBytes(mvcc.Revision{Main: -1}, bytes)
		return b.Put(bytes, []byte{})
	})
	require.NoError(t, err)
	db.Close()

	_, err = NewV3(zap.NewNop()).Status(dbpath)
	require.ErrorContains(t, err, "negative revision")
}

// TestSnapshotStatusNegativeRevisionSub tests if snapshot status command fails when sub revision number is negative.
func TestSnapshotStatusNegativeRevisionSub(t *testing.T) {
	dbpath := createDB(t, insertKeys(t, 1, 0))

	db, err := bbolt.Open(dbpath, 0o666, nil)
	require.NoError(t, err)
	defer db.Close()

	err = db.Update(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte("key"))
		if b == nil {
			return errors.New("key bucket not found")
		}
		bytes := mvcc.NewRevBytes()
		mvcc.RevToBytes(mvcc.Revision{Sub: -1}, bytes)
		return b.Put(bytes, []byte{})
	})
	require.NoError(t, err)
	db.Close()

	_, err = NewV3(zap.NewNop()).Status(dbpath)
	require.ErrorContains(t, err, "negative revision")
}

// TestSnapshotStatusTotalKey tests if snapshot status command correctly reports total number of valid keys.
func TestSnapshotStatusTotalKey(t *testing.T) {
	cases := []struct {
		name     string
		prepare  func(srv *etcdserver.EtcdServer)
		expected int
	}{
		{
			name: "duplicate keys",
			prepare: func(srv *etcdserver.EtcdServer) {
				keys := []string{"key1", "key2", "key1"}
				val := make([]byte, len(keys))
				for _, key := range keys {
					for i := 0; i < 3; i++ {
						req := etcdserverpb.PutRequest{
							Key:   []byte(key),
							Value: val,
						}
						_, err := srv.Put(t.Context(), &req)
						require.NoError(t, err)
					}
				}
			},
			expected: 2,
		},
		{
			name: "mixed revisions",
			prepare: func(srv *etcdserver.EtcdServer) {
				// key1: create -> put -> delete
				key := []byte("key1")
				for i := 0; i < 3; i++ {
					if i < 2 {
						_, err := srv.Put(t.Context(), &etcdserverpb.PutRequest{Key: key, Value: []byte(strconv.Itoa(i))})
						require.NoError(t, err)
					} else {
						_, err := srv.DeleteRange(t.Context(), &etcdserverpb.DeleteRangeRequest{Key: key})
						require.NoError(t, err)
					}
				}
			},
			expected: 0,
		},
		{
			name: "ignored tombstones",
			prepare: func(srv *etcdserver.EtcdServer) {
				// key1: create -> delete -> re-create -> delete
				key := []byte("key1")
				for i := 0; i < 2; i++ {
					_, err := srv.Put(t.Context(), &etcdserverpb.PutRequest{Key: key, Value: make([]byte, 1)})
					require.NoError(t, err)
					_, err = srv.DeleteRange(t.Context(), &etcdserverpb.DeleteRangeRequest{Key: key})
					require.NoError(t, err)
				}
			},
			expected: 0,
		},
		{
			name: "restored keys",
			prepare: func(srv *etcdserver.EtcdServer) {
				// key1: create -> delete -> re-create -> delete -> re-create
				key := []byte("key1")
				for i := 0; i < 5; i++ {
					if i%2 == 0 {
						_, err := srv.Put(t.Context(), &etcdserverpb.PutRequest{Key: key, Value: make([]byte, 1)})
						require.NoError(t, err)
					} else {
						_, err := srv.DeleteRange(t.Context(), &etcdserverpb.DeleteRangeRequest{Key: key})
						require.NoError(t, err)
					}
				}
			},
			expected: 1,
		},
		{
			name: "mixed deletions",
			prepare: func(srv *etcdserver.EtcdServer) {
				// Put("key1") -> Put("key2")-> Delete("key1")
				_, err := srv.Put(t.Context(), &etcdserverpb.PutRequest{Key: []byte("key1"), Value: make([]byte, 1)})
				require.NoError(t, err)
				_, err = srv.Put(t.Context(), &etcdserverpb.PutRequest{Key: []byte("key2"), Value: make([]byte, 1)})
				require.NoError(t, err)
				_, err = srv.DeleteRange(t.Context(), &etcdserverpb.DeleteRangeRequest{Key: []byte("key1")})
				require.NoError(t, err)
			},
			expected: 1,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dbpath := createDB(t, tc.prepare)

			status, err := NewV3(zap.NewNop()).Status(dbpath)
			require.NoError(t, err)

			assert.Equal(t, tc.expected, status.TotalKey)
		})
	}
}

// insertKeys insert `numKeys` number of keys of `valueSize` size into a running etcd server.
func insertKeys(t *testing.T, numKeys, valueSize int) func(*etcdserver.EtcdServer) {
	t.Helper()
	return func(srv *etcdserver.EtcdServer) {
		val := make([]byte, valueSize)
		for i := 0; i < numKeys; i++ {
			req := etcdserverpb.PutRequest{
				Key:   []byte(strconv.Itoa(i)),
				Value: val,
			}
			_, err := srv.Put(t.Context(), &req)
			require.NoError(t, err)
		}
	}
}

// createDB creates a bbolt database file by running an embedded etcd server.
// While the server is running, `generateContent` function is called to insert values.
// It returns the path of bbolt database.
func createDB(t *testing.T, generateContent func(*etcdserver.EtcdServer)) string {
	t.Helper()

	cfg := embed.NewConfig()
	cfg.BackendBatchLimit = 1
	cfg.LogLevel = "fatal"
	cfg.Dir = t.TempDir()

	etcd, err := embed.StartEtcd(cfg)
	require.NoError(t, err)
	defer etcd.Close()

	select {
	case <-etcd.Server.ReadyNotify():
	case <-time.After(10 * time.Second):
		t.FailNow()
	}

	generateContent(etcd.Server)

	return filepath.Join(cfg.Dir, "member", "snap", "db")
}
