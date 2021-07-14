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

package backend_test

import (
	"fmt"
	"io/ioutil"
	"reflect"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	bolt "go.etcd.io/bbolt"
	"go.etcd.io/etcd/server/v3/storage/backend"
	betesting "go.etcd.io/etcd/server/v3/storage/backend/testing"
	"go.etcd.io/etcd/server/v3/storage/schema"
)

func TestBackendClose(t *testing.T) {
	b, _ := betesting.NewTmpBackend(t, time.Hour, 10000)

	// check close could work
	done := make(chan struct{})
	go func() {
		err := b.Close()
		if err != nil {
			t.Errorf("close error = %v, want nil", err)
		}
		done <- struct{}{}
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Errorf("failed to close database in 10s")
	}
}

func TestBackendSnapshot(t *testing.T) {
	b, _ := betesting.NewTmpBackend(t, time.Hour, 10000)
	defer betesting.Close(t, b)

	tx := b.BatchTx()
	tx.Lock()
	tx.UnsafeCreateBucket(schema.Test)
	tx.UnsafePut(schema.Test, []byte("foo"), []byte("bar"))
	tx.Unlock()
	b.ForceCommit()

	// write snapshot to a new file
	f, err := ioutil.TempFile(t.TempDir(), "etcd_backend_test")
	if err != nil {
		t.Fatal(err)
	}
	snap := b.Snapshot()
	defer func() { assert.NoError(t, snap.Close()) }()
	if _, err := snap.WriteTo(f); err != nil {
		t.Fatal(err)
	}
	assert.NoError(t, f.Close())

	// bootstrap new backend from the snapshot
	bcfg := backend.DefaultBackendConfig()
	bcfg.Path, bcfg.BatchInterval, bcfg.BatchLimit = f.Name(), time.Hour, 10000
	nb := backend.New(bcfg)
	defer betesting.Close(t, nb)

	newTx := nb.BatchTx()
	newTx.Lock()
	ks, _ := newTx.UnsafeRange(schema.Test, []byte("foo"), []byte("goo"), 0)
	if len(ks) != 1 {
		t.Errorf("len(kvs) = %d, want 1", len(ks))
	}
	newTx.Unlock()
}

func TestBackendBatchIntervalCommit(t *testing.T) {
	// start backend with super short batch interval so
	// we do not need to wait long before commit to happen.
	b, _ := betesting.NewTmpBackend(t, time.Nanosecond, 10000)
	defer betesting.Close(t, b)

	pc := backend.CommitsForTest(b)

	tx := b.BatchTx()
	tx.Lock()
	tx.UnsafeCreateBucket(schema.Test)
	tx.UnsafePut(schema.Test, []byte("foo"), []byte("bar"))
	tx.Unlock()

	for i := 0; i < 10; i++ {
		if backend.CommitsForTest(b) >= pc+1 {
			break
		}
		time.Sleep(time.Duration(i*100) * time.Millisecond)
	}

	// check whether put happens via db view
	assert.NoError(t, backend.DbFromBackendForTest(b).View(func(tx *bolt.Tx) error {
		bucket := tx.Bucket([]byte("test"))
		if bucket == nil {
			t.Errorf("bucket test does not exit")
			return nil
		}
		v := bucket.Get([]byte("foo"))
		if v == nil {
			t.Errorf("foo key failed to written in backend")
		}
		return nil
	}))
}

func TestBackendDefrag(t *testing.T) {
	b, _ := betesting.NewDefaultTmpBackend(t)
	defer betesting.Close(t, b)

	tx := b.BatchTx()
	tx.Lock()
	tx.UnsafeCreateBucket(schema.Test)
	for i := 0; i < backend.DefragLimitForTest()+100; i++ {
		tx.UnsafePut(schema.Test, []byte(fmt.Sprintf("foo_%d", i)), []byte("bar"))
	}
	tx.Unlock()
	b.ForceCommit()

	// remove some keys to ensure the disk space will be reclaimed after defrag
	tx = b.BatchTx()
	tx.Lock()
	for i := 0; i < 50; i++ {
		tx.UnsafeDelete(schema.Test, []byte(fmt.Sprintf("foo_%d", i)))
	}
	tx.Unlock()
	b.ForceCommit()

	size := b.Size()

	// shrink and check hash
	oh, err := b.Hash(nil)
	if err != nil {
		t.Fatal(err)
	}

	err = b.Defrag()
	if err != nil {
		t.Fatal(err)
	}

	nh, err := b.Hash(nil)
	if err != nil {
		t.Fatal(err)
	}
	if oh != nh {
		t.Errorf("hash = %v, want %v", nh, oh)
	}

	nsize := b.Size()
	if nsize >= size {
		t.Errorf("new size = %v, want < %d", nsize, size)
	}

	// try put more keys after shrink.
	tx = b.BatchTx()
	tx.Lock()
	tx.UnsafeCreateBucket(schema.Test)
	tx.UnsafePut(schema.Test, []byte("more"), []byte("bar"))
	tx.Unlock()
	b.ForceCommit()
}

