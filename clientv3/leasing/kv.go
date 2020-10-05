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

package leasing

import (
	"context"
	"strings"
	"sync"
	"time"

	v3 "go.etcd.io/etcd/v3/clientv3"
	"go.etcd.io/etcd/v3/clientv3/concurrency"
	"go.etcd.io/etcd/v3/etcdserver/api/v3rpc/rpctypes"
	pb "go.etcd.io/etcd/v3/etcdserver/etcdserverpb"
	"go.etcd.io/etcd/v3/mvcc/mvccpb"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type leasingKV struct {
	cl     *v3.Client
	kv     v3.KV
	pfx    string
	leases leaseCache

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	sessionOpts []concurrency.SessionOption
	session     *concurrency.Session
	sessionc    chan struct{}
}

var closedCh chan struct{}

func init() {
	closedCh = make(chan struct{})
	close(closedCh)
}

// NewKV wraps a KV instance so that all requests are wired through a leasing protocol.
func NewKV(cl *v3.Client, pfx string, opts ...concurrency.SessionOption) (v3.KV, func(), error) {
	cctx, cancel := context.WithCancel(cl.Ctx())
	lkv := &leasingKV{
		cl:          cl,
		kv:          cl.KV,
		pfx:         pfx,
		leases:      leaseCache{revokes: make(map[string]time.Time)},
		ctx:         cctx,
		cancel:      cancel,
		sessionOpts: opts,
		sessionc:    make(chan struct{}),
	}
	lkv.wg.Add(2)
	go func() {
		defer lkv.wg.Done()
		lkv.monitorSession()
	}()
	go func() {
		defer lkv.wg.Done()
		lkv.leases.clearOldRevokes(cctx)
	}()
	return lkv, lkv.Close, lkv.waitSession(cctx)
}

func (lkv *leasingKV) Close() {
	lkv.cancel()
	lkv.wg.Wait()
}

func (lkv *leasingKV) Get(ctx context.Context, key string, opts ...v3.OpOption) (*v3.GetResponse, error) {
	return lkv.get(ctx, v3.OpGet(key, opts...))
}

// TODO yxj
func (lkv *leasingKV) GetStream(ctx context.Context, key string, opts ...v3.OpOption) (*v3.GetStreamResponse, error) {
	panic("unsupported")
}

func (lkv *leasingKV) Put(ctx context.Context, key, val string, opts ...v3.OpOption) (*v3.PutResponse, error) {
	return lkv.put(ctx, v3.OpPut(key, val, opts...))
}

func (lkv *leasingKV) Delete(ctx context.Context, key string, opts ...v3.OpOption) (*v3.DeleteResponse, error) {
	return lkv.delete(ctx, v3.OpDelete(key, opts...))
}

func (lkv *leasingKV) Do(ctx context.Context, op v3.Op) (v3.OpResponse, error) {
	switch {
	case op.IsGet():
		resp, err := lkv.get(ctx, op)
		return resp.OpResponse(), err
	case op.IsPut():
		resp, err := lkv.put(ctx, op)
		return resp.OpResponse(), err
	case op.IsDelete():
		resp, err := lkv.delete(ctx, op)
		return resp.OpResponse(), err
	case op.IsTxn():
		cmps, thenOps, elseOps := op.Txn()
		resp, err := lkv.Txn(ctx).If(cmps...).Then(thenOps...).Else(elseOps...).Commit()
		return resp.OpResponse(), err
	}
	return v3.OpResponse{}, nil
}

func (lkv *leasingKV) Compact(ctx context.Context, rev int64, opts ...v3.CompactOption) (*v3.CompactResponse, error) {
	return lkv.kv.Compact(ctx, rev, opts...)
}

func (lkv *leasingKV) Txn(ctx context.Context) v3.Txn {
	return &txnLeasing{Txn: lkv.kv.Txn(ctx), lkv: lkv, ctx: ctx}
}

func (lkv *leasingKV) monitorSession() {
	for lkv.ctx.Err() == nil {
		if lkv.session != nil {
			select {
			case <-lkv.session.Done():
			case <-lkv.ctx.Done():
				return
			}
		}
		lkv.leases.mu.Lock()
		select {
		case <-lkv.sessionc:
			lkv.sessionc = make(chan struct{})
		default:
		}
		lkv.leases.entries = make(map[string]*leaseKey)
		lkv.leases.mu.Unlock()

		s, err := concurrency.NewSession(lkv.cl, lkv.sessionOpts...)
		if err != nil {
			continue
		}

		lkv.leases.mu.Lock()
		lkv.session = s
		close(lkv.sessionc)
		lkv.leases.mu.Unlock()
	}
}

func (lkv *leasingKV) monitorLease(ctx context.Context, key string, rev int64) {
	cctx, cancel := context.WithCancel(lkv.ctx)
	defer cancel()
	for cctx.Err() == nil {
		if rev == 0 {
			resp, err := lkv.kv.Get(ctx, lkv.pfx+key)
			if err != nil {
				continue
			}
			rev = resp.Header.Revision
			if len(resp.Kvs) == 0 || string(resp.Kvs[0].Value) == "REVOKE" {
				lkv.rescind(cctx, key, rev)
				return
			}
		}
		wch := lkv.cl.Watch(cctx, lkv.pfx+key, v3.WithRev(rev+1))
		for resp := range wch {
			for _, ev := range resp.Events {
				if string(ev.Kv.Value) != "REVOKE" {
					continue
				}
				if v3.LeaseID(ev.Kv.Lease) == lkv.leaseID() {
					lkv.rescind(cctx, key, ev.Kv.ModRevision)
				}
				return
			}
		}
		rev = 0
	}
}

// rescind releases a lease from this client.
func (lkv *leasingKV) rescind(ctx context.Context, key string, rev int64) {
	if lkv.leases.Evict(key) > rev {
		return
	}
	cmp := v3.Compare(v3.CreateRevision(lkv.pfx+key), "<", rev)
	op := v3.OpDelete(lkv.pfx + key)
	for ctx.Err() == nil {
		if _, err := lkv.kv.Txn(ctx).If(cmp).Then(op).Commit(); err == nil {
			return
		}
	}
}

func (lkv *leasingKV) waitRescind(ctx context.Context, key string, rev int64) error {
	cctx, cancel := context.WithCancel(ctx)
	defer cancel()
	wch := lkv.cl.Watch(cctx, lkv.pfx+key, v3.WithRev(rev+1))
	for resp := range wch {
		for _, ev := range resp.Events {
			if ev.Type == v3.EventTypeDelete {
				return ctx.Err()
			}
		}
	}
	return ctx.Err()
}

func (lkv *leasingKV) tryModifyOp(ctx context.Context, op v3.Op) (*v3.TxnResponse, chan<- struct{}, error) {
	key := string(op.KeyBytes())
	wc, rev := lkv.leases.Lock(key)
	cmp := v3.Compare(v3.CreateRevision(lkv.pfx+key), "<", rev+1)
	resp, err := lkv.kv.Txn(ctx).If(cmp).Then(op).Commit()
	switch {
	case err != nil:
		lkv.leases.Evict(key)
		fallthrough
	case !resp.Succeeded:
		if wc != nil {
			close(wc)
		}
		return nil, nil, err
	}
	return resp, wc, nil
}

func (lkv *leasingKV) put(ctx context.Context, op v3.Op) (pr *v3.PutResponse, err error) {
	if err := lkv.waitSession(ctx); err != nil {
		return nil, err
	}
	for ctx.Err() == nil {
		resp, wc, err := lkv.tryModifyOp(ctx, op)
		if err != nil || wc == nil {
			resp, err = lkv.revoke(ctx, string(op.KeyBytes()), op)
		}
		if err != nil {
			return nil, err
		}
		if resp.Succeeded {
			lkv.leases.mu.Lock()
			lkv.leases.Update(op.KeyBytes(), op.ValueBytes(), resp.Header)
			lkv.leases.mu.Unlock()
			pr = (*v3.PutResponse)(resp.Responses[0].GetResponsePut())
			pr.Header = resp.Header
		}
		if wc != nil {
			close(wc)
		}
		if resp.Succeeded {
			return pr, nil
		}
	}
	return nil, ctx.Err()
}

func (lkv *leasingKV) acquire(ctx context.Context, key string, op v3.Op) (*v3.TxnResponse, error) {
	for ctx.Err() == nil {
		if err := lkv.waitSession(ctx); err != nil {
			return nil, err
		}
		lcmp := v3.Cmp{Key: []byte(key), Target: pb.Compare_LEASE}
		resp, err := lkv.kv.Txn(ctx).If(
			v3.Compare(v3.CreateRevision(lkv.pfx+key), "=", 0),
			v3.Compare(lcmp, "=", 0)).
			Then(
				op,
				v3.OpPut(lkv.pfx+key, "", v3.WithLease(lkv.leaseID()))).
			Else(
				op,
				v3.OpGet(lkv.pfx+key),
			).Commit()
		if err == nil {
			if !resp.Succeeded {
				kvs := resp.Responses[1].GetResponseRange().Kvs
				// if txn failed since already owner, lease is acquired
				resp.Succeeded = len(kvs) > 0 && v3.LeaseID(kvs[0].Lease) == lkv.leaseID()
			}
			return resp, nil
		}
		// retry if transient error
		if _, ok := err.(rpctypes.EtcdError); ok {
			return nil, err
		}
		if ev, ok := status.FromError(err); ok && ev.Code() != codes.Unavailable {
			return nil, err
		}
	}
	return nil, ctx.Err()
}

func (lkv *leasingKV) get(ctx context.Context, op v3.Op) (*v3.GetResponse, error) {
	do := func() (*v3.GetResponse, error) {
		r, err := lkv.kv.Do(ctx, op)
		return r.Get(), err
	}
	if !lkv.readySession() {
		return do()
	}

	if resp, ok := lkv.leases.Get(ctx, op); resp != nil {
		return resp, nil
	} else if !ok || op.IsSerializable() {
		// must be handled by server or can skip linearization
		return do()
	}

	key := string(op.KeyBytes())
	if !lkv.leases.MayAcquire(key) {
		resp, err := lkv.kv.Do(ctx, op)
		return resp.Get(), err
	}

	resp, err := lkv.acquire(ctx, key, v3.OpGet(key))
	if err != nil {
		return nil, err
	}
	getResp := (*v3.GetResponse)(resp.Responses[0].GetResponseRange())
	getResp.Header = resp.Header
	if resp.Succeeded {
		getResp = lkv.leases.Add(key, getResp, op)
		lkv.wg.Add(1)
		go func() {
			defer lkv.wg.Done()
			lkv.monitorLease(ctx, key, resp.Header.Revision)
		}()
	}
	return getResp, nil
}

func (lkv *leasingKV) deleteRangeRPC(ctx context.Context, maxLeaseRev int64, key, end string) (*v3.DeleteResponse, error) {
	lkey, lend := lkv.pfx+key, lkv.pfx+end
	resp, err := lkv.kv.Txn(ctx).If(
		v3.Compare(v3.CreateRevision(lkey).WithRange(lend), "<", maxLeaseRev+1),
	).Then(
		v3.OpGet(key, v3.WithRange(end), v3.WithKeysOnly()),
		v3.OpDelete(key, v3.WithRange(end)),
	).Commit()
	if err != nil {
		lkv.leases.EvictRange(key, end)
		return nil, err
	}
	if !resp.Succeeded {
		return nil, nil
	}
	for _, kv := range resp.Responses[0].GetResponseRange().Kvs {
		lkv.leases.Delete(string(kv.Key), resp.Header)
	}
	delResp := (*v3.DeleteResponse)(resp.Responses[1].GetResponseDeleteRange())
	delResp.Header = resp.Header
	return delResp, nil
}

func (lkv *leasingKV) deleteRange(ctx context.Context, op v3.Op) (*v3.DeleteResponse, error) {
	key, end := string(op.KeyBytes()), string(op.RangeBytes())
	for ctx.Err() == nil {
		maxLeaseRev, err := lkv.revokeRange(ctx, key, end)
		if err != nil {
			return nil, err
		}
		wcs := lkv.leases.LockRange(key, end)
		delResp, err := lkv.deleteRangeRPC(ctx, maxLeaseRev, key, end)
		closeAll(wcs)
		if err != nil || delResp != nil {
			return delResp, err
		}
	}
	return nil, ctx.Err()
}

func (lkv *leasingKV) delete(ctx context.Context, op v3.Op) (dr *v3.DeleteResponse, err error) {
	if err := lkv.waitSession(ctx); err != nil {
		return nil, err
	}
	if len(op.RangeBytes()) > 0 {
		return lkv.deleteRange(ctx, op)
	}
	key := string(op.KeyBytes())
	for ctx.Err() == nil {
		resp, wc, err := lkv.tryModifyOp(ctx, op)
		if err != nil || wc == nil {
			resp, err = lkv.revoke(ctx, key, op)
		}
		if err != nil {
			// don't know if delete was processed
			lkv.leases.Evict(key)
			return nil, err
		}
		if resp.Succeeded {
			dr = (*v3.DeleteResponse)(resp.Responses[0].GetResponseDeleteRange())
			dr.Header = resp.Header
			lkv.leases.Delete(key, dr.Header)
		}
		if wc != nil {
			close(wc)
		}
		if resp.Succeeded {
			return dr, nil
		}
	}
	return nil, ctx.Err()
}

func (lkv *leasingKV) revoke(ctx context.Context, key string, op v3.Op) (*v3.TxnResponse, error) {
	rev := lkv.leases.Rev(key)
	txn := lkv.kv.Txn(ctx).If(v3.Compare(v3.CreateRevision(lkv.pfx+key), "<", rev+1)).Then(op)
	resp, err := txn.Else(v3.OpPut(lkv.pfx+key, "REVOKE", v3.WithIgnoreLease())).Commit()
	if err != nil || resp.Succeeded {
		return resp, err
	}
	return resp, lkv.waitRescind(ctx, key, resp.Header.Revision)
}

func (lkv *leasingKV) revokeRange(ctx context.Context, begin, end string) (int64, error) {
	lkey, lend := lkv.pfx+begin, ""
	if len(end) > 0 {
		lend = lkv.pfx + end
	}
	leaseKeys, err := lkv.kv.Get(ctx, lkey, v3.WithRange(lend))
	if err != nil {
		return 0, err
	}
	return lkv.revokeLeaseKvs(ctx, leaseKeys.Kvs)
}

func (lkv *leasingKV) revokeLeaseKvs(ctx context.Context, kvs []*mvccpb.KeyValue) (int64, error) {
	maxLeaseRev := int64(0)
	for _, kv := range kvs {
		if rev := kv.CreateRevision; rev > maxLeaseRev {
			maxLeaseRev = rev
		}
		if v3.LeaseID(kv.Lease) == lkv.leaseID() {
			// don't revoke own keys
			continue
		}
		key := strings.TrimPrefix(string(kv.Key), lkv.pfx)
		if _, err := lkv.revoke(ctx, key, v3.OpGet(key)); err != nil {
			return 0, err
		}
	}
	return maxLeaseRev, nil
}

func (lkv *leasingKV) waitSession(ctx context.Context) error {
	lkv.leases.mu.RLock()
	sessionc := lkv.sessionc
	lkv.leases.mu.RUnlock()
	select {
	case <-sessionc:
		return nil
	case <-lkv.ctx.Done():
		return lkv.ctx.Err()
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (lkv *leasingKV) readySession() bool {
	lkv.leases.mu.RLock()
	defer lkv.leases.mu.RUnlock()
	if lkv.session == nil {
		return false
	}
	select {
	case <-lkv.session.Done():
	default:
		return true
	}
	return false
}

func (lkv *leasingKV) leaseID() v3.LeaseID {
	lkv.leases.mu.RLock()
	defer lkv.leases.mu.RUnlock()
	return lkv.session.Lease()
}
