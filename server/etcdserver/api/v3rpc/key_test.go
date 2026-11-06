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

package v3rpc

import (
	"testing"

	"github.com/stretchr/testify/require"

	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	"go.etcd.io/etcd/api/v3/v3rpc/rpctypes"
)

func TestCheckRangeRequest(t *testing.T) {
	rangeReqs := []struct {
		sortOrder     pb.RangeRequest_SortOrder
		sortTarget    pb.RangeRequest_SortTarget
		expectedError error
	}{
		{
			sortOrder:     pb.RangeRequest_ASCEND,
			sortTarget:    pb.RangeRequest_CREATE,
			expectedError: nil,
		},
		{
			sortOrder:     pb.RangeRequest_ASCEND,
			sortTarget:    100,
			expectedError: rpctypes.ErrGRPCInvalidSortOption,
		},
		{
			sortOrder:     200,
			sortTarget:    pb.RangeRequest_MOD,
			expectedError: rpctypes.ErrGRPCInvalidSortOption,
		},
	}

	for _, req := range rangeReqs {
		rangeReq := pb.RangeRequest{
			Key:        []byte{1, 2, 3},
			SortOrder:  req.sortOrder,
			SortTarget: req.sortTarget,
		}

		actualRet := checkRangeRequest(&rangeReq)
		if getError(actualRet) != getError(req.expectedError) {
			t.Errorf("expected sortOrder (%d) and sortTarget (%d) to be %q, but got %q",
				req.sortOrder, req.sortTarget, getError(req.expectedError), getError(actualRet))
		}
	}
}

func getError(err error) string {
	if err == nil {
		return ""
	}

	return err.Error()
}

func TestCheckIntervalsOpenEndedDeleteRange(t *testing.T) {
	delOp := func(key, rangeEnd string) *pb.RequestOp {
		return &pb.RequestOp{Request: &pb.RequestOp_RequestDeleteRange{
			RequestDeleteRange: &pb.DeleteRangeRequest{Key: []byte(key), RangeEnd: []byte(rangeEnd)},
		}}
	}
	putOp := func(key string) *pb.RequestOp {
		return &pb.RequestOp{Request: &pb.RequestOp_RequestPut{
			RequestPut: &pb.PutRequest{Key: []byte(key), Value: []byte("v")},
		}}
	}

	tests := []struct {
		name        string
		reqs        []*pb.RequestOp
		expectedErr error
	}{
		{
			name:        "finite range delete overlapping a put",
			reqs:        []*pb.RequestOp{delOp("a", "c"), putOp("b")},
			expectedErr: rpctypes.ErrGRPCDuplicateKey,
		},
		{
			name:        "finite range delete not overlapping a put",
			reqs:        []*pb.RequestOp{delOp("a", "c"), putOp("d")},
			expectedErr: nil,
		},
		{
			name:        "open ended delete overlapping a put above the start key",
			reqs:        []*pb.RequestOp{delOp("a", "\x00"), putOp("b")},
			expectedErr: rpctypes.ErrGRPCDuplicateKey,
		},
		{
			name:        "open ended delete over the whole keyspace overlapping a put",
			reqs:        []*pb.RequestOp{delOp("\x00", "\x00"), putOp("b")},
			expectedErr: rpctypes.ErrGRPCDuplicateKey,
		},
		{
			name:        "open ended delete not overlapping a put below the start key",
			reqs:        []*pb.RequestOp{delOp("a", "\x00"), putOp("0")},
			expectedErr: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := checkIntervals(tt.reqs)
			if tt.expectedErr == nil {
				require.NoError(t, err)
				return
			}
			require.EqualError(t, err, tt.expectedErr.Error())
		})
	}
}
