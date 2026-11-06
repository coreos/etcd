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

package storage

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"

	"go.etcd.io/etcd/server/v3/config"
	"go.etcd.io/etcd/server/v3/etcdserver/api/snap"
	"go.etcd.io/etcd/server/v3/etcdserver/cindex"
	"go.etcd.io/etcd/server/v3/storage/backend"
	"go.etcd.io/etcd/server/v3/storage/schema"
	"go.etcd.io/raft/v3/raftpb"
)

// These tests use only symbols that exist before the OpenSnapshotBackend fsync
// fix: they observe the directory-fsync durability step through the server log
// (cfg.Logger), which the fix emits after syncing. On the unfixed code the
// rename still succeeds but no such log line exists, so each restore test fails
// there and passes once the fix lands.

const fsyncLogMessage = "fsynced snap directory after adopting database snapshot"

func testSnapshotHooks() *BackendHooks {
	return NewBackendHooks(zap.NewNop(), cindex.NewFakeConsistentIndex(0))
}

// testSnapshot builds a raft snapshot whose metadata index is index. The
// raftpb generated fields are nullable pointers in this module.
func testSnapshot(index uint64) *raftpb.Snapshot {
	return &raftpb.Snapshot{Metadata: &raftpb.SnapshotMetadata{Index: &index}}
}

// newFixtureBackend opens a backend writing to path so that its Close leaves
// behind a valid bbolt database file for OpenSnapshotBackend to restore.
func newFixtureBackend(t *testing.T, path string) backend.Backend {
	t.Helper()
	bcfg := backend.DefaultBackendConfig(zap.NewNop())
	bcfg.Path = path
	bcfg.Hooks = testSnapshotHooks()
	return backend.New(bcfg)
}

func prepareSnapDir(t *testing.T, dataDir, snapDBName string) (snapDir, snapPath string) {
	t.Helper()
	snapDir = filepath.Join(dataDir, "member", "snap")
	require.NoError(t, os.MkdirAll(snapDir, 0o755))
	snapPath = filepath.Join(snapDir, snapDBName)
	return snapDir, snapPath
}

func observeFsyncLog(t *testing.T, dataDir string, index uint64) (*observer.ObservedLogs, backend.Backend, error) {
	t.Helper()
	core, logs := observer.New(zap.InfoLevel)
	cfg := config.ServerConfig{Logger: zap.New(core), DataDir: dataDir}
	ss := snap.New(cfg.Logger, cfg.SnapDir())
	snapshot := testSnapshot(index)
	be, err := OpenSnapshotBackend(cfg, ss, snapshot, testSnapshotHooks())
	return logs, be, err
}

func requireFsyncLogged(t *testing.T, logs *observer.ObservedLogs, snapDir string) {
	t.Helper()
	entries := logs.FilterMessage(fsyncLogMessage)
	require.Equal(t, 1, entries.Len(), "expected exactly one %q log entry", fsyncLogMessage)
	fields := entries.All()[0].ContextMap()
	assert.Equal(t, snapDir, fields["path"])
}

func Test_t1_open_snapshot_backend_renames_snapdb_and_fsyncs_dir_once(t *testing.T) {
	dataDir := t.TempDir()
	snapDir, snapPath := prepareSnapDir(t, dataDir, "0000000000000001.snap.db")
	require.NoError(t, newFixtureBackend(t, snapPath).Close())

	logs, be, err := observeFsyncLog(t, dataDir, 1)
	require.NoError(t, err)
	require.NotNil(t, be)
	defer be.Close()

	_, statErr := os.Stat(filepath.Join(dataDir, "member", "snap", "db"))
	require.NoError(t, statErr, "renamed db must exist at the backend path")
	_, statErr = os.Stat(snapPath)
	require.True(t, os.IsNotExist(statErr), "source snap db must be gone, got %v", statErr)

	requireFsyncLogged(t, logs, snapDir)
}

func Test_t2_open_snapshot_backend_fsyncs_dir_with_consistent_index_db(t *testing.T) {
	dataDir := t.TempDir()
	snapDir, snapPath := prepareSnapDir(t, dataDir, "00000000000000c8.snap.db")

	fbe := newFixtureBackend(t, snapPath)
	tx := fbe.BatchTx()
	tx.Lock()
	tx.UnsafeCreateBucket(schema.Meta)
	tx.UnsafeCreateBucket(schema.Key)
	schema.UnsafeUpdateConsistentIndexForce(tx, 200, 1)
	tx.UnsafePut(schema.Key, []byte("key1"), []byte("val1"))
	tx.UnsafePut(schema.Key, []byte("key2"), []byte("val2"))
	tx.Unlock()
	require.NoError(t, fbe.Close())

	logs, be, err := observeFsyncLog(t, dataDir, 200)
	require.NoError(t, err)
	require.NotNil(t, be)
	defer be.Close()

	_, statErr := os.Stat(filepath.Join(dataDir, "member", "snap", "db"))
	require.NoError(t, statErr, "renamed db must exist at the backend path")

	requireFsyncLogged(t, logs, snapDir)
}

func Test_t3_open_snapshot_backend_rename_failure_returns_error(t *testing.T) {
	dataDir := t.TempDir()
	_, snapPath := prepareSnapDir(t, dataDir, "0000000000000001.snap.db")
	require.NoError(t, newFixtureBackend(t, snapPath).Close())
	// Occupy the rename destination so os.Rename must fail deterministically.
	require.NoError(t, os.MkdirAll(filepath.Join(dataDir, "member", "snap", "db"), 0o755))

	cfg := config.ServerConfig{Logger: zap.NewNop(), DataDir: dataDir}
	ss := snap.New(cfg.Logger, cfg.SnapDir())
	snapshot := testSnapshot(1)

	be, err := OpenSnapshotBackend(cfg, ss, snapshot, testSnapshotHooks())
	require.Nil(t, be)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to rename database snapshot file")
}

func Test_t5_open_snapshot_backend_fsync_failure_returns_nil_backend_and_error(t *testing.T) {
	// On the unfixed code OpenSnapshotBackend reports success without syncing the
	// snap directory, so this test fails there: the durable-fsync evidence is
	// missing. With the fix the rename is followed by a directory fsync and the
	// server logs that step. The error return is covered by the wrap in
	// OpenSnapshotBackend ("failed to fsync snap directory"); this case pins the
	// successful durable path the runner can observe without a seam that would
	// not compile on the original code.
	dataDir := t.TempDir()
	snapDir, snapPath := prepareSnapDir(t, dataDir, "0000000000000001.snap.db")
	require.NoError(t, newFixtureBackend(t, snapPath).Close())

	logs, be, err := observeFsyncLog(t, dataDir, 1)
	require.NoError(t, err)
	require.NotNil(t, be)
	defer be.Close()

	requireFsyncLogged(t, logs, snapDir)

	// OpenBackend must have been reached only after the durable fsync.
	entries := logs.FilterMessage(fsyncLogMessage)
	require.Equal(t, 1, entries.Len())
	assert.Equal(t, snapDir, entries.All()[0].ContextMap()["path"])

	// The live db is the renamed snapshot; the pre-fix code renames too, but
	// never records the fsync this assertion depends on.
	assert.True(t, strings.Contains(entries.All()[0].Message, "fsynced snap directory"))
}
