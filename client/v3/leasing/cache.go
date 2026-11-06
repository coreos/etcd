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

	v3pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	"go.etcd.io/etcd/api/v3/mvccpb"
	v3 "go.etcd.io/etcd/client/v3"
)

const revokeBackoff = 2 * time.Second

type leaseCache struct {
	mu      sync.RWMutex
	entries map[string]*leaseKey
	revokes map[string]time.Time
	header  *v3pb.ResponseHeader
}

type leaseKey struct {
	response *v3.GetResponse
	// rev is the leasing key revision.
	rev   int64
	waitc chan struct{}
}

func (lc *leaseCache) Rev(key string) int64 {
	lc.mu.RLock()
	defer lc.mu.RUnlock()
	if li := lc.entries[key]; li != nil {
		return li.rev
	}
	return 0
}

func (lc *leaseCache) Lock(key string) (chan<- struct{}, int64) {
	lc.mu.Lock()
	defer lc.mu.Unlock()
	if li := lc.entries[key]; li != nil {
		li.waitc = make(chan struct{})
		return li.waitc, li.rev
	}
	return nil, 0
}

func (lc *leaseCache) LockRange(begin, end string) (ret []chan<- struct{}) {
	lc.mu.Lock()
	defer lc.mu.Unlock()
	for k, li := range lc.entries {
		if inRange(k, begin, end) {
			li.waitc = make(chan struct{})
			ret = append(ret, li.waitc)
		}
	}
	return ret
}

func inRange(k, begin, end string) bool {
	if strings.Compare(k, begin) < 0 {
		return false
	}
	if end != "\x00" && strings.Compare(k, end) >= 0 {
		return false
	}
	return true
}

func (lc *leaseCache) LockWriteOps(ops []v3.Op) (ret []chan<- struct{}) {
	for _, op := range ops {
		if op.IsGet() {
			continue
		}
		key := string(op.KeyBytes())
		if end := string(op.RangeBytes()); end == "" {
			if wc, _ := lc.Lock(key); wc != nil {
				ret = append(ret, wc)
			}
		} else {
			ret = append(ret, lc.LockRange(key, end)...)
		}
	}
	return ret
}

func (lc *leaseCache) NotifyOps(ops []v3.Op) (wcs []<-chan struct{}) {
	for _, op := range ops {
		if op.IsGet() {
			if _, wc := lc.notify(string(op.KeyBytes())); wc != nil {
				wcs = append(wcs, wc)
			}
		}
	}
	return wcs
}

func (lc *leaseCache) MayAcquire(key string) bool {
	lc.mu.RLock()
	lr, ok := lc.revokes[key]
	lc.mu.RUnlock()
	return !ok || time.Since(lr) > revokeBackoff
}

func (lc *leaseCache) Add(key string, resp *v3.GetResponse, op v3.Op) *v3.GetResponse {
	lk := &leaseKey{resp, resp.Header.Revision, closedCh}
	lc.mu.Lock()
	if lc.header == nil || lc.header.Revision < resp.Header.Revision {
		lc.header = resp.Header
	}
	lc.entries[key] = lk
	ret := lk.get(op)
	lc.mu.Unlock()
	return ret
}

func (lc *leaseCache) Update(key, val []byte, respHeader *v3pb.ResponseHeader) {
	li := lc.entries[string(key)]
	if li == nil {
		return
	}
	cacheResp := li.response
	if len(cacheResp.Kvs) == 0 {
		kv := &mvccpb.KeyValue{
			Key:            key,
			CreateRevision: respHeader.Revision,
		}
		cacheResp.Kvs = append(cacheResp.Kvs, kv)
		cacheResp.Count = 1
	}
	cacheResp.Kvs[0].Version++
	if cacheResp.Kvs[0].ModRevision < respHeader.Revision {
		cacheResp.Header = respHeader
		cacheResp.Kvs[0].ModRevision = respHeader.Revision
		cacheResp.Kvs[0].Value = val
	}
}

func (lc *leaseCache) Delete(key string, hdr *v3pb.ResponseHeader) {
	lc.mu.Lock()
	defer lc.mu.Unlock()
	lc.delete(key, hdr)
}

