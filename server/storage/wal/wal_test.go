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

package wal

import (
	"bytes"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest"
	"google.golang.org/protobuf/testing/protocmp"

	"go.etcd.io/etcd/client/pkg/v3/fileutil"
	"go.etcd.io/etcd/pkg/v3/pbutil"
	"go.etcd.io/etcd/server/v3/storage/wal/walpb"
	"go.etcd.io/raft/v3/raftpb"
)

var confState = raftpb.ConfState{
	Voters:    []uint64{0x00ffca74},
	AutoLeave: new(bool(false)),
}

func TestNew(t *testing.T) {
	p := t.TempDir()

	w, err := Create(zaptest.NewLogger(t), p, []byte("somedata"))
	require.NoErrorf(t, err, "err = %v, want nil", err)
	if g := filepath.Base(w.tail().Name()); g != walName(0, 0) {
		t.Errorf("name = %+v, want %+v", g, walName(0, 0))
	}
	defer w.Close()

	// file is preallocated to segment size; only read data written by wal
	off, err := w.tail().Seek(0, io.SeekCurrent)
	if err != nil {
		t.Fatal(err)
	}
	gd := make([]byte, off)
	f, err := os.Open(filepath.Join(p, filepath.Base(w.tail().Name())))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err = io.ReadFull(f, gd); err != nil {
		t.Fatalf("err = %v, want nil", err)
	}

	if os.Getenv("ETCD_UPDATE_FIXTURE_DATA") == "true" {
		os.WriteFile("testdata/TestNew.wal", gd, os.FileMode(0644))
	}
	fixtureData, err := os.ReadFile("testdata/TestNew.wal")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gd, fixtureData) {
		t.Log("data did not match fixture, rerun with ETCD_UPDATE_FIXTURE_DATA=true to regenerate fixture")
		t.Errorf("data = %v, want %v", gd, fixtureData)
	}

	var wb bytes.Buffer
	e := newEncoder(&wb, 0, 0)
	err = e.encode(&walpb.Record{Type: new(CrcType), Crc: new(uint32(0))})
	require.NoErrorf(t, err, "err = %v, want nil", err)
	err = e.encode(&walpb.Record{Type: new(MetadataType), Data: []byte("somedata")})
	require.NoErrorf(t, err, "err = %v, want nil", err)
	r := &walpb.Record{
		Type: new(SnapshotType),
		Data: pbutil.MustMarshalMessage(&walpb.Snapshot{Index: new(uint64(0)), Term: new(uint64(0))}),
	}
	err = e.encode(r)
	require.NoErrorf(t, err, "err = %v, want nil", err)
	e.flush()
	if !bytes.Equal(gd, wb.Bytes()) {
		t.Errorf("data = %v, want %v", gd, wb.Bytes())
	}
}

func TestCreateNewWALFile(t *testing.T) {
	tests := []struct {
		name     string
		fileType any
		forceNew bool
	}{
		{
			name:     "creating standard file should succeed and not truncate file",
			fileType: &os.File{},
			forceNew: false,
		},
		{
			name:     "creating locked file should succeed and not truncate file",
			fileType: &fileutil.LockedFile{},
			forceNew: false,
		},
		{
			name:     "creating standard file with forceNew should truncate file",
			fileType: &os.File{},
			forceNew: true,
		},
		{
			name:     "creating locked file with forceNew should truncate file",
			fileType: &fileutil.LockedFile{},
			forceNew: true,
		},
	}

	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), walName(0, uint64(i)))

			// create initial file with some data to verify truncate behavior
			err := os.WriteFile(p, []byte("test data"), fileutil.PrivateFileMode)
			require.NoError(t, err)

			var f any
			switch tt.fileType.(type) {
			case *os.File:
				f, err = createNewWALFile[*os.File](p, tt.forceNew)
				require.IsType(t, &os.File{}, f)
			case *fileutil.LockedFile:
				f, err = createNewWALFile[*fileutil.LockedFile](p, tt.forceNew)
				require.IsType(t, &fileutil.LockedFile{}, f)
			default:
				panic("unknown file type")
			}

			require.NoError(t, err)

			// validate the file permissions
			fi, err := os.Stat(p)
			require.NoError(t, err)
			expectedPerms := fmt.Sprintf("%o", os.FileMode(fileutil.PrivateFileMode))
			actualPerms := fmt.Sprintf("%o", fi.Mode().Perm())
			require.Equalf(t, expectedPerms, actualPerms, "unexpected file permissions on %q", p)

			content, err := os.ReadFile(p)
			require.NoError(t, err)

			if tt.forceNew {
				require.Emptyf(t, string(content), "file content should be truncated but it wasn't")
			} else {
				require.Equalf(t, "test data", string(content), "file content should not be truncated but it was")
			}
		})
	}
}

func TestCreateFailFromPollutedDir(t *testing.T) {
	p := t.TempDir()
	os.WriteFile(filepath.Join(p, "test.wal"), []byte("data"), os.ModeTemporary)

	_, err := Create(zaptest.NewLogger(t), p, []byte("data"))
	require.ErrorIsf(t, err, os.ErrExist, "expected %v, got %v", os.ErrExist, err)
}

func TestWalCleanup(t *testing.T) {
	testRoot := t.TempDir()
	p, err := os.MkdirTemp(testRoot, "waltest")
	if err != nil {
		t.Fatal(err)
	}

	logger := zaptest.NewLogger(t)
	w, err := Create(logger, p, []byte(""))
	require.NoErrorf(t, err, "err = %v, want nil", err)
	w.cleanupWAL(logger)
	fnames, err := fileutil.ReadDir(testRoot)
	require.NoErrorf(t, err, "err = %v, want nil", err)
	require.Lenf(t, fnames, 1, "expected 1 file under %v, got %v", testRoot, len(fnames))
	pattern := fmt.Sprintf(`%s.broken\.[\d]{8}\.[\d]{6}\.[\d]{1,6}?`, filepath.Base(p))
	match, _ := regexp.MatchString(pattern, fnames[0])
	if !match {
		t.Errorf("match = false, expected true for %v with pattern %v", fnames[0], pattern)
	}
}

