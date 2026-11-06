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
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"go.etcd.io/etcd/server/v3/config"
	"go.etcd.io/etcd/server/v3/etcdserver/api/snap"
	"go.etcd.io/etcd/server/v3/etcdserver/cindex"
	"go.etcd.io/raft/v3/raftpb"
)

// setupSnapDirForBackend creates a member/snap layout under a temp data dir
// with one snap.db file at the given index.
func setupSnapDirForBackend(t *testing.T, index uint64) config.ServerConfig {
	t.Helper()
	cfg := config.ServerConfig{
		DataDir: t.TempDir(),
		Logger:  zap.NewNop(),
	}
	require.NoError(t, os.MkdirAll(cfg.SnapDir(), 0o700))
	snapPath := filepath.Join(cfg.SnapDir(), fmt.Sprintf("%016x.snap.db", index))
	require.NoError(t, os.WriteFile(snapPath, nil, 0o600))
	return cfg
}

func TestOpenSnapshotBackendSyncsSnapDir(t *testing.T) {
	cfg := setupSnapDirForBackend(t, 1)

	syncDirs := []string{}
	orig := syncSnapDir
	syncSnapDir = func(dir string) error {
		syncDirs = append(syncDirs, dir)
		return orig(dir)
	}
	t.Cleanup(func() { syncSnapDir = orig })

	ss := snap.New(nil, cfg.SnapDir())
	snapshot := &raftpb.Snapshot{Metadata: &raftpb.SnapshotMetadata{Index: new(uint64(1))}}
	be, err := OpenSnapshotBackend(cfg, ss, snapshot, NewBackendHooks(zap.NewNop(), cindex.NewConsistentIndex(nil)))
	require.NoError(t, err)
	require.NotNil(t, be)
	t.Cleanup(func() { be.Close() })

	assert.Equal(t, []string{cfg.SnapDir()}, syncDirs)
	assert.FileExists(t, cfg.BackendPath())
}

func TestOpenSnapshotBackendSyncError(t *testing.T) {
	cfg := setupSnapDirForBackend(t, 1)

	syncErr := errors.New("sync failed")
	orig := syncSnapDir
	syncSnapDir = func(dir string) error { return syncErr }
	t.Cleanup(func() { syncSnapDir = orig })

	ss := snap.New(nil, cfg.SnapDir())
	snapshot := &raftpb.Snapshot{Metadata: &raftpb.SnapshotMetadata{Index: new(uint64(1))}}
	be, err := OpenSnapshotBackend(cfg, ss, snapshot, NewBackendHooks(zap.NewNop(), cindex.NewConsistentIndex(nil)))
	require.ErrorIs(t, err, syncErr)
	assert.Nil(t, be)
}

func TestOpenSnapshotBackendMissingSnapshot(t *testing.T) {
	cfg := setupSnapDirForBackend(t, 1)

	syncCalls := 0
	orig := syncSnapDir
	syncSnapDir = func(dir string) error {
		syncCalls++
		return orig(dir)
	}
	t.Cleanup(func() { syncSnapDir = orig })

	ss := snap.New(nil, cfg.SnapDir())
	// Index 2 was never written, so there is nothing to rename or sync.
	snapshot := &raftpb.Snapshot{Metadata: &raftpb.SnapshotMetadata{Index: new(uint64(2))}}
	be, err := OpenSnapshotBackend(cfg, ss, snapshot, NewBackendHooks(zap.NewNop(), cindex.NewConsistentIndex(nil)))
	require.Error(t, err)
	assert.Nil(t, be)
	assert.Zero(t, syncCalls)
}
