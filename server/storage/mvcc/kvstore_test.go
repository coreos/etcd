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
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	mrand "math/rand"
	"reflect"
	"sort"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/testing/protocmp"

	"go.etcd.io/etcd/api/v3/mvccpb"
	"go.etcd.io/etcd/client/pkg/v3/testutil"
	"go.etcd.io/etcd/pkg/v3/schedule"
	"go.etcd.io/etcd/pkg/v3/traceutil"
	"go.etcd.io/etcd/server/v3/lease"
	"go.etcd.io/etcd/server/v3/storage/backend"
	betesting "go.etcd.io/etcd/server/v3/storage/backend/testing"
	"go.etcd.io/etcd/server/v3/storage/schema"
)

func TestStoreRev(t *testing.T) {
	b, _ := betesting.NewDefaultTmpBackend(t)
	s := NewStore(zaptest.NewLogger(t), b, &lease.FakeLessor{}, StoreConfig{})
	defer s.Close()

	for i := 1; i <= 3; i++ {
		s.Put([]byte("foo"), []byte("bar"), lease.NoLease)
		if r := s.Rev(); r != int64(i+1) {
			t.Errorf("#%d: rev = %d, want %d", i, r, i+1)
		}
	}
}

func TestStorePut(t *testing.T) {
	lg := zaptest.NewLogger(t)
	kv := mvccpb.KeyValue{
		Key:            []byte("foo"),
		Value:          []byte("bar"),
		CreateRevision: 1,
		ModRevision:    2,
		Version:        1,
	}
	kvb, err := proto.Marshal(&kv)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		rev Revision
		r   indexGetResp
		rr  *rangeResp

		wrev    Revision
		wkey    []byte
		wkv     *mvccpb.KeyValue
		wputrev Revision
	}{
		{
			Revision{Main: 1},
			indexGetResp{Revision{}, Revision{}, 0, ErrRevisionNotFound},
			nil,

			Revision{Main: 2},
			newTestRevBytes(Revision{Main: 2}),
			&mvccpb.KeyValue{
				Key:            []byte("foo"),
				Value:          []byte("bar"),
				CreateRevision: 2,
				ModRevision:    2,
				Version:        1,
				Lease:          1,
			},
			Revision{Main: 2},
		},
		{
			Revision{Main: 1, Sub: 1},
			indexGetResp{Revision{Main: 2}, Revision{Main: 2}, 1, nil},
			&rangeResp{[][]byte{newTestRevBytes(Revision{Main: 2, Sub: 1})}, [][]byte{kvb}},

			Revision{Main: 2},
			newTestRevBytes(Revision{Main: 2}),
			&mvccpb.KeyValue{
				Key:            []byte("foo"),
				Value:          []byte("bar"),
				CreateRevision: 2,
				ModRevision:    2,
				Version:        2,
				Lease:          2,
			},
			Revision{Main: 2},
		},
		{
			Revision{Main: 2},
			indexGetResp{Revision{Main: 2, Sub: 1}, Revision{Main: 2}, 2, nil},
			&rangeResp{[][]byte{newTestRevBytes(Revision{Main: 2, Sub: 1})}, [][]byte{kvb}},

			Revision{Main: 3},
			newTestRevBytes(Revision{Main: 3}),
			&mvccpb.KeyValue{
				Key:            []byte("foo"),
				Value:          []byte("bar"),
				CreateRevision: 2,
				ModRevision:    3,
				Version:        3,
				Lease:          3,
			},
			Revision{Main: 3},
		},
	}
	for i, tt := range tests {
		s := newFakeStore(lg)
		b := s.b.(*fakeBackend)
		fi := s.kvindex.(*fakeIndex)

		s.currentRev = tt.rev.Main
		fi.indexGetRespc <- tt.r
		if tt.rr != nil {
			b.tx.rangeRespc <- *tt.rr
		}

		s.Put([]byte("foo"), []byte("bar"), lease.LeaseID(i+1))

		data, err := proto.Marshal(tt.wkv)
		if err != nil {
			t.Errorf("#%d: marshal err = %v, want nil", i, err)
		}

		wact := []testutil.Action{
			{Name: "seqput", Params: []any{schema.Key, tt.wkey, data}},
		}

		if tt.rr != nil {
			wact = []testutil.Action{
				{Name: "seqput", Params: []any{schema.Key, tt.wkey, data}},
			}
		}

		if g := b.tx.Action(); !reflect.DeepEqual(g, wact) {
			t.Errorf("#%d: tx action = %+v, want %+v", i, g, wact)
		}
		wact = []testutil.Action{
			{Name: "get", Params: []any{[]byte("foo"), tt.wputrev.Main}},
			{Name: "put", Params: []any{[]byte("foo"), lease.LeaseID(i + 1), tt.wputrev}},
		}
		if g := fi.Action(); !reflect.DeepEqual(g, wact) {
			t.Errorf("#%d: index action = %+v, want %+v", i, g, wact)
		}
		if s.currentRev != tt.wrev.Main {
			t.Errorf("#%d: rev = %+v, want %+v", i, s.currentRev, tt.wrev)
		}

		s.Close()
	}
}