func TestCreateFailFromNoSpaceLeft(t *testing.T) {
	p := t.TempDir()

	oldSegmentSizeBytes := SegmentSizeBytes
	defer func() {
		SegmentSizeBytes = oldSegmentSizeBytes
	}()
	SegmentSizeBytes = math.MaxInt64

	_, err := Create(zaptest.NewLogger(t), p, []byte("data"))
	require.Errorf(t, err, "expected error 'no space left on device', got nil") // no space left on device
}

func TestNewForInitedDir(t *testing.T) {
	p := t.TempDir()

	os.Create(filepath.Join(p, walName(0, 0)))
	if _, err := Create(zaptest.NewLogger(t), p, nil); err == nil || !errors.Is(err, os.ErrExist) {
		t.Errorf("err = %v, want %v", err, os.ErrExist)
	}
}

func TestOpenAtIndex(t *testing.T) {
	dir := t.TempDir()

	f, err := os.Create(filepath.Join(dir, walName(0, 0)))
	if err != nil {
		t.Fatal(err)
	}
	f.Close()

	w, err := Open(zaptest.NewLogger(t), dir, &walpb.Snapshot{})
	require.NoErrorf(t, err, "err = %v, want nil", err)
	if g := filepath.Base(w.tail().Name()); g != walName(0, 0) {
		t.Errorf("name = %+v, want %+v", g, walName(0, 0))
	}
	if w.seq() != 0 {
		t.Errorf("seq = %d, want %d", w.seq(), 0)
	}
	w.Close()

	wname := walName(2, 10)
	f, err = os.Create(filepath.Join(dir, wname))
	if err != nil {
		t.Fatal(err)
	}
	f.Close()

	w, err = Open(zaptest.NewLogger(t), dir, &walpb.Snapshot{Index: new(uint64(5))})
	require.NoErrorf(t, err, "err = %v, want nil", err)
	if g := filepath.Base(w.tail().Name()); g != wname {
		t.Errorf("name = %+v, want %+v", g, wname)
	}
	if w.seq() != 2 {
		t.Errorf("seq = %d, want %d", w.seq(), 2)
	}
	w.Close()

	emptydir := t.TempDir()
	if _, err = Open(zaptest.NewLogger(t), emptydir, &walpb.Snapshot{}); !errors.Is(err, ErrFileNotFound) {
		t.Errorf("err = %v, want %v", err, ErrFileNotFound)
	}
}