// TestBackendWriteback ensures writes are stored to the read txn on write txn unlock.
func TestBackendWriteback(t *testing.T) {
	b, _ := betesting.NewDefaultTmpBackend(t)
	defer betesting.Close(t, b)

	tx := b.BatchTx()
	tx.Lock()
	tx.UnsafeCreateBucket(schema.Key)
	tx.UnsafePut(schema.Key, []byte("abc"), []byte("bar"))
	tx.UnsafePut(schema.Key, []byte("def"), []byte("baz"))
	tx.UnsafePut(schema.Key, []byte("overwrite"), []byte("1"))
	tx.Unlock()

	// overwrites should be propagated too
	tx.Lock()
	tx.UnsafePut(schema.Key, []byte("overwrite"), []byte("2"))
	tx.Unlock()

	keys := []struct {
		key   []byte
		end   []byte
		limit int64

		wkey [][]byte
		wval [][]byte
	}{
		{
			key: []byte("abc"),
			end: nil,

			wkey: [][]byte{[]byte("abc")},
			wval: [][]byte{[]byte("bar")},
		},
		{
			key: []byte("abc"),
			end: []byte("def"),

			wkey: [][]byte{[]byte("abc")},
			wval: [][]byte{[]byte("bar")},
		},
		{
			key: []byte("abc"),
			end: []byte("deg"),

			wkey: [][]byte{[]byte("abc"), []byte("def")},
			wval: [][]byte{[]byte("bar"), []byte("baz")},
		},
		{
			key:   []byte("abc"),
			end:   []byte("\xff"),
			limit: 1,

			wkey: [][]byte{[]byte("abc")},
			wval: [][]byte{[]byte("bar")},
		},
		{
			key: []byte("abc"),
			end: []byte("\xff"),

			wkey: [][]byte{[]byte("abc"), []byte("def"), []byte("overwrite")},
			wval: [][]byte{[]byte("bar"), []byte("baz"), []byte("2")},
		},
	}
	rtx := b.ReadTx()
	for i, tt := range keys {
		func() {
			rtx.RLock()
			defer rtx.RUnlock()
			k, v := rtx.UnsafeRange(schema.Key, tt.key, tt.end, tt.limit)
			if !reflect.DeepEqual(tt.wkey, k) || !reflect.DeepEqual(tt.wval, v) {
				t.Errorf("#%d: want k=%+v, v=%+v; got k=%+v, v=%+v", i, tt.wkey, tt.wval, k, v)
			}
		}()
	}
}

// TestConcurrentReadTx ensures that current read transaction can see all prior writes stored in read buffer
func TestConcurrentReadTx(t *testing.T) {
	b, _ := betesting.NewTmpBackend(t, time.Hour, 10000)
	defer betesting.Close(t, b)

	wtx1 := b.BatchTx()
	wtx1.Lock()
	wtx1.UnsafeCreateBucket(schema.Key)
	wtx1.UnsafePut(schema.Key, []byte("abc"), []byte("ABC"))
	wtx1.UnsafePut(schema.Key, []byte("overwrite"), []byte("1"))
	wtx1.Unlock()

	wtx2 := b.BatchTx()
	wtx2.Lock()
	wtx2.UnsafePut(schema.Key, []byte("def"), []byte("DEF"))
	wtx2.UnsafePut(schema.Key, []byte("overwrite"), []byte("2"))
	wtx2.Unlock()

	rtx := b.ConcurrentReadTx()
	rtx.RLock() // no-op
	k, v := rtx.UnsafeRange(schema.Key, []byte("abc"), []byte("\xff"), 0)
	rtx.RUnlock()
	wKey := [][]byte{[]byte("abc"), []byte("def"), []byte("overwrite")}
	wVal := [][]byte{[]byte("ABC"), []byte("DEF"), []byte("2")}
	if !reflect.DeepEqual(wKey, k) || !reflect.DeepEqual(wVal, v) {
		t.Errorf("want k=%+v, v=%+v; got k=%+v, v=%+v", wKey, wVal, k, v)
	}
}

// TestBackendWritebackForEach checks that partially written / buffered
// data is visited in the same order as fully committed data.
func TestBackendWritebackForEach(t *testing.T) {
	b, _ := betesting.NewTmpBackend(t, time.Hour, 10000)
	defer betesting.Close(t, b)

	tx := b.BatchTx()
	tx.Lock()
	tx.UnsafeCreateBucket(schema.Key)
	for i := 0; i < 5; i++ {
		k := []byte(fmt.Sprintf("%04d", i))
		tx.UnsafePut(schema.Key, k, []byte("bar"))
	}
	tx.Unlock()

	// writeback
	b.ForceCommit()

	tx.Lock()
	tx.UnsafeCreateBucket(schema.Key)
	for i := 5; i < 20; i++ {
		k := []byte(fmt.Sprintf("%04d", i))
		tx.UnsafePut(schema.Key, k, []byte("bar"))
	}
	tx.Unlock()

	seq := ""
	getSeq := func(k, v []byte) error {
		seq += string(k)
		return nil
	}
	rtx := b.ReadTx()
	rtx.RLock()
	assert.NoError(t, rtx.UnsafeForEach(schema.Key, getSeq))
	rtx.RUnlock()

	partialSeq := seq

	seq = ""
	b.ForceCommit()

	tx.Lock()
	assert.NoError(t, tx.UnsafeForEach(schema.Key, getSeq))
	tx.Unlock()

	if seq != partialSeq {
		t.Fatalf("expected %q, got %q", seq, partialSeq)
	}
}
