// Copyright 2025 The etcd Authors
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

package txn

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"go.uber.org/zap/zaptest"

	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	"go.etcd.io/etcd/server/v3/lease"
	betesting "go.etcd.io/etcd/server/v3/storage/backend/testing"
	"go.etcd.io/etcd/server/v3/storage/mvcc"
)

func TestRangeWithSortAndLimit(t *testing.T) {
	s, _ := setup(t, testSetup{})
	ctx := context.Background()
	lg := zaptest.NewLogger(t)

	// Put some keys
	numKeys := 10
	for i := 0; i < numKeys; i++ {
		s.Put([]byte(fmt.Sprintf("key%02d", i)), []byte(fmt.Sprintf("val%02d", i)), 0)
	}

	tests := []struct {
		name       string
		req        *pb.RangeRequest
		wantKeys   []string
		wantMore   bool
		wantCount  int64
	}{
		{
			name: "No sort, no limit",
			req: &pb.RangeRequest{
				Key:      []byte("key00"),
				RangeEnd: []byte("key10"),
			},
			wantKeys:  []string{"key00", "key01", "key02", "key03", "key04", "key05", "key06", "key07", "key08", "key09"},
			wantMore:  false,
			wantCount: 10,
		},
		{
			name: "No sort, limit 5",
			req: &pb.RangeRequest{
				Key:      []byte("key00"),
				RangeEnd: []byte("key10"),
				Limit:    5,
			},
			wantKeys:  []string{"key00", "key01", "key02", "key03", "key04"},
			wantMore:  true,
			wantCount: 10,
		},
		{
			name: "Key ascend, limit 5 (optimized path)",
			req: &pb.RangeRequest{
				Key:        []byte("key00"),
				RangeEnd:   []byte("key10"),
				Limit:      5,
				SortTarget: pb.RangeRequest_KEY,
				SortOrder:  pb.RangeRequest_ASCEND,
			},
			wantKeys:  []string{"key00", "key01", "key02", "key03", "key04"},
			wantMore:  true,
			wantCount: 10,
		},
		{
			name: "Key descend, limit 5",
			req: &pb.RangeRequest{
				Key:        []byte("key00"),
				RangeEnd:   []byte("key10"),
				Limit:      5,
				SortTarget: pb.RangeRequest_KEY,
				SortOrder:  pb.RangeRequest_DESCEND,
			},
			wantKeys:  []string{"key09", "key08", "key07", "key06", "key05"},
			wantMore:  true,
			wantCount: 10,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp, _, err := Range(ctx, lg, s, tt.req)
			assert.NoError(t, err)
			
			var gotKeys []string
			for _, kv := range resp.Kvs {
				gotKeys = append(gotKeys, string(kv.Key))
			}
			assert.Equalf(t, tt.wantKeys, gotKeys, "returned keys mismatch")
			assert.Equalf(t, tt.wantMore, resp.More, "More flag mismatch")
			assert.Equalf(t, tt.wantCount, resp.Count, "Count mismatch")
		})
	}
}

func TestRangeKeyAscendOptimizationCorrectness(t *testing.T) {
	s, _ := setup(t, testSetup{})
	ctx := context.Background()
	lg := zaptest.NewLogger(t)

	// Put some keys
	numKeys := 100
	for i := 0; i < numKeys; i++ {
		s.Put([]byte(fmt.Sprintf("key%03d", i)), []byte(fmt.Sprintf("val%03d", i)), 0)
	}

	limit := int64(10)
	
	// Request without explicit sort (default is key ascend)
	reqNoSort := &pb.RangeRequest{
		Key:      []byte("key000"),
		RangeEnd: []byte("key999"),
		Limit:    limit,
	}
	
	// Request with explicit key ascend sort (optimized path)
	reqAscend := &pb.RangeRequest{
		Key:        []byte("key000"),
		RangeEnd:   []byte("key999"),
		Limit:      limit,
		SortTarget: pb.RangeRequest_KEY,
		SortOrder:  pb.RangeRequest_ASCEND,
	}

	respNoSort, _, err := Range(ctx, lg, s, reqNoSort)
	assert.NoError(t, err)
	
	respAscend, _, err := Range(ctx, lg, s, reqAscend)
	assert.NoError(t, err)

	assert.Len(t, respAscend.Kvs, len(respNoSort.Kvs), "number of keys mismatch")
	for i := range respNoSort.Kvs {
		assert.Equalf(t, string(respNoSort.Kvs[i].Key), string(respAscend.Kvs[i].Key), "key at index %d mismatch", i)
	}
	assert.Equalf(t, respNoSort.More, respAscend.More, "More flag mismatch")
	assert.Equalf(t, respNoSort.Count, respAscend.Count, "Count mismatch")
}

func BenchmarkRange(b *testing.B) {
	be, _ := betesting.NewDefaultTmpBackend(nil)
	lessor := &lease.FakeLessor{LeaseSet: map[lease.LeaseID]struct{}{}}
	s := mvcc.NewStore(zaptest.NewLogger(nil), be, lessor, mvcc.StoreConfig{})
	defer s.Close()
	defer betesting.Close(nil, be)

	numKeys := 10000
	for i := 0; i < numKeys; i++ {
		s.Put([]byte(fmt.Sprintf("key%05d", i)), []byte("value"), 0)
	}

	ctx := context.Background()
	lg := zaptest.NewLogger(nil)

	b.Run("KeyAscendOptimized", func(b *testing.B) {
		req := &pb.RangeRequest{
			Key:        []byte("key00000"),
			RangeEnd:   []byte("key99999"),
			Limit:      10,
			SortTarget: pb.RangeRequest_KEY,
			SortOrder:  pb.RangeRequest_ASCEND,
		}
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			_, _, _ = Range(ctx, lg, s, req)
		}
	})

	b.Run("KeyDescendNonOptimized", func(b *testing.B) {
		req := &pb.RangeRequest{
			Key:        []byte("key00000"),
			RangeEnd:   []byte("key99999"),
			Limit:      10,
			SortTarget: pb.RangeRequest_KEY,
			SortOrder:  pb.RangeRequest_DESCEND,
		}
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			_, _, _ = Range(ctx, lg, s, req)
		}
	})
}