func TestStoreRange(t *testing.T) {
	lg := zaptest.NewLogger(t)
	key := newTestRevBytes(Revision{Main: 2})
	kv := &mvccpb.KeyValue{
		Key:            []byte("foo"),
		Value:          []byte("bar"),
		CreateRevision: 1,
		ModRevision:    2,
		Version:        1,
	}
	kvb, err := proto.Marshal(kv)
	if err != nil {
		t.Fatal(err)
	}
	wrev := int64(2)

	tests := []struct {
		idxr indexRangeResp
		r    rangeResp
	}{
		{
			indexRangeResp{[][]byte{[]byte("foo")}, []lease.LeaseID{lease.NoLease}, []Revision{{Main: 2}}, []Revision{{Main: 1}}, []int64{1}, 1},
			rangeResp{[][]byte{key}, [][]byte{kvb}},
		},
		{
			indexRangeResp{[][]byte{[]byte("foo"), []byte("foo1")}, []lease.LeaseID{lease.NoLease, lease.NoLease}, []Revision{{Main: 2}, {Main: 3}}, []Revision{{Main: 2}, {Main: 3}}, []int64{1, 1}, 2},
			rangeResp{[][]byte{key}, [][]byte{kvb}},
		},
	}

	ro := RangeOptions{Limit: 1, Rev: 0, CountOnly: false}
	for i, tt := range tests {
		s := newFakeStore(lg)
		b := s.b.(*fakeBackend)
		fi := s.kvindex.(*fakeIndex)

		s.currentRev = 2
		b.tx.rangeRespc <- tt.r
		fi.indexRangeRespc <- tt.idxr

		ret, err := s.Range(t.Context(), []byte("foo"), []byte("goo"), ro)
		if err != nil {
			t.Errorf("#%d: err = %v, want nil", i, err)
		}
		if w := []*mvccpb.KeyValue{kv}; !cmp.Equal(ret.KVs, w, protocmp.Transform()) {
			t.Errorf("#%d: kvs = %+v, want %+v", i, ret.KVs, w)
		}
		if ret.Rev != wrev {
			t.Errorf("#%d: rev = %d, want %d", i, ret.Rev, wrev)
		}

		wstart := NewRevBytes()
		wstart = RevToBytes(tt.idxr.revs[0], wstart)
		wact := []testutil.Action{
			{Name: "range", Params: []any{schema.Key, wstart, []byte(nil), int64(0)}},
		}
		if g := b.tx.Action(); !reflect.DeepEqual(g, wact) {
			t.Errorf("#%d: tx action = %+v, want %+v", i, g, wact)
		}
		wact = []testutil.Action{
			{Name: "range", Params: []any{[]byte("foo"), []byte("goo"), wrev}},
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
	lg := zaptest.NewLogger(t)
	key := newTestRevBytes(Revision{Main: 2})
	kv := mvccpb.KeyValue{
		Key:            []byte("foo"),
		Value:          []byte("bar"),
		CreateRevision: 1,
		ModRevision:    2,
		Version:        1,
	}
	kvb, err := proto.Marshal(&kv)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		rev Revision
		r   indexRangeResp
		rr  rangeResp

		wkey    []byte
		wrev    Revision
		wrrev   int64
		wdelrev Revision
	}{
		{
			Revision{Main: 2},
			indexRangeResp{[][]byte{[]byte("foo")}, []lease.LeaseID{lease.NoLease}, []Revision{{Main: 2}}, []Revision{{Main: 2}}, []int64{1}, 1},
			rangeResp{[][]byte{key}, [][]byte{kvb}},

			newTestBucketKeyBytes(newBucketKey(3, 0, true)),
			Revision{Main: 3},
			2,
			Revision{Main: 3},
		},
	}
	for i, tt := range tests {
		s := newFakeStore(lg)
		b := s.b.(*fakeBackend)
		fi := s.kvindex.(*fakeIndex)

		s.currentRev = tt.rev.Main
		fi.indexRangeRespc <- tt.r
		b.tx.rangeRespc <- tt.rr

		n, _ := s.DeleteRange([]byte("foo"), []byte("goo"))
		if n != 1 {
			t.Errorf("#%d: n = %d, want 1", i, n)
		}

		data, err := proto.Marshal(&mvccpb.KeyValue{
			Key: []byte("foo"),
		})
		if err != nil {
			t.Errorf("#%d: marshal err = %v, want nil", i, err)
		}
		wact := []testutil.Action{
			{Name: "seqput", Params: []any{schema.Key, tt.wkey, data}},
		}
		if g := b.tx.Action(); !reflect.DeepEqual(g, wact) {
			t.Errorf("#%d: tx action = %+v, want %+v", i, g, wact)
		}
		wact = []testutil.Action{
			{Name: "range", Params: []any{[]byte("foo"), []byte("goo"), tt.wrrev}},
			{Name: "tombstone", Params: []any{[]byte("foo"), tt.wdelrev}},
		}
		if g := fi.Action(); !reflect.DeepEqual(g, wact) {
			t.Errorf("#%d: index action = %+v, want %+v", i, g, wact)
		}
		if s.currentRev != tt.wrev.Main {
			t.Errorf("#%d: rev = %+v, want %+v", i, s.currentRev, tt.wrev)
		}
		s.Close()
	}
}

func TestStoreCompact(t *testing.T) {
	lg := zaptest.NewLogger(t)
	s := newFakeStore(lg)
	defer s.Close()
	b := s.b.(*fakeBackend)
	fi := s.kvindex.(*fakeIndex)

	s.currentRev = 3
	fi.indexCompactRespc <- map[Revision]struct{}{{Main: 1}: {}}
	key1 := newTestRevBytes(Revision{Main: 1})
	key2 := newTestRevBytes(Revision{Main: 2})
	b.tx.rangeRespc <- rangeResp{[][]byte{}, [][]byte{}}
	b.tx.rangeRespc <- rangeResp{[][]byte{}, [][]byte{}}
	b.tx.rangeRespc <- rangeResp{[][]byte{key1, key2}, [][]byte{[]byte("alice"), []byte("bob")}}

	s.Compact(traceutil.TODO(), 3)
	s.fifoSched.WaitFinish(1)

	if s.compactMainRev != 3 {
		t.Errorf("compact main rev = %d, want 3", s.compactMainRev)
	}
	end := make([]byte, 8)
	binary.BigEndian.PutUint64(end, uint64(4))
	wact := []testutil.Action{
		{Name: "range", Params: []any{schema.Meta, schema.ScheduledCompactKeyName, []uint8(nil), int64(0)}},
		{Name: "range", Params: []any{schema.Meta, schema.FinishedCompactKeyName, []uint8(nil), int64(0)}},
		{Name: "put", Params: []any{schema.Meta, schema.ScheduledCompactKeyName, newTestRevBytes(Revision{Main: 3})}},
		{Name: "range", Params: []any{schema.Key, make([]byte, 17), end, int64(10000)}},
		{Name: "delete", Params: []any{schema.Key, key2}},
		{Name: "put", Params: []any{schema.Meta, schema.FinishedCompactKeyName, newTestRevBytes(Revision{Main: 3})}},
	}
	if g := b.tx.Action(); !reflect.DeepEqual(g, wact) {
		t.Errorf("tx actions = %+v, want %+v", g, wact)
	}
	wact = []testutil.Action{
		{Name: "compact", Params: []any{int64(3)}},
	}
	if g := fi.Action(); !reflect.DeepEqual(g, wact) {
		t.Errorf("index action = %+v, want %+v", g, wact)
	}
}

func TestStoreRestore(t *testing.T) {
	lg := zaptest.NewLogger(t)
	s := newFakeStore(lg)
	b := s.b.(*fakeBackend)
	fi := s.kvindex.(*fakeIndex)
	defer s.Close()

	putkey := newTestRevBytes(Revision{Main: 3})
	putkv := mvccpb.KeyValue{
		Key:            []byte("foo"),
		Value:          []byte("bar"),
		CreateRevision: 4,
		ModRevision:    4,
		Version:        1,
	}
	putkvb, err := proto.Marshal(&putkv)
	if err != nil {
		t.Fatal(err)
	}
	delkey := newTestBucketKeyBytes(newBucketKey(5, 0, true))
	delkv := mvccpb.KeyValue{
		Key: []byte("foo"),
	}
	delkvb, err := proto.Marshal(&delkv)
	if err != nil {
		t.Fatal(err)
	}
	b.tx.rangeRespc <- rangeResp{[][]byte{schema.FinishedCompactKeyName}, [][]byte{newTestRevBytes(Revision{Main: 3})}}
	b.tx.rangeRespc <- rangeResp{[][]byte{schema.ScheduledCompactKeyName}, [][]byte{newTestRevBytes(Revision{Main: 3})}}

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
		{Name: "range", Params: []any{schema.Meta, schema.FinishedCompactKeyName, []byte(nil), int64(0)}},
		{Name: "range", Params: []any{schema.Meta, schema.ScheduledCompactKeyName, []byte(nil), int64(0)}},
		{Name: "range", Params: []any{schema.Key, newTestRevBytes(Revision{Main: 1}), newTestRevBytes(Revision{Main: math.MaxInt64, Sub: math.MaxInt64}), int64(restoreChunkKeys)}},
	}
	if g := b.tx.Action(); !reflect.DeepEqual(g, wact) {
		t.Errorf("tx actions = %+v, want %+v", g, wact)
	}

	gens := []generation{
		{created: Revision{Main: 4}, ver: 2, revs: []revisionLease{{revision: Revision{Main: 3}, leaseID: lease.NoLease}, {revision: Revision{Main: 5}, leaseID: lease.NoLease}}},
		{created: Revision{Main: 0}, ver: 0, revs: nil},
	}
	ki := &keyIndex{key: []byte("foo"), modified: Revision{Main: 5}, generations: gens}
	wact = []testutil.Action{
		{Name: "keyIndex", Params: []any{ki}},
		{Name: "insert", Params: []any{ki}},
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
	s := NewStore(zaptest.NewLogger(t), b, &lease.FakeLessor{}, StoreConfig{})
	defer b.Close()

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

	s = NewStore(zaptest.NewLogger(t), b, &lease.FakeLessor{}, StoreConfig{})
	defer s.Close()
	for i := 0; i < 20; i++ {
		ks := fmt.Sprintf("foo-%d", i)
		r, err := s.Range(t.Context(), []byte(ks), nil, RangeOptions{})
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
		test := test

		t.Run(test, func(t *testing.T) {
			b, _ := betesting.NewDefaultTmpBackend(t)
			s0 := NewStore(zaptest.NewLogger(t), b, &lease.FakeLessor{}, StoreConfig{})

			s0.Put([]byte("foo"), []byte("bar"), lease.NoLease)
			s0.Put([]byte("foo"), []byte("bar1"), lease.NoLease)
			s0.Put([]byte("foo"), []byte("bar2"), lease.NoLease)

			// write scheduled compaction, but not do compaction
			tx := s0.b.BatchTx()
			tx.Lock()
			UnsafeSetScheduledCompact(tx, 2)
			tx.Unlock()

			var s *store
			switch test {
			case "recreate":
				s0.Close()
				s = NewStore(zaptest.NewLogger(t), b, &lease.FakeLessor{}, StoreConfig{})
			case "restore":
				// TODO(fuweid): store doesn't support to restore
				// from a closed status because there is no lock
				// for `Close` or action to mark it is closed.
				s0.Restore(b)
				s = s0
			}
			defer cleanup(s, b)

			// wait for scheduled compaction to be finished
			time.Sleep(100 * time.Millisecond)

			if _, err := s.Range(t.Context(), []byte("foo"), nil, RangeOptions{Rev: 1}); !errors.Is(err, ErrCompacted) {
				t.Errorf("range on compacted rev error = %v, want %v", err, ErrCompacted)
			}
			// check the key in backend is deleted
			revbytes := NewRevBytes()
			revbytes = BucketKeyToBytes(newBucketKey(1, 0, false), revbytes)

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
			t.Errorf("key for rev %+v still exists, want deleted", BytesToBucketKey(revbytes))
		})
	}
}

type hashKVResult struct {
	hash       uint32
	compactRev int64
}

// TestHashKVWhenCompacting ensures that HashKV returns correct hash when compacting.
func TestHashKVWhenCompacting(t *testing.T) {
	b, _ := betesting.NewDefaultTmpBackend(t)
	s := NewStore(zaptest.NewLogger(t), b, &lease.FakeLessor{}, StoreConfig{})
	defer cleanup(s, b)

	rev := 10000
	for i := 2; i <= rev; i++ {
		s.Put([]byte("foo"), []byte(fmt.Sprintf("bar%d", i)), lease.NoLease)
	}

	hashCompactc := make(chan hashKVResult, 1)
	var wg sync.WaitGroup
	donec := make(chan struct{})
	stopc := make(chan struct{})

	// Call HashByRev(10000) in multiple goroutines until donec is closed
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				hash, _, err := s.HashStorage().HashByRev(int64(rev))
				if err != nil {
					t.Error(err)
				}
				select {
				case <-stopc:
					return
				case <-donec:
					return
				case hashCompactc <- hashKVResult{hash.Hash, hash.CompactRevision}:
				}
			}
		}()
	}

	// Check computed hashes by HashByRev are correct in a goroutine, until donec is closed
	wg.Add(1)
	go func() {
		defer wg.Done()
		revHash := make(map[int64]uint32)
		for {
			select {
			case r := <-hashCompactc:
				if revHash[r.compactRev] == 0 {
					revHash[r.compactRev] = r.hash
				}

				if r.hash != revHash[r.compactRev] {
					t.Errorf("Hashes differ (current %v) != (saved %v)", r.hash, revHash[r.compactRev])
				}
			case <-stopc:
				return
			case <-donec:
				return
			}
		}
	}()

	// Compact the store in a goroutine, using RevisionTombstone 9900 to 10000 and close donec when finished
	wg.Add(1)
	go func() {
		defer func() {
			close(donec)
			wg.Done()
		}()

		for i := 100; i >= 0; i-- {
			select {
			case <-stopc:
				return
			default:
			}

			_, err := s.Compact(traceutil.TODO(), int64(rev-i))
			if err != nil {
				t.Error(err)
			}
			// Wait for the compaction job to finish
			s.fifoSched.WaitFinish(1)
			// Leave time for calls to HashByRev to take place after each compaction
			time.Sleep(10 * time.Millisecond)
		}
	}()

	select {
	case <-donec:
	case <-time.After(20 * time.Second):
		close(stopc)
		wg.Wait()
		testutil.FatalStack(t, "timeout")
	}

	close(stopc)
	wg.Wait()
}

