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

package etcdserver

import (
	"io"
	"testing"

	"go.uber.org/zap/zaptest"
)

type mockSnapshot struct {
	writeToErr error
	closeErr   error
	size       int64
	written    bool
	closed     bool
}

func (m *mockSnapshot) WriteTo(w io.Writer) (int64, error) {
	m.written = true
	if m.writeToErr != nil {
		return 0, m.writeToErr
	}
	n, _ := w.Write([]byte("mock snapshot data"))
	return int64(n), nil
}

func (m *mockSnapshot) Close() error {
	m.closed = true
	return m.closeErr
}

func (m *mockSnapshot) Size() int64 {
	return m.size
}

func TestSnapshotReaderCloserErrorPropagation(t *testing.T) {
	lg := zaptest.NewLogger(t)

	snap := &mockSnapshot{
		size: 100,
	}
	rc := newSnapshotReaderCloser(lg, snap)

	data, err := io.ReadAll(rc)
	if err != nil {
		t.Errorf("unexpected error reading snapshot: %v", err)
	}
	if len(data) == 0 {
		t.Errorf("expected to read data from snapshot")
	}
	if !snap.written {
		t.Errorf("snapshot WriteTo was not called")
	}
	if !snap.closed {
		t.Errorf("snapshot Close was not called")
	}
}

func TestSnapshotReaderCloserWriteErrorPropagates(t *testing.T) {
	lg := zaptest.NewLogger(t)

	snap := &mockSnapshot{
		writeToErr: io.ErrUnexpectedEOF,
		size:       100,
	}

	rc := newSnapshotReaderCloser(lg, snap)
	defer rc.Close()

	_, err := io.ReadAll(rc)
	if err == nil {
		t.Errorf("expected error from snapshot write, got nil")
	}
	if err != io.ErrUnexpectedEOF {
		t.Errorf("expected io.ErrUnexpectedEOF, got %v", err)
	}
	if !snap.written {
		t.Errorf("snapshot WriteTo was not called")
	}
	if !snap.closed {
		t.Errorf("snapshot Close was not called")
	}
}
