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
	ti.Put([]byte("foo"), Revision{Main: 2}, 0)
	ti.Put([]byte("foo"), Revision{Main: 4}, 0)
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
		ti.Put(allKeys[i], allRevs[i], 0)
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
	ti.Put([]byte("foo"), Revision{Main: 1}, 2)

	sizeDelta, err := ti.Tombstone([]byte("foo"), Revision{Main: 2})
	if err != nil {
		t.Errorf("tombstone error = %v, want nil", err)
	}
	if sizeDelta != -5 {
		t.Errorf("tombstone size delta = %d, want -5", sizeDelta)
	}

	_, _, _, err = ti.Get([]byte("foo"), 2)
	if !errors.Is(err, ErrRevisionNotFound) {
		t.Errorf("get error = %v, want ErrRevisionNotFound", err)
	}
	sizeDelta, err = ti.Tombstone([]byte("foo"), Revision{Main: 3})
	if !errors.Is(err, ErrRevisionNotFound) {
		t.Errorf("tombstone error = %v, want %v", err, ErrRevisionNotFound)
	}
	if sizeDelta != 0 {
		t.Errorf("failed tombstone size delta = %d, want 0", sizeDelta)
	}
}

func TestIndexRevision(t *testing.T) {
	allKeys := [][]byte{[]byte("foo"), []byte("foo1"), []byte("foo2"), []byte("foo2"), []byte("foo1"), []byte("foo")}
	allRevs := []Revision{{Main: 1}, {Main: 2}, {Main: 3}, {Main: 4}, {Main: 5}, {Main: 6}}

	ti := newTreeIndex(zaptest.NewLogger(t))
	for i := range allKeys {
		ti.Put(allKeys[i], allRevs[i], 0)
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
		ti.Put(allKeys[i], allRevs[i], 0)
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

	// key: "foo"
	// modified: 10
	// generations:
	//	{{10, 0}}
	//	{{1, 0}, {5, 0}, {9, 0}(t)}
	//
	// key: "foo1"
	// modified: 10, 1
	// generations:
	//	{{10, 1}}
	//	{{2, 0}, {6, 0}, {7, 0}(t)}
	//
	// key: "foo2"
	// modified: 8
	// generations:
	//	{empty}
	//	{{3, 0}, {4, 0}, {8, 0}(t)}
	//
	buildTreeIndex := func() index {
		ti := newTreeIndex(zaptest.NewLogger(t))

		ti.Put([]byte("foo"), Revision{Main: 1}, 0)
		ti.Put([]byte("foo1"), Revision{Main: 2}, 0)
		ti.Put([]byte("foo2"), Revision{Main: 3}, 0)
		ti.Put([]byte("foo2"), Revision{Main: 4}, 0)
		ti.Put([]byte("foo"), Revision{Main: 5}, 0)
		ti.Put([]byte("foo1"), Revision{Main: 6}, 0)
		_, err := ti.Tombstone([]byte("foo1"), Revision{Main: 7})
		require.NoError(t, err)
		_, err = ti.Tombstone([]byte("foo2"), Revision{Main: 8})
		require.NoError(t, err)
		_, err = ti.Tombstone([]byte("foo"), Revision{Main: 9})
		require.NoError(t, err)
		ti.Put([]byte("foo"), Revision{Main: 10}, 0)
		ti.Put([]byte("foo1"), Revision{Main: 10, Sub: 1}, 0)
		return ti
	}

	afterCompacts := []struct {
		atRev      int
		keyIndexes []keyIndex
		keep       map[Revision]struct{}
		compacted  map[Revision]struct{}
	}{
		{
			atRev: 1,
			keyIndexes: []keyIndex{
				{
					key:      []byte("foo"),
					modified: Revision{Main: 10},
					generations: []generation{
						{ver: 3, created: Revision{Main: 1}, revs: []Revision{{Main: 1}, {Main: 5}, {Main: 9}}},
						{ver: 1, created: Revision{Main: 10}, revs: []Revision{{Main: 10}}},
					},
				},
				{
					key:      []byte("foo1"),
					modified: Revision{Main: 10, Sub: 1},
					generations: []generation{
						{ver: 3, created: Revision{Main: 2}, revs: []Revision{{Main: 2}, {Main: 6}, {Main: 7}}},
						{ver: 1, created: Revision{Main: 10, Sub: 1}, revs: []Revision{{Main: 10, Sub: 1}}},
					},
				},
				{
					key:      []byte("foo2"),
					modified: Revision{Main: 8},
					generations: []generation{
						{ver: 3, created: Revision{Main: 3}, revs: []Revision{{Main: 3}, {Main: 4}, {Main: 8}}},
						{},
					},
				},
			},
			keep: map[Revision]struct{}{
				{Main: 1}: {},
			},
			compacted: map[Revision]struct{}{
				{Main: 1}: {},
			},
		},
		{
			atRev: 2,
			keyIndexes: []keyIndex{
				{
					key:      []byte("foo"),
					modified: Revision{Main: 10},
					generations: []generation{
						{ver: 3, created: Revision{Main: 1}, revs: []Revision{{Main: 1}, {Main: 5}, {Main: 9}}},
						{ver: 1, created: Revision{Main: 10}, revs: []Revision{{Main: 10}}},
					},
				},
				{
					key:      []byte("foo1"),
					modified: Revision{Main: 10, Sub: 1},
					generations: []generation{
						{ver: 3, created: Revision{Main: 2}, revs: []Revision{{Main: 2}, {Main: 6}, {Main: 7}}},
						{ver: 1, created: Revision{Main: 10, Sub: 1}, revs: []Revision{{Main: 10, Sub: 1}}},
					},
				},
				{
					key:      []byte("foo2"),
					modified: Revision{Main: 8},
					generations: []generation{
						{ver: 3, created: Revision{Main: 3}, revs: []Revision{{Main: 3}, {Main: 4}, {Main: 8}}},
						{},
					},
				},
			},
			keep: map[Revision]struct{}{
				{Main: 1}: {},
				{Main: 2}: {},
			},
			compacted: map[Revision]struct{}{
				{Main: 1}: {},
				{Main: 2}: {},
			},
		},
		{
			atRev: 3,
			keyIndexes: []keyIndex{
				{
					key:      []byte("foo"),
					modified: Revision{Main: 10},
					generations: []generation{
						{ver: 3, created: Revision{Main: 1}, revs: []Revision{{Main: 1}, {Main: 5}, {Main: 9}}},
						{ver: 1, created: Revision{Main: 10}, revs: []Revision{{Main: 10}}},
					},
				},
				{
					key:      []byte("foo1"),
					modified: Revision{Main: 10, Sub: 1},
					generations: []generation{
						{ver: 3, created: Revision{Main: 2}, revs: []Revision{{Main: 2}, {Main: 6}, {Main: 7}}},
						{ver: 1, created: Revision{Main: 10, Sub: 1}, revs: []Revision{{Main: 10, Sub: 1}}},
					},
				},
				{
					key:      []byte("foo2"),
					modified: Revision{Main: 8},
					generations: []generation{
						{ver: 3, created: Revision{Main: 3}, revs: []Revision{{Main: 3}, {Main: 4}, {Main: 8}}},
						{},
					},
				},
			},
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
		{
			atRev: 4,
			keyIndexes: []keyIndex{
				{
					key:      []byte("foo"),
					modified: Revision{Main: 10},
					generations: []generation{
						{ver: 3, created: Revision{Main: 1}, revs: []Revision{{Main: 1}, {Main: 5}, {Main: 9}}},
						{ver: 1, created: Revision{Main: 10}, revs: []Revision{{Main: 10}}},
					},
				},
				{
					key:      []byte("foo1"),
					modified: Revision{Main: 10, Sub: 1},
					generations: []generation{
						{ver: 3, created: Revision{Main: 2}, revs: []Revision{{Main: 2}, {Main: 6}, {Main: 7}}},
						{ver: 1, created: Revision{Main: 10, Sub: 1}, revs: []Revision{{Main: 10, Sub: 1}}},
					},
				},
				{
					key:      []byte("foo2"),
					modified: Revision{Main: 8},
					generations: []generation{
						{ver: 3, created: Revision{Main: 3}, revs: []Revision{{Main: 4}, {Main: 8}}},
						{},
					},
				},
			},
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
		{
			atRev: 5,
			keyIndexes: []keyIndex{
				{
					key:      []byte("foo"),
					modified: Revision{Main: 10},
					generations: []generation{
						{ver: 3, created: Revision{Main: 1}, revs: []Revision{{Main: 5}, {Main: 9}}},
						{ver: 1, created: Revision{Main: 10}, revs: []Revision{{Main: 10}}},
					},
				},
				{
					key:      []byte("foo1"),
					modified: Revision{Main: 10, Sub: 1},
					generations: []generation{
						{ver: 3, created: Revision{Main: 2}, revs: []Revision{{Main: 2}, {Main: 6}, {Main: 7}}},
						{ver: 1, created: Revision{Main: 10, Sub: 1}, revs: []Revision{{Main: 10, Sub: 1}}},
					},
				},
				{
					key:      []byte("foo2"),
					modified: Revision{Main: 8},
					generations: []generation{
						{ver: 3, created: Revision{Main: 3}, revs: []Revision{{Main: 4}, {Main: 8}}},
						{},
					},
				},
			},
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
		{
			atRev: 6,
			keyIndexes: []keyIndex{
				{
					key:      []byte("foo"),
					modified: Revision{Main: 10},
					generations: []generation{
						{ver: 3, created: Revision{Main: 1}, revs: []Revision{{Main: 5}, {Main: 9}}},
						{ver: 1, created: Revision{Main: 10}, revs: []Revision{{Main: 10}}},
					},
				},
				{
					key:      []byte("foo1"),
					modified: Revision{Main: 10, Sub: 1},
					generations: []generation{
						{ver: 3, created: Revision{Main: 2}, revs: []Revision{{Main: 6}, {Main: 7}}},
						{ver: 1, created: Revision{Main: 10, Sub: 1}, revs: []Revision{{Main: 10, Sub: 1}}},
					},
				},
				{
					key:      []byte("foo2"),
					modified: Revision{Main: 8},
					generations: []generation{
						{ver: 3, created: Revision{Main: 3}, revs: []Revision{{Main: 4}, {Main: 8}}},
						{},
					},
				},
			},
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
		{
			atRev: 7,
			keyIndexes: []keyIndex{
				{
					key:      []byte("foo"),
					modified: Revision{Main: 10},
					generations: []generation{
						{ver: 3, created: Revision{Main: 1}, revs: []Revision{{Main: 5}, {Main: 9}}},
						{ver: 1, created: Revision{Main: 10}, revs: []Revision{{Main: 10}}},
					},
				},
				{
					key:      []byte("foo1"),
					modified: Revision{Main: 10, Sub: 1},
					generations: []generation{
						{ver: 3, created: Revision{Main: 2}, revs: []Revision{{Main: 7}}},
						{ver: 1, created: Revision{Main: 10, Sub: 1}, revs: []Revision{{Main: 10, Sub: 1}}},
					},
				},
				{
					key:      []byte("foo2"),
					modified: Revision{Main: 8},
					generations: []generation{
						{ver: 3, created: Revision{Main: 3}, revs: []Revision{{Main: 4}, {Main: 8}}},
						{},
					},
				},
			},
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
		{
			atRev: 8,
			keyIndexes: []keyIndex{
				{
					key:      []byte("foo"),
					modified: Revision{Main: 10},
					generations: []generation{
						{ver: 3, created: Revision{Main: 1}, revs: []Revision{{Main: 5}, {Main: 9}}},
						{ver: 1, created: Revision{Main: 10}, revs: []Revision{{Main: 10}}},
					},
				},
				{
					key:      []byte("foo1"),
					modified: Revision{Main: 10, Sub: 1},
					generations: []generation{
						{ver: 1, created: Revision{Main: 10, Sub: 1}, revs: []Revision{{Main: 10, Sub: 1}}},
					},
				},
				{
					key:      []byte("foo2"),
					modified: Revision{Main: 8},
					generations: []generation{
						{ver: 3, created: Revision{Main: 3}, revs: []Revision{{Main: 8}}},
						{},
					},
				},
			},
			keep: map[Revision]struct{}{
				{Main: 5}: {},
			},
			compacted: map[Revision]struct{}{
				{Main: 8}: {},
				{Main: 5}: {},
			},
		},
		{
			atRev: 9,
			keyIndexes: []keyIndex{
				{
					key:      []byte("foo"),
					modified: Revision{Main: 10},
					generations: []generation{
						{ver: 3, created: Revision{Main: 1}, revs: []Revision{{Main: 9}}},
						{ver: 1, created: Revision{Main: 10}, revs: []Revision{{Main: 10}}},
					},
				},
				{
					key:      []byte("foo1"),
					modified: Revision{Main: 10, Sub: 1},
					generations: []generation{
						{ver: 1, created: Revision{Main: 10, Sub: 1}, revs: []Revision{{Main: 10, Sub: 1}}},
					},
				},
			},
			keep: map[Revision]struct{}{},
			compacted: map[Revision]struct{}{
				{Main: 9}: {},
			},
		},
		{
			atRev: 10,
			keyIndexes: []keyIndex{
				{
					key:      []byte("foo"),
					modified: Revision{Main: 10},
					generations: []generation{
						{ver: 1, created: Revision{Main: 10}, revs: []Revision{{Main: 10}}},
					},
				},
				{
					key:      []byte("foo1"),
					modified: Revision{Main: 10, Sub: 1},
					generations: []generation{
						{ver: 1, created: Revision{Main: 10, Sub: 1}, revs: []Revision{{Main: 10, Sub: 1}}},
					},
				},
			},
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

		nti := newTreeIndex(zaptest.NewLogger(t)).(*treeIndex)
		for k := range afterCompacts[j].keyIndexes {
			ki := afterCompacts[j].keyIndexes[k]
			if !ki.generations[len(ki.generations)-1].isEmpty() {
				ki.liveSize = int64(len(ki.key))
			}
			nti.tree.ReplaceOrInsert(&ki)
		}
		require.Truef(t, ti.Equal(nti), "#%d: not equal ti", i)
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

		nti := newTreeIndex(zaptest.NewLogger(t)).(*treeIndex)
		for k := range afterCompacts[j].keyIndexes {
			ki := afterCompacts[j].keyIndexes[k]
			if !ki.generations[len(ki.generations)-1].isEmpty() {
				ki.liveSize = int64(len(ki.key))
			}
			nti.tree.ReplaceOrInsert(&ki)
		}

		require.Truef(t, ti.Equal(nti), "#%d: not equal ti", i)
	}
}