// TestHashKVWithCompactedAndFutureRevisions ensures that HashKV returns a correct hash when called
// with a past RevisionTombstone (lower than compacted), a future RevisionTombstone, and the exact compacted RevisionTombstone
func TestHashKVWithCompactedAndFutureRevisions(t *testing.T) {
	b, _ := betesting.NewDefaultTmpBackend(t)
	s := NewStore(zaptest.NewLogger(t), b, &lease.FakeLessor{}, StoreConfig{})
	defer cleanup(s, b)

	rev := 10000
	compactRev := rev / 2

	for i := 2; i <= rev; i++ {
		s.Put([]byte("foo"), []byte(fmt.Sprintf("bar%d", i)), lease.NoLease)
	}
	if _, err := s.Compact(traceutil.TODO(), int64(compactRev)); err != nil {
		t.Fatal(err)
	}

	_, _, errFutureRev := s.HashStorage().HashByRev(int64(rev + 1))
	if !errors.Is(errFutureRev, ErrFutureRev) {
		t.Error(errFutureRev)
	}

	_, _, errPastRev := s.HashStorage().HashByRev(int64(compactRev - 1))
	if !errors.Is(errPastRev, ErrCompacted) {
		t.Error(errPastRev)
	}

	_, _, errCompactRev := s.HashStorage().HashByRev(int64(compactRev))
	if errCompactRev != nil {
		t.Error(errCompactRev)
	}
}

