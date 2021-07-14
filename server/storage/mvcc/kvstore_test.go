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

package mvcc

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"math"
	mrand "math/rand"
	"os"
	"reflect"
	"sort"
	"strconv"
	"sync"
	"testing"
	"time"

	"go.etcd.io/etcd/api/v3/mvccpb"
	"go.etcd.io/etcd/client/pkg/v3/testutil"
	"go.etcd.io/etcd/pkg/v3/schedule"
	"go.etcd.io/etcd/pkg/v3/traceutil"
	"go.etcd.io/etcd/server/v3/lease"
	"go.etcd.io/etcd/server/v3/storage/backend"
	betesting "go.etcd.io/etcd/server/v3/storage/backend/testing"
	"go.etcd.io/etcd/server/v3/storage/schema"

	"go.uber.org/zap"
)

func TestStoreRev(t *testing.T) {
	b, _ := betesting.NewDefaultTmpBackend(t)
	s := NewStore(zap.NewExample(), b, &lease.FakeLessor{}, StoreConfig{})
	defer s.Close()

	for i := 1; i <= 3; i++ {
		s.Put([]byte("foo"), []byte("bar"), lease.NoLease)
		if r := s.Rev(); r != int64(i+1) {
			t.Errorf("#%d: rev = %d, want %d", i, r, i+1)
		}
	}
}

func TestStorePut(t *testing.T) {
	kv := mvccpb.KeyValue{
		Key:            []byte("foo"),
		Value:          []byte("bar"),
		CreateRevision: 1,
		ModRevision:    2,
		Version:        1,
	}
	kvb, err := kv.Marshal()
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		rev revision
		r   indexGetResp
		rr  *rangeResp

		wrev    revision
		wkey    []byte
		wkv     mvccpb.KeyValue
		wputrev revision
	}{
		{
			revision{1, 0},
			indexGetResp{revision{}, revision{}, 0, ErrRevisionNotFound},
			nil,

			revision{2, 0},
			newTestKeyBytes(revision{2, 0}, false),
			mvccpb.KeyValue{
				Key:            []byte("foo"),
				Value:          []byte("bar"),
				CreateRevision: 2,
				ModRevision:    2,
				Version:        1,
				Lease:          1,
			},
			revision{2, 0},
		},
		{
			revision{1, 1},
			indexGetResp{revision{2, 0}, revision{2, 0}, 1, nil},
			&rangeResp{[][]byte{newTestKeyBytes(revision{2, 1}, false)}, [][]byte{kvb}},

			revision{2, 0},
			newTestKeyBytes(revision{2, 0}, false),
			mvccpb.KeyValue{
				Key:            []byte("foo"),
				Value:          []byte("bar"),
				CreateRevision: 2,
				ModRevision:    2,
				Version:        2,
				Lease:          2,
			},
			revision{2, 0},
		},
		{
			revision{2, 0},
			indexGetResp{revision{2, 1}, revision{2, 0}, 2, nil},
			&rangeResp{[][]byte{newTestKeyBytes(revision{2, 1}, false)}, [][]byte{kvb}},

			revision{3, 0},
			newTestKeyBytes(revision{3, 0}, false),
			mvccpb.KeyValue{
				Key:            []byte("foo"),
				Value:          []byte("bar"),
				CreateRevision: 2,
				ModRevision:    3,
				Version:        3,
				Lease:          3,
			},
			revision{3, 0},
		},
	}
	for i, tt := range tests {
		s := newFakeStore()
		b := s.b.(*fakeBackend)
		fi := s.kvindex.(*fakeIndex)

		s.currentRev = tt.rev.main
		fi.indexGetRespc <- tt.r
		if tt.rr != nil {
			b.tx.rangeRespc <- *tt.rr
		}

		s.Put([]byte("foo"), []byte("bar"), lease.LeaseID(i+1))

		data, err := tt.wkv.Marshal()
		if err != nil {
			t.Errorf("#%d: marshal err = %v, want nil", i, err)
		}

		wact := []testutil.Action{
			{Name: "seqput", Params: []interface{}{schema.Key, tt.wkey, data}},
		}

		if tt.rr != nil {
			wact = []testutil.Action{
				{Name: "seqput", Params: []interface{}{schema.Key, tt.wkey, data}},
			}
		}

		if g := b.tx.Action(); !reflect.DeepEqual(g, wact) {
			t.Errorf("#%d: tx action = %+v, want %+v", i, g, wact)
		}
		wact = []testutil.Action{
			{Name: "get", Params: []interface{}{[]byte("foo"), tt.wputrev.main}},
			{Name: "put", Params: []interface{}{[]byte("foo"), tt.wputrev}},
		}
		if g := fi.Action(); !reflect.DeepEqual(g, wact) {
			t.Errorf("#%d: index action = %+v, want %+v", i, g, wact)
		}
		if s.currentRev != tt.wrev.main {
			t.Errorf("#%d: rev = %+v, want %+v", i, s.currentRev, tt.wrev)
		}

		s.Close()
	}
}

