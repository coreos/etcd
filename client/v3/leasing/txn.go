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

	v3pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	v3 "go.etcd.io/etcd/client/v3"
)

type txnLeasing struct {
	v3.Txn
	lkv  *leasingKV
	ctx  context.Context
	cs   []v3.Cmp
	opst []v3.Op
	opse []v3.Op
}

func (txn *txnLeasing) If(cs ...v3.Cmp) v3.Txn {
	txn.cs = append(txn.cs, cs...)
	txn.Txn = txn.Txn.If(cs...)
	return txn
}

func (txn *txnLeasing) Then(ops ...v3.Op) v3.Txn {
	txn.opst = append(txn.opst, ops...)
	txn.Txn = txn.Txn.Then(ops...)
	return txn
}

func (txn *txnLeasing) Else(ops ...v3.Op) v3.Txn {
	txn.opse = append(txn.opse, ops...)
	txn.Txn = txn.Txn.Else(ops...)
	return txn
}

func (txn *txnLeasing) Commit() (*v3.TxnResponse, error) {
	if err := txn.lkv.validateDeleteOps(txn.opst); err != nil {
		return nil, err
	}
	if err := txn.lkv.validateDeleteOps(txn.opse); err != nil {
		return nil, err
	}
	if resp, err := txn.eval(); resp != nil || err != nil {
		return resp, err
	}
	return txn.serverTxn()
}

func (txn *txnLeasing) eval() (*v3.TxnResponse, error) {
	// TODO: wait on keys in comparisons
	thenOps, elseOps := gatherOps(txn.opst), gatherOps(txn.opse)
	ops := make([]v3.Op, 0, len(thenOps)+len(elseOps))
	ops = append(ops, thenOps...)
	ops = append(ops, elseOps...)

	for _, ch := range txn.lkv.leases.NotifyOps(ops) {
		select {
		case <-ch:
		case <-txn.ctx.Done():
			return nil, txn.ctx.Err()
		}
	}

	txn.lkv.leases.mu.RLock()
	defer txn.lkv.leases.mu.RUnlock()
	succeeded, ok := txn.lkv.leases.evalCmp(txn.cs)
	if !ok || txn.lkv.leases.header == nil {
		return nil, nil
	}
	if ops = txn.opst; !succeeded {
		ops = txn.opse
	}

	resps, ok := txn.lkv.leases.evalOps(ops)
	if !ok {
		return nil, nil
	}
	return &v3.TxnResponse{Header: copyHeader(txn.lkv.leases.header), Succeeded: succeeded, Responses: resps}, nil
}

// fallback computes the ops to fetch all possible conflicting
// leasing keys for a list of ops.
func (txn *txnLeasing) fallback(ops []v3.Op) (fbOps []v3.Op) {
	for _, op := range ops {
		if op.IsGet() {
			continue
		}
		lkey, lend := txn.lkv.pfx+string(op.KeyBytes()), ""
		if len(op.RangeBytes()) > 0 {
			lend = txn.lkv.pfx + string(op.RangeBytes())
		}
		fbOps = append(fbOps, v3.OpGet(lkey, v3.WithRange(lend)))
	}
	return fbOps
}

func (txn *txnLeasing) guardKeys(ops []v3.Op) (cmps []v3.Cmp) {
	seen := make(map[string]bool)
	for _, op := range ops {
		key := string(op.KeyBytes())
		if op.IsGet() || len(op.RangeBytes()) != 0 || seen[key] {
			continue
		}
		rev := txn.lkv.leases.Rev(key)
		cmps = append(cmps, v3.Compare(v3.CreateRevision(txn.lkv.pfx+key), "<", rev+1))
		seen[key] = true
	}
	return cmps
}

func (txn *txnLeasing) guardRanges(ops []v3.Op) (cmps []v3.Cmp, err error) {
	for _, op := range ops {
		if op.IsGet() || len(op.RangeBytes()) == 0 {
			continue
		}

		key, end := string(op.KeyBytes()), string(op.RangeBytes())
		maxRevLK, err := txn.lkv.revokeRange(txn.ctx, key, end)
		if err != nil {
			return nil, err
		}

		opts := append(v3.WithLastRev(), v3.WithRange(end))
		getResp, err := txn.lkv.kv.Get(txn.ctx, key, opts...)
		if err != nil {
			return nil, err
		}
		maxModRev := int64(0)
		if len(getResp.Kvs) > 0 {
			maxModRev = getResp.Kvs[0].ModRevision
		}

		noKeyUpdate := v3.Compare(v3.ModRevision(key).WithRange(end), "<", maxModRev+1)
		noLeaseUpdate := v3.Compare(
			v3.CreateRevision(txn.lkv.pfx+key).WithRange(txn.lkv.pfx+end),
			"<",
			maxRevLK+1)
		cmps = append(cmps, noKeyUpdate, noLeaseUpdate)
	}
	return cmps, nil
}

func (txn *txnLeasing) guard(ops []v3.Op) ([]v3.Cmp, error) {
	cmps := txn.guardKeys(ops)
	rangeCmps, err := txn.guardRanges(ops)
	return append(cmps, rangeCmps...), err
}

func (txn *txnLeasing) commitToCache(txnResp *v3pb.TxnResponse, userTxn v3.Op) {
	ops := gatherResponseOps(txnResp.Responses, []v3.Op{userTxn})
	txn.lkv.leases.mu.Lock()
	for _, op := range ops {
		key := string(op.KeyBytes())
		if op.IsDelete() && len(op.RangeBytes()) > 0 {
			end := string(op.RangeBytes())
			for k := range txn.lkv.leases.entries {
				if inRange(k, key, end) {
					txn.lkv.leases.delete(k, txnResp.Header)
				}
			}
		} else if op.IsDelete() {
			txn.lkv.leases.delete(key, txnResp.Header)
		}
		if op.IsPut() {
			txn.lkv.leases.Update(op.KeyBytes(), op.ValueBytes(), txnResp.Header)
		}
	}
	txn.lkv.leases.mu.Unlock()
}

func (txn *txnLeasing) revokeFallback(fbResps []*v3pb.ResponseOp) error {
	for _, resp := range fbResps {
		_, err := txn.lkv.revokeLeaseKvs(txn.ctx, resp.GetResponseRange().Kvs)
		if err != nil {
			return err
		}
	}
	return nil
}

func (txn *txnLeasing) serverTxn() (*v3.TxnResponse, error) {
	if err := txn.lkv.waitSession(txn.ctx); err != nil {
		return nil, err
	}

	userOps := gatherOps(append(txn.opst, txn.opse...))
	userTxn := v3.OpTxn(txn.cs, txn.opst, txn.opse)
	fbOps := txn.fallback(userOps)

	defer closeAll(txn.lkv.leases.LockWriteOps(userOps))
	for {
		cmps, err := txn.guard(userOps)
		if err != nil {
			return nil, err
		}
		resp, err := txn.lkv.kv.Txn(txn.ctx).If(cmps...).Then(userTxn).Else(fbOps...).Commit()
		if err != nil {
			for _, cmp := range cmps {
				txn.lkv.leases.Evict(strings.TrimPrefix(string(cmp.KeyBytes()), txn.lkv.pfx))
			}
			return nil, err
		}
		if resp.Succeeded {
			txn.commitToCache((*v3pb.TxnResponse)(resp), userTxn)
			userResp := resp.Responses[0].GetResponseTxn()
			userResp.Header = resp.Header
			return (*v3.TxnResponse)(userResp), nil
		}
		if err := txn.revokeFallback(resp.Responses); err != nil {
			return nil, err
		}
	}
}
