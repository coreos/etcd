// Copyright 2015 The etcd Authors
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

package snap

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.uber.org/zap/zaptest"

	"go.etcd.io/etcd/client/pkg/v3/fileutil"
	"go.etcd.io/raft/v3/raftpb"
)

func TestReleaseSnapDBs(t *testing.T) {
	dir := filepath.Join(os.TempDir(), "snapshot")
	err := os.Mkdir(dir, 0o700)
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	snapIndices := []uint64{100, 200, 300, 400}
	for _, index := range snapIndices {
		filename := filepath.Join(dir, fmt.Sprintf("%016x.snap.db", index))
		if err := os.WriteFile(filename, []byte("snap file\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	ss := New(zaptest.NewLogger(t), dir)

	if err := ss.ReleaseSnapDBs(&raftpb.Snapshot{Metadata: &raftpb.SnapshotMetadata{Index: new(uint64(300))}}); err != nil {
		t.Fatal(err)
	}

	deleted := []uint64{100, 200}
	for _, index := range deleted {
		filename := filepath.Join(dir, fmt.Sprintf("%016x.snap.db", index))
		if fileutil.Exist(filename) {
			t.Errorf("expected %s (index: %d)  to be deleted, but it still exists", filename, index)
		}
	}

	retained := []uint64{300, 400}
	for _, index := range retained {
		filename := filepath.Join(dir, fmt.Sprintf("%016x.snap.db", index))
		if !fileutil.Exist(filename) {
			t.Errorf("expected %s (index: %d) to be retained, but it no longer exists", filename, index)
		}
	}
}

// TestReleaseSnapDBsRetainsSnapshotPendingApply reproduces the scenario from
// https://github.com/etcd-io/etcd/issues/18055. A follower saves the database
// file for snapshot A on receipt (rafthttp -> SaveDBFrom) and the snapshot
// waits in the apply queue. Before A is applied, a newer snapshot B arrives
// and the raft loop releases all older database files (ReleaseSnapDBs),
// deleting A's file while its apply is still pending. When the apply loop
// finally opens A's file (OpenSnapshotBackend -> DBFilePath), the file is
// gone and etcdserver panics with "failed to open snapshot backend".
func TestReleaseSnapDBsRetainsSnapshotPendingApply(t *testing.T) {
	dir := t.TempDir()
	ss := New(zaptest.NewLogger(t), dir)

	const (
		indexA uint64 = 100
		indexB uint64 = 200
	)

	// Snapshot A is received and its database file saved.
	if _, err := ss.SaveDBFrom(strings.NewReader("A"), indexA); err != nil {
		t.Fatal(err)
	}

	// Snapshot B arrives before A has been applied; the raft loop releases
	// older snapshot database files.
	if err := ss.ReleaseSnapDBs(&raftpb.Snapshot{Metadata: &raftpb.SnapshotMetadata{Index: new(uint64(indexB))}}); err != nil {
		t.Fatal(err)
	}

	// The apply loop now looks up snapshot A's database file to apply it.
	if _, err := ss.DBFilePath(indexA); err != nil {
		t.Errorf("DBFilePath(%d) = %v, want success: a saved snapshot db must not be released before it has been applied", indexA, err)
	}

	// Once the apply path has consumed the file, the protection is dropped
	// and any leftover file becomes eligible for cleanup again.
	ss.ReleaseDBSnapshot(indexA)
	if err := ss.ReleaseSnapDBs(&raftpb.Snapshot{Metadata: &raftpb.SnapshotMetadata{Index: new(uint64(indexB))}}); err != nil {
		t.Fatal(err)
	}
	if fileutil.Exist(filepath.Join(dir, fmt.Sprintf("%016x.snap.db", indexA))) {
		t.Errorf("expected snapshot db for index %d to be deleted after its apply completed", indexA)
	}
}