func TestStoreRange(t *testing.T) {
	key := newTestKeyBytes(revision{2, 0}, false)
	kv := mvccpb.KeyValue{
		Key:            []byte("foo"),
		Value:          []byte("bar"),
		CreateRevision: 1,
		ModRevision:    2,
		Version:        1,
	}
	kvb, err := kv.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	wrev := int64(2)

	tests := []struct {
		idxr indexRangeResp
		r    rangeResp
	}{
		{
			indexRangeResp{[][]byte{[]byte("foo")}, []revision{{2, 0}}},
			rangeResp{[][]byte{key}, [][]byte{kvb}},
		},
		{
			indexRangeResp{[][]byte{[]byte("foo"), []byte("foo1")}, []revision{{2, 0}, {3, 0}}},
			rangeResp{[][]byte{key}, [][]byte{kvb}},
		},
	}

	ro := RangeOptions{Limit: 1, Rev: 0, Count: false}
	for i, tt := range tests {
		s := newFakeStore()
		b := s.b.(*fakeBackend)
		fi := s.kvindex.(*fakeIndex)

		s.currentRev = 2
		b.tx.rangeRespc <- tt.r
		fi.indexRangeRespc <- tt.idxr

		ret, err := s.Range(context.TODO(), []byte("foo"), []byte("goo"), ro)
		if err != nil {
			t.Errorf("#%d: err = %v, want nil", i, err)
		}
		if w := []mvccpb.KeyValue{kv}; !reflect.DeepEqual(ret.KVs, w) {
			t.Errorf("#%d: kvs = %+v, want %+v", i, ret.KVs, w)
		}
		if ret.Rev != wrev {
			t.Errorf("#%d: rev = %d, want %d", i, ret.Rev, wrev)
		}

		wstart := newRevBytes()
		revToBytes(tt.idxr.revs[0], wstart)
		wact := []testutil.Action{
			{Name: "range", Params: []interface{}{schema.Key, wstart, []byte(nil), int64(0)}},
		}
		if g := b.tx.Action(); !reflect.DeepEqual(g, wact) {
			t.Errorf("#%d: tx action = %+v, want %+v", i, g, wact)
		}
		wact = []testutil.Action{
			{Name: "range", Params: []interface{}{[]byte("foo"), []byte("goo"), wrev}},
		}
		if g := fi.Action(); !reflect.DeepEqual(g, wact) {
			t.Errorf("#%d: index action = %+v, want %+v", i, g, wact)
		}
		if s.currentRev != 2 {
			t.Errorf("#%d: current rev = %+v, want %+v", i, s.currentRev, 2)
		}

		s.Close()
	}
}

func TestStoreDeleteRange(t *testing.T) {
	key := newTestKeyBytes(revision{2, 0}, false)
	kv := mvccpb.KeyValue{
		Key:            []byte("foo"),
		Value:          []byte("bar"),
		CreateRevision: 1,
		ModRevision:    2,
		Version:        1,
	}
	kvb, err := kv.Marshal()
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		rev revision
		r   indexRangeResp
		rr  rangeResp

		wkey    []byte
		wrev    revision
		wrrev   int64
		wdelrev revision
	}{
		{
			revision{2, 0},
			indexRangeResp{[][]byte{[]byte("foo")}, []revision{{2, 0}}},
			rangeResp{[][]byte{key}, [][]byte{kvb}},

			newTestKeyBytes(revision{3, 0}, true),
			revision{3, 0},
			2,
			revision{3, 0},
		},
	}
	for i, tt := range tests {
		s := newFakeStore()
		b := s.b.(*fakeBackend)
		fi := s.kvindex.(*fakeIndex)

		s.currentRev = tt.rev.main
		fi.indexRangeRespc <- tt.r
		b.tx.rangeRespc <- tt.rr

		n, _ := s.DeleteRange([]byte("foo"), []byte("goo"))
		if n != 1 {
			t.Errorf("#%d: n = %d, want 1", i, n)
		}

		data, err := (&mvccpb.KeyValue{
			Key: []byte("foo"),
		}).Marshal()
		if err != nil {
			t.Errorf("#%d: marshal err = %v, want nil", i, err)
		}
		wact := []testutil.Action{
			{Name: "seqput", Params: []interface{}{schema.Key, tt.wkey, data}},
		}
		if g := b.tx.Action(); !reflect.DeepEqual(g, wact) {
			t.Errorf("#%d: tx action = %+v, want %+v", i, g, wact)
		}
		wact = []testutil.Action{
			{Name: "range", Params: []interface{}{[]byte("foo"), []byte("goo"), tt.wrrev}},
			{Name: "tombstone", Params: []interface{}{[]byte("foo"), tt.wdelrev}},
		}
		if g := fi.Action(); !reflect.DeepEqual(g, wact) {
			t.Errorf("#%d: index action = %+v, want %+v", i, g, wact)
		}
		if s.currentRev != tt.wrev.main {
			t.Errorf("#%d: rev = %+v, want %+v", i, s.currentRev, tt.wrev)
		}
	}
}