// TestVerify tests that Verify throws a non-nil error when the WAL is corrupted.
// The test creates a WAL directory and cuts out multiple WAL files. Then
// it corrupts one of the files by completely truncating it.
func TestVerify(t *testing.T) {
	lg := zaptest.NewLogger(t)
	walDir := t.TempDir()

	// create WAL
	w, err := Create(lg, walDir, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	// make 5 separate files
	for i := 0; i < 5; i++ {
		es := []*raftpb.Entry{{Index: new(uint64(i)), Data: []byte(fmt.Sprintf("waldata%d", i+1))}}
		if err = w.Save(&raftpb.HardState{}, es); err != nil {
			t.Fatal(err)
		}
		if err = w.cut(); err != nil {
			t.Fatal(err)
		}
	}

	hs := raftpb.HardState{Term: new(uint64(1)), Vote: new(uint64(3)), Commit: new(uint64(5))}
	require.NoError(t, w.Save(&hs, nil))

	// to verify the WAL is not corrupted at this point
	hardstate, err := Verify(lg, walDir, &walpb.Snapshot{})
	if err != nil {
		t.Errorf("expected a nil error, got %v", err)
	}
	if diff := cmp.Diff(&hs, hardstate, protocmp.Transform()); diff != "" {
		t.Errorf("unexpected hardstate (-want +got):\n%s", diff)
	}

	walFiles, err := os.ReadDir(walDir)
	if err != nil {
		t.Fatal(err)
	}

	// corrupt the WAL by truncating one of the WAL files completely
	err = os.Truncate(path.Join(walDir, walFiles[2].Name()), 0)
	if err != nil {
		t.Fatal(err)
	}

	_, err = Verify(lg, walDir, &walpb.Snapshot{})
	if err == nil {
		t.Error("expected a non-nil error, got nil")
	}
}

// TestCut tests cut
// TODO: split it into smaller tests for better readability
func TestCut(t *testing.T) {
	p := t.TempDir()

	w, err := Create(zaptest.NewLogger(t), p, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	state := &raftpb.HardState{Term: new(uint64(1))}
	if err = w.Save(state, nil); err != nil {
		t.Fatal(err)
	}
	if err = w.cut(); err != nil {
		t.Fatal(err)
	}
	wname := walName(1, 1)
	if g := filepath.Base(w.tail().Name()); g != wname {
		t.Errorf("name = %s, want %s", g, wname)
	}

	es := []*raftpb.Entry{{Index: new(uint64(1)), Term: new(uint64(1)), Data: []byte{1}}}
	if err = w.Save(&raftpb.HardState{}, es); err != nil {
		t.Fatal(err)
	}
	if err = w.cut(); err != nil {
		t.Fatal(err)
	}
	snap := walpb.Snapshot{Index: new(uint64(2)), Term: new(uint64(1)), ConfState: &confState}
	if err = w.SaveSnapshot(&snap); err != nil {
		t.Fatal(err)
	}
	wname = walName(2, 2)
	if g := filepath.Base(w.tail().Name()); g != wname {
		t.Errorf("name = %s, want %s", g, wname)
	}

	// check the state in the last WAL
	// We do check before closing the WAL to ensure that Cut syncs the data
	// into the disk.
	f, err := os.Open(filepath.Join(p, wname))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	nw := &WAL{
		decoder: NewDecoder(fileutil.NewFileReader(f)),
		start:   &snap,
	}
	_, gst, _, err := nw.ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(state, gst, protocmp.Transform()); diff != "" {
		t.Errorf("unexpected state (-want +got):\n%s", diff)
	}
}

func TestSaveWithCut(t *testing.T) {
	p := t.TempDir()

	w, err := Create(zaptest.NewLogger(t), p, []byte("metadata"))
	if err != nil {
		t.Fatal(err)
	}

	state := &raftpb.HardState{Term: new(uint64(1))}
	if err = w.Save(state, nil); err != nil {
		t.Fatal(err)
	}
	bigData := make([]byte, 500)
	strdata := "Hello World!!"
	copy(bigData, strdata)
	// set a lower value for SegmentSizeBytes, else the test takes too long to complete
	restoreLater := SegmentSizeBytes
	const EntrySize int = 500
	SegmentSizeBytes = 2 * 1024
	defer func() { SegmentSizeBytes = restoreLater }()
	index := uint64(0)
	for totalSize := 0; totalSize < int(SegmentSizeBytes); totalSize += EntrySize {
		ents := []*raftpb.Entry{{Index: new(index), Term: new(uint64(1)), Data: bigData}}
		if err = w.Save(state, ents); err != nil {
			t.Fatal(err)
		}
		index++
	}

	w.Close()

	neww, err := Open(zaptest.NewLogger(t), p, &walpb.Snapshot{})
	require.NoErrorf(t, err, "err = %v, want nil", err)
	defer neww.Close()
	wname := walName(1, index)
	if g := filepath.Base(neww.tail().Name()); g != wname {
		t.Errorf("name = %s, want %s", g, wname)
	}

	_, newhardstate, entries, err := neww.ReadAll()
	if err != nil {
		t.Fatal(err)
	}

	if diff := cmp.Diff(state, newhardstate, protocmp.Transform()); diff != "" {
		t.Errorf("unexpected Hard State (-want +got):\n%s", diff)
	}
	if len(entries) != int(SegmentSizeBytes/int64(EntrySize)) {
		t.Errorf("Number of entries = %d, expected = %d", len(entries), int(SegmentSizeBytes/int64(EntrySize)))
	}
	for _, oneent := range entries {
		if !bytes.Equal(oneent.Data, bigData) {
			t.Errorf("the saved data does not match at Index %d : found: %s , want :%s", oneent.GetIndex(), oneent.Data, bigData)
		}
	}
}

func TestRecover(t *testing.T) {
	cases := []struct {
		name string
		size int
	}{
		{
			name: "10MB",
			size: 10 * 1024 * 1024,
		},
		{
			name: "20MB",
			size: 20 * 1024 * 1024,
		},
		{
			name: "40MB",
			size: 40 * 1024 * 1024,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := t.TempDir()

			w, err := Create(zaptest.NewLogger(t), p, []byte("metadata"))
			if err != nil {
				t.Fatal(err)
			}
			if err = w.SaveSnapshot(&walpb.Snapshot{Index: new(uint64(0)), Term: new(uint64(0))}); err != nil {
				t.Fatal(err)
			}

			data := make([]byte, tc.size)
			n, err := rand.Read(data)
			assert.Equal(t, tc.size, n)
			if err != nil {
				t.Errorf("Unexpected error: %v", err)
			}

			ents := []*raftpb.Entry{
				{Index: new(uint64(1)), Term: new(uint64(1)), Data: data},
				{Index: new(uint64(2)), Term: new(uint64(2)), Data: data},
			}
			if err = w.Save(&raftpb.HardState{}, ents); err != nil {
				t.Fatal(err)
			}
			sts := []*raftpb.HardState{
				{Term: new(uint64(1)), Vote: new(uint64(1)), Commit: new(uint64(1))},
				{Term: new(uint64(2)), Vote: new(uint64(2)), Commit: new(uint64(2))},
			}
			for _, s := range sts {
				if err = w.Save(s, nil); err != nil {
					t.Fatal(err)
				}
			}
			w.Close()

			if w, err = Open(zaptest.NewLogger(t), p, &walpb.Snapshot{}); err != nil {
				t.Fatal(err)
			}
			metadata, state, entries, err := w.ReadAll()
			if err != nil {
				t.Fatal(err)
			}

			if !bytes.Equal(metadata, []byte("metadata")) {
				t.Errorf("metadata = %s, want %s", metadata, "metadata")
			}
			if diff := cmp.Diff(ents, entries, protocmp.Transform(), cmp.Comparer(bytes.Equal)); diff != "" {
				t.Errorf("unexpected entries (-want +got):\n%s", diff)
			}
			// only the latest state is recorded
			s := sts[len(sts)-1]
			if diff := cmp.Diff(s, state, protocmp.Transform()); diff != "" {
				t.Errorf("unexpected state (-want +got):\n%s", diff)
			}
			w.Close()
		})
	}
}

func TestSearchIndex(t *testing.T) {
	tests := []struct {
		names []string
		index uint64
		widx  int
		wok   bool
	}{
		{
			[]string{
				"0000000000000000-0000000000000000.wal",
				"0000000000000001-0000000000001000.wal",
				"0000000000000002-0000000000002000.wal",
			},
			0x1000, 1, true,
		},
		{
			[]string{
				"0000000000000001-0000000000004000.wal",
				"0000000000000002-0000000000003000.wal",
				"0000000000000003-0000000000005000.wal",
			},
			0x4000, 1, true,
		},
		{
			[]string{
				"0000000000000001-0000000000002000.wal",
				"0000000000000002-0000000000003000.wal",
				"0000000000000003-0000000000005000.wal",
			},
			0x1000, -1, false,
		},
	}
	for i, tt := range tests {
		idx, ok := searchIndex(zaptest.NewLogger(t), tt.names, tt.index)
		if idx != tt.widx {
			t.Errorf("#%d: idx = %d, want %d", i, idx, tt.widx)
		}
		if ok != tt.wok {
			t.Errorf("#%d: ok = %v, want %v", i, ok, tt.wok)
		}
	}
}

func TestScanWalName(t *testing.T) {
	tests := []struct {
		str          string
		wseq, windex uint64
		wok          bool
	}{
		{"0000000000000000-0000000000000000.wal", 0, 0, true},
		{"0000000000000000.wal", 0, 0, false},
		{"0000000000000000-0000000000000000.snap", 0, 0, false},
	}
	for i, tt := range tests {
		s, index, err := parseWALName(tt.str)
		if g := err == nil; g != tt.wok {
			t.Errorf("#%d: ok = %v, want %v", i, g, tt.wok)
		}
		if s != tt.wseq {
			t.Errorf("#%d: seq = %d, want %d", i, s, tt.wseq)
		}
		if index != tt.windex {
			t.Errorf("#%d: index = %d, want %d", i, index, tt.windex)
		}
	}
}

func TestRecoverAfterCut(t *testing.T) {
	p := t.TempDir()

	md, err := Create(zaptest.NewLogger(t), p, []byte("metadata"))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		if err = md.SaveSnapshot(&walpb.Snapshot{Index: new(uint64(i)), Term: new(uint64(1)), ConfState: &confState}); err != nil {
			t.Fatal(err)
		}
		es := []*raftpb.Entry{{Index: new(uint64(i))}}
		if err = md.Save(&raftpb.HardState{}, es); err != nil {
			t.Fatal(err)
		}
		if err = md.cut(); err != nil {
			t.Fatal(err)
		}
	}
	md.Close()

	if err := os.Remove(filepath.Join(p, walName(4, 4))); err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 10; i++ {
		w, err := Open(zaptest.NewLogger(t), p, &walpb.Snapshot{Index: new(uint64(i)), Term: new(uint64(1))})
		if err != nil {
			if i <= 4 {
				if !strings.Contains(err.Error(), "do not increase continuously") {
					t.Errorf("#%d: err = %v isn't expected, want: '* do not increase continuously'", i, err)
				}
			} else {
				t.Errorf("#%d: err = %v, want nil", i, err)
			}
			continue
		}
		metadata, _, entries, err := w.ReadAll()
		if err != nil {
			t.Errorf("#%d: err = %v, want nil", i, err)
			continue
		}
		if !bytes.Equal(metadata, []byte("metadata")) {
			t.Errorf("#%d: metadata = %s, want %s", i, metadata, "metadata")
		}
		for j, e := range entries {
			if e.GetIndex() != uint64(j+i+1) {
				t.Errorf("#%d: ents[%d].Index = %+v, want %+v", i, j, e.GetIndex(), j+i+1)
			}
		}
		w.Close()
	}
}

