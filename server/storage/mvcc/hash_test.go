// Copyright 2022 The etcd Authors
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
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"

	"go.etcd.io/etcd/pkg/v3/traceutil"
	"go.etcd.io/etcd/server/v3/lease"
	betesting "go.etcd.io/etcd/server/v3/storage/backend/testing"
	"go.etcd.io/etcd/server/v3/storage/mvcc/testutil"
)

// TestHashByRevValue test HashByRevValue values to ensure we don't change the
// output which would have catastrophic consequences. Expected output is just
// hardcoded, so please regenerate it every time you change input parameters.
func TestHashByRevValue(t *testing.T) {
	b, _ := betesting.NewDefaultTmpBackend(t)
	s := NewStore(zaptest.NewLogger(t), b, &lease.FakeLessor{}, StoreConfig{})
	defer cleanup(s, b)

	var totalRevisions int64 = 1210
	assert.Less(t, int64(s.cfg.CompactionBatchLimit), totalRevisions)
	assert.Less(t, int64(testutil.CompactionCycle*10), totalRevisions)
	var rev int64
	var got []KeyValueHash
	for ; rev < totalRevisions; rev += testutil.CompactionCycle {
		putKVs(s, rev, testutil.CompactionCycle)
		hash := testHashByRev(t, s, rev+testutil.CompactionCycle/2)
		got = append(got, hash)
	}
	putKVs(s, rev, totalRevisions)
	hash := testHashByRev(t, s, rev+totalRevisions/2)
	got = append(got, hash)
	assert.Equal(t, []KeyValueHash{
		{4082599214, -1, 35},
		{2279933401, 35, 106},
		{3284231217, 106, 177},
		{126286495, 177, 248},
		{900108730, 248, 319},
		{2475485232, 319, 390},
		{1226296507, 390, 461},
		{2503661030, 461, 532},
		{4155130747, 532, 603},
		{106915399, 603, 674},
		{406914006, 674, 745},
		{1882211381, 745, 816},
		{806177088, 816, 887},
		{664311366, 887, 958},
		{1496914449, 958, 1029},
		{2434525091, 1029, 1100},
		{3988652253, 1100, 1171},
		{1122462288, 1171, 1242},
		{724436716, 1242, 1883},
	}, got)
}

func TestHashByRevValueLastRevision(t *testing.T) {
	b, _ := betesting.NewDefaultTmpBackend(t)
	s := NewStore(zaptest.NewLogger(t), b, &lease.FakeLessor{}, StoreConfig{})
	defer cleanup(s, b)

	var totalRevisions int64 = 1210
	assert.Less(t, int64(s.cfg.CompactionBatchLimit), totalRevisions)
	assert.Less(t, int64(testutil.CompactionCycle*10), totalRevisions)
	var rev int64
	var got []KeyValueHash
	for ; rev < totalRevisions; rev += testutil.CompactionCycle {
		putKVs(s, rev, testutil.CompactionCycle)
		hash := testHashByRev(t, s, 0)
		got = append(got, hash)
	}
	putKVs(s, rev, totalRevisions)
	hash := testHashByRev(t, s, 0)
	got = append(got, hash)
	assert.Equal(t, []KeyValueHash{
		{1913897190, -1, 73},
		{224860069, 73, 145},
		{1565167519, 145, 217},
		{1566261620, 217, 289},
		{2037173024, 289, 361},
		{691659396, 361, 433},
		{2713730748, 433, 505},
		{3919322507, 505, 577},
		{769967540, 577, 649},
		{2909194793, 649, 721},
		{1576921157, 721, 793},
		{4067701532, 793, 865},
		{2226384237, 865, 937},
		{2923408134, 937, 1009},
		{2680329256, 1009, 1081},
		{1546717673, 1081, 1153},
		{2713657846, 1153, 1225},
		{1046575299, 1225, 1297},
		{2017735779, 1297, 2508},
	}, got)
}

func putKVs(s *store, rev, count int64) {
	for i := rev; i <= rev+count; i++ {
		s.Put([]byte(testutil.PickKey(i)), []byte(fmt.Sprint(i)), 0)
	}
}

func testHashByRev(t *testing.T, s *store, rev int64) KeyValueHash {
	if rev == 0 {
		rev = s.Rev()
	}
	hash, _, err := s.hashByRev(rev)
	require.NoErrorf(t, err, "error on rev %v", rev)
	_, err = s.Compact(traceutil.TODO(), rev)
	assert.NoErrorf(t, err, "error on compact %v", rev)
	return hash
}

// TestCompactionHash tests compaction hash
// TODO: Change this to fuzz test
func TestCompactionHash(t *testing.T) {
	b, _ := betesting.NewDefaultTmpBackend(t)
	s := NewStore(zaptest.NewLogger(t), b, &lease.FakeLessor{}, StoreConfig{})
	defer cleanup(s, b)

	testutil.TestCompactionHash(t.Context(), t, hashTestCase{s}, s.cfg.CompactionBatchLimit)
}

