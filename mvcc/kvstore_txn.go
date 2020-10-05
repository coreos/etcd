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

package mvcc

import (
	"go.etcd.io/etcd/v3/lease"
	"go.etcd.io/etcd/v3/mvcc/backend"
	"go.etcd.io/etcd/v3/mvcc/mvccpb"
	"go.etcd.io/etcd/v3/pkg/traceutil"
	"go.uber.org/zap"
)

const RangeStreamBatch int = 1000

type storeTxnRead struct {
	s  *store
	tx backend.ReadTx

	firstRev int64
	rev      int64

	trace *traceutil.Trace
}

func (s *store) Read(trace *traceutil.Trace) TxnRead {
	s.mu.RLock()
	s.revMu.RLock()
	// backend holds b.readTx.RLock() only when creating the concurrentReadTx. After
	// ConcurrentReadTx is created, it will not block write transaction.
	tx := s.b.ConcurrentReadTx()
	tx.RLock() // RLock is no-op. concurrentReadTx does not need to be locked after it is created.
	firstRev, rev := s.compactMainRev, s.currentRev
	s.revMu.RUnlock()
	return newMetricsTxnRead(&storeTxnRead{s, tx, firstRev, rev, trace})
}

func (tr *storeTxnRead) FirstRev() int64 { return tr.firstRev }
func (tr *storeTxnRead) Rev() int64      { return tr.rev }

func (tr *storeTxnRead) Range(key, end []byte, ro RangeOptions) (r *RangeResult, err error) {
	return tr.rangeKeys(key, end, tr.Rev(), ro)
}

func (tr *storeTxnRead) RangeStream(key, end []byte, ro RangeOptions, streamC chan *RangeResult) (err error) {
	return tr.rangeStreamKeys(key, end, tr.Rev(), ro, streamC)
}

func (tr *storeTxnRead) End() {
	tr.tx.RUnlock() // RUnlock signals the end of concurrentReadTx.
	tr.s.mu.RUnlock()
}

type storeTxnWrite struct {
	storeTxnRead
	tx backend.BatchTx
	// beginRev is the revision where the txn begins; it will write to the next revision.
	beginRev int64
	changes  []mvccpb.KeyValue
}

func (s *store) Write(trace *traceutil.Trace) TxnWrite {
	s.mu.RLock()
	tx := s.b.BatchTx()
	tx.Lock()
	tw := &storeTxnWrite{
		storeTxnRead: storeTxnRead{s, tx, 0, 0, trace},
		tx:           tx,
		beginRev:     s.currentRev,
		changes:      make([]mvccpb.KeyValue, 0, 4),
	}
	return newMetricsTxnWrite(tw)
}

func (tw *storeTxnWrite) Rev() int64 { return tw.beginRev }

func (tw *storeTxnWrite) Range(key, end []byte, ro RangeOptions) (r *RangeResult, err error) {
	rev := tw.beginRev
	if len(tw.changes) > 0 {
		rev++
	}
	return tw.rangeKeys(key, end, rev, ro)
}

func (tw *storeTxnWrite) RangeStream(key, end []byte, ro RangeOptions, streamC chan *RangeResult) (err error) {
	rev := tw.beginRev
	if len(tw.changes) > 0 {
		rev++
	}
	return tw.rangeStreamKeys(key, end, rev, ro, streamC)
}

func (tw *storeTxnWrite) DeleteRange(key, end []byte) (int64, int64) {
	if n := tw.deleteRange(key, end); n != 0 || len(tw.changes) > 0 {
		return n, tw.beginRev + 1
	}
	return 0, tw.beginRev
}

func (tw *storeTxnWrite) Put(key, value []byte, lease lease.LeaseID) int64 {
	tw.put(key, value, lease)
	return tw.beginRev + 1
}

func (tw *storeTxnWrite) End() {
	// only update index if the txn modifies the mvcc state.
	if len(tw.changes) != 0 {
		tw.s.saveIndex(tw.tx)
		// hold revMu lock to prevent new read txns from opening until writeback.
		tw.s.revMu.Lock()
		tw.s.currentRev++
	}
	tw.tx.Unlock()
	if len(tw.changes) != 0 {
		tw.s.revMu.Unlock()
	}
	tw.s.mu.RUnlock()
}

