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

package clientv3

import (
	"context"
	v3rpc "go.etcd.io/etcd/v3/etcdserver/api/v3rpc/rpctypes"
	"go.uber.org/zap"
	"io"
	"time"

	pb "go.etcd.io/etcd/v3/etcdserver/etcdserverpb"

	"google.golang.org/grpc"
)

type (
	CompactResponse   pb.CompactionResponse
	PutResponse       pb.PutResponse
	GetResponse       pb.RangeResponse
	GetStreamResponse pb.RangeStreamResponse
	DeleteResponse    pb.DeleteRangeResponse
	TxnResponse       pb.TxnResponse
)

type KV interface {
	// Put puts a key-value pair into etcd.
	// Note that key,value can be plain bytes array and string is
	// an immutable representation of that bytes array.
	// To get a string of bytes, do string([]byte{0x10, 0x20}).
	Put(ctx context.Context, key, val string, opts ...OpOption) (*PutResponse, error)

	// Get retrieves keys.
	// By default, Get will return the value for "key", if any.
	// When passed WithRange(end), Get will return the keys in the range [key, end).
	// When passed WithFromKey(), Get returns keys greater than or equal to key.
	// When passed WithRev(rev) with rev > 0, Get retrieves keys at the given revision;
	// if the required revision is compacted, the request will fail with ErrCompacted .
	// When passed WithLimit(limit), the number of returned keys is bounded by limit.
	// When passed WithSort(), the keys will be sorted.
	Get(ctx context.Context, key string, opts ...OpOption) (*GetResponse, error)

	GetStream(ctx context.Context, key string, opts ...OpOption) (*GetStreamResponse, error)

	// Delete deletes a key, or optionally using WithRange(end), [key, end).
	Delete(ctx context.Context, key string, opts ...OpOption) (*DeleteResponse, error)

	// Compact compacts etcd KV history before the given rev.
	Compact(ctx context.Context, rev int64, opts ...CompactOption) (*CompactResponse, error)

	// Do applies a single Op on KV without a transaction.
	// Do is useful when creating arbitrary operations to be issued at a
	// later time; the user can range over the operations, calling Do to
	// execute them. Get/Put/Delete, on the other hand, are best suited
	// for when the operation should be issued at the time of declaration.
	Do(ctx context.Context, op Op) (OpResponse, error)

	// Txn creates a transaction.
	Txn(ctx context.Context) Txn
}

type OpResponse struct {
	put       *PutResponse
	get       *GetResponse
	getStream *GetStreamResponse
	del       *DeleteResponse
	txn       *TxnResponse
}

func (op OpResponse) Put() *PutResponse             { return op.put }
func (op OpResponse) Get() *GetResponse             { return op.get }
func (op OpResponse) GetStream() *GetStreamResponse { return op.getStream }
func (op OpResponse) Del() *DeleteResponse          { return op.del }
func (op OpResponse) Txn() *TxnResponse             { return op.txn }

func (resp *PutResponse) OpResponse() OpResponse {
	return OpResponse{put: resp}
}
func (resp *GetResponse) OpResponse() OpResponse {
	return OpResponse{get: resp}
}
func (resp *GetStreamResponse) OpStreamResponse() OpResponse {
	return OpResponse{getStream: resp}
}
func (resp *DeleteResponse) OpResponse() OpResponse {
	return OpResponse{del: resp}
}
func (resp *TxnResponse) OpResponse() OpResponse {
	return OpResponse{txn: resp}
}

type kv struct {
	remote   pb.KVClient
	callOpts []grpc.CallOption
	lg       *zap.Logger
}

func NewKV(c *Client) KV {
	api := &kv{remote: RetryKVClient(c), lg: c.lg}
	if c != nil {
		api.callOpts = c.callOpts
	}
	return api
}

func NewKVFromKVClient(remote pb.KVClient, c *Client) KV {
	api := &kv{remote: remote, lg: c.lg}
	if c != nil {
		api.callOpts = c.callOpts
	}
	return api
}

func (kv *kv) Put(ctx context.Context, key, val string, opts ...OpOption) (*PutResponse, error) {
	r, err := kv.Do(ctx, OpPut(key, val, opts...))
	return r.put, toErr(ctx, err)
}

func (kv *kv) Get(ctx context.Context, key string, opts ...OpOption) (*GetResponse, error) {
	r, err := kv.Do(ctx, OpGet(key, opts...))
	return r.get, toErr(ctx, err)
}

func (kv *kv) GetStream(ctx context.Context, key string, opts ...OpOption) (*GetStreamResponse, error) {
	r, err := kv.Do(ctx, OpGetStream(key, opts...))
	return r.getStream, toErr(ctx, err)
}

func (kv *kv) Delete(ctx context.Context, key string, opts ...OpOption) (*DeleteResponse, error) {
	r, err := kv.Do(ctx, OpDelete(key, opts...))
	return r.del, toErr(ctx, err)
}

