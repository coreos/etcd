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

package leasing

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v3pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	"go.etcd.io/etcd/api/v3/mvccpb"
	v3 "go.etcd.io/etcd/client/v3"
)

func TestLeaseCacheDeleteClearsCount(t *testing.T) {
	lc := &leaseCache{
		entries: make(map[string]*leaseKey),
		revokes: make(map[string]time.Time),
	}

	// Simulate a cached Get response with Kvs and Count set.
	key := "test-key"
	header := &v3pb.ResponseHeader{Revision: 100}
	resp := &v3.GetResponse{
		Header: header,
		Kvs: []*mvccpb.KeyValue{
			{
				Key:            []byte(key),
				Value:          []byte("value"),
				CreateRevision: 50,
				ModRevision:    100,
				Version:        1,
			},
		},
		Count: 1,
	}

	// Add to cache.
	lc.Add(key, resp, v3.OpGet(key))

	// Verify initial cached state via leaseKey.get.
	lk := lc.entries[key]
	require.NotNil(t, lk)
	require.Equal(t, int64(1), lk.response.Count)
	require.Len(t, lk.response.Kvs, 1)

	// Simulate delete with a newer revision.
	deleteHeader := &v3pb.ResponseHeader{Revision: 200}
	lc.Delete(key, deleteHeader)

	// After delete, Kvs should be nil and Count should be 0.
	require.Len(t, lk.response.Kvs, 0, "Kvs should be empty after delete")
	require.Equal(t, int64(0), lk.response.Count, "Count should be 0 after delete")
}

func TestLeaseCacheDeleteDoesNotClearCountWhenRevisionOlder(t *testing.T) {
	lc := &leaseCache{
		entries: make(map[string]*leaseKey),
		revokes: make(map[string]time.Time),
	}

	key := "test-key"
	header := &v3pb.ResponseHeader{Revision: 200}
	resp := &v3.GetResponse{
		Header: header,
		Kvs: []*mvccpb.KeyValue{
			{
				Key:            []byte(key),
				Value:          []byte("value"),
				CreateRevision: 50,
				ModRevision:    200,
				Version:        2,
			},
		},
		Count: 1,
	}

	lc.Add(key, resp, v3.OpGet(key))

	// Delete with an older revision — should be a no-op.
	olderHeader := &v3pb.ResponseHeader{Revision: 100}
	lc.Delete(key, olderHeader)

	lk := lc.entries[key]
	require.NotNil(t, lk)
	require.Len(t, lk.response.Kvs, 1)
	require.Equal(t, int64(1), lk.response.Count)
}
