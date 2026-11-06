// Copyright 2017 The etcd Authors
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

package ordering

import (
	"context"
	"errors"
	"sync"
	"testing"

	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	clientv3 "go.etcd.io/etcd/client/v3"
)

type mockKV struct {
	clientv3.KV
	response clientv3.OpResponse
}

func (kv *mockKV) Do(ctx context.Context, op clientv3.Op) (clientv3.OpResponse, error) {
	return kv.response, nil
}

type mockReplayKV struct {
	clientv3.KV
	responses []clientv3.OpResponse
	calls     int
}

func (kv *mockReplayKV) Do(ctx context.Context, op clientv3.Op) (clientv3.OpResponse, error) {
	resp := kv.responses[kv.calls]
	kv.calls++
	return resp, nil
}

var rangeTests = []struct {
	prevRev  int64
	response *clientv3.GetResponse
}{
	{
		5,
		&clientv3.GetResponse{
			Header: &pb.ResponseHeader{
				Revision: 5,
			},
		},
	},
	{
		5,
		&clientv3.GetResponse{
			Header: &pb.ResponseHeader{
				Revision: 4,
			},
		},
	},
	{
		5,
		&clientv3.GetResponse{
			Header: &pb.ResponseHeader{
				Revision: 6,
			},
		},
	},
}

func TestKvOrdering(t *testing.T) {
	for i, tt := range rangeTests {
		mKV := &mockKV{clientv3.NewKVFromKVClient(nil, nil), tt.response.OpResponse()}
		kv := &kvOrdering{
			mKV,
			func(r *clientv3.GetResponse) OrderViolationFunc {
				return func(op clientv3.Op, resp clientv3.OpResponse, prevRev int64) error {
					r.Header.Revision++
					return nil
				}
			}(tt.response),
			tt.prevRev,
			sync.RWMutex{},
		}
		res, err := kv.Get(t.Context(), "mockKey")
		if err != nil {
			t.Errorf("#%d: expected response %+v, got error %+v", i, tt.response, err)
		}
		if rev := res.Header.Revision; rev < tt.prevRev {
			t.Errorf("#%d: expected revision %d, got %d", i, tt.prevRev, rev)
		}
	}
}

var txnTests = []struct {
	prevRev  int64
	response *clientv3.TxnResponse
}{
	{
		5,
		&clientv3.TxnResponse{
			Header: &pb.ResponseHeader{
				Revision: 5,
			},
		},
	},
	{
		5,
		&clientv3.TxnResponse{
			Header: &pb.ResponseHeader{
				Revision: 8,
			},
		},
	},
	{
		5,
		&clientv3.TxnResponse{
			Header: &pb.ResponseHeader{
				Revision: 4,
			},
		},
	},
}

func TestTxnOrdering(t *testing.T) {
	for i, tt := range txnTests {
		mKV := &mockKV{clientv3.NewKVFromKVClient(nil, nil), tt.response.OpResponse()}
		kv := &kvOrdering{
			mKV,
			func(r *clientv3.TxnResponse) OrderViolationFunc {
				return func(op clientv3.Op, resp clientv3.OpResponse, prevRev int64) error {
					r.Header.Revision++
					return nil
				}
			}(tt.response),
			tt.prevRev,
			sync.RWMutex{},
		}
		txn := &txnOrdering{
			kv.Txn(t.Context()),
			kv,
			t.Context(),
			sync.Mutex{},
			[]clientv3.Cmp{},
			[]clientv3.Op{},
			[]clientv3.Op{},
		}
		res, err := txn.Commit()
		if err != nil {
			t.Errorf("#%d: expected response %+v, got error %+v", i, tt.response, err)
		}
		if rev := res.Header.Revision; rev < tt.prevRev {
			t.Errorf("#%d: expected revision %d, got %d", i, tt.prevRev, rev)
		}
	}
}

