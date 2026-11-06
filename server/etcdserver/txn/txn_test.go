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

package txn

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"

	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	"go.etcd.io/etcd/pkg/v3/traceutil"
	"go.etcd.io/etcd/server/v3/lease"
	betesting "go.etcd.io/etcd/server/v3/storage/backend/testing"
	"go.etcd.io/etcd/server/v3/storage/mvcc"
)

type testCase struct {
	name        string
	setup       testSetup
	op          *pb.RequestOp
	expectError string
}

type testSetup struct {
	compactRevision int64
	lease           int64
	key             []byte
}

var futureRev int64 = 1000

var rangeTestCases = []testCase{
	{
		name: "Range with revision 0 should succeed",
		op: &pb.RequestOp{
			Request: &pb.RequestOp_RequestRange{
				RequestRange: &pb.RangeRequest{
					Revision: 0,
				},
			},
		},
	},
	{
		name: "Range on future rev should fail",
		op: &pb.RequestOp{
			Request: &pb.RequestOp_RequestRange{
				RequestRange: &pb.RangeRequest{
					Revision: futureRev,
				},
			},
		},
		expectError: "mvcc: required revision is a future revision",
	},
	{
		name:  "Range on compacted rev should fail",
		setup: testSetup{compactRevision: 10},
		op: &pb.RequestOp{
			Request: &pb.RequestOp_RequestRange{
				RequestRange: &pb.RangeRequest{
					Revision: 9,
				},
			},
		},
		expectError: "mvcc: required revision has been compacted",
	},
}

var putTestCases = []testCase{
	{
		name: "Put without lease should succeed",
		op: &pb.RequestOp{
			Request: &pb.RequestOp_RequestPut{
				RequestPut: &pb.PutRequest{},
			},
		},
	},
	{
		name: "Put with non-existing lease should fail",
		op: &pb.RequestOp{
			Request: &pb.RequestOp_RequestPut{
				RequestPut: &pb.PutRequest{
					Lease: 123,
				},
			},
		},
		expectError: "lease not found",
	},
	{
		name:  "Put with existing lease should succeed",
		setup: testSetup{lease: 123},
		op: &pb.RequestOp{
			Request: &pb.RequestOp_RequestPut{
				RequestPut: &pb.PutRequest{
					Lease: 123,
				},
			},
		},
	},
	{
		name: "Put with ignore value without previous key should fail",
		op: &pb.RequestOp{
			Request: &pb.RequestOp_RequestPut{
				RequestPut: &pb.PutRequest{
					IgnoreValue: true,
				},
			},
		},
		expectError: "etcdserver: key not found",
	},
	{
		name: "Put with ignore lease without previous key should fail",
		op: &pb.RequestOp{
			Request: &pb.RequestOp_RequestPut{
				RequestPut: &pb.PutRequest{
					IgnoreLease: true,
				},
			},
		},
		expectError: "etcdserver: key not found",
	},
	{
		name:  "Put with ignore value with previous key should succeeded",
		setup: testSetup{key: []byte("ignore-value")},
		op: &pb.RequestOp{
			Request: &pb.RequestOp_RequestPut{
				RequestPut: &pb.PutRequest{
					IgnoreValue: true,
					Key:         []byte("ignore-value"),
				},
			},
		},
	},
	{
		name:  "Put with ignore lease with previous key should succeed ",
		setup: testSetup{key: []byte("ignore-lease")},
		op: &pb.RequestOp{
			Request: &pb.RequestOp_RequestPut{
				RequestPut: &pb.PutRequest{
					IgnoreLease: true,
					Key:         []byte("ignore-lease"),
				},
			},
		},
	},
}

func TestCheckTxn(t *testing.T) {
	type txnTestCase struct {
		name        string
		setup       testSetup
		txn         *pb.TxnRequest
		expectError string
	}
	testCases := []txnTestCase{}
	for _, tc := range append(rangeTestCases, putTestCases...) {
		testCases = append(testCases, txnTestCase{
			name:  tc.name,
			setup: tc.setup,
			txn: &pb.TxnRequest{
				Success: []*pb.RequestOp{
					tc.op,
				},
			},
			expectError: tc.expectError,
		})
	}
	invalidOperation := &pb.RequestOp{
		Request: &pb.RequestOp_RequestRange{
			RequestRange: &pb.RangeRequest{
				Revision: futureRev,
			},
		},
	}
	testCases = append(testCases, txnTestCase{
		name: "Invalid operation on failed path should succeed",
		txn: &pb.TxnRequest{
			Failure: []*pb.RequestOp{
				invalidOperation,
			},
		},
	})

	testCases = append(testCases, txnTestCase{
		name: "Invalid operation on subtransaction should fail",
		txn: &pb.TxnRequest{
			Success: []*pb.RequestOp{
				{
					Request: &pb.RequestOp_RequestTxn{
						RequestTxn: &pb.TxnRequest{
							Success: []*pb.RequestOp{
								invalidOperation,
							},
						},
					},
				},
			},
		},
		expectError: "mvcc: required revision is a future revision",
	})

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			s, lessor := setup(t, tc.setup)

			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			_, _, err := Txn(ctx, zaptest.NewLogger(t), tc.txn, false, s, lessor, false)

			gotErr := ""
			if err != nil {
				gotErr = err.Error()
			}
			if gotErr != tc.expectError {
				t.Errorf("Error not matching, got %q, expected %q", gotErr, tc.expectError)
			}
		})
	}
}

