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
	"context"
	"os"
	"reflect"
	"testing"
	"time"

	"go.etcd.io/etcd/pkg/v3/traceutil"
	"go.etcd.io/etcd/server/v3/lease"
	betesting "go.etcd.io/etcd/server/v3/mvcc/backend/testing"
	"go.etcd.io/etcd/server/v3/mvcc/buckets"
	"go.uber.org/zap"
)

func TestScheduleCompaction(t *testing.T) {
	revs := []revision{{1, 0}, {2, 0}, {3, 0}}

	tests := []struct {
		rev   int64
		keep  map[revision]struct{}
		wrevs []revision
	}{
		// compact at 1 and discard all history
		{
			1,
			nil,
			revs[1:],
		},
		// compact at 3 and discard all history
		{
			3,
			nil,
			nil,
		},
		// compact at 1 and keeps history one step earlier
		{
			1,
			map[revision]struct{}{
				{main: 1}: {},
			},
			revs,
		},
		// compact at 1 and keeps history two steps earlier
		{
			3,
			map[revision]struct{}{
				{main: 2}: {},
				{main: 3}: {},
			},
			revs[1:],
		},
	}
	for i, tt := range tests {
		b, tmpPath := betesting.NewDefaultTmpBackend(t)
		s := NewStore(zap.NewExample(), b, &lease.FakeLessor{}, StoreConfig{})
		tx := s.b.BatchTx()

		tx.Lock()
		ibytes := newRevBytes()
		for _, rev := range revs {
			revToBytes(rev, ibytes)
			tx.UnsafePut(buckets.Key, ibytes, []byte("bar"))
		}
		tx.Unlock()

		s.scheduleCompaction(tt.rev, tt.keep)

		tx.Lock()
		for _, rev := range tt.wrevs {
			revToBytes(rev, ibytes)
			keys, _ := tx.UnsafeRange(buckets.Key, ibytes, nil, 0)
			if len(keys) != 1 {
				t.Errorf("#%d: range on %v = %d, want 1", i, rev, len(keys))
			}
		}
		vals, _ := UnsafeReadFinishedCompact(tx)
		if !reflect.DeepEqual(vals, tt.rev) {
			t.Errorf("#%d: finished compact equal %+v, want %+v", i, vals, tt.rev)
		}
		tx.Unlock()

		cleanup(s, b, tmpPath)
	}
}

func TestCompactAllAndRestore(t *testing.T) {
	b, tmpPath := betesting.NewDefaultTmpBackend(t)
	s0 := NewStore(zap.NewExample(), b, &lease.FakeLessor{}, StoreConfig{})
	defer os.Remove(tmpPath)

	s0.Put([]byte("foo"), []byte("bar"), lease.NoLease)
	s0.Put([]byte("foo"), []byte("bar1"), lease.NoLease)
	s0.Put([]byte("foo"), []byte("bar2"), lease.NoLease)
	s0.DeleteRange([]byte("foo"), nil)

	rev := s0.Rev()
	// compact all keys
	done, err := s0.Compact(traceutil.TODO(), rev)
	if err != nil {
		t.Fatal(err)
	}

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("timeout waiting for compaction to finish")
	}

	err = s0.Close()
	if err != nil {
		t.Fatal(err)
	}

	s1 := NewStore(zap.NewExample(), b, &lease.FakeLessor{}, StoreConfig{})
	if s1.Rev() != rev {
		t.Errorf("rev = %v, want %v", s1.Rev(), rev)
	}
	_, err = s1.Range(context.TODO(), []byte("foo"), nil, RangeOptions{})
	if err != nil {
		t.Errorf("unexpect range error %v", err)
	}
}