// TestHashKVZeroRevision ensures that "HashByRev(0)" computes
// correct hash value with latest RevisionTombstone.
func TestHashKVZeroRevision(t *testing.T) {
	b, _ := betesting.NewDefaultTmpBackend(t)
	s := NewStore(zaptest.NewLogger(t), b, &lease.FakeLessor{}, StoreConfig{})
	defer cleanup(s, b)

	rev := 10000
	for i := 2; i <= rev; i++ {
		s.Put([]byte("foo"), []byte(fmt.Sprintf("bar%d", i)), lease.NoLease)
	}
	if _, err := s.Compact(traceutil.TODO(), int64(rev/2)); err != nil {
		t.Fatal(err)
	}

	hash1, _, err := s.HashStorage().HashByRev(int64(rev))
	if err != nil {
		t.Fatal(err)
	}
	var hash2 KeyValueHash
	hash2, _, err = s.HashStorage().HashByRev(0)
	if err != nil {
		t.Fatal(err)
	}
	if hash1 != hash2 {
		t.Errorf("hash %d (rev %d) != hash %d (rev 0)", hash1, rev, hash2)
	}
}

// TestHashConcurrentWithRestore ensures that Hash does not race with Restore
// replacing the backend. The race detector reports a failure if it does.
func TestHashConcurrentWithRestore(t *testing.T) {
	b, _ := betesting.NewDefaultTmpBackend(t)
	s := NewStore(zaptest.NewLogger(t), b, &lease.FakeLessor{}, StoreConfig{})
	s.Put([]byte("foo"), []byte("bar"), lease.NoLease)

	newBackend, _ := betesting.NewDefaultTmpBackend(t)
	s2 := NewStore(zaptest.NewLogger(t), newBackend, &lease.FakeLessor{}, StoreConfig{})
	s2.Put([]byte("foo"), []byte("bar2"), lease.NoLease)
	s2.Close()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		if _, _, err := s.HashStorage().Hash(); err != nil {
			t.Error(err)
		}
	}()
	err := s.Restore(newBackend)
	wg.Wait()
	if err != nil {
		t.Fatal(err)
	}

	cleanup(s, newBackend)
	b.Close()
}

