// Copyright 2023 The etcd Authors
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

package backend

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
)

func Test_bucketBuffer_CopyUsed_After_Add(t *testing.T) {
	bb := &bucketBuffer{buf: make([]kv, 10), used: 0}
	for i := 0; i < 20; i++ {
		k := fmt.Sprintf("key%d", i)
		v := fmt.Sprintf("val%d", i)
		bb.add([]byte(k), []byte(v))
		bbCopy := bb.CopyUsed()
		assert.Equal(t, bb.used, bbCopy.used)
		assert.Len(t, bbCopy.buf, bbCopy.used)
		assert.GreaterOrEqual(t, len(bb.buf), len(bbCopy.buf))
	}
}

func Test_bucketBuffer_CopyUsed(t *testing.T) {
	tests := []struct {
		name       string
		bufLen     int
		used       int
		wantPanic  bool
		wantUsed   int
		wantBufLen int
	}{
		{
			name:       "used is 0",
			bufLen:     10,
			used:       0,
			wantPanic:  false,
			wantUsed:   0,
			wantBufLen: 0,
		},
		{
			name:       "used is greater than 0 and less than len(buf)",
			bufLen:     10,
			used:       5,
			wantPanic:  false,
			wantUsed:   5,
			wantBufLen: 5,
		},
		{
			name:       "used is equal to len(buf)",
			bufLen:     10,
			used:       10,
			wantPanic:  false,
			wantUsed:   10,
			wantBufLen: 10,
		},
		{
			name:      "used is greater than len(buf)",
			bufLen:    10,
			used:      11,
			wantPanic: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bb := &bucketBuffer{buf: make([]kv, tt.bufLen), used: tt.used}
			if tt.wantPanic {
				assert.Panicsf(t, func() {
					bb.CopyUsed()
				}, "expected panic when used (%d) and the length of buf (%d)", tt.used, tt.bufLen)
			} else {
				bbCopy := bb.CopyUsed()
				assert.Equal(t, tt.wantUsed, bbCopy.used)
				assert.Len(t, bbCopy.buf, tt.wantBufLen)
			}
		})
	}
}

func TestDedupe(t *testing.T) {
	tests := []struct {
		name                                   string
		keys, vals, expectedKeys, expectedVals []string
	}{
		{
			name:         "empty",
			keys:         []string{},
			vals:         []string{},
			expectedKeys: []string{},
			expectedVals: []string{},
		},
		{
			name:         "single kv",
			keys:         []string{"key1"},
			vals:         []string{"val1"},
			expectedKeys: []string{"key1"},
			expectedVals: []string{"val1"},
		},
		{
			name:         "duplicate key",
			keys:         []string{"key1", "key1"},
			vals:         []string{"val1", "val2"},
			expectedKeys: []string{"key1"},
			expectedVals: []string{"val2"},
		},
		{
			name:         "unordered keys",
			keys:         []string{"key3", "key1", "key4", "key2", "key1", "key4"},
			vals:         []string{"val1", "val5", "val3", "val4", "val2", "val6"},
			expectedKeys: []string{"key1", "key2", "key3", "key4"},
			expectedVals: []string{"val2", "val4", "val1", "val6"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bb := &bucketBuffer{buf: make([]kv, 10), used: 0}
			for i := 0; i < len(tt.keys); i++ {
				bb.add([]byte(tt.keys[i]), []byte(tt.vals[i]))
			}
			bb.dedupe()
			assert.Len(t, tt.expectedKeys, bb.used)
			for i := 0; i < bb.used; i++ {
				assert.Equal(t, bb.buf[i].key, []byte(tt.expectedKeys[i]))
				assert.Equal(t, bb.buf[i].val, []byte(tt.expectedVals[i]))
			}
		})
	}
}

// TestWritebackDedupesExistingBucket checks that writeback does not leak
// duplicate keys into the read buffer when the bucket is already present
// there. bucketBuffer.merge only dedupes when the two buffers overlap, so a
// write buffer holding the same key twice would otherwise survive, and
// bucketBuffer.Range would return the older of the two values.
func TestWritebackDedupesExistingBucket(t *testing.T) {
	tests := []struct {
		name     string
		seedKey  string
		wantVals []string
	}{
		{
			// read buffer max key sorts below the write buffer min key, so
			// merge takes its no-overlap fast path and skips dedupe.
			name:     "no overlap between buffers",
			seedKey:  "aaa",
			wantVals: []string{"new"},
		},
		{
			name:     "overlapping buffers",
			seedKey:  "zzzz",
			wantVals: []string{"new"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			const bucketID = BucketID(1)

			seed := newBucketBuffer()
			seed.add([]byte(tt.seedKey), []byte("seed"))
			txr := &txReadBuffer{txBuffer: txBuffer{buckets: map[BucketID]*bucketBuffer{bucketID: seed}}}

			wb := newBucketBuffer()
			wb.add([]byte("zzz"), []byte("old"))
			wb.add([]byte("zzz"), []byte("new"))
			txw := &txWriteBuffer{
				txBuffer:   txBuffer{buckets: map[BucketID]*bucketBuffer{bucketID: wb}},
				bucket2seq: map[BucketID]bool{bucketID: false},
			}

			txw.writeback(txr)

			keys, vals := txr.buckets[bucketID].Range([]byte("zzz"), []byte("zzz\x00"), 10)
			assert.Lenf(t, keys, len(tt.wantVals), "duplicate key survived writeback")
			var got []string
			for _, v := range vals {
				got = append(got, string(v))
			}
			assert.Equalf(t, tt.wantVals, got, "writeback must keep the newest value")
		})
	}
}
