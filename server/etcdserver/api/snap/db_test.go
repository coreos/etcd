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

package snap

import (
	"errors"
	"io"
	"os"
	"strings"
	"testing"
)

func TestSaveDBFromSyncsDirectory(t *testing.T) {
	for _, tc := range []struct {
		name     string
		existing bool
	}{
		{name: "new"},
		{name: "existing", existing: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			s := New(nil, dir)
			if tc.existing {
				writeErr := os.WriteFile(s.dbFilePath(1), []byte("old"), 0o600)
				if writeErr != nil {
					t.Fatal(writeErr)
				}
			}

			syncErr := errors.New("sync failed")
			syncCalls := 0
			s.fsyncDir = func(gotDir string) error {
				if gotDir != dir {
					t.Errorf("fsync directory = %q, want %q", gotDir, dir)
				}
				syncCalls++
				return syncErr
			}

			const content = "snapshot"
			n, err := s.SaveDBFrom(strings.NewReader(content), 1)
			if !errors.Is(err, syncErr) {
				t.Errorf("SaveDBFrom error = %v, want %v", err, syncErr)
			}
			if n != int64(len(content)) {
				t.Errorf("SaveDBFrom bytes = %d, want %d", n, len(content))
			}
			if syncCalls != 1 {
				t.Errorf("fsync calls = %d, want 1", syncCalls)
			}
		})
	}
}

func TestSaveDBFromRejectsCloseFailureBeforePublishing(t *testing.T) {
	for _, existing := range []bool{false, true} {
		name := "new"
		if existing {
			name = "existing"
		}
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			s := New(nil, dir)
			if existing {
				if err := os.WriteFile(s.dbFilePath(7), []byte("previous-snapshot"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			closeErr := errors.New("simulated delayed close I/O failure")
			closeCalls := 0
			s.closeFile = func(f *os.File) error {
				closeCalls++
				if err := f.Close(); err != nil {
					return err
				}
				return closeErr
			}
			dirSyncCalls := 0
			s.fsyncDir = func(string) error {
				dirSyncCalls++
				return nil
			}

			const payload = "new snapshot bytes"
			n, err := s.SaveDBFrom(strings.NewReader(payload), 7)
			if !errors.Is(err, closeErr) {
				t.Fatalf("SaveDBFrom() error = %v, want delayed Close error", err)
			}
			if n != int64(len(payload)) {
				t.Fatalf("bytes copied = %d, want %d", n, len(payload))
			}
			if closeCalls != 1 || dirSyncCalls != 0 {
				t.Fatalf("close calls = %d, directory sync calls = %d, want 1 and 0", closeCalls, dirSyncCalls)
			}
			entries, err := os.ReadDir(dir)
			if err != nil {
				t.Fatal(err)
			}
			for _, e := range entries {
				if strings.HasPrefix(e.Name(), "tmp") {
					t.Fatalf("temporary failed snapshot still exists: %q", e.Name())
				}
			}

			actual, err := os.ReadFile(s.dbFilePath(7))
			if existing {
				if err != nil || string(actual) != "previous-snapshot" {
					t.Fatalf("existing snapshot changed: %q, %v", actual, err)
				}
			} else if !os.IsNotExist(err) {
				t.Fatalf("failed snapshot was published: %q, %v", actual, err)
			}
		})
	}
}

type closeFailureReader struct {
	read bool
}

func (r *closeFailureReader) Read(p []byte) (int, error) {
	if r.read {
		return 0, io.EOF
	}
	r.read = true
	n := copy(p, "partial")
	return n, io.ErrUnexpectedEOF
}

func TestSaveDBFromPreservesCopyErrorAlongsideCloseFailure(t *testing.T) {
	s := New(nil, t.TempDir())
	closeErr := errors.New("close error")
	s.closeFile = func(f *os.File) error {
		_ = f.Close()
		return closeErr
	}
	n, err := s.SaveDBFrom(&closeFailureReader{}, 1)
	if n != int64(len("partial")) {
		t.Fatalf("bytes = %d", n)
	}
	if !errors.Is(err, io.ErrUnexpectedEOF) || !errors.Is(err, closeErr) {
		t.Fatalf("combined error = %v, want both copy and close errors", err)
	}
	if _, err := os.Stat(s.dbFilePath(1)); !os.IsNotExist(err) {
		t.Fatalf("snapshot unexpectedly published: %v", err)
	}
}

func TestSaveDBFromCloseSuccessStillPublishes(t *testing.T) {
	dir := t.TempDir()
	s := New(nil, dir)
	var dirSyncCalls int
	s.fsyncDir = func(string) error {
		dirSyncCalls++
		return nil
	}
	const payload = "valid snapshot"
	if n, err := s.SaveDBFrom(strings.NewReader(payload), 17); err != nil || n != int64(len(payload)) {
		t.Fatalf("SaveDBFrom() bytes = %d, error = %v", n, err)
	}
	got, err := os.ReadFile(s.dbFilePath(17))
	if err != nil || string(got) != payload {
		t.Fatalf("published snapshot = %q, error %v", got, err)
	}
	if dirSyncCalls != 1 {
		t.Fatalf("directory syncs = %d, want 1", dirSyncCalls)
	}
}

func TestSaveDBFromPreservesCopyErrorWhenCloseSucceeds(t *testing.T) {
	s := New(nil, t.TempDir())
	n, err := s.SaveDBFrom(&closeFailureReader{}, 2)
	if n != int64(len("partial")) {
		t.Fatalf("copied bytes = %d", n)
	}
	// Keep the original error value for consumers that compare it directly.
	if err != io.ErrUnexpectedEOF {
		t.Fatalf("copy error = %v, want exact io.ErrUnexpectedEOF", err)
	}
	if _, err := os.Stat(s.dbFilePath(2)); !os.IsNotExist(err) {
		t.Fatalf("failed snapshot unexpectedly published: %v", err)
	}
}
