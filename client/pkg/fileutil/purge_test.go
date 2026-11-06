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

package fileutil

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"
)

func TestPurgeFile(t *testing.T) {
	dir := t.TempDir()

	// minimal file set
	for i := 0; i < 3; i++ {
		f, ferr := os.Create(filepath.Join(dir, fmt.Sprintf("%d.test", i)))
		require.NoError(t, ferr)
		f.Close()
	}

	stop, purgec := make(chan struct{}), make(chan string, 10)

	// keep 3 most recent files
	errch := purgeFile(zaptest.NewLogger(t), dir, "test", 3, time.Millisecond, stop, purgec, nil, false)
	select {
	case f := <-purgec:
		t.Errorf("unexpected purge on %q", f)
	case <-time.After(10 * time.Millisecond):
	}

	// rest of the files
	for i := 4; i < 10; i++ {
		go func(n int) {
			f, ferr := os.Create(filepath.Join(dir, fmt.Sprintf("%d.test", n)))
			if ferr != nil {
				t.Error(ferr)
			}
			f.Close()
		}(i)
	}

	// watch files purge away
	for i := 4; i < 10; i++ {
		select {
		case <-purgec:
		case <-time.After(time.Second):
			t.Errorf("purge took too long")
		}
	}

	fnames, rerr := ReadDir(dir)
	require.NoError(t, rerr)
	wnames := []string{"7.test", "8.test", "9.test"}
	if !reflect.DeepEqual(fnames, wnames) {
		t.Errorf("filenames = %v, want %v", fnames, wnames)
	}

	// no error should be reported from purge routine
	select {
	case f := <-purgec:
		t.Errorf("unexpected purge on %q", f)
	case err := <-errch:
		t.Errorf("unexpected purge error %v", err)
	case <-time.After(10 * time.Millisecond):
	}
	close(stop)
}

func TestPurgeFileHoldingLockFile(t *testing.T) {
	dir := t.TempDir()

	for i := 0; i < 10; i++ {
		var f *os.File
		f, err := os.Create(filepath.Join(dir, fmt.Sprintf("%d.test", i)))
		require.NoError(t, err)
		f.Close()
	}

	// create a purge barrier at 5
	p := filepath.Join(dir, fmt.Sprintf("%d.test", 5))
	l, err := LockFile(p, os.O_WRONLY, PrivateFileMode)
	require.NoError(t, err)

	stop, purgec := make(chan struct{}), make(chan string, 10)
	errch := purgeFile(zaptest.NewLogger(t), dir, "test", 3, time.Millisecond, stop, purgec, nil, true)

	for i := 0; i < 5; i++ {
		select {
		case <-purgec:
		case <-time.After(time.Second):
			t.Fatalf("purge took too long")
		}
	}

	fnames, rerr := ReadDir(dir)
	require.NoError(t, rerr)

	wnames := []string{"5.test", "6.test", "7.test", "8.test", "9.test"}
	if !reflect.DeepEqual(fnames, wnames) {
		t.Errorf("filenames = %v, want %v", fnames, wnames)
	}

	select {
	case s := <-purgec:
		t.Errorf("unexpected purge %q", s)
	case err = <-errch:
		t.Errorf("unexpected purge error %v", err)
	case <-time.After(10 * time.Millisecond):
	}

	// remove the purge barrier
	require.NoError(t, l.Close())

	// wait for rest of purges (5, 6)
	for i := 0; i < 2; i++ {
		select {
		case <-purgec:
		case <-time.After(time.Second):
			t.Fatalf("purge took too long")
		}
	}

	fnames, rerr = ReadDir(dir)
	require.NoError(t, rerr)
	wnames = []string{"7.test", "8.test", "9.test"}
	if !reflect.DeepEqual(fnames, wnames) {
		t.Errorf("filenames = %v, want %v", fnames, wnames)
	}

	select {
	case f := <-purgec:
		t.Errorf("unexpected purge on %q", f)
	case err := <-errch:
		t.Errorf("unexpected purge error %v", err)
	case <-time.After(10 * time.Millisecond):
	}

	close(stop)
}

// TestPurgeFileCloseOnRemoveError verifies that when flock is true and os.Remove
// fails, the LockedFile is properly closed. This tests the fix for the FD leak
// where the LockedFile l acquired by TryLockFile was never closed when Remove failed.
func TestPurgeFileCloseOnRemoveError(t *testing.T) {
	dir := t.TempDir()

	// Create files that will be candidates for purging
	// We need more files than max to trigger purging
	// Root bypasses directory write permission checks (CAP_DAC_OVERRIDE),
	// so chmod'ing the parent directory cannot make os.Remove fail here.
	// Skip instead of reporting a false failure.
	if os.Geteuid() == 0 {
		t.Skip("skipping test: running as root, cannot make os.Remove fail via directory permissions")
	}

	maxFiles := uint(3)
	totalFiles := 10
	for i := 0; i < totalFiles; i++ {
		f, err := os.Create(filepath.Join(dir, fmt.Sprintf("%d.test", i)))
		if err != nil {
			t.Fatal(err)
		}
		f.Close()
	}

	// Make directory read-only so os.Remove fails for ALL files
	// This ensures the first file that purge tries to lock will fail on Remove
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Skipf("skipping test: failed to make directory read-only: %v", err)
	}
	defer os.Chmod(dir, 0o755)

	stop := make(chan struct{})
	errch := purgeFile(zaptest.NewLogger(t), dir, "test", maxFiles, time.Millisecond, stop, nil, nil, true)

	// Wait for the error to be reported (os.Remove should fail on any file)
	select {
	case err := <-errch:
		if err == nil {
			t.Errorf("expected error from purge, got nil")
		}
	case <-time.After(time.Second):
		t.Errorf("timeout waiting for purge error")
	}

	close(stop)

	// Give the goroutine time to exit
	time.Sleep(10 * time.Millisecond)

	// Restore permissions so we can try locking files
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Try to lock the first file that purge tried to acquire (0.test based on alphabetical order)
	// If the lock was leaked, TryLockFile would fail with "resource temporarily unavailable"
	firstFile := filepath.Join(dir, "0.test")
	lock, lockErr := TryLockFile(firstFile, os.O_WRONLY, PrivateFileMode)
	if lockErr != nil {
		t.Errorf("LockedFile was leaked after Remove failure: failed to lock file: %v", lockErr)
	} else {
		lock.Close()
	}

	// Also try a middle file (5.test) to ensure they were all cleaned up
	middleFile := filepath.Join(dir, "5.test")
	lock, lockErr = TryLockFile(middleFile, os.O_WRONLY, PrivateFileMode)
	if lockErr != nil {
		t.Errorf("LockedFile was leaked after Remove failure: failed to lock file: %v", lockErr)
	} else {
		lock.Close()
	}
}
