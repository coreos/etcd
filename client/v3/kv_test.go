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

package clientv3

import (
	"context"
	"errors"
	"testing"

	"google.golang.org/grpc"

	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	"go.etcd.io/etcd/api/v3/v3rpc/rpctypes"
)

// errRemoteCalled is returned by recordingKVClient's RPCs so tests can assert
// that the request reached the remote rather than being rejected client-side.
var errRemoteCalled = errors.New("remote called")

// recordingKVClient is a pb.KVClient that records whether each RPC was invoked
// and always fails, so empty-key requests can be distinguished from requests
// that made it past the client-side guard.
type recordingKVClient struct {
	rangeCalled       bool
	rangeStreamCalled bool
	putCalled         bool
	deleteRangeCalled bool
}

func (c *recordingKVClient) Range(context.Context, *pb.RangeRequest, ...grpc.CallOption) (*pb.RangeResponse, error) {
	c.rangeCalled = true
	return nil, errRemoteCalled
}

func (c *recordingKVClient) RangeStream(context.Context, *pb.RangeRequest, ...grpc.CallOption) (grpc.ServerStreamingClient[pb.RangeStreamResponse], error) {
	c.rangeStreamCalled = true
	return nil, errRemoteCalled
}

func (c *recordingKVClient) Put(context.Context, *pb.PutRequest, ...grpc.CallOption) (*pb.PutResponse, error) {
	c.putCalled = true
	return nil, errRemoteCalled
}

func (c *recordingKVClient) DeleteRange(context.Context, *pb.DeleteRangeRequest, ...grpc.CallOption) (*pb.DeleteRangeResponse, error) {
	c.deleteRangeCalled = true
	return nil, errRemoteCalled
}

func (c *recordingKVClient) Txn(context.Context, *pb.TxnRequest, ...grpc.CallOption) (*pb.TxnResponse, error) {
	return nil, errRemoteCalled
}

func (c *recordingKVClient) Compact(context.Context, *pb.CompactionRequest, ...grpc.CallOption) (*pb.CompactionResponse, error) {
	return nil, errRemoteCalled
}

// TestEmptyKeyRejectedBeforeRemote verifies that Get, Put, Delete, and
// GetStream reject an empty key with ErrEmptyKey without issuing an RPC.
func TestEmptyKeyRejectedBeforeRemote(t *testing.T) {
	remote := &recordingKVClient{}
	kv := &kv{remote: remote}
	ctx := context.Background()

	if _, err := kv.Get(ctx, ""); !errors.Is(err, rpctypes.ErrEmptyKey) {
		t.Errorf("Get(\"\") error = %v, want %v", err, rpctypes.ErrEmptyKey)
	}
	if _, err := kv.Put(ctx, "", "val"); !errors.Is(err, rpctypes.ErrEmptyKey) {
		t.Errorf("Put(\"\") error = %v, want %v", err, rpctypes.ErrEmptyKey)
	}
	if _, err := kv.Delete(ctx, ""); !errors.Is(err, rpctypes.ErrEmptyKey) {
		t.Errorf("Delete(\"\") error = %v, want %v", err, rpctypes.ErrEmptyKey)
	}
	if _, err := kv.GetStream(ctx, ""); !errors.Is(err, rpctypes.ErrEmptyKey) {
		t.Errorf("GetStream(\"\") error = %v, want %v", err, rpctypes.ErrEmptyKey)
	}

	if remote.rangeCalled || remote.putCalled || remote.deleteRangeCalled || remote.rangeStreamCalled {
		t.Errorf("empty-key request reached remote: %+v", remote)
	}
}

// TestGetStreamEmptyKeyWithOptions verifies that WithPrefix and WithFromKey
// substitute the empty key with "\x00" before the guard, so GetStream still
// reaches the remote (mirroring Get).
func TestGetStreamEmptyKeyWithOptions(t *testing.T) {
	for _, tt := range []struct {
		name string
		opts []OpOption
	}{
		{"WithPrefix", []OpOption{WithPrefix()}},
		{"WithFromKey", []OpOption{WithFromKey()}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			remote := &recordingKVClient{}
			kv := &kv{remote: remote}

			_, err := kv.GetStream(context.Background(), "", tt.opts...)
			if errors.Is(err, rpctypes.ErrEmptyKey) {
				t.Fatalf("GetStream(\"\", %s) rejected as empty key, want passthrough to remote", tt.name)
			}
			if !remote.rangeStreamCalled {
				t.Errorf("GetStream(\"\", %s) did not reach remote", tt.name)
			}
		})
	}
}