func TestStoreCompact(t *testing.T) {
	s := newFakeStore()
	defer s.Close()
	b := s.b.(*fakeBackend)
	fi := s.kvindex.(*fakeIndex)

	s.currentRev = 3
	fi.indexCompactRespc <- map[revision]struct{}{{1, 0}: {}}
	key1 := newTestKeyBytes(revision{1, 0}, false)
	key2 := newTestKeyBytes(revision{2, 0}, false)
	b.tx.rangeRespc <- rangeResp{[][]byte{key1, key2}, nil}

	s.Compact(traceutil.TODO(), 3)
	s.fifoSched.WaitFinish(1)

	if s.compactMainRev != 3 {
		t.Errorf("compact main rev = %d, want 3", s.compactMainRev)
	}
	end := make([]byte, 8)
	binary.BigEndian.PutUint64(end, uint64(4))
	wact := []testutil.Action{
		{Name: "put", Params: []interface{}{schema.Meta, schema.ScheduledCompactKeyName, newTestRevBytes(revision{3, 0})}},
		{Name: "range", Params: []interface{}{schema.Key, make([]byte, 17), end, int64(10000)}},
		{Name: "delete", Params: []interface{}{schema.Key, key2}},
		{Name: "put", Params: []interface{}{schema.Meta, schema.FinishedCompactKeyName, newTestRevBytes(revision{3, 0})}},
	}
	if g := b.tx.Action(); !reflect.DeepEqual(g, wact) {
		t.Errorf("tx actions = %+v, want %+v", g, wact)
	}
	wact = []testutil.Action{
		{Name: "compact", Params: []interface{}{int64(3)}},
	}
	if g := fi.Action(); !reflect.DeepEqual(g, wact) {
		t.Errorf("index action = %+v, want %+v", g, wact)
	}
}

func TestStoreRestore(t *testing.T) {
	s := newFakeStore()
	b := s.b.(*fakeBackend)
	fi := s.kvindex.(*fakeIndex)

	putkey := newTestKeyBytes(revision{3, 0}, false)
	putkv := mvccpb.KeyValue{
		Key:            []byte("foo"),
		Value:          []byte("bar"),
		CreateRevision: 4,
		ModRevision:    4,
		Version:        1,
	}
	putkvb, err := putkv.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	delkey := newTestKeyBytes(revision{5, 0}, true)
	delkv := mvccpb.KeyValue{
		Key: []byte("foo"),
	}
	delkvb, err := delkv.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	b.tx.rangeRespc <- rangeResp{[][]byte{schema.FinishedCompactKeyName}, [][]byte{newTestRevBytes(revision{3, 0})}}
	b.tx.rangeRespc <- rangeResp{[][]byte{schema.ScheduledCompactKeyName}, [][]byte{newTestRevBytes(revision{3, 0})}}

	b.tx.rangeRespc <- rangeResp{[][]byte{putkey, delkey}, [][]byte{putkvb, delkvb}}
	b.tx.rangeRespc <- rangeResp{nil, nil}

	s.restore()

	if s.compactMainRev != 3 {
		t.Errorf("compact rev = %d, want 3", s.compactMainRev)
	}
	if s.currentRev != 5 {
		t.Errorf("current rev = %v, want 5", s.currentRev)
	}
	wact := []testutil.Action{
		{Name: "range", Params: []interface{}{schema.Meta, schema.FinishedCompactKeyName, []byte(nil), int64(0)}},
		{Name: "range", Params: []interface{}{schema.Meta, schema.ScheduledCompactKeyName, []byte(nil), int64(0)}},
		{Name: "range", Params: []interface{}{schema.Key, newTestRevBytes(revision{1, 0}), newTestRevBytes(revision{math.MaxInt64, math.MaxInt64}), int64(restoreChunkKeys)}},
	}
	if g := b.tx.Action(); !reflect.DeepEqual(g, wact) {
		t.Errorf("tx actions = %+v, want %+v", g, wact)
	}

	gens := []generation{
		{created: revision{4, 0}, ver: 2, revs: []revision{{3, 0}, {5, 0}}},
		{created: revision{0, 0}, ver: 0, revs: nil},
	}
	ki := &keyIndex{key: []byte("foo"), modified: revision{5, 0}, generations: gens}
	wact = []testutil.Action{
		{Name: "keyIndex", Params: []interface{}{ki}},
		{Name: "insert", Params: []interface{}{ki}},
	}
	if g := fi.Action(); !reflect.DeepEqual(g, wact) {
		t.Errorf("index action = %+v, want %+v", g, wact)
	}
}

