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
	"errors"
	"reflect"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"
)

func TestIndexGet(t *testing.T) {
	ti := newTreeIndex(zaptest.NewLogger(t))
	ti.Put([]byte("foo"), Revision{Main: 2})
	ti.Put([]byte("foo"), Revision{Main: 4})
	ti.Tombstone([]byte("foo"), Revision{Main: 6})

	tests := []struct {
		rev int64

		wrev     Revision
		wcreated Revision
		wver     int64
		werr     error
	}{
		{0, Revision{}, Revision{}, 0, ErrRevisionNotFound},
		{1, Revision{}, Revision{}, 0, ErrRevisionNotFound},
		{2, Revision{Main: 2}, Revision{Main: 2}, 1, nil},
		{3, Revision{Main: 2}, Revision{Main: 2}, 1, nil},
		{4, Revision{Main: 4}, Revision{Main: 2}, 2, nil},
		{5, Revision{Main: 4}, Revision{Main: 2}, 2, nil},
		{6, Revision{}, Revision{}, 0, ErrRevisionNotFound},
	}
	for i, tt := range tests {
		rev, created, ver, err := ti.Get([]byte("foo"), tt.rev)
		if !errors.Is(err, tt.werr) {
			t.Errorf("#%d: err = %v, want %v", i, err, tt.werr)
		}
		if rev != tt.wrev {
			t.Errorf("#%d: rev = %+v, want %+v", i, rev, tt.wrev)
		}
		if created != tt.wcreated {
			t.Errorf("#%d: created = %+v, want %+v", i, created, tt.wcreated)
		}
		if ver != tt.wver {
			t.Errorf("#%d: ver = %d, want %d", i, ver, tt.wver)
		}
	}
}

