// Copyright 2021 The etcd Authors
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

package schema

import (
	"encoding/binary"
	"math"

	"go.etcd.io/etcd/server/v3/lease/leasepb"
	"go.etcd.io/etcd/server/v3/storage/backend"
)

func UnsafeCreateLeaseBucket(tx backend.BatchTx) {
	tx.UnsafeCreateBucket(Lease)
}

func MustUnsafeGetAllLeases(tx backend.ReadTx) []*leasepb.Lease {
	_, vs := tx.UnsafeRange(Lease, leaseIdToBytes(0), leaseIdToBytes(math.MaxInt64), 0)
	ls := make([]*leasepb.Lease, 0, len(vs))
	for i := range vs {
		var lpb leasepb.Lease
		err := lpb.Unmarshal(vs[i])
		if err != nil {
			panic("failed to unmarshal lease proto item")
		}
		ls = append(ls, &lpb)
	}
	return ls
}

func MustUnsafePutLease(tx backend.BatchTx, lpb *leasepb.Lease) {
	key := leaseIdToBytes(lpb.ID)

	val, err := lpb.Marshal()
	if err != nil {
		panic("failed to marshal lease proto item")
	}
	tx.UnsafePut(Lease, key, val)
}

func UnsafeDeleteLease(tx backend.BatchTx, lpb *leasepb.Lease) {
	tx.UnsafeDelete(Lease, leaseIdToBytes(lpb.ID))
}

func MustUnsafeGetLease(tx backend.BatchTx, leaseID int64) *leasepb.Lease {
	_, vs := tx.UnsafeRange(Lease, leaseIdToBytes(leaseID), nil, 0)
	if len(vs) != 1 {
		return nil
	}
	var lpb leasepb.Lease
	err := lpb.Unmarshal(vs[0])
	if err != nil {
		panic("failed to unmarshal lease proto item")
	}
	return &lpb
}

func leaseIdToBytes(n int64) []byte {
	bytes := make([]byte, 8)
	binary.BigEndian.PutUint64(bytes, uint64(n))
	return bytes
}