func TestCheckPut(t *testing.T) {
	for _, tc := range putTestCases {
		t.Run(tc.name, func(t *testing.T) {
			s, lessor := setup(t, tc.setup)

			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			_, _, err := Put(ctx, zaptest.NewLogger(t), lessor, s, tc.op.GetRequestPut())

			gotErr := ""
			if err != nil {
				gotErr = err.Error()
			}
			if gotErr != tc.expectError {
				t.Errorf("Error not matching, got %q, expected %q", gotErr, tc.expectError)
			}
		})
	}
}

func TestCheckRange(t *testing.T) {
	for _, tc := range rangeTestCases {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := setup(t, tc.setup)

			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			_, _, err := Range(ctx, zaptest.NewLogger(t), s, tc.op.GetRequestRange(), true)

			gotErr := ""
			if err != nil {
				gotErr = err.Error()
			}
			if gotErr != tc.expectError {
				t.Errorf("Error not matching, got %q, expected %q", gotErr, tc.expectError)
			}
		})
	}
}

func setup(t *testing.T, setup testSetup) (mvcc.KV, lease.Lessor) {
	b, _ := betesting.NewDefaultTmpBackend(t)
	t.Cleanup(func() {
		betesting.Close(t, b)
	})
	lessor := &lease.FakeLessor{LeaseSet: map[lease.LeaseID]struct{}{}}
	s := mvcc.NewStore(zaptest.NewLogger(t), b, lessor, mvcc.StoreConfig{})
	t.Cleanup(func() {
		s.Close()
	})

	if setup.compactRevision != 0 {
		for i := 0; int64(i) < setup.compactRevision; i++ {
			s.Put([]byte("a"), []byte("b"), 0)
		}
		s.Compact(traceutil.TODO(), setup.compactRevision)
	}
	if setup.lease != 0 {
		lessor.Grant(lease.LeaseID(setup.lease), 0)
	}
	if len(setup.key) != 0 {
		s.Put(setup.key, []byte("b"), 0)
	}
	return s, lessor
}