func TestIndexRange(t *testing.T) {
	allKeys := [][]byte{[]byte("foo"), []byte("foo1"), []byte("foo2")}
	allRevs := []Revision{{Main: 1}, {Main: 2}, {Main: 3}}

	ti := newTreeIndex(zaptest.NewLogger(t))
	for i := range allKeys {
		ti.Put(allKeys[i], allRevs[i])
	}

	atRev := int64(3)
	tests := []struct {
		name                 string
		rangeStart, rangeEnd []byte
		rangeLimit           int
		withTotalCount       bool
		expectKeys           [][]byte
		expectRevisions      []Revision
		expectTotalCount     int
	}{
		{
			name:             "single key that not found",
			rangeStart:       []byte("bar"),
			rangeEnd:         nil,
			expectKeys:       nil,
			expectRevisions:  nil,
			expectTotalCount: 0,
			rangeLimit:       0,
			withTotalCount:   false,
		},
		{
			name:             "single key that found",
			rangeStart:       []byte("foo"),
			rangeEnd:         nil,
			expectKeys:       allKeys[:1],
			expectRevisions:  allRevs[:1],
			expectTotalCount: 1,
			rangeLimit:       0,
			withTotalCount:   false,
		},
		{
			name:             "range keys, return first member",
			rangeStart:       []byte("foo"),
			rangeEnd:         []byte("foo1"),
			expectKeys:       allKeys[:1],
			expectRevisions:  allRevs[:1],
			expectTotalCount: 1,
			rangeLimit:       0,
			withTotalCount:   false,
		},
		{
			name:             "range keys, return first two members",
			rangeStart:       []byte("foo"),
			rangeEnd:         []byte("foo2"),
			expectKeys:       allKeys[:2],
			expectRevisions:  allRevs[:2],
			expectTotalCount: 2,
			rangeLimit:       0,
			withTotalCount:   false,
		},
		{
			name:             "range keys, return all members",
			rangeStart:       []byte("foo"),
			rangeEnd:         []byte("fop"),
			expectKeys:       allKeys,
			expectRevisions:  allRevs,
			expectTotalCount: 3,
			rangeLimit:       0,
			withTotalCount:   false,
		},
		{
			name:             "range keys limiting to one result without total count, return the first member",
			rangeStart:       []byte("foo"),
			rangeEnd:         []byte("fop"),
			expectKeys:       allKeys[:1],
			expectRevisions:  allRevs[:1],
			expectTotalCount: 1,
			rangeLimit:       1,
			withTotalCount:   false,
		},
		{
			name:             "range keys with limit equal the index size without total count, return all members",
			rangeStart:       []byte("foo"),
			rangeEnd:         []byte("fop"),
			expectKeys:       allKeys,
			expectRevisions:  allRevs,
			expectTotalCount: 3,
			rangeLimit:       3,
			withTotalCount:   false,
		},
		{
			name:             "range keys with over limit, without total count, return all members",
			rangeStart:       []byte("foo"),
			rangeEnd:         []byte("fop"),
			expectKeys:       allKeys,
			expectRevisions:  allRevs,
			expectTotalCount: 3,
			rangeLimit:       4,
			withTotalCount:   false,
		},
		{
			name:             "range keys limiting to one result with total count, return the first member and all total count",
			rangeStart:       []byte("foo"),
			rangeEnd:         []byte("fop"),
			expectKeys:       allKeys[:1],
			expectRevisions:  allRevs[:1],
			expectTotalCount: 3,
			rangeLimit:       1,
			withTotalCount:   true,
		},
		{
			name:             "range keys with limit on non-existent keys, return nothing",
			rangeStart:       []byte("fo"),
			rangeEnd:         []byte("foo"),
			expectKeys:       nil,
			expectRevisions:  nil,
			expectTotalCount: 0,
			rangeLimit:       3,
			withTotalCount:   true,
		},
		{
			name:             "range keys with more limit than the one key found, returning the key",
			rangeStart:       []byte("foo"),
			rangeEnd:         []byte("foo1"),
			expectKeys:       allKeys[:1],
			expectRevisions:  allRevs[:1],
			expectTotalCount: 1,
			rangeLimit:       3,
			withTotalCount:   true,
		},
		{
			name:             "range of 1 keys with 1 limit and total count, returning 1 key with 1 count",
			rangeStart:       []byte("foo"),
			rangeEnd:         []byte("foo1"),
			expectKeys:       allKeys[:1],
			expectRevisions:  allRevs[:1],
			expectTotalCount: 1,
			rangeLimit:       1,
			withTotalCount:   true,
		},
		{
			name:             "range of 3 keys with 1 limit and total count, returning 1 key with 3 count",
			rangeStart:       []byte("foo"),
			rangeEnd:         []byte("foo3"),
			expectKeys:       allKeys[:1],
			expectRevisions:  allRevs[:1],
			expectTotalCount: 3,
			rangeLimit:       1,
			withTotalCount:   true,
		},
		{
			name:             "range of 2 keys not starting from the beginning with 1 limit and total count, returning 1 key with 2 count",
			rangeStart:       []byte("foo1"),
			rangeEnd:         []byte("foo3"),
			expectKeys:       allKeys[1:2],
			expectRevisions:  allRevs[1:2],
			expectTotalCount: 2,
			rangeLimit:       1,
			withTotalCount:   true,
		},
		{
			name:             "range of all keys with equal-sized limit and total count, returning all keys with the same count",
			rangeStart:       []byte("foo"),
			rangeEnd:         []byte("fop"),
			expectKeys:       allKeys,
			expectRevisions:  allRevs,
			expectTotalCount: 3,
			rangeLimit:       3,
			withTotalCount:   true,
		},
		{
			name:             "range keys, return last two members",
			rangeStart:       []byte("foo1"),
			rangeEnd:         []byte("fop"),
			expectKeys:       allKeys[1:],
			expectRevisions:  allRevs[1:],
			expectTotalCount: 2,
			rangeLimit:       0,
			withTotalCount:   false,
		},
		{
			name:             "range keys, return last member",
			rangeStart:       []byte("foo2"),
			rangeEnd:         []byte("fop"),
			expectKeys:       allKeys[2:],
			expectRevisions:  allRevs[2:],
			expectTotalCount: 1,
			rangeLimit:       0,
			withTotalCount:   false,
		},
		{
			name:             "range keys, return nothing",
			rangeStart:       []byte("foo3"),
			rangeEnd:         []byte("fop"),
			expectKeys:       nil,
			expectRevisions:  nil,
			expectTotalCount: 0,
			rangeLimit:       0,
			withTotalCount:   false,
		},
	}
	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			keys, revs, _, _, total := ti.Range(tt.rangeStart, tt.rangeEnd, atRev, tt.rangeLimit, tt.withTotalCount)
			if !reflect.DeepEqual(keys, tt.expectKeys) {
				t.Errorf("#%d: keys = %+v, want %+v", i, keys, tt.expectKeys)
			}
			if !reflect.DeepEqual(revs, tt.expectRevisions) {
				t.Errorf("#%d: revs = %+v, want %+v", i, revs, tt.expectRevisions)
			}
			if total != tt.expectTotalCount {
				t.Errorf("#%d: total = %+d, want %+d", i, total, tt.expectTotalCount)
			}
		})
	}
}