func TestOpenAtUncommittedIndex(t *testing.T) {
	p := t.TempDir()

	w, err := Create(zaptest.NewLogger(t), p, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = w.SaveSnapshot(&walpb.Snapshot{Index: new(uint64(0)), Term: new(uint64(0))}); err != nil {
		t.Fatal(err)
	}
	if err = w.Save(&raftpb.HardState{}, []*raftpb.Entry{{Index: new(uint64(0))}}); err != nil {
		t.Fatal(err)
	}
	w.Close()

	w, err = Open(zaptest.NewLogger(t), p, &walpb.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	// commit up to index 0, try to read index 1
	if _, _, _, err = w.ReadAll(); err != nil {
		t.Errorf("err = %v, want nil", err)
	}
	w.Close()
}

// TestOpenForRead tests that OpenForRead can load all files.
// The tests creates WAL directory, and cut out multiple WAL files. Then
// it releases the lock of part of data, and excepts that OpenForRead
// can read out all files even if some are locked for write.
func TestOpenForRead(t *testing.T) {
	p := t.TempDir()
	// create WAL
	w, err := Create(zaptest.NewLogger(t), p, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	// make 10 separate files
	for i := 0; i < 10; i++ {
		es := []*raftpb.Entry{{Index: new(uint64(i))}}
		if err = w.Save(&raftpb.HardState{}, es); err != nil {
			t.Fatal(err)
		}
		if err = w.cut(); err != nil {
			t.Fatal(err)
		}
	}
	// release the lock to 5
	unlockIndex := uint64(5)
	w.ReleaseLockTo(unlockIndex)

	// All are available for read
	w2, err := OpenForRead(zaptest.NewLogger(t), p, &walpb.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	defer w2.Close()
	_, _, ents, err := w2.ReadAll()
	require.NoErrorf(t, err, "err = %v, want nil", err)
	if g := ents[len(ents)-1].GetIndex(); g != 9 {
		t.Errorf("last index read = %d, want %d", g, 9)
	}
}

func TestOpenWithMaxIndex(t *testing.T) {
	p := t.TempDir()
	// create WAL
	w1, err := Create(zaptest.NewLogger(t), p, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if w1 != nil {
			w1.Close()
		}
	}()

	es := []*raftpb.Entry{{Index: new(uint64(math.MaxInt64))}}
	if err = w1.Save(&raftpb.HardState{}, es); err != nil {
		t.Fatal(err)
	}
	w1.Close()
	w1 = nil

	w2, err := Open(zaptest.NewLogger(t), p, &walpb.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	defer w2.Close()

	_, _, _, err = w2.ReadAll()
	require.ErrorIsf(t, err, ErrSliceOutOfRange, "err = %v, want ErrSliceOutOfRange", err)
}

func TestSaveEmpty(t *testing.T) {
	var buf bytes.Buffer
	var est raftpb.HardState
	w := WAL{
		encoder: newEncoder(&buf, 0, 0),
	}
	if err := w.saveState(&est); err != nil {
		t.Errorf("err = %v, want nil", err)
	}
	if len(buf.Bytes()) != 0 {
		t.Errorf("buf.Bytes = %d, want 0", len(buf.Bytes()))
	}
}

func TestReleaseLockTo(t *testing.T) {
	p := t.TempDir()
	// create WAL
	w, err := Create(zaptest.NewLogger(t), p, nil)
	defer func() {
		if err = w.Close(); err != nil {
			t.Fatal(err)
		}
	}()
	if err != nil {
		t.Fatal(err)
	}

	// release nothing if no files
	err = w.ReleaseLockTo(10)
	if err != nil {
		t.Errorf("err = %v, want nil", err)
	}

	// make 10 separate files
	for i := 0; i < 10; i++ {
		es := []*raftpb.Entry{{Index: new(uint64(i))}}
		if err = w.Save(&raftpb.HardState{}, es); err != nil {
			t.Fatal(err)
		}
		if err = w.cut(); err != nil {
			t.Fatal(err)
		}
	}
	// release the lock to 5
	unlockIndex := uint64(5)
	w.ReleaseLockTo(unlockIndex)

	// expected remaining are 4,5,6,7,8,9,10
	if len(w.locks) != 7 {
		t.Errorf("len(w.locks) = %d, want %d", len(w.locks), 7)
	}
	for i, l := range w.locks {
		var lockIndex uint64
		_, lockIndex, err = parseWALName(filepath.Base(l.Name()))
		if err != nil {
			t.Fatal(err)
		}

		if lockIndex != uint64(i+4) {
			t.Errorf("#%d: lockindex = %d, want %d", i, lockIndex, uint64(i+4))
		}
	}

	// release the lock to 15
	unlockIndex = uint64(15)
	w.ReleaseLockTo(unlockIndex)

	// expected remaining is 10
	if len(w.locks) != 1 {
		t.Errorf("len(w.locks) = %d, want %d", len(w.locks), 1)
	}
	_, lockIndex, err := parseWALName(filepath.Base(w.locks[0].Name()))
	if err != nil {
		t.Fatal(err)
	}

	if lockIndex != uint64(10) {
		t.Errorf("lockindex = %d, want %d", lockIndex, 10)
	}
}

// TestTailWriteNoSlackSpace ensures that tail writes append if there's no preallocated space.
func TestTailWriteNoSlackSpace(t *testing.T) {
	p := t.TempDir()

	// create initial WAL
	w, err := Create(zaptest.NewLogger(t), p, []byte("metadata"))
	if err != nil {
		t.Fatal(err)
	}
	// write some entries
	for i := 1; i <= 5; i++ {
		es := []*raftpb.Entry{{Index: new(uint64(i)), Term: new(uint64(1)), Data: []byte{byte(i)}}}
		if err = w.Save(&raftpb.HardState{Term: new(uint64(1))}, es); err != nil {
			t.Fatal(err)
		}
	}
	// get rid of slack space by truncating file
	off, serr := w.tail().Seek(0, io.SeekCurrent)
	if serr != nil {
		t.Fatal(serr)
	}
	if terr := w.tail().Truncate(off); terr != nil {
		t.Fatal(terr)
	}
	w.Close()

	// open, write more
	w, err = Open(zaptest.NewLogger(t), p, &walpb.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	_, _, ents, rerr := w.ReadAll()
	if rerr != nil {
		t.Fatal(rerr)
	}
	require.Lenf(t, ents, 5, "got entries %+v, expected 5 entries", ents)
	// write more entries
	for i := 6; i <= 10; i++ {
		es := []*raftpb.Entry{{Index: new(uint64(i)), Term: new(uint64(1)), Data: []byte{byte(i)}}}
		if err = w.Save(&raftpb.HardState{Term: new(uint64(1))}, es); err != nil {
			t.Fatal(err)
		}
	}
	w.Close()

	// confirm all writes
	w, err = Open(zaptest.NewLogger(t), p, &walpb.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	_, _, ents, rerr = w.ReadAll()
	if rerr != nil {
		t.Fatal(rerr)
	}
	if len(ents) != 10 {
		t.Fatalf("got entries %+v, expected 10 entries", ents)
	}
	w.Close()
}

// TestRestartCreateWal ensures that an interrupted WAL initialization is clobbered on restart
func TestRestartCreateWal(t *testing.T) {
	p := t.TempDir()
	var err error

	// make temporary directory so it looks like initialization is interrupted
	tmpdir := filepath.Clean(p) + ".tmp"
	if err = os.Mkdir(tmpdir, fileutil.PrivateDirMode); err != nil {
		t.Fatal(err)
	}
	if _, err = os.OpenFile(filepath.Join(tmpdir, "test"), os.O_WRONLY|os.O_CREATE, fileutil.PrivateFileMode); err != nil {
		t.Fatal(err)
	}

	w, werr := Create(zaptest.NewLogger(t), p, []byte("abc"))
	if werr != nil {
		t.Fatal(werr)
	}
	w.Close()
	if Exist(tmpdir) {
		t.Fatalf("got %q exists, expected it to not exist", tmpdir)
	}

	if w, err = OpenForRead(zaptest.NewLogger(t), p, &walpb.Snapshot{}); err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	if meta, _, _, rerr := w.ReadAll(); rerr != nil || string(meta) != "abc" {
		t.Fatalf("got error %v and meta %q, expected nil and %q", rerr, meta, "abc")
	}
}

// TestOpenOnTornWrite ensures that entries past the torn write are truncated.
func TestOpenOnTornWrite(t *testing.T) {
	maxEntries := 40
	clobberIdx := 20
	overwriteEntries := 5

	p := t.TempDir()
	w, err := Create(zaptest.NewLogger(t), p, nil)
	defer func() {
		if err = w.Close(); err != nil && !errors.Is(err, os.ErrInvalid) {
			t.Fatal(err)
		}
	}()
	if err != nil {
		t.Fatal(err)
	}

	// get offset of end of each saved entry
	offsets := make([]int64, maxEntries)
	for i := range offsets {
		es := []*raftpb.Entry{{Index: new(uint64(i))}}
		if err = w.Save(&raftpb.HardState{}, es); err != nil {
			t.Fatal(err)
		}
		if offsets[i], err = w.tail().Seek(0, io.SeekCurrent); err != nil {
			t.Fatal(err)
		}
	}

	fn := filepath.Join(p, filepath.Base(w.tail().Name()))
	w.Close()

	// clobber some entry with 0's to simulate a torn write
	f, ferr := os.OpenFile(fn, os.O_WRONLY, fileutil.PrivateFileMode)
	if ferr != nil {
		t.Fatal(ferr)
	}
	defer f.Close()
	_, err = f.Seek(offsets[clobberIdx], io.SeekStart)
	if err != nil {
		t.Fatal(err)
	}
	zeros := make([]byte, offsets[clobberIdx+1]-offsets[clobberIdx])
	_, err = f.Write(zeros)
	if err != nil {
		t.Fatal(err)
	}
	f.Close()

	w, err = Open(zaptest.NewLogger(t), p, &walpb.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	// seek up to clobbered entry
	_, _, _, err = w.ReadAll()
	if err != nil {
		t.Fatal(err)
	}

	// write a few entries past the clobbered entry
	for i := 0; i < overwriteEntries; i++ {
		// Index is different from old, truncated entries
		es := []*raftpb.Entry{{Index: new(uint64(i + clobberIdx)), Data: []byte("new")}}
		if err = w.Save(&raftpb.HardState{}, es); err != nil {
			t.Fatal(err)
		}
	}
	w.Close()

	// read back the entries, confirm number of entries matches expectation
	w, err = OpenForRead(zaptest.NewLogger(t), p, &walpb.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}

	_, _, ents, rerr := w.ReadAll()
	if rerr != nil {
		// CRC error? the old entries were likely never truncated away
		t.Fatal(rerr)
	}
	wEntries := (clobberIdx - 1) + overwriteEntries
	require.Equalf(t, len(ents), wEntries, "expected len(ents) = %d, got %d", wEntries, len(ents))
}

func TestRenameFail(t *testing.T) {
	p := t.TempDir()

	oldSegmentSizeBytes := SegmentSizeBytes
	defer func() {
		SegmentSizeBytes = oldSegmentSizeBytes
	}()
	SegmentSizeBytes = math.MaxInt64

	tp := t.TempDir()
	os.RemoveAll(tp)

	w := &WAL{
		lg:  zaptest.NewLogger(t),
		dir: p,
	}
	w2, werr := w.renameWAL(tp)
	if w2 != nil || werr == nil { // os.Rename should fail from 'no such file or directory'
		t.Fatalf("expected error, got %v", werr)
	}
}

// TestReadAllFail ensure ReadAll error if used without opening the WAL
func TestReadAllFail(t *testing.T) {
	dir := t.TempDir()

	// create initial WAL
	f, err := Create(zaptest.NewLogger(t), dir, []byte("metadata"))
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	// try to read without opening the WAL
	_, _, _, err = f.ReadAll()
	if err == nil || !errors.Is(err, ErrDecoderNotFound) {
		t.Fatalf("err = %v, want ErrDecoderNotFound", err)
	}
}

// TestValidSnapshotEntries ensures ValidSnapshotEntries returns all valid wal snapshot entries, accounting
// for hardstate
func TestValidSnapshotEntries(t *testing.T) {
	p := t.TempDir()
	snap0 := walpb.Snapshot{Index: new(uint64(0)), Term: new(uint64(0))}
	snap1 := walpb.Snapshot{Index: new(uint64(1)), Term: new(uint64(1)), ConfState: &confState}
	state1 := raftpb.HardState{Commit: new(uint64(1)), Term: new(uint64(1))}
	snap2 := walpb.Snapshot{Index: new(uint64(2)), Term: new(uint64(1)), ConfState: &confState}
	snap3 := walpb.Snapshot{Index: new(uint64(3)), Term: new(uint64(2)), ConfState: &confState}
	state2 := raftpb.HardState{Commit: new(uint64(3)), Term: new(uint64(2))}
	snap4 := walpb.Snapshot{Index: new(uint64(4)), Term: new(uint64(2)), ConfState: &confState} // will be orphaned since the last committed entry will be snap3
	func() {
		w, err := Create(zaptest.NewLogger(t), p, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer w.Close()

		// snap0 is implicitly created at index 0, term 0
		if err = w.SaveSnapshot(&snap1); err != nil {
			t.Fatal(err)
		}
		if err = w.Save(&state1, nil); err != nil {
			t.Fatal(err)
		}
		if err = w.SaveSnapshot(&snap2); err != nil {
			t.Fatal(err)
		}
		if err = w.SaveSnapshot(&snap3); err != nil {
			t.Fatal(err)
		}
		if err = w.Save(&state2, nil); err != nil {
			t.Fatal(err)
		}
		if err = w.SaveSnapshot(&snap4); err != nil {
			t.Fatal(err)
		}
	}()
	walSnaps, err := ValidSnapshotEntries(zaptest.NewLogger(t), p)
	if err != nil {
		t.Fatal(err)
	}
	expected := []*walpb.Snapshot{&snap0, &snap1, &snap2, &snap3}
	if diff := cmp.Diff(expected, walSnaps, protocmp.Transform()); diff != "" {
		t.Errorf("walSnaps mismatch (-want +got):\n%s", diff)
	}
}

// TestValidSnapshotEntriesAfterPurgeWal ensure that there are many wal files, and after cleaning the first wal file,
// it can work well.
func TestValidSnapshotEntriesAfterPurgeWal(t *testing.T) {
	oldSegmentSizeBytes := SegmentSizeBytes
	SegmentSizeBytes = 64
	defer func() {
		SegmentSizeBytes = oldSegmentSizeBytes
	}()
	p := t.TempDir()
	snap0 := walpb.Snapshot{}
	snap1 := walpb.Snapshot{Index: new(uint64(1)), Term: new(uint64(1)), ConfState: &confState}
	state1 := raftpb.HardState{Commit: new(uint64(1)), Term: new(uint64(1))}
	snap2 := walpb.Snapshot{Index: new(uint64(2)), Term: new(uint64(1)), ConfState: &confState}
	snap3 := walpb.Snapshot{Index: new(uint64(3)), Term: new(uint64(2)), ConfState: &confState}
	state2 := raftpb.HardState{Commit: new(uint64(3)), Term: new(uint64(2))}
	func() {
		w, err := Create(zaptest.NewLogger(t), p, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer w.Close()

		// snap0 is implicitly created at index 0, term 0
		if err = w.SaveSnapshot(&snap1); err != nil {
			t.Fatal(err)
		}
		if err = w.Save(&state1, nil); err != nil {
			t.Fatal(err)
		}
		if err = w.SaveSnapshot(&snap2); err != nil {
			t.Fatal(err)
		}
		if err = w.SaveSnapshot(&snap3); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 128; i++ {
			if err = w.Save(&state2, nil); err != nil {
				t.Fatal(err)
			}
		}
	}()
	files, _, err := selectWALFiles(nil, p, &snap0)
	if err != nil {
		t.Fatal(err)
	}
	os.Remove(p + "/" + files[0])
	_, err = ValidSnapshotEntries(zaptest.NewLogger(t), p)
	if err != nil {
		t.Fatal(err)
	}
}

func TestValidSnapshotEntriesRetryOnTransientNotExist(t *testing.T) {
	p := t.TempDir()
	snap1 := walpb.Snapshot{Index: new(uint64(1)), Term: new(uint64(1)), ConfState: &confState}
	state1 := raftpb.HardState{Commit: new(uint64(1)), Term: new(uint64(1))}
	snap2 := walpb.Snapshot{Index: new(uint64(2)), Term: new(uint64(1)), ConfState: &confState}
	state2 := raftpb.HardState{Commit: new(uint64(2)), Term: new(uint64(1))}

	func() {
		w, err := Create(zaptest.NewLogger(t), p, nil)
		require.NoError(t, err)
		defer w.Close()

		err = w.SaveSnapshot(&snap1)
		require.NoError(t, err)
		err = w.Save(&state1, nil)
		require.NoError(t, err)
		err = w.SaveSnapshot(&snap2)
		require.NoError(t, err)
		err = w.Save(&state2, nil)
		require.NoError(t, err)
	}()

	expected, err := ValidSnapshotEntries(zaptest.NewLogger(t), p)
	require.NoError(t, err)

	originalOpen := openWALFilesFn
	t.Cleanup(func() {
		openWALFilesFn = originalOpen
	})

	injected := false
	openWALFilesFn = func(lg *zap.Logger, dirpath string, names []string, nameIndex int, write bool) ([]fileutil.FileReader, []*fileutil.LockedFile, func() error, error) {
		if !injected {
			injected = true
			return nil, nil, nil, &os.PathError{Op: "open", Path: filepath.Join(dirpath, names[nameIndex]), Err: os.ErrNotExist}
		}
		return originalOpen(lg, dirpath, names, nameIndex, write)
	}

	got, err := ValidSnapshotEntries(zaptest.NewLogger(t), p)
	require.NoError(t, err)

	if diff := cmp.Diff(expected, got, protocmp.Transform()); diff != "" {
		t.Errorf("walSnaps mismatch (-want +got):\n%s", diff)
	}
}

func TestLastRecordLengthExceedFileEnd(t *testing.T) {
	/* The data below was generated by code something like below. The length
	 * of the last record was intentionally changed to 1000 in order to make
	 * sure it exceeds the end of the file.
	 *
	 *  for i := 0; i < 3; i++ {
	 *		   es := []*raftpb.Entry{{Index: uint64(i + 1), Data: []byte(fmt.Sprintf("waldata%d", i+1))}}
	 *			if err = w.Save(&raftpb.HardState{}, es); err != nil {
	 *					t.Fatal(err)
	 *			}
	 *	}
	 *  ......
	 *	var sb strings.Builder
	 *	for _, ch := range buf {
	 *		sb.WriteString(fmt.Sprintf("\\x%02x", ch))
	 *	}
	 */
	// Generate WAL file
	t.Log("Generate a WAL file with the last record's length modified.")
	data := []byte("\x04\x00\x00\x00\x00\x00\x00\x84\x08\x04\x10\x00\x00" +
		"\x00\x00\x00\x04\x00\x00\x00\x00\x00\x00\x84\x08\x01\x10\x00\x00" +
		"\x00\x00\x00\x0e\x00\x00\x00\x00\x00\x00\x82\x08\x05\x10\xa0\xb3" +
		"\x9b\x8f\x08\x1a\x04\x08\x00\x10\x00\x00\x00\x1a\x00\x00\x00\x00" +
		"\x00\x00\x86\x08\x02\x10\xba\x8b\xdc\x85\x0f\x1a\x10\x08\x00\x10" +
		"\x00\x18\x01\x22\x08\x77\x61\x6c\x64\x61\x74\x61\x31\x00\x00\x00" +
		"\x00\x00\x00\x1a\x00\x00\x00\x00\x00\x00\x86\x08\x02\x10\xa1\xe8" +
		"\xff\x9c\x02\x1a\x10\x08\x00\x10\x00\x18\x02\x22\x08\x77\x61\x6c" +
		"\x64\x61\x74\x61\x32\x00\x00\x00\x00\x00\x00\xe8\x03\x00\x00\x00" +
		"\x00\x00\x86\x08\x02\x10\xa1\x9c\xa1\xaa\x04\x1a\x10\x08\x00\x10" +
		"\x00\x18\x03\x22\x08\x77\x61\x6c\x64\x61\x74\x61\x33\x00\x00\x00" +
		"\x00\x00\x00")

	buf := bytes.NewBuffer(data)
	f, err := createFileWithData(t, buf)
	fileName := f.Name()
	require.NoError(t, err)
	t.Logf("fileName: %v", fileName)

	// Verify low-level decoder directly
	t.Log("Verify all records can be parsed correctly.")
	rec := &walpb.Record{}
	decoder := NewDecoder(fileutil.NewFileReader(f))
	for {
		if err = decoder.Decode(rec); err != nil {
			require.ErrorIs(t, err, io.ErrUnexpectedEOF)
			break
		}
		if rec.GetType() == EntryType {
			e := MustUnmarshalEntry(rec.Data)
			t.Logf("Validating normal entry: %v", e)
			recData := fmt.Sprintf("waldata%d", e.GetIndex())
			require.Equal(t, raftpb.EntryNormal, e.GetType())
			require.Equal(t, recData, string(e.Data))
		}
		rec = &walpb.Record{}
	}
	require.NoError(t, f.Close())

	// Verify w.ReadAll() returns io.ErrUnexpectedEOF in the error chain.
	t.Log("Verify the w.ReadAll returns io.ErrUnexpectedEOF in the error chain")
	newFileName := filepath.Join(filepath.Dir(fileName), "0000000000000000-0000000000000000.wal")
	require.NoError(t, os.Rename(fileName, newFileName))

	w, err := Open(zaptest.NewLogger(t), filepath.Dir(fileName), &walpb.Snapshot{
		Index: new(uint64(0)),
		Term:  new(uint64(0)),
	})
	require.NoError(t, err)
	defer w.Close()

	_, _, _, err = w.ReadAll()
	// Note: The wal file will be repaired automatically in production
	// environment, but only once.
	require.ErrorIs(t, err, io.ErrUnexpectedEOF)
}

func walFsyncCount() uint64 {
	var m dto.Metric
	if err := walFsyncSec.Write(&m); err != nil {
		panic(err)
	}
	return m.GetHistogram().GetSampleCount()
}

// TestReadAllTailFsync verifies that ReadAll fsyncs the tail file in write mode
// and skips fsync when unsafeNoFsync is set or in read mode.
func TestReadAllTailFsync(t *testing.T) {
	p := t.TempDir()
	w, err := Create(zaptest.NewLogger(t), p, []byte("metadata"))
	require.NoError(t, err)

	es := []*raftpb.Entry{
		{Index: new(uint64(1)), Term: new(uint64(1)), Data: []byte("entry1")},
		{Index: new(uint64(2)), Term: new(uint64(1)), Data: []byte("entry2")},
	}
	require.NoError(t, w.Save(&raftpb.HardState{}, es))
	require.NoError(t, w.Close())

	t.Run("write mode fsyncs tail file", func(t *testing.T) {
		w, err := Open(zaptest.NewLogger(t), p, &walpb.Snapshot{})
		require.NoError(t, err)
		defer w.Close()

		before := walFsyncCount()
		_, _, ents, err := w.ReadAll()
		require.NoError(t, err)
		require.Len(t, ents, 2)
		after := walFsyncCount()
		require.Equal(t, before+1, after, "expected ReadAll in write mode to fsync tail file")
	})

	t.Run("write mode with unsafeNoFsync skips fsync", func(t *testing.T) {
		w, err := Open(zaptest.NewLogger(t), p, &walpb.Snapshot{})
		require.NoError(t, err)
		defer w.Close()

		w.SetUnsafeNoFsync()
		before := walFsyncCount()
		_, _, ents, err := w.ReadAll()
		require.NoError(t, err)
		require.Len(t, ents, 2)
		after := walFsyncCount()
		require.Equal(t, before, after, "expected ReadAll with unsafeNoFsync to skip tail fsync")
	})

	t.Run("read mode skips fsync", func(t *testing.T) {
		w, err := OpenForRead(zaptest.NewLogger(t), p, &walpb.Snapshot{})
		require.NoError(t, err)
		defer w.Close()

		before := walFsyncCount()
		_, _, ents, err := w.ReadAll()
		require.NoError(t, err)
		require.Len(t, ents, 2)
		after := walFsyncCount()
		require.Equal(t, before, after, "expected ReadAll in read mode to skip tail fsync")
	})
}