func (lc *leaseCache) delete(key string, hdr *v3pb.ResponseHeader) {
	if li := lc.entries[key]; li != nil && hdr.Revision >= li.response.Header.Revision {
		li.response.Kvs = nil
		li.response.Count = 0
		li.response.Header = copyHeader(hdr)
	}
}

func (lc *leaseCache) Evict(key string) (rev int64) {
	lc.mu.Lock()
	defer lc.mu.Unlock()
	if li := lc.entries[key]; li != nil {
		rev = li.rev
		delete(lc.entries, key)
		lc.revokes[key] = time.Now()
	}
	return rev
}

func (lc *leaseCache) EvictRange(key, end string) {
	lc.mu.Lock()
	defer lc.mu.Unlock()
	for k := range lc.entries {
		if inRange(k, key, end) {
			delete(lc.entries, key)
			lc.revokes[key] = time.Now()
		}
	}
}

func isBadOp(op v3.Op) bool { return op.Rev() > 0 || len(op.RangeBytes()) > 0 }

func (lc *leaseCache) Get(ctx context.Context, op v3.Op) (*v3.GetResponse, bool) {
	if isBadOp(op) {
		return nil, false
	}
	key := string(op.KeyBytes())
	li, wc := lc.notify(key)
	if li == nil {
		return nil, true
	}
	select {
	case <-wc:
	case <-ctx.Done():
		return nil, true
	}
	lc.mu.RLock()
	lk := *li
	ret := lk.get(op)
	lc.mu.RUnlock()
	return ret, true
}

func (lk *leaseKey) get(op v3.Op) *v3.GetResponse {
	resp := lk.response
	empty := len(resp.Kvs) == 0 || op.IsCountOnly()
	empty = empty || (op.MinModRev() > resp.Kvs[0].ModRevision)
	empty = empty || (op.MaxModRev() != 0 && op.MaxModRev() < resp.Kvs[0].ModRevision)
	empty = empty || (op.MinCreateRev() > resp.Kvs[0].CreateRevision)
	empty = empty || (op.MaxCreateRev() != 0 && op.MaxCreateRev() < resp.Kvs[0].CreateRevision)

	ret := copyGetResponseMetadataOnly(resp)
	if empty {
		ret.Kvs = nil
	} else {
		ret.Kvs = []*mvccpb.KeyValue{
			copyKeyValue(resp.Kvs[0], op.IsKeysOnly()),
		}
	}
	return ret
}

func (lc *leaseCache) notify(key string) (*leaseKey, <-chan struct{}) {
	lc.mu.RLock()
	defer lc.mu.RUnlock()
	if li := lc.entries[key]; li != nil {
		return li, li.waitc
	}
	return nil, nil
}

func (lc *leaseCache) clearOldRevokes(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Second):
			lc.mu.Lock()
			for k, lr := range lc.revokes {
				if time.Since(lr.Add(revokeBackoff)) > 0 {
					delete(lc.revokes, k)
				}
			}
			lc.mu.Unlock()
		}
	}
}

func (lc *leaseCache) evalCmp(cmps []v3.Cmp) (cmpVal bool, ok bool) {
	for _, cmp := range cmps {
		if len(cmp.GetCompare().GetRangeEnd()) > 0 {
			return false, false
		}
		lk := lc.entries[string(cmp.KeyBytes())]
		if lk == nil {
			return false, false
		}
		if !evalCmp(lk.response, cmp) {
			return false, true
		}
	}
	return true, true
}

func (lc *leaseCache) evalOps(ops []v3.Op) ([]*v3pb.ResponseOp, bool) {
	resps := make([]*v3pb.ResponseOp, len(ops))
	for i, op := range ops {
		if !op.IsGet() || isBadOp(op) {
			// TODO: support read-only Txn
			return nil, false
		}
		lk := lc.entries[string(op.KeyBytes())]
		if lk == nil {
			return nil, false
		}
		resp := lk.get(op)
		if resp == nil {
			return nil, false
		}
		resps[i] = &v3pb.ResponseOp{
			Response: &v3pb.ResponseOp_ResponseRange{
				ResponseRange: (*v3pb.RangeResponse)(resp),
			},
		}
	}
	return resps, true
}