func TestIndexTombstone(t *testing.T) {
	ti := newTreeIndex(zaptest.NewLogger(t))
	ti.Put([]byte("foo"), Revision{Main: 1})

	err := ti.Tombstone([]byte("foo"), Revision{Main: 2})
	if err != nil {
		t.Errorf("tombstone error = %v, want nil", err)
	}

	_, _, _, err = ti.Get([]byte("foo"), 2)
	if !errors.Is(err, ErrRevisionNotFound) {
		t.Errorf("get error = %v, want ErrRevisionNotFound", err)
	}
	err = ti.Tombstone([]byte("foo"), Revision{Main: 3})
	if !errors.Is(err, ErrRevisionNotFound) {
		t.Errorf("tombstone error = %v, want %v", err, ErrRevisionNotFound)
	}
}

func TestIndexRevision(t *testing.T) {
	allKeys := [][]byte{[]byte("foo"), []byte("foo1"), []byte("foo2"), []byte("foo2"), []byte("foo1"), []byte("foo")}
	allRevs := []Revision{{Main: 1}, {Main: 2}, {Main: 3}, {Main: 4}, {Main: 5}, {Main: 6}}

	ti := newTreeIndex(zaptest.NewLogger(t))
	for i := range allKeys {
		ti.Put(allKeys[i], allRevs[i])
	}

	tests := []struct {
		key, end []byte
		atRev    int64
		limit    int
		wrevs    []Revision
		wcounts  int
	}{
		// single key that not found
		{
			[]byte("bar"), nil, 6, 0, nil, 0,
		},
		// single key that found
		{
			[]byte("foo"), nil, 6, 0, []Revision{{Main: 6}}, 1,
		},
		// various range keys, fixed atRev, unlimited
		{
			[]byte("foo"), []byte("foo1"), 6, 0, []Revision{{Main: 6}}, 1,
		},
		{
			[]byte("foo"), []byte("foo2"), 6, 0, []Revision{{Main: 6}, {Main: 5}}, 2,
		},
		{
			[]byte("foo"), []byte("fop"), 6, 0, []Revision{{Main: 6}, {Main: 5}, {Main: 4}}, 3,
		},
		{
			[]byte("foo1"), []byte("fop"), 6, 0, []Revision{{Main: 5}, {Main: 4}}, 2,
		},
		{
			[]byte("foo2"), []byte("fop"), 6, 0, []Revision{{Main: 4}}, 1,
		},
		{
			[]byte("foo3"), []byte("fop"), 6, 0, nil, 0,
		},
		// fixed range keys, various atRev, unlimited
		{
			[]byte("foo1"), []byte("fop"), 1, 0, nil, 0,
		},
		{
			[]byte("foo1"), []byte("fop"), 2, 0, []Revision{{Main: 2}}, 1,
		},
		{
			[]byte("foo1"), []byte("fop"), 3, 0, []Revision{{Main: 2}, {Main: 3}}, 2,
		},
		{
			[]byte("foo1"), []byte("fop"), 4, 0, []Revision{{Main: 2}, {Main: 4}}, 2,
		},
		{
			[]byte("foo1"), []byte("fop"), 5, 0, []Revision{{Main: 5}, {Main: 4}}, 2,
		},
		{
			[]byte("foo1"), []byte("fop"), 6, 0, []Revision{{Main: 5}, {Main: 4}}, 2,
		},
		// fixed range keys, fixed atRev, various limit
		{
			[]byte("foo"), []byte("fop"), 6, 1, []Revision{{Main: 6}}, 3,
		},
		{
			[]byte("foo"), []byte("fop"), 6, 2, []Revision{{Main: 6}, {Main: 5}}, 3,
		},
		{
			[]byte("foo"), []byte("fop"), 6, 3, []Revision{{Main: 6}, {Main: 5}, {Main: 4}}, 3,
		},
		{
			[]byte("foo"), []byte("fop"), 3, 1, []Revision{{Main: 1}}, 3,
		},
		{
			[]byte("foo"), []byte("fop"), 3, 2, []Revision{{Main: 1}, {Main: 2}}, 3,
		},
		{
			[]byte("foo"), []byte("fop"), 3, 3, []Revision{{Main: 1}, {Main: 2}, {Main: 3}}, 3,
		},
	}
	for i, tt := range tests {
		revs, _ := ti.Revisions(tt.key, tt.end, tt.atRev, tt.limit, true)
		if !reflect.DeepEqual(revs, tt.wrevs) {
			t.Errorf("#%d limit %d: revs = %+v, want %+v", i, tt.limit, revs, tt.wrevs)
		}
		count := ti.CountRevisions(tt.key, tt.end, tt.atRev)
		if count != tt.wcounts {
			t.Errorf("#%d: count = %d, want %v", i, count, tt.wcounts)
		}
	}
}