func TestRestoreDelete(t *testing.T) {
	oldChunk := restoreChunkKeys
	restoreChunkKeys = mrand.Intn(3) + 2
	defer func() { restoreChunkKeys = oldChunk }()

	b, _ := betesting.NewDefaultTmpBackend(t)
	s := NewStore(zap.NewExample(), b, &lease.FakeLessor{}, StoreConfig{})

	keys := make(map[string]struct{})
	for i := 0; i < 20; i++ {
		ks := fmt.Sprintf("foo-%d", i)
		k := []byte(ks)
		s.Put(k, []byte("bar"), lease.NoLease)
		keys[ks] = struct{}{}
		switch mrand.Intn(3) {
		case 0:
			// put random key from past via random range on map
			ks = fmt.Sprintf("foo-%d", mrand.Intn(i+1))
			s.Put([]byte(ks), []byte("baz"), lease.NoLease)
			keys[ks] = struct{}{}
		case 1:
			// delete random key via random range on map
			for k := range keys {
				s.DeleteRange([]byte(k), nil)
				delete(keys, k)
				break
			}
		}
	}
	s.Close()

	s = NewStore(zap.NewExample(), b, &lease.FakeLessor{}, StoreConfig{})
	defer s.Close()
	for i := 0; i < 20; i++ {
		ks := fmt.Sprintf("foo-%d", i)
		r, err := s.Range(context.TODO(), []byte(ks), nil, RangeOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := keys[ks]; ok {
			if len(r.KVs) == 0 {
				t.Errorf("#%d: expected %q, got deleted", i, ks)
			}
		} else if len(r.KVs) != 0 {
			t.Errorf("#%d: expected deleted, got %q", i, ks)
		}
	}
}

func TestRestoreContinueUnfinishedCompaction(t *testing.T) {
	tests := []string{"recreate", "restore"}
	for _, test := range tests {
		b, _ := betesting.NewDefaultTmpBackend(t)
		s0 := NewStore(zap.NewExample(), b, &lease.FakeLessor{}, StoreConfig{})

		s0.Put([]byte("foo"), []byte("bar"), lease.NoLease)
		s0.Put([]byte("foo"), []byte("bar1"), lease.NoLease)
		s0.Put([]byte("foo"), []byte("bar2"), lease.NoLease)

		// write scheduled compaction, but not do compaction
		rbytes := newRevBytes()
		revToBytes(revision{main: 2}, rbytes)
		tx := s0.b.BatchTx()
		tx.Lock()
		tx.UnsafePut(schema.Meta, schema.ScheduledCompactKeyName, rbytes)
		tx.Unlock()

		s0.Close()

		var s *store
		switch test {
		case "recreate":
			s = NewStore(zap.NewExample(), b, &lease.FakeLessor{}, StoreConfig{})
		case "restore":
			s0.Restore(b)
			s = s0
		}

		// wait for scheduled compaction to be finished
		time.Sleep(100 * time.Millisecond)

		if _, err := s.Range(context.TODO(), []byte("foo"), nil, RangeOptions{Rev: 1}); err != ErrCompacted {
			t.Errorf("range on compacted rev error = %v, want %v", err, ErrCompacted)
		}
		// check the key in backend is deleted
		revbytes := newRevBytes()
		revToBytes(revision{main: 1}, revbytes)

		// The disk compaction is done asynchronously and requires more time on slow disk.
		// try 5 times for CI with slow IO.
		for i := 0; i < 5; i++ {
			tx := s.b.BatchTx()
			tx.Lock()
			ks, _ := tx.UnsafeRange(schema.Key, revbytes, nil, 0)
			tx.Unlock()
			if len(ks) != 0 {
				time.Sleep(100 * time.Millisecond)
				continue
			}
			return
		}

		t.Errorf("key for rev %+v still exists, want deleted", bytesToRev(revbytes))
	}
}

type hashKVResult struct {
	hash       uint32
	compactRev int64
}

// TestHashKVWhenCompacting ensures that HashKV returns correct hash when compacting.
func TestHashKVWhenCompacting(t *testing.T) {
	b, tmpPath := betesting.NewDefaultTmpBackend(t)
	s := NewStore(zap.NewExample(), b, &lease.FakeLessor{}, StoreConfig{})
	defer os.Remove(tmpPath)

	rev := 10000
	for i := 2; i <= rev; i++ {
		s.Put([]byte("foo"), []byte(fmt.Sprintf("bar%d", i)), lease.NoLease)
	}

	hashCompactc := make(chan hashKVResult, 1)

	donec := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				hash, _, compactRev, err := s.HashByRev(int64(rev))
				if err != nil {
					t.Error(err)
				}
				select {
				case <-donec:
					return
				case hashCompactc <- hashKVResult{hash, compactRev}:
				}
			}
		}()
	}

	go func() {
		defer close(donec)
		revHash := make(map[int64]uint32)
		for round := 0; round < 1000; round++ {
			r := <-hashCompactc
			if revHash[r.compactRev] == 0 {
				revHash[r.compactRev] = r.hash
			}
			if r.hash != revHash[r.compactRev] {
				t.Errorf("Hashes differ (current %v) != (saved %v)", r.hash, revHash[r.compactRev])
			}
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 100; i >= 0; i-- {
			_, err := s.Compact(traceutil.TODO(), int64(rev-1-i))
			if err != nil {
				t.Error(err)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()

	select {
	case <-donec:
		wg.Wait()
	case <-time.After(10 * time.Second):
		testutil.FatalStack(t, "timeout")
	}
}

// TestHashKVZeroRevision ensures that "HashByRev(0)" computes
// correct hash value with latest revision.
func TestHashKVZeroRevision(t *testing.T) {
	b, tmpPath := betesting.NewDefaultTmpBackend(t)
	s := NewStore(zap.NewExample(), b, &lease.FakeLessor{}, StoreConfig{})
	defer os.Remove(tmpPath)

	rev := 10000
	for i := 2; i <= rev; i++ {
		s.Put([]byte("foo"), []byte(fmt.Sprintf("bar%d", i)), lease.NoLease)
	}
	if _, err := s.Compact(traceutil.TODO(), int64(rev/2)); err != nil {
		t.Fatal(err)
	}

	hash1, _, _, err := s.HashByRev(int64(rev))
	if err != nil {
		t.Fatal(err)
	}
	var hash2 uint32
	hash2, _, _, err = s.HashByRev(0)
	if err != nil {
		t.Fatal(err)
	}
	if hash1 != hash2 {
		t.Errorf("hash %d (rev %d) != hash %d (rev 0)", hash1, rev, hash2)
	}
}

func TestTxnPut(t *testing.T) {
	// assign arbitrary size
	bytesN := 30
	sliceN := 100
	keys := createBytesSlice(bytesN, sliceN)
	vals := createBytesSlice(bytesN, sliceN)

	b, tmpPath := betesting.NewDefaultTmpBackend(t)
	s := NewStore(zap.NewExample(), b, &lease.FakeLessor{}, StoreConfig{})
	defer cleanup(s, b, tmpPath)

	for i := 0; i < sliceN; i++ {
		txn := s.Write(traceutil.TODO())
		base := int64(i + 2)
		if rev := txn.Put(keys[i], vals[i], lease.NoLease); rev != base {
			t.Errorf("#%d: rev = %d, want %d", i, rev, base)
		}
		txn.End()
	}
}

// TestConcurrentReadNotBlockingWrite ensures Read does not blocking Write after its creation
func TestConcurrentReadNotBlockingWrite(t *testing.T) {
	b, tmpPath := betesting.NewDefaultTmpBackend(t)
	s := NewStore(zap.NewExample(), b, &lease.FakeLessor{}, StoreConfig{})
	defer os.Remove(tmpPath)

	// write something to read later
	s.Put([]byte("foo"), []byte("bar"), lease.NoLease)

	// readTx simulates a long read request
	readTx1 := s.Read(ConcurrentReadTxMode, traceutil.TODO())

	// write should not be blocked by reads
	done := make(chan struct{}, 1)
	go func() {
		s.Put([]byte("foo"), []byte("newBar"), lease.NoLease) // this is a write Txn
		done <- struct{}{}
	}()
	select {
	case <-done:
	case <-time.After(1 * time.Second):
		t.Fatalf("write should not be blocked by read")
	}

	// readTx2 simulates a short read request
	readTx2 := s.Read(ConcurrentReadTxMode, traceutil.TODO())
	ro := RangeOptions{Limit: 1, Rev: 0, Count: false}
	ret, err := readTx2.Range(context.TODO(), []byte("foo"), nil, ro)
	if err != nil {
		t.Fatalf("failed to range: %v", err)
	}
	// readTx2 should see the result of new write
	w := mvccpb.KeyValue{
		Key:            []byte("foo"),
		Value:          []byte("newBar"),
		CreateRevision: 2,
		ModRevision:    3,
		Version:        2,
	}
	if !reflect.DeepEqual(ret.KVs[0], w) {
		t.Fatalf("range result = %+v, want = %+v", ret.KVs[0], w)
	}
	readTx2.End()

	ret, err = readTx1.Range(context.TODO(), []byte("foo"), nil, ro)
	if err != nil {
		t.Fatalf("failed to range: %v", err)
	}
	// readTx1 should not see the result of new write
	w = mvccpb.KeyValue{
		Key:            []byte("foo"),
		Value:          []byte("bar"),
		CreateRevision: 2,
		ModRevision:    2,
		Version:        1,
	}
	if !reflect.DeepEqual(ret.KVs[0], w) {
		t.Fatalf("range result = %+v, want = %+v", ret.KVs[0], w)
	}
	readTx1.End()
}

// TestConcurrentReadTxAndWrite creates random concurrent Reads and Writes, and ensures Reads always see latest Writes
func TestConcurrentReadTxAndWrite(t *testing.T) {
	var (
		numOfReads           = 100
		numOfWrites          = 100
		maxNumOfPutsPerWrite = 10
		committedKVs         kvs        // committedKVs records the key-value pairs written by the finished Write Txns
		mu                   sync.Mutex // mu protects committedKVs
	)
	b, tmpPath := betesting.NewDefaultTmpBackend(t)
	s := NewStore(zap.NewExample(), b, &lease.FakeLessor{}, StoreConfig{})
	defer os.Remove(tmpPath)

	var wg sync.WaitGroup
	wg.Add(numOfWrites)
	for i := 0; i < numOfWrites; i++ {
		go func() {
			defer wg.Done()
			time.Sleep(time.Duration(mrand.Intn(100)) * time.Millisecond) // random starting time

			tx := s.Write(traceutil.TODO())
			numOfPuts := mrand.Intn(maxNumOfPutsPerWrite) + 1
			var pendingKvs kvs
			for j := 0; j < numOfPuts; j++ {
				k := []byte(strconv.Itoa(mrand.Int()))
				v := []byte(strconv.Itoa(mrand.Int()))
				tx.Put(k, v, lease.NoLease)
				pendingKvs = append(pendingKvs, kv{k, v})
			}
			// reads should not see above Puts until write is finished
			mu.Lock()
			committedKVs = merge(committedKVs, pendingKvs) // update shared data structure
			tx.End()
			mu.Unlock()
		}()
	}

	wg.Add(numOfReads)
	for i := 0; i < numOfReads; i++ {
		go func() {
			defer wg.Done()
			time.Sleep(time.Duration(mrand.Intn(100)) * time.Millisecond) // random starting time

			mu.Lock()
			wKVs := make(kvs, len(committedKVs))
			copy(wKVs, committedKVs)
			tx := s.Read(ConcurrentReadTxMode, traceutil.TODO())
			mu.Unlock()
			// get all keys in backend store, and compare with wKVs
			ret, err := tx.Range(context.TODO(), []byte("\x00000000"), []byte("\xffffffff"), RangeOptions{})
			tx.End()
			if err != nil {
				t.Errorf("failed to range keys: %v", err)
				return
			}
			if len(wKVs) == 0 && len(ret.KVs) == 0 { // no committed KVs yet
				return
			}
			var result kvs
			for _, keyValue := range ret.KVs {
				result = append(result, kv{keyValue.Key, keyValue.Value})
			}
			if !reflect.DeepEqual(wKVs, result) {
				t.Errorf("unexpected range result") // too many key value pairs, skip printing them
			}
		}()
	}

	// wait until goroutines finish or timeout
	doneC := make(chan struct{})
	go func() {
		wg.Wait()
		close(doneC)
	}()
	select {
	case <-doneC:
	case <-time.After(5 * time.Minute):
		testutil.FatalStack(t, "timeout")
	}
}

type kv struct {
	key []byte
	val []byte
}

type kvs []kv

func (kvs kvs) Len() int           { return len(kvs) }
func (kvs kvs) Less(i, j int) bool { return bytes.Compare(kvs[i].key, kvs[j].key) < 0 }
func (kvs kvs) Swap(i, j int)      { kvs[i], kvs[j] = kvs[j], kvs[i] }

func merge(dst, src kvs) kvs {
	dst = append(dst, src...)
	sort.Stable(dst)
	// remove duplicates, using only the newest value
	// ref: tx_buffer.go
	widx := 0
	for ridx := 1; ridx < len(dst); ridx++ {
		if !bytes.Equal(dst[widx].key, dst[ridx].key) {
			widx++
		}
		dst[widx] = dst[ridx]
	}
	return dst[:widx+1]
}

// TODO: test attach key to lessor

func newTestRevBytes(rev revision) []byte {
	bytes := newRevBytes()
	revToBytes(rev, bytes)
	return bytes
}

func newTestKeyBytes(rev revision, tombstone bool) []byte {
	bytes := newRevBytes()
	revToBytes(rev, bytes)
	if tombstone {
		bytes = appendMarkTombstone(zap.NewExample(), bytes)
	}
	return bytes
}

func newFakeStore() *store {
	b := &fakeBackend{&fakeBatchTx{
		Recorder:   &testutil.RecorderBuffered{},
		rangeRespc: make(chan rangeResp, 5)}}
	fi := &fakeIndex{
		Recorder:              &testutil.RecorderBuffered{},
		indexGetRespc:         make(chan indexGetResp, 1),
		indexRangeRespc:       make(chan indexRangeResp, 1),
		indexRangeEventsRespc: make(chan indexRangeEventsResp, 1),
		indexCompactRespc:     make(chan map[revision]struct{}, 1),
	}
	s := &store{
		cfg:            StoreConfig{CompactionBatchLimit: 10000},
		b:              b,
		le:             &lease.FakeLessor{},
		kvindex:        fi,
		currentRev:     0,
		compactMainRev: -1,
		fifoSched:      schedule.NewFIFOScheduler(),
		stopc:          make(chan struct{}),
		lg:             zap.NewExample(),
	}
	s.ReadView, s.WriteView = &readView{s}, &writeView{s}
	return s
}

type rangeResp struct {
	keys [][]byte
	vals [][]byte
}

type fakeBatchTx struct {
	testutil.Recorder
	rangeRespc chan rangeResp
}

func (b *fakeBatchTx) Lock()                                    {}
func (b *fakeBatchTx) Unlock()                                  {}
func (b *fakeBatchTx) RLock()                                   {}
func (b *fakeBatchTx) RUnlock()                                 {}
func (b *fakeBatchTx) UnsafeCreateBucket(bucket backend.Bucket) {}
func (b *fakeBatchTx) UnsafeDeleteBucket(bucket backend.Bucket) {}
func (b *fakeBatchTx) UnsafePut(bucket backend.Bucket, key []byte, value []byte) {
	b.Recorder.Record(testutil.Action{Name: "put", Params: []interface{}{bucket, key, value}})
}
func (b *fakeBatchTx) UnsafeSeqPut(bucket backend.Bucket, key []byte, value []byte) {
	b.Recorder.Record(testutil.Action{Name: "seqput", Params: []interface{}{bucket, key, value}})
}
func (b *fakeBatchTx) UnsafeRange(bucket backend.Bucket, key, endKey []byte, limit int64) (keys [][]byte, vals [][]byte) {
	b.Recorder.Record(testutil.Action{Name: "range", Params: []interface{}{bucket, key, endKey, limit}})
	r := <-b.rangeRespc
	return r.keys, r.vals
}
func (b *fakeBatchTx) UnsafeDelete(bucket backend.Bucket, key []byte) {
	b.Recorder.Record(testutil.Action{Name: "delete", Params: []interface{}{bucket, key}})
}
func (b *fakeBatchTx) UnsafeForEach(bucket backend.Bucket, visitor func(k, v []byte) error) error {
	return nil
}
func (b *fakeBatchTx) Commit()        {}
func (b *fakeBatchTx) CommitAndStop() {}

type fakeBackend struct {
	tx *fakeBatchTx
}

func (b *fakeBackend) BatchTx() backend.BatchTx                                   { return b.tx }
func (b *fakeBackend) ReadTx() backend.ReadTx                                     { return b.tx }
func (b *fakeBackend) ConcurrentReadTx() backend.ReadTx                           { return b.tx }
func (b *fakeBackend) Hash(func(bucketName, keyName []byte) bool) (uint32, error) { return 0, nil }
func (b *fakeBackend) Size() int64                                                { return 0 }
func (b *fakeBackend) SizeInUse() int64                                           { return 0 }
func (b *fakeBackend) OpenReadTxN() int64                                         { return 0 }
func (b *fakeBackend) Snapshot() backend.Snapshot                                 { return nil }
func (b *fakeBackend) ForceCommit()                                               {}
func (b *fakeBackend) Defrag() error                                              { return nil }
func (b *fakeBackend) Close() error                                               { return nil }

type indexGetResp struct {
	rev     revision
	created revision
	ver     int64
	err     error
}

type indexRangeResp struct {
	keys [][]byte
	revs []revision
}

type indexRangeEventsResp struct {
	revs []revision
}

type fakeIndex struct {
	testutil.Recorder
	indexGetRespc         chan indexGetResp
	indexRangeRespc       chan indexRangeResp
	indexRangeEventsRespc chan indexRangeEventsResp
	indexCompactRespc     chan map[revision]struct{}
}

func (i *fakeIndex) Revisions(key, end []byte, atRev int64, limit int) ([]revision, int) {
	_, rev := i.Range(key, end, atRev)
	if len(rev) >= limit {
		rev = rev[:limit]
	}
	return rev, len(rev)
}

func (i *fakeIndex) CountRevisions(key, end []byte, atRev int64) int {
	_, rev := i.Range(key, end, atRev)
	return len(rev)
}

func (i *fakeIndex) Get(key []byte, atRev int64) (rev, created revision, ver int64, err error) {
	i.Recorder.Record(testutil.Action{Name: "get", Params: []interface{}{key, atRev}})
	r := <-i.indexGetRespc
	return r.rev, r.created, r.ver, r.err
}
func (i *fakeIndex) Range(key, end []byte, atRev int64) ([][]byte, []revision) {
	i.Recorder.Record(testutil.Action{Name: "range", Params: []interface{}{key, end, atRev}})
	r := <-i.indexRangeRespc
	return r.keys, r.revs
}
func (i *fakeIndex) Put(key []byte, rev revision) {
	i.Recorder.Record(testutil.Action{Name: "put", Params: []interface{}{key, rev}})
}
func (i *fakeIndex) Tombstone(key []byte, rev revision) error {
	i.Recorder.Record(testutil.Action{Name: "tombstone", Params: []interface{}{key, rev}})
	return nil
}
func (i *fakeIndex) RangeSince(key, end []byte, rev int64) []revision {
	i.Recorder.Record(testutil.Action{Name: "rangeEvents", Params: []interface{}{key, end, rev}})
	r := <-i.indexRangeEventsRespc
	return r.revs
}
func (i *fakeIndex) Compact(rev int64) map[revision]struct{} {
	i.Recorder.Record(testutil.Action{Name: "compact", Params: []interface{}{rev}})
	return <-i.indexCompactRespc
}
func (i *fakeIndex) Keep(rev int64) map[revision]struct{} {
	i.Recorder.Record(testutil.Action{Name: "keep", Params: []interface{}{rev}})
	return <-i.indexCompactRespc
}
func (i *fakeIndex) Equal(b index) bool { return false }

func (i *fakeIndex) Insert(ki *keyIndex) {
	i.Recorder.Record(testutil.Action{Name: "insert", Params: []interface{}{ki}})
}

func (i *fakeIndex) KeyIndex(ki *keyIndex) *keyIndex {
	i.Recorder.Record(testutil.Action{Name: "keyIndex", Params: []interface{}{ki}})
	return nil
}

func createBytesSlice(bytesN, sliceN int) [][]byte {
	rs := [][]byte{}
	for len(rs) != sliceN {
		v := make([]byte, bytesN)
		if _, err := rand.Read(v); err != nil {
			panic(err)
		}
		rs = append(rs, v)
	}
	return rs
}