func TestTxnPut(t *testing.T) {
	// assign arbitrary size
	bytesN := 30
	sliceN := 100
	keys := createBytesSlice(bytesN, sliceN)
	vals := createBytesSlice(bytesN, sliceN)

	b, _ := betesting.NewDefaultTmpBackend(t)
	s := NewStore(zaptest.NewLogger(t), b, &lease.FakeLessor{}, StoreConfig{})
	defer cleanup(s, b)

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
	b, _ := betesting.NewDefaultTmpBackend(t)
	s := NewStore(zaptest.NewLogger(t), b, &lease.FakeLessor{}, StoreConfig{})
	defer cleanup(s, b)

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
	ro := RangeOptions{Limit: 1, Rev: 0, CountOnly: false}
	ret, err := readTx2.Range(t.Context(), []byte("foo"), nil, ro)
	if err != nil {
		t.Fatalf("failed to range: %v", err)
	}
	// readTx2 should see the result of new write
	w := &mvccpb.KeyValue{
		Key:            []byte("foo"),
		Value:          []byte("newBar"),
		CreateRevision: 2,
		ModRevision:    3,
		Version:        2,
	}
	if !cmp.Equal(ret.KVs[0], w, protocmp.Transform()) {
		t.Fatalf("range result = %+v, want = %+v", ret.KVs[0], w)
	}
	readTx2.End()

	ret, err = readTx1.Range(t.Context(), []byte("foo"), nil, ro)
	if err != nil {
		t.Fatalf("failed to range: %v", err)
	}
	// readTx1 should not see the result of new write
	w = &mvccpb.KeyValue{
		Key:            []byte("foo"),
		Value:          []byte("bar"),
		CreateRevision: 2,
		ModRevision:    2,
		Version:        1,
	}
	if !cmp.Equal(ret.KVs[0], w, protocmp.Transform()) {
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
	b, _ := betesting.NewDefaultTmpBackend(t)
	s := NewStore(zaptest.NewLogger(t), b, &lease.FakeLessor{}, StoreConfig{})
	defer cleanup(s, b)

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
			ret, err := tx.Range(t.Context(), []byte("\x00000000"), []byte("\xffffffff"), RangeOptions{})
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

func newTestRevBytes(rev Revision) []byte {
	bytes := NewRevBytes()
	return RevToBytes(rev, bytes)
}

func newTestBucketKeyBytes(rev BucketKey) []byte {
	bytes := NewRevBytes()
	return BucketKeyToBytes(rev, bytes)
}

func newFakeStore(lg *zap.Logger) *store {
	b := &fakeBackend{&fakeBatchTx{
		Recorder:   &testutil.RecorderBuffered{},
		rangeRespc: make(chan rangeResp, 5),
	}}
	s := &store{
		cfg: StoreConfig{
			CompactionBatchLimit:    10000,
			CompactionSleepInterval: defaultCompactionSleepInterval,
		},
		b:              b,
		le:             &lease.FakeLessor{},
		kvindex:        newFakeIndex(),
		currentRev:     0,
		compactMainRev: -1,
		fifoSched:      schedule.NewFIFOScheduler(lg),
		stopc:          make(chan struct{}),
		lg:             lg,
	}
	s.ReadView, s.WriteView = &readView{s}, &writeView{s}
	s.hashes = NewHashStorage(lg, s)
	return s
}

func newFakeIndex() *fakeIndex {
	return &fakeIndex{
		Recorder:              &testutil.RecorderBuffered{},
		indexGetRespc:         make(chan indexGetResp, 1),
		indexRangeRespc:       make(chan indexRangeResp, 1),
		indexRangeEventsRespc: make(chan indexRangeEventsResp, 1),
		indexCompactRespc:     make(chan map[Revision]struct{}, 1),
	}
}

type rangeResp struct {
	keys [][]byte
	vals [][]byte
}

type fakeBatchTx struct {
	testutil.Recorder
	rangeRespc chan rangeResp
}

func (b *fakeBatchTx) LockInsideApply()                         {}
func (b *fakeBatchTx) LockOutsideApply()                        {}
func (b *fakeBatchTx) Lock()                                    {}
func (b *fakeBatchTx) Unlock()                                  {}
func (b *fakeBatchTx) RLock()                                   {}
func (b *fakeBatchTx) RUnlock()                                 {}
func (b *fakeBatchTx) UnsafeCreateBucket(bucket backend.Bucket) {}
func (b *fakeBatchTx) UnsafeDeleteBucket(bucket backend.Bucket) {}
func (b *fakeBatchTx) UnsafePut(bucket backend.Bucket, key []byte, value []byte) {
	b.Recorder.Record(testutil.Action{Name: "put", Params: []any{bucket, key, value}})
}

func (b *fakeBatchTx) UnsafeSeqPut(bucket backend.Bucket, key []byte, value []byte) {
	b.Recorder.Record(testutil.Action{Name: "seqput", Params: []any{bucket, key, value}})
}

func (b *fakeBatchTx) UnsafeRange(bucket backend.Bucket, key, endKey []byte, limit int64) (keys [][]byte, vals [][]byte) {
	b.Recorder.Record(testutil.Action{Name: "range", Params: []any{bucket, key, endKey, limit}})
	r := <-b.rangeRespc
	return r.keys, r.vals
}

func (b *fakeBatchTx) UnsafeDelete(bucket backend.Bucket, key []byte) {
	b.Recorder.Record(testutil.Action{Name: "delete", Params: []any{bucket, key}})
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
func (b *fakeBackend) SetTxPostLockInsideApplyHook(func())                        {}
func (b *fakeBackend) LockForSafeRangeDelete()                                    {}
func (b *fakeBackend) UnlockForSafeRangeDelete()                                  {}

type indexGetResp struct {
	rev     Revision
	created Revision
	ver     int64
	err     error
}

type indexRangeResp struct {
	keys     [][]byte
	leases   []lease.LeaseID
	revs     []Revision
	creates  []Revision
	versions []int64
	total    int
}

type indexRangeEventsResp struct {
	revs []Revision
}

type fakeIndex struct {
	testutil.Recorder
	indexGetRespc         chan indexGetResp
	indexRangeRespc       chan indexRangeResp
	indexRangeEventsRespc chan indexRangeEventsResp
	indexCompactRespc     chan map[Revision]struct{}
}

func (i *fakeIndex) Revisions(key, end []byte, atRev int64, limit int, withTotalCount bool) ([]Revision, int) {
	_, _, rev, _, _, _ := i.Range(key, end, atRev, limit, withTotalCount)
	if len(rev) >= limit {
		rev = rev[:limit]
	}
	return rev, len(rev)
}

func (i *fakeIndex) CountRevisions(key, end []byte, atRev int64) int {
	_, _, rev, _, _, _ := i.Range(key, end, atRev, 0, true)
	return len(rev)
}

func (i *fakeIndex) Get(key []byte, atRev int64) (rev, created Revision, ver int64, err error) {
	i.Recorder.Record(testutil.Action{Name: "get", Params: []any{key, atRev}})
	r := <-i.indexGetRespc
	return r.rev, r.created, r.ver, r.err
}

func (i *fakeIndex) Range(key, end []byte, atRev int64, limit int, withTotalCount bool) (keys [][]byte, leases []lease.LeaseID, modifies, creates []Revision, versions []int64, total int) {
	i.Recorder.Record(testutil.Action{Name: "range", Params: []any{key, end, atRev}})
	r := <-i.indexRangeRespc
	return r.keys, r.leases, r.revs, r.creates, r.versions, r.total
}

func (i *fakeIndex) Put(key []byte, leaseID lease.LeaseID, rev Revision) {
	i.Recorder.Record(testutil.Action{Name: "put", Params: []any{key, leaseID, rev}})
}

func (i *fakeIndex) Tombstone(key []byte, rev Revision) error {
	i.Recorder.Record(testutil.Action{Name: "tombstone", Params: []any{key, rev}})
	return nil
}

func (i *fakeIndex) RangeSince(key, end []byte, rev int64) []Revision {
	i.Recorder.Record(testutil.Action{Name: "rangeEvents", Params: []any{key, end, rev}})
	r := <-i.indexRangeEventsRespc
	return r.revs
}

func (i *fakeIndex) Compact(rev int64) map[Revision]struct{} {
	i.Recorder.Record(testutil.Action{Name: "compact", Params: []any{rev}})
	return <-i.indexCompactRespc
}

func (i *fakeIndex) Keep(rev int64) map[Revision]struct{} {
	i.Recorder.Record(testutil.Action{Name: "keep", Params: []any{rev}})
	return <-i.indexCompactRespc
}
func (i *fakeIndex) Equal(b index) bool { return false }

func (i *fakeIndex) Insert(ki *keyIndex) {
	i.Recorder.Record(testutil.Action{Name: "insert", Params: []any{ki}})
}

func (i *fakeIndex) KeyIndex(ki *keyIndex) *keyIndex {
	i.Recorder.Record(testutil.Action{Name: "keyIndex", Params: []any{ki}})
	return nil
}

func createBytesSlice(bytesN, sliceN int) [][]byte {
	var rs [][]byte
	for len(rs) != sliceN {
		v := make([]byte, bytesN)
		if _, err := rand.Read(v); err != nil {
			panic(err)
		}
		rs = append(rs, v)
	}
	return rs
}