func TestTxnOrderingDoesNotRetryMutableTxn(t *testing.T) {
	tests := []struct {
		name      string
		succeeded bool
		thenOps   []clientv3.Op
		elseOps   []clientv3.Op
	}{
		{
			name:      "write in selected then branch",
			succeeded: true,
			thenOps:   []clientv3.Op{clientv3.OpPut("key", "value")},
		},
		{
			name:    "write in selected else branch",
			elseOps: []clientv3.Op{clientv3.OpDelete("key")},
		},
		{
			name:      "write in nested transaction",
			succeeded: true,
			thenOps: []clientv3.Op{clientv3.OpTxn(
				nil,
				[]clientv3.Op{clientv3.OpPut("key", "value")},
				nil,
			)},
		},
		{
			name:    "write in unselected then branch",
			thenOps: []clientv3.Op{clientv3.OpPut("key", "value")},
		},
		{
			name:      "write in unselected else branch",
			succeeded: true,
			elseOps:   []clientv3.Op{clientv3.OpDelete("key")},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			staleResponse := (&clientv3.TxnResponse{
				Header:    &pb.ResponseHeader{Revision: 9},
				Succeeded: tt.succeeded,
			}).OpResponse()
			freshResponse := (&clientv3.TxnResponse{Header: &pb.ResponseHeader{Revision: 10}}).OpResponse()
			mKV := &mockReplayKV{
				KV:        clientv3.NewKVFromKVClient(nil, nil),
				responses: []clientv3.OpResponse{staleResponse, freshResponse},
			}
			violationCalls := 0
			kv := &kvOrdering{
				KV: mKV,
				orderViolationFunc: func(op clientv3.Op, resp clientv3.OpResponse, prevRev int64) error {
					violationCalls++
					return nil
				},
				prevRev: 10,
			}

			cmp := clientv3.Compare(clientv3.Version("condition"), "=", 1)
			_, _ = kv.Txn(t.Context()).If(cmp).Then(tt.thenOps...).Else(tt.elseOps...).Commit()
			if violationCalls != 1 {
				t.Fatalf("expected one ordering violation, got %d", violationCalls)
			}
			if mKV.calls != 1 {
				t.Fatalf("expected one transaction execution, got %d", mKV.calls)
			}
		})
	}
}

func TestTxnOrderingReturnsViolationErrorWithoutRetry(t *testing.T) {
	staleResponse := (&clientv3.TxnResponse{
		Header:    &pb.ResponseHeader{Revision: 9},
		Succeeded: true,
	}).OpResponse()
	mKV := &mockReplayKV{
		KV:        clientv3.NewKVFromKVClient(nil, nil),
		responses: []clientv3.OpResponse{staleResponse},
	}
	wantErr := errors.New("ordering violation")
	kv := &kvOrdering{
		KV: mKV,
		orderViolationFunc: func(op clientv3.Op, resp clientv3.OpResponse, prevRev int64) error {
			return wantErr
		},
		prevRev: 10,
	}

	_, err := kv.Txn(t.Context()).Then(clientv3.OpPut("key", "value")).Commit()
	if !errors.Is(err, wantErr) {
		t.Fatalf("expected error %v, got %v", wantErr, err)
	}
	if mKV.calls != 1 {
		t.Fatalf("expected one transaction execution, got %d", mKV.calls)
	}
}

func TestTxnOrderingRetriesReadOnlyTxn(t *testing.T) {
	staleResponse := (&clientv3.TxnResponse{Header: &pb.ResponseHeader{Revision: 9}}).OpResponse()
	freshResponse := (&clientv3.TxnResponse{Header: &pb.ResponseHeader{Revision: 10}}).OpResponse()
	mKV := &mockReplayKV{
		KV:        clientv3.NewKVFromKVClient(nil, nil),
		responses: []clientv3.OpResponse{staleResponse, freshResponse},
	}
	violationCalls := 0
	kv := &kvOrdering{
		KV: mKV,
		orderViolationFunc: func(op clientv3.Op, resp clientv3.OpResponse, prevRev int64) error {
			violationCalls++
			return nil
		},
		prevRev: 10,
	}

	nestedGet := clientv3.OpTxn(nil, []clientv3.Op{clientv3.OpGet("nested")}, nil)
	resp, err := kv.Txn(t.Context()).Then(clientv3.OpGet("key"), nestedGet).Commit()
	if err != nil {
		t.Fatalf("expected response, got error %v", err)
	}
	if resp.Header.Revision != 10 {
		t.Fatalf("expected revision 10, got %d", resp.Header.Revision)
	}
	if violationCalls != 1 {
		t.Fatalf("expected one ordering violation, got %d", violationCalls)
	}
	if mKV.calls != 2 {
		t.Fatalf("expected two transaction executions, got %d", mKV.calls)
	}
}