func TestIndexRevisionsWithTotalCount(t *testing.T) {
	allKeys := [][]byte{[]byte("foo"), []byte("foo1"), []byte("foo2"), []byte("foo2"), []byte("foo1"), []byte("foo")}
	allRevs := []Revision{{Main: 1}, {Main: 2}, {Main: 3}, {Main: 4}, {Main: 5}, {Main: 6}}

	ti := newTreeIndex(zaptest.NewLogger(t))
	for i := range allKeys {
		ti.Put(allKeys[i], allRevs[i])
	}

	// Range [foo, fop) @ rev 6 matches 3 keys: foo, foo1, foo2.
	tests := []struct {
		name           string
		key, end       []byte
		atRev          int64
		limit          int
		withTotalCount bool
		wrevs          []Revision
		wtotal         int
	}{
		{
			name:           "withTotalCount=true with limit returns the full total",
			key:            []byte("foo"),
			end:            []byte("fop"),
			atRev:          6,
			limit:          1,
			withTotalCount: true,
			wrevs:          []Revision{{Main: 6}},
			wtotal:         3,
		},
		{
			name:           "withTotalCount=false with limit caps total at len(revs)",
			key:            []byte("foo"),
			end:            []byte("fop"),
			atRev:          6,
			limit:          1,
			withTotalCount: false,
			wrevs:          []Revision{{Main: 6}},
			wtotal:         1,
		},
		{
			name:           "withTotalCount=false with limit larger than matches returns full total",
			key:            []byte("foo"),
			end:            []byte("fop"),
			atRev:          6,
			limit:          10,
			withTotalCount: false,
			wrevs:          []Revision{{Main: 6}, {Main: 5}, {Main: 4}},
			wtotal:         3,
		},
		{
			name:           "withTotalCount=false without limit returns full total",
			key:            []byte("foo"),
			end:            []byte("fop"),
			atRev:          6,
			limit:          0,
			withTotalCount: false,
			wrevs:          []Revision{{Main: 6}, {Main: 5}, {Main: 4}},
			wtotal:         3,
		},
		{
			name:           "withTotalCount=true without limit returns full total",
			key:            []byte("foo"),
			end:            []byte("fop"),
			atRev:          6,
			limit:          0,
			withTotalCount: true,
			wrevs:          []Revision{{Main: 6}, {Main: 5}, {Main: 4}},
			wtotal:         3,
		},
		{
			name:           "withTotalCount=false with no matches",
			key:            []byte("bar"),
			end:            []byte("baz"),
			atRev:          6,
			limit:          5,
			withTotalCount: false,
			wrevs:          nil,
			wtotal:         0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			revs, total := ti.Revisions(tt.key, tt.end, tt.atRev, tt.limit, tt.withTotalCount)
			if !reflect.DeepEqual(revs, tt.wrevs) {
				t.Errorf("revs = %+v, want %+v", revs, tt.wrevs)
			}
			if total != tt.wtotal {
				t.Errorf("total = %d, want %d", total, tt.wtotal)
			}
		})
	}
}