func TestSkippedRangeExecutionResponseHeaderRevision(t *testing.T) {
	tests := []struct {
		name string
		txn  *pb.TxnRequest
	}{
		{
			name: "range before write",
			txn: &pb.TxnRequest{
				Success: []*pb.RequestOp{
					{
						Request: &pb.RequestOp_RequestRange{
							RequestRange: &pb.RangeRequest{Key: []byte("foo")},
						},
					},
					{
						Request: &pb.RequestOp_RequestPut{
							RequestPut: &pb.PutRequest{Key: []byte("foo"), Value: []byte("bar")},
						},
					},
				},
			},
		},
		{
			name: "range after write",
			txn: &pb.TxnRequest{
				Success: []*pb.RequestOp{
					{
						Request: &pb.RequestOp_RequestPut{
							RequestPut: &pb.PutRequest{Key: []byte("foo"), Value: []byte("bar")},
						},
					},
					{
						Request: &pb.RequestOp_RequestRange{
							RequestRange: &pb.RangeRequest{Key: []byte("foo")},
						},
					},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := t.Context()
			lg := zaptest.NewLogger(t)
			normalKV, normalLessor := setup(t, testSetup{})
			skippedKV, skippedLessor := setup(t, testSetup{})

			normalResp, _, err := Txn(ctx, lg, tt.txn, false, normalKV, normalLessor, false)
			require.NoError(t, err)
			skippedResp, _, err := Txn(ctx, lg, tt.txn, false, skippedKV, skippedLessor, true)
			require.NoError(t, err)

			var normalRangeHeader, skippedRangeHeader *pb.ResponseHeader
			for i, resp := range normalResp.Responses {
				rangeResp := resp.GetResponseRange()
				if rangeResp == nil {
					continue
				}
				normalRangeHeader = rangeResp.Header
				skippedRangeHeader = skippedResp.Responses[i].GetResponseRange().Header
				break
			}
			require.NotNil(t, normalRangeHeader)
			require.NotNil(t, skippedRangeHeader)
			require.Equal(t, normalRangeHeader.Revision, skippedRangeHeader.Revision)
		})
	}
}

func TestReadonlyTxnError(t *testing.T) {
	b, _ := betesting.NewDefaultTmpBackend(t)
	defer betesting.Close(t, b)
	s := mvcc.NewStore(zaptest.NewLogger(t), b, &lease.FakeLessor{}, mvcc.StoreConfig{})
	defer s.Close()

	// setup cancelled context
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	// put some data to prevent early termination in rangeKeys
	// we are expecting failure on cancelled context check
	s.Put([]byte("foo"), []byte("bar"), lease.NoLease)

	txn := &pb.TxnRequest{
		Success: []*pb.RequestOp{
			{
				Request: &pb.RequestOp_RequestRange{
					RequestRange: &pb.RangeRequest{
						Key: []byte("foo"),
					},
				},
			},
		},
	}

	_, _, err := Txn(ctx, zaptest.NewLogger(t), txn, false, s, &lease.FakeLessor{}, false)
	if err == nil || !strings.Contains(err.Error(), "applyTxn: failed Range: rangeKeys: context cancelled: context canceled") {
		t.Fatalf("Expected context canceled error, got %v", err)
	}
}

func TestWriteTxnPanicWithoutApply(t *testing.T) {
	b, bePath := betesting.NewDefaultTmpBackend(t)
	s := mvcc.NewStore(zaptest.NewLogger(t), b, &lease.FakeLessor{}, mvcc.StoreConfig{})
	defer s.Close()

	// setup cancelled context
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	// write txn that puts some data and then fails in range due to cancelled context
	txn := &pb.TxnRequest{
		Success: []*pb.RequestOp{
			{
				Request: &pb.RequestOp_RequestPut{
					RequestPut: &pb.PutRequest{
						Key:   []byte("foo"),
						Value: []byte("bar"),
					},
				},
			},
			{
				Request: &pb.RequestOp_RequestRange{
					RequestRange: &pb.RangeRequest{
						Key: []byte("foo"),
					},
				},
			},
		},
	}

	// compute DB file hash before applying the txn
	dbHashBefore, err := computeFileHash(bePath)
	require.NoErrorf(t, err, "failed to compute DB file hash before txn")

	// we verify the following properties below:
	// 1. server panics after a write txn aply fails (invariant: server should never try to move on from a failed write)
	// 2. no writes from the txn are applied to the backend (invariant: failed write should have no side-effect on DB state besides panic)
	assert.Panicsf(t, func() { Txn(ctx, zaptest.NewLogger(t), txn, false, s, &lease.FakeLessor{}, false) }, "Expected panic in Txn with writes")
	dbHashAfter, err := computeFileHash(bePath)
	require.NoErrorf(t, err, "failed to compute DB file hash after txn")
	require.Equalf(t, dbHashBefore, dbHashAfter, "mismatch in DB hash before and after failed write txn")
}

func computeFileHash(filePath string) (string, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return "", err
	}
	defer file.Close()

	h := sha256.New()
	if _, err := io.Copy(h, file); err != nil {
		return "", err
	}
	return string(h.Sum(nil)), nil
}

func TestRangeSortLimit(t *testing.T) {
	s, _ := setup(t, testSetup{})
	s.Put([]byte("c"), []byte("1"), 0)
	s.Put([]byte("b"), []byte("2"), 0)
	s.Put([]byte("a"), []byte("3"), 0)

	ctx := t.Context()
	lg := zaptest.NewLogger(t)

	for _, limit := range []int64{1, 2, 3} {
		t.Run(fmt.Sprintf("limit_%d", limit), func(t *testing.T) {
			resp, _, err := Range(ctx, lg, s, &pb.RangeRequest{
				Key:        []byte("a"),
				RangeEnd:   []byte("d"),
				SortTarget: pb.RangeRequest_CREATE,
				SortOrder:  pb.RangeRequest_NONE,
				Limit:      limit,
			}, false)
			require.NoError(t, err)

			var keys []string
			for _, kv := range resp.Kvs {
				keys = append(keys, string(kv.Key))
			}

			expected := []string{"c", "b", "a"}[:limit]
			require.Equal(t, expected, keys)
		})
	}
}