func (kv *kv) Compact(ctx context.Context, rev int64, opts ...CompactOption) (*CompactResponse, error) {
	resp, err := kv.remote.Compact(ctx, OpCompact(rev, opts...).toRequest(), kv.callOpts...)
	if err != nil {
		return nil, toErr(ctx, err)
	}
	return (*CompactResponse)(resp), err
}

func (kv *kv) Txn(ctx context.Context) Txn {
	return &txn{
		kv:       kv,
		ctx:      ctx,
		callOpts: kv.callOpts,
	}
}

func (kv *kv) Do(ctx context.Context, op Op) (OpResponse, error) {
	var err error
	switch op.t {
	case tRange:
		var resp *pb.RangeResponse
		resp, err = kv.remote.Range(ctx, op.toRangeRequest(), kv.callOpts...)
		if err == nil {
			return OpResponse{get: (*GetResponse)(resp)}, nil
		}
	case tRangeStream:
		var rangeStreamClient pb.KV_RangeStreamClient
		var resp *pb.RangeStreamResponse
		rangeStreamClient, err = kv.openRangeStreamClient(ctx, op.toRangeStreamRequest(), kv.callOpts...)
		resp, err = kv.serveRangeStream(ctx, rangeStreamClient)
		if err == nil {
			return OpResponse{getStream: (*GetStreamResponse)(resp)}, nil
		}
	case tPut:
		var resp *pb.PutResponse
		r := &pb.PutRequest{Key: op.key, Value: op.val, Lease: int64(op.leaseID), PrevKv: op.prevKV, IgnoreValue: op.ignoreValue, IgnoreLease: op.ignoreLease}
		resp, err = kv.remote.Put(ctx, r, kv.callOpts...)
		if err == nil {
			return OpResponse{put: (*PutResponse)(resp)}, nil
		}
	case tDeleteRange:
		var resp *pb.DeleteRangeResponse
		r := &pb.DeleteRangeRequest{Key: op.key, RangeEnd: op.end, PrevKv: op.prevKV}
		resp, err = kv.remote.DeleteRange(ctx, r, kv.callOpts...)
		if err == nil {
			return OpResponse{del: (*DeleteResponse)(resp)}, nil
		}
	case tTxn:
		var resp *pb.TxnResponse
		resp, err = kv.remote.Txn(ctx, op.toTxnRequest(), kv.callOpts...)
		if err == nil {
			return OpResponse{txn: (*TxnResponse)(resp)}, nil
		}
	default:
		panic("Unknown op")
	}
	return OpResponse{}, toErr(ctx, err)
}

// openRangeStreamClient retries opening a rangeStream client until success or halt.
// manually retry in case "rsc==nil && err==nil"
// TODO: remove FailFast=false
func (kv *kv) openRangeStreamClient(ctx context.Context, in *pb.RangeStreamRequest, opts ...grpc.CallOption) (rsc pb.KV_RangeStreamClient, err error) {
	backoff := time.Millisecond
	for {
		select {
		case <-ctx.Done():
			if err == nil {
				return nil, ctx.Err()
			}
			return nil, err
		default:
		}
		if rsc, err = kv.remote.RangeStream(ctx, in, opts...); rsc != nil && err == nil {
			break
		}
		if isHaltErr(ctx, err) {
			return nil, v3rpc.Error(err)
		}
		if isUnavailableErr(ctx, err) {
			// retry, but backoff
			if backoff < maxBackoff {
				// 25% backoff factor
				backoff = backoff + backoff/4
				if backoff > maxBackoff {
					backoff = maxBackoff
				}
			}
			time.Sleep(backoff)
		}
	}
	return rsc, nil
}

func (kv *kv) serveRangeStream(ctx context.Context, rsc pb.KV_RangeStreamClient) (*pb.RangeStreamResponse, error) {
	rspC := make(chan *pb.RangeStreamResponse)
	errC := make(chan error)

	mainRSP := &pb.RangeStreamResponse{}

	go kv.handleRangeStream(ctx, rsc, rspC, errC)

Loop:
	for {
		select {
		case subRsp := <-rspC:
			if subRsp == nil {
				break Loop
			}

			mainRSP.Kvs = append(mainRSP.Kvs, subRsp.Kvs...)
			mainRSP.TotalCount = subRsp.TotalCount
		case err := <-errC:
			return nil, err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	return mainRSP, nil
}

func (kv *kv) handleRangeStream(ctx context.Context, rsc pb.KV_RangeStreamClient, rspC chan *pb.RangeStreamResponse, errC chan error) {
	defer func() {
		if err := recover(); err != nil {
			switch e := err.(type) {
			case error:
				kv.lg.Error("kv handleRangeStream() panic error", zap.Error(e))
			}
		}
	}()

	defer func() {
		close(rspC)
		close(errC)
	}()

	for {
		resp, err := rsc.Recv()
		if err != nil {
			if err == io.EOF {
				select {
				case rspC <- nil:
				case <-ctx.Done():
					return
				}
				break
			}

			select {
			case errC <- err:
			case <-ctx.Done():
			}
			return
		}

		select {
		case rspC <- resp:
		case <-ctx.Done():
			return
		}
	}
	return
}