func (tr *storeTxnRead) rangeKeys(key, end []byte, curRev int64, ro RangeOptions) (*RangeResult, error) {
	rev := ro.Rev
	if rev > curRev {
		return &RangeResult{KVs: nil, Count: -1, Rev: curRev}, ErrFutureRev
	}
	if rev <= 0 {
		rev = curRev
	}
	if rev < tr.s.compactMainRev {
		return &RangeResult{KVs: nil, Count: -1, Rev: 0}, ErrCompacted
	}
	if ro.Count {
		total := tr.s.kvindex.CountRevisions(key, end, rev, int(ro.Limit))
		tr.trace.Step("count revisions from in-memory index tree")
		return &RangeResult{KVs: nil, Count: total, Rev: curRev}, nil
	}
	revpairs := tr.s.kvindex.Revisions(key, end, rev, int(ro.Limit))
	tr.trace.Step("range keys from in-memory index tree")
	if len(revpairs) == 0 {
		return &RangeResult{KVs: nil, Count: 0, Rev: curRev}, nil
	}

	limit := int(ro.Limit)
	if limit <= 0 || limit > len(revpairs) {
		limit = len(revpairs)
	}

	kvs := make([]mvccpb.KeyValue, limit)
	revBytes := newRevBytes()
	for i, revpair := range revpairs[:len(kvs)] {
		revToBytes(revpair, revBytes)
		_, vs := tr.tx.UnsafeRange(keyBucketName, revBytes, nil, 0)
		if len(vs) != 1 {
			tr.s.lg.Fatal(
				"range failed to find revision pair",
				zap.Int64("revision-main", revpair.main),
				zap.Int64("revision-sub", revpair.sub),
			)
		}
		if err := kvs[i].Unmarshal(vs[0]); err != nil {
			tr.s.lg.Fatal(
				"failed to unmarshal mvccpb.KeyValue",
				zap.Error(err),
			)
		}
	}
	tr.trace.Step("range keys from bolt db")
	return &RangeResult{KVs: kvs, Count: len(revpairs), Rev: curRev}, nil
}

func (tr *storeTxnRead) rangeStreamKeys(key, end []byte, curRev int64, ro RangeOptions, streamC chan *RangeResult) error {
	defer func() {
		if err := recover(); err != nil {
			switch e := err.(type) {
			case error:
				tr.s.lg.Error(
					"storeTxnRead rangeStreamKeys() panic error", zap.Error(e))
			}
		}
	}()

	defer close(streamC)

	rev := ro.Rev
	if rev > curRev {
		streamC <- &RangeResult{KVs: nil, Count: -1, Rev: curRev, Err: ErrFutureRev}
		return ErrFutureRev
	}
	if rev <= 0 {
		rev = curRev
	}
	if rev < tr.s.compactMainRev {
		streamC <- &RangeResult{KVs: nil, Count: -1, Rev: 0, Err: ErrCompacted}
		return ErrCompacted
	}
	revpairs := tr.s.kvindex.Revisions(key, end, rev, int(ro.Limit))
	tr.trace.Step("range keys from in-memory index tree")
	if len(revpairs) == 0 {
		streamC <- &RangeResult{KVs: nil, Count: 0, Rev: curRev}
		streamC <- nil
		return nil
	}

	limit := int(ro.Limit)
	if limit <= 0 || limit > len(revpairs) {
		limit = len(revpairs)
	}

	var kvsLen int
	totalRevpairsCount := len(revpairs)

	if limit <= RangeStreamBatch {
		kvsLen = limit
	} else {
		kvsLen = RangeStreamBatch
		limit -= RangeStreamBatch
	}
	kvs := make([]mvccpb.KeyValue, kvsLen)

	revBytes := newRevBytes()
	for i, revpair := range revpairs {
		revToBytes(revpair, revBytes)
		_, vs := tr.tx.UnsafeRange(keyBucketName, revBytes, nil, 0)
		if len(vs) != 1 {
			tr.s.lg.Fatal(
				"range failed to find revision pair",
				zap.Int64("revision-main", revpair.main),
				zap.Int64("revision-sub", revpair.sub),
			)
		}
		if err := kvs[i%RangeStreamBatch].Unmarshal(vs[0]); err != nil {
			tr.s.lg.Fatal(
				"failed to unmarshal mvccpb.KeyValue",
				zap.Error(err),
			)
		}
		if (i+1)%RangeStreamBatch == 0 && (i+1) != totalRevpairsCount {
			streamC <- &RangeResult{KVs: kvs, Count: RangeStreamBatch, Rev: curRev}

			if limit <= RangeStreamBatch {
				kvsLen = limit
			} else {
				kvsLen = RangeStreamBatch
				limit -= RangeStreamBatch
			}
			kvs = make([]mvccpb.KeyValue, kvsLen)
		}
	}

	tr.trace.Step("range keys from bolt db")
	streamC <- &RangeResult{KVs: kvs, Count: kvsLen, Rev: curRev}
	streamC <- nil
	return nil
}

