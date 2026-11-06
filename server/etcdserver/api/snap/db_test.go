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

func Test_t4_save_db_from_fsyncs_dir_exactly_once(t *testing.T) {
	dir := t.TempDir()
	s := New(nil, dir)

	var gotDirs []string
	s.fsyncDir = func(gotDir string) error {
		gotDirs = append(gotDirs, gotDir)
		return nil
	}

	const content = "snapshot"
	n, err := s.SaveDBFrom(strings.NewReader(content), 1)
	if err != nil {
		t.Fatalf("SaveDBFrom error = %v, want nil", err)
	}
	if n != int64(len(content)) {
		t.Errorf("SaveDBFrom bytes = %d, want %d", n, int64(len(content)))
	}
	if len(gotDirs) != 1 {
		t.Fatalf("fsync calls = %d, want 1", len(gotDirs))
	}
	if gotDirs[0] != dir {
		t.Errorf("fsync directory = %q, want %q", gotDirs[0], dir)
	}
}