// HashKV at a revision must not depend on whether the physical compaction to
// that revision has run: members compare it across the cluster, and a member
// may still be deleting compacted revisions when it answers. After the
// compaction finishes, the hash must not change from the hash that earlier
// versions return, so that members of mixed versions agree.
func TestHashByRevAcrossPhysicalCompaction(t *testing.T) {
	for _, tc := range []struct {
		name string
		// setup writes the keys and returns the revision to compact.
		setup func(s *store) int64
		// after runs after the compaction finishes.
		after func(s *store)
		// want is the hash that versions before this test return after the
		// compaction finishes.
		want KeyValueHash
	}{
		{
			name: "every key deleted at the compact revision",
			setup: func(s *store) int64 {
				putKeys(s, "k", 20)
				_, rev := s.DeleteRange([]byte("k"), []byte("l"))
				return rev
			},
			want: KeyValueHash{Hash: 0x40a4756d, CompactRevision: 22, Revision: 22},
		},
		{
			name: "one key not deleted",
			setup: func(s *store) int64 {
				putKeys(s, "k", 20)
				s.Put([]byte("survivor"), []byte("v"), lease.NoLease)
				_, rev := s.DeleteRange([]byte("k"), []byte("l"))
				return rev
			},
			want: KeyValueHash{Hash: 0x145acb52, CompactRevision: 23, Revision: 23},
		},
		{
			name: "every key deleted after the compact revision",
			setup: func(s *store) int64 {
				putKeys(s, "k", 20)
				putKeys(s, "k", 20)
				return s.Rev()
			},
			after: func(s *store) {
				s.DeleteRange([]byte("k"), []byte("l"))
			},
			want: KeyValueHash{Hash: 0x4803ce7, CompactRevision: 41, Revision: 42},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b, _ := betesting.NewDefaultTmpBackend(t)
			s := NewStore(zaptest.NewLogger(t), b, &lease.FakeLessor{}, StoreConfig{})
			defer cleanup(s, b)
			rev := tc.setup(s)

			// Record the compaction, but do not delete revisions yet. A member
			// is in this state until the scheduled compaction finishes.
			_, prev, err := s.updateCompactRev(rev)
			require.NoError(t, err)
			s.kvindex.Compact(rev)
			if tc.after != nil {
				tc.after(s)
			}
			before, _, err := s.hashByRev(0)
			require.NoError(t, err)

			_, err = s.scheduleCompaction(rev, prev)
			require.NoError(t, err)
			after, _, err := s.hashByRev(0)
			require.NoError(t, err)

			assert.Equal(t, tc.want, after, "hash after the compaction")
			assert.Equal(t, after, before, "hash during the compaction")
		})
	}
}

func putKeys(s *store, prefix string, n int) {
	for i := range n {
		s.Put([]byte(fmt.Sprint(prefix, i)), []byte(fmt.Sprint(i)), lease.NoLease)
	}
}

type hashTestCase struct {
	*store
}

func (tc hashTestCase) Put(ctx context.Context, key, value string) error {
	tc.store.Put([]byte(key), []byte(value), 0)
	return nil
}

func (tc hashTestCase) Delete(ctx context.Context, key string) error {
	tc.store.DeleteRange([]byte(key), nil)
	return nil
}

func (tc hashTestCase) HashByRev(ctx context.Context, rev int64) (testutil.KeyValueHash, error) {
	hash, _, err := tc.store.HashStorage().HashByRev(rev)
	return testutil.KeyValueHash{Hash: hash.Hash, CompactRevision: hash.CompactRevision, Revision: hash.Revision}, err
}

func (tc hashTestCase) Defrag(ctx context.Context) error {
	return tc.store.b.Defrag()
}

func (tc hashTestCase) Compact(ctx context.Context, rev int64) error {
	done, err := tc.store.Compact(traceutil.TODO(), rev)
	if err != nil {
		return err
	}
	select {
	case <-done:
	case <-ctx.Done():
		return ctx.Err()
	}
	return nil
}

func TestHasherStore(t *testing.T) {
	lg := zaptest.NewLogger(t)
	store := newFakeStore(lg)
	s := NewHashStorage(lg, store)
	defer store.Close()

	var hashes []KeyValueHash
	for i := 0; i < hashStorageMaxSize; i++ {
		hash := KeyValueHash{Hash: uint32(i), Revision: int64(i) + 10, CompactRevision: int64(i) + 100}
		hashes = append(hashes, hash)
		s.Store(hash)
	}

	for _, want := range hashes {
		got, _, err := s.HashByRev(want.Revision)
		if err != nil {
			t.Fatal(err)
		}
		if want.Hash != got.Hash {
			t.Errorf("Expected stored hash to match, got: %d, expected: %d", want.Hash, got.Hash)
		}
		if want.Revision != got.Revision {
			t.Errorf("Expected stored revision to match, got: %d, expected: %d", want.Revision, got.Revision)
		}
		if want.CompactRevision != got.CompactRevision {
			t.Errorf("Expected stored compact revision to match, got: %d, expected: %d", want.CompactRevision, got.CompactRevision)
		}
	}
}

func TestHasherStoreFull(t *testing.T) {
	lg := zaptest.NewLogger(t)
	store := newFakeStore(lg)
	s := NewHashStorage(lg, store)
	defer store.Close()

	var minRevision int64 = 100
	maxRevision := minRevision + hashStorageMaxSize
	for i := 0; i < hashStorageMaxSize; i++ {
		s.Store(KeyValueHash{Revision: int64(i) + minRevision})
	}

	// Hash for old revision should be discarded as storage is already full
	s.Store(KeyValueHash{Revision: minRevision - 1})
	hash, _, err := s.HashByRev(minRevision - 1)
	if err == nil {
		t.Errorf("Expected an error as old revision should be discarded, got: %v", hash)
	}
	// Hash for new revision should be stored even when storage is full
	s.Store(KeyValueHash{Revision: maxRevision + 1})
	_, _, err = s.HashByRev(maxRevision + 1)
	if err != nil {
		t.Errorf("Didn't expect error for new revision, err: %v", err)
	}
}
