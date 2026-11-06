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

package wal

import (
	"io"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"

	"go.etcd.io/etcd/client/pkg/v3/fileutil"
	"go.etcd.io/etcd/server/v3/storage/wal/walpb"
	"go.etcd.io/raft/v3/raftpb"
)

const benchEntryCount = 10000

// writeBenchWAL creates a WAL with benchEntryCount entries of entrySize bytes each.
func writeBenchWAL(tb testing.TB, dir string, entrySize int) {
	w, err := Create(zaptest.NewLogger(tb), dir, []byte("metadata"))
	require.NoError(tb, err)
	data := make([]byte, entrySize)
	for i := range data {
		data[i] = byte(i)
	}
	for i := 0; i < benchEntryCount; i++ {
		e := &raftpb.Entry{Term: new(uint64(1)), Index: new(uint64(i + 1)), Type: new(raftpb.EntryNormal), Data: data}
		require.NoError(tb, w.saveEntry(e))
	}
	require.NoError(tb, w.sync())
	require.NoError(tb, w.Close())
}

func benchWALFiles(tb testing.TB, dir string) []string {
	ents, err := os.ReadDir(dir)
	require.NoError(tb, err)
	var files []string
	for _, e := range ents {
		if filepath.Ext(e.Name()) == ".wal" {
			files = append(files, filepath.Join(dir, e.Name()))
		}
	}
	sort.Strings(files)
	require.NotEmpty(tb, files)
	return files
}

// benchmarkDecoderDecode measures the raw record decode loop (NewDecoder + Decode
// until EOF) over an existing WAL segment. This is the decode path exercised
// during WAL replay at startup.
func benchmarkDecoderDecode(b *testing.B, entrySize int) {
	p := b.TempDir()
	writeBenchWAL(b, p, entrySize)
	files := benchWALFiles(b, p)
	require.Len(b, files, 1)

	f, err := os.Open(files[0])
	require.NoError(b, err)
	defer f.Close()

	b.SetBytes(int64(entrySize * benchEntryCount))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			b.Fatal(err)
		}
		dec := NewDecoder(fileutil.NewFileReader(f))
		rec := &walpb.Record{}
		n := 0
		for {
			err := dec.Decode(rec)
			if err == io.EOF {
				break
			}
			if err != nil {
				b.Fatal(err)
			}
			n++
		}
		if n < benchEntryCount {
			b.Fatalf("decoded %d records, want at least %d", n, benchEntryCount)
		}
	}
}

// benchmarkWALReadAll measures the full replay path used at node startup:
// Open WAL + ReadAll + Close.
func benchmarkWALReadAll(b *testing.B, entrySize int) {
	p := b.TempDir()
	writeBenchWAL(b, p, entrySize)
	lg := zaptest.NewLogger(b)
	snap := &walpb.Snapshot{}

	b.SetBytes(int64(entrySize * benchEntryCount))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		w, err := Open(lg, p, snap)
		require.NoError(b, err)
		_, _, ents, err := w.ReadAll()
		if err != nil {
			b.Fatal(err)
		}
		if len(ents) != benchEntryCount {
			b.Fatalf("got %d entries, want %d", len(ents), benchEntryCount)
		}
		require.NoError(b, w.Close())
	}
}

func BenchmarkDecoderDecodeEntry64B(b *testing.B)  { benchmarkDecoderDecode(b, 64) }
func BenchmarkDecoderDecodeEntry512B(b *testing.B) { benchmarkDecoderDecode(b, 512) }
func BenchmarkDecoderDecodeEntry4KB(b *testing.B)  { benchmarkDecoderDecode(b, 4096) }
func BenchmarkWALReadAllEntry64B(b *testing.B)     { benchmarkWALReadAll(b, 64) }
func BenchmarkWALReadAllEntry512B(b *testing.B)    { benchmarkWALReadAll(b, 512) }
func BenchmarkWALReadAllEntry4KB(b *testing.B)     { benchmarkWALReadAll(b, 4096) }