func (tw *storeTxnWrite) put(key, value []byte, leaseID lease.LeaseID) {
	rev := tw.beginRev + 1
	c := rev
	oldLease := lease.NoLease

	// if the key exists before, use its previous created and
	// get its previous leaseID
	_, created, ver, err := tw.s.kvindex.Get(key, rev)
	if err == nil {
		c = created.main
		oldLease = tw.s.le.GetLease(lease.LeaseItem{Key: string(key)})
	}
	tw.trace.Step("get key's previous created_revision and leaseID")
	ibytes := newRevBytes()
	idxRev := revision{main: rev, sub: int64(len(tw.changes))}
	revToBytes(idxRev, ibytes)

	ver = ver + 1
	kv := mvccpb.KeyValue{
		Key:            key,
		Value:          value,
		CreateRevision: c,
		ModRevision:    rev,
		Version:        ver,
		Lease:          int64(leaseID),
	}

	d, err := kv.Marshal()
	if err != nil {
		tw.storeTxnRead.s.lg.Fatal(
			"failed to marshal mvccpb.KeyValue",
			zap.Error(err),
		)
	}

	tw.trace.Step("marshal mvccpb.KeyValue")
	tw.tx.UnsafeSeqPut(keyBucketName, ibytes, d)
	tw.s.kvindex.Put(key, idxRev)
	tw.changes = append(tw.changes, kv)
	tw.trace.Step("store kv pair into bolt db")

	if oldLease != lease.NoLease {
		if tw.s.le == nil {
			panic("no lessor to detach lease")
		}
		err = tw.s.le.Detach(oldLease, []lease.LeaseItem{{Key: string(key)}})
		if err != nil {
			tw.storeTxnRead.s.lg.Error(
				"failed to detach old lease from a key",
				zap.Error(err),
			)
		}
	}
	if leaseID != lease.NoLease {
		if tw.s.le == nil {
			panic("no lessor to attach lease")
		}
		err = tw.s.le.Attach(leaseID, []lease.LeaseItem{{Key: string(key)}})
		if err != nil {
			panic("unexpected error from lease Attach")
		}
	}
	tw.trace.Step("attach lease to kv pair")
}

func (tw *storeTxnWrite) deleteRange(key, end []byte) int64 {
	rrev := tw.beginRev
	if len(tw.changes) > 0 {
		rrev++
	}
	keys, _ := tw.s.kvindex.Range(key, end, rrev)
	if len(keys) == 0 {
		return 0
	}
	for _, key := range keys {
		tw.delete(key)
	}
	return int64(len(keys))
}

func (tw *storeTxnWrite) delete(key []byte) {
	ibytes := newRevBytes()
	idxRev := revision{main: tw.beginRev + 1, sub: int64(len(tw.changes))}
	revToBytes(idxRev, ibytes)

	ibytes = appendMarkTombstone(tw.storeTxnRead.s.lg, ibytes)

	kv := mvccpb.KeyValue{Key: key}

	d, err := kv.Marshal()
	if err != nil {
		tw.storeTxnRead.s.lg.Fatal(
			"failed to marshal mvccpb.KeyValue",
			zap.Error(err),
		)
	}

	tw.tx.UnsafeSeqPut(keyBucketName, ibytes, d)
	err = tw.s.kvindex.Tombstone(key, idxRev)
	if err != nil {
		tw.storeTxnRead.s.lg.Fatal(
			"failed to tombstone an existing key",
			zap.String("key", string(key)),
			zap.Error(err),
		)
	}
	tw.changes = append(tw.changes, kv)

	item := lease.LeaseItem{Key: string(key)}
	leaseID := tw.s.le.GetLease(item)

	if leaseID != lease.NoLease {
		err = tw.s.le.Detach(leaseID, []lease.LeaseItem{item})
		if err != nil {
			tw.storeTxnRead.s.lg.Error(
				"failed to detach old lease from a key",
				zap.Error(err),
			)
		}
	}
}

func (tw *storeTxnWrite) Changes() []mvccpb.KeyValue { return tw.changes }
