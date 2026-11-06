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
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest"

	"go.etcd.io/etcd/client/pkg/v3/fileutil"
	"go.etcd.io/raft/v3/raftpb"
)

// The periodic snapshot purger and Snapshotter.ReleaseSnapDBs can select the
// same obsolete .snap.db file. If release removes it first, the purger's
// os.Remove returns ENOENT. That must be treated as already cleaned (as long
// as the directory is still readable) instead of stopping the worker with an
// error. See https://github.com/etcd-io/etcd/issues/22470.
func TestPurgeSnapDBsDuringRelease(t *testing.T) {
	dir := t.TempDir()
	writeSnapshot := func(index uint64) {
		t.Helper()
		name := filepath.Join(dir, fmt.Sprintf("%016x.snap.db", index))
		require.NoError(t, os.WriteFile(name, []byte("snap file\n"), 0o600))
	}
	for index := uint64(1); index <= 8; index++ {
		writeSnapshot(index)
	}

	paused, resume, progressed := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var resumeOnce sync.Once
	resumePurge := func() { resumeOnce.Do(func() { close(resume) }) }
	purged := 0
	lg := zaptest.NewLogger(t).WithOptions(zap.Hooks(func(entry zapcore.Entry) error {
		if entry.Message == "purged" {
			purged++
			if purged == 1 {
				close(paused)
				<-resume
			}
			if purged == 4 {
				close(progressed)
			}
		}
		return nil
	}))
	stop := make(chan struct{})
	done, errc := fileutil.PurgeFileWithoutFlock(lg, dir, "snap.db", 5, time.Millisecond, stop)
	t.Cleanup(func() {
		close(stop)
		resumePurge()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("purger did not stop")
		}
	})
	select {
	case <-paused:
	case err := <-errc:
		t.Fatalf("purger failed before snapshot release: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("purger did not select the old snapshots")
	}

	ss := New(zaptest.NewLogger(t), dir)
	require.NoError(t, ss.ReleaseSnapDBs(&raftpb.Snapshot{Metadata: &raftpb.SnapshotMetadata{Index: new(uint64(4))}}))
	writeSnapshot(9)
	resumePurge()
	select {
	case <-progressed:
	case err := <-errc:
		t.Fatalf("concurrent snapshot release stopped the purger: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("purger did not resume cleanup")
	}
	names, err := fileutil.ReadDir(dir)
	require.NoError(t, err)
	var want []string
	for index := uint64(5); index <= 9; index++ {
		want = append(want, fmt.Sprintf("%016x.snap.db", index))
	}
	require.Equal(t, want, names)
}