func TestIndexCompactAndKeep(t *testing.T) {
	maxRev := int64(20)

	// foo:  put@1, put@5, delete@9, put@10
	// foo1: put@2, put@6, delete@7, put@10.1
	// foo2: put@3, put@4, delete@8
	buildTreeIndex := func() index {
		ti := newTreeIndex(zaptest.NewLogger(t))

		ti.Put([]byte("foo"), Revision{Main: 1})
		ti.Put([]byte("foo1"), Revision{Main: 2})
		ti.Put([]byte("foo2"), Revision{Main: 3})
		ti.Put([]byte("foo2"), Revision{Main: 4})
		ti.Put([]byte("foo"), Revision{Main: 5})
		ti.Put([]byte("foo1"), Revision{Main: 6})
		require.NoError(t, ti.Tombstone([]byte("foo1"), Revision{Main: 7}))
		require.NoError(t, ti.Tombstone([]byte("foo2"), Revision{Main: 8}))
		require.NoError(t, ti.Tombstone([]byte("foo"), Revision{Main: 9}))
		ti.Put([]byte("foo"), Revision{Main: 10})
		ti.Put([]byte("foo1"), Revision{Main: 10, Sub: 1})
		return ti
	}

	// Compact and Keep report the revisions retained at or before atRev.
	// Compact includes a tombstone at atRev; Keep omits it for hash compatibility.
	afterCompacts := []struct {
		atRev     int
		keep      map[Revision]struct{}
		compacted map[Revision]struct{}
	}{
		// At revision 1, retain foo@1; foo1 and foo2 have no earlier versions.
		{
			atRev: 1,
			keep: map[Revision]struct{}{
				{Main: 1}: {},
			},
			compacted: map[Revision]struct{}{
				{Main: 1}: {},
			},
		},
		// At revision 2, retain foo@1 and foo1@2; foo2 has not been created yet.
		{
			atRev: 2,
			keep: map[Revision]struct{}{
				{Main: 1}: {},
				{Main: 2}: {},
			},
			compacted: map[Revision]struct{}{
				{Main: 1}: {},
				{Main: 2}: {},
			},
		},
		// At revision 3, retain each key's first version: foo@1, foo1@2, foo2@3.
		{
			atRev: 3,
			keep: map[Revision]struct{}{
				{Main: 1}: {},
				{Main: 2}: {},
				{Main: 3}: {},
			},
			compacted: map[Revision]struct{}{
				{Main: 1}: {},
				{Main: 2}: {},
				{Main: 3}: {},
			},
		},
		// At revision 4, foo2@4 replaces foo2@3; foo@1 and foo1@2 remain.
		{
			atRev: 4,
			keep: map[Revision]struct{}{
				{Main: 1}: {},
				{Main: 2}: {},
				{Main: 4}: {},
			},
			compacted: map[Revision]struct{}{
				{Main: 1}: {},
				{Main: 2}: {},
				{Main: 4}: {},
			},
		},
		// At revision 5, foo@5 replaces foo@1; retain foo1@2 and foo2@4.
		{
			atRev: 5,
			keep: map[Revision]struct{}{
				{Main: 2}: {},
				{Main: 4}: {},
				{Main: 5}: {},
			},
			compacted: map[Revision]struct{}{
				{Main: 2}: {},
				{Main: 4}: {},
				{Main: 5}: {},
			},
		},
		// At revision 6, foo1@6 replaces foo1@2; retain foo@5 and foo2@4.
		{
			atRev: 6,
			keep: map[Revision]struct{}{
				{Main: 6}: {},
				{Main: 4}: {},
				{Main: 5}: {},
			},
			compacted: map[Revision]struct{}{
				{Main: 6}: {},
				{Main: 4}: {},
				{Main: 5}: {},
			},
		},
		// At revision 7, foo1 is deleted. Compact retains its tombstone@7,
		// while Keep omits it. Both retain foo@5 and foo2@4.
		{
			atRev: 7,
			keep: map[Revision]struct{}{
				{Main: 4}: {},
				{Main: 5}: {},
			},
			compacted: map[Revision]struct{}{
				{Main: 7}: {},
				{Main: 4}: {},
				{Main: 5}: {},
			},
		},
		// At revision 8, foo1's tombstone@7 is removed. Compact retains foo@5
		// and foo2's tombstone@8; Keep retains only foo@5.
		{
			atRev: 8,
			keep: map[Revision]struct{}{
				{Main: 5}: {},
			},
			compacted: map[Revision]struct{}{
				{Main: 8}: {},
				{Main: 5}: {},
			},
		},
		// At revision 9, foo2's tombstone@8 is removed. Compact retains foo's
		// tombstone@9; Keep is empty because all three keys are deleted.
		{
			atRev: 9,
			keep:  map[Revision]struct{}{},
			compacted: map[Revision]struct{}{
				{Main: 9}: {},
			},
		},
		// At revision 10, retain the recreated foo@10 and foo1@10.1. Older
		// history and foo2 are removed, so Compact and Keep agree again.
		{
			atRev: 10,
			keep: map[Revision]struct{}{
				{Main: 10}:         {},
				{Main: 10, Sub: 1}: {},
			},
			compacted: map[Revision]struct{}{
				{Main: 10}:         {},
				{Main: 10, Sub: 1}: {},
			},
		},
	}

	// checkRevisions verifies the result of Compact(compactRev) by the index
	// interface only. We don't check the tree internals here, so the test
	// isn't tied to the treeIndex implementation.
	//
	// For this fixture, compaction retains at most one live revision per key at
	// or before compactRev. All other live revisions in that range are removed.
	// So, for any atRev <= compactRev, Revisions should only return the kept
	// revisions which are not greater than atRev. If it returns any other
	// revision, that revision wasn't removed by compaction.
	//
	// Note that querying at compactRev only can't catch this issue, because
	// the uncompacted index returns the same result at compactRev.
	checkRevisions := func(ti index, compactRev int64, kept map[Revision]struct{}) {
		t.Helper()
		for atRev := int64(1); atRev <= compactRev; atRev++ {
			want := make([]Revision, 0, len(kept))
			for rev := range kept {
				if rev.Main <= atRev {
					want = append(want, rev)
				}
			}
			got, total := ti.Revisions([]byte("foo"), []byte("fop"), atRev, 0, true)
			require.ElementsMatchf(t, want, got, "revisions at %d after compact(%d)", atRev, compactRev)
			require.Equalf(t, len(want), total, "total at %d after compact(%d)", atRev, compactRev)
		}
	}

	ti := buildTreeIndex()
	// Continuous Compact and Keep
	for i := int64(1); i < maxRev; i++ {
		j := i - 1
		if i >= int64(len(afterCompacts)) {
			j = int64(len(afterCompacts)) - 1
		}

		am := ti.Compact(i)
		require.Equalf(t, afterCompacts[j].compacted, am, "#%d: compact(%d) != expected", i, i)

		keep := ti.Keep(i)
		require.Equalf(t, afterCompacts[j].keep, keep, "#%d: keep(%d) != expected", i, i)

		checkRevisions(ti, i, afterCompacts[j].keep)
	}

	// Once Compact and Keep
	for i := int64(1); i < maxRev; i++ {
		ti := buildTreeIndex()

		j := i - 1
		if i >= int64(len(afterCompacts)) {
			j = int64(len(afterCompacts)) - 1
		}

		am := ti.Compact(i)
		require.Equalf(t, afterCompacts[j].compacted, am, "#%d: compact(%d) != expected", i, i)

		keep := ti.Keep(i)
		require.Equalf(t, afterCompacts[j].keep, keep, "#%d: keep(%d) != expected", i, i)

		checkRevisions(ti, i, afterCompacts[j].keep)
	}
}
