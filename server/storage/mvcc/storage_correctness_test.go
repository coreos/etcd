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

package mvcc

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math/rand"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anishathalye/porcupine"
	"github.com/stretchr/testify/require"

	"go.etcd.io/etcd/api/v3/mvccpb"
	"go.etcd.io/etcd/pkg/v3/traceutil"
	"go.etcd.io/etcd/server/v3/lease"
)

type StorageOpType int

const (
	OpPut StorageOpType = iota
	OpGet
	OpRange
	OpDelete
	OpDeleteRange
	OpCompact
	OpTxn
)

type TxnSubOpType int

const (
	TxnSubOpPut TxnSubOpType = iota
	TxnSubOpDeleteRange
)

type TxnSubOp struct {
	Type  TxnSubOpType
	Key   []byte
	End   []byte
	Value []byte
}

type StorageRequest struct {
	Op        StorageOpType
	Key       []byte
	End       []byte
	Value     []byte
	Rev       int64
	Limit     int64
	CountOnly bool
	TxnOps    []TxnSubOp
}

type StorageResponse struct {
	KVs   []*mvccpb.KeyValue
	Count int
	Rev   int64
	Err   error
}

type storageLinearState struct {
	items      map[string]*mvccpb.KeyValue
	compactRev int64
	currentRev int64
}

func matchKeyRange(k string, key, end []byte) bool {
	if end == nil {
		return k == string(key)
	}
	if len(end) == 0 {
		return k >= string(key)
	}
	return k >= string(key) && k < string(end)
}

func (s *storageLinearState) clone() *storageLinearState {
	n := &storageLinearState{
		items:      make(map[string]*mvccpb.KeyValue, len(s.items)),
		compactRev: s.compactRev,
		currentRev: s.currentRev,
	}
	for k, v := range s.items {
		n.items[k] = &mvccpb.KeyValue{
			Key:            append([]byte(nil), v.Key...),
			Value:          append([]byte(nil), v.Value...),
			CreateRevision: v.CreateRevision,
			ModRevision:    v.ModRevision,
			Version:        v.Version,
			Lease:          v.Lease,
		}
	}
	return n
}

var storagePorcupineModel = porcupine.Model{
	Init: func() any {
		return &storageLinearState{
			items:      make(map[string]*mvccpb.KeyValue),
			compactRev: 0,
			currentRev: 1,
		}
	},
	Step: func(state, input, output any) (bool, any) {
		st := state.(*storageLinearState)
		req := input.(StorageRequest)
		res := output.(StorageResponse)

		switch req.Op {
		case OpPut:
			if res.Err != nil {
				return false, state
			}
			nextState := st.clone()
			nextState.currentRev++

			var createRev int64 = nextState.currentRev
			var ver int64 = 1
			if existing, exists := st.items[string(req.Key)]; exists {
				createRev = existing.CreateRevision
				ver = existing.Version + 1
			}

			nextState.items[string(req.Key)] = &mvccpb.KeyValue{
				Key:            append([]byte(nil), req.Key...),
				Value:          append([]byte(nil), req.Value...),
				CreateRevision: createRev,
				ModRevision:    nextState.currentRev,
				Version:        ver,
			}
			return true, nextState

		case OpDelete:
			if res.Err != nil {
				return false, state
			}
			nextState := st.clone()
			if _, exists := nextState.items[string(req.Key)]; exists {
				nextState.currentRev++
				delete(nextState.items, string(req.Key))
			}
			return true, nextState

		case OpDeleteRange:
			if res.Err != nil {
				return false, state
			}
			nextState := st.clone()
			deleted := false
			for k := range nextState.items {
				if matchKeyRange(k, req.Key, req.End) {
					delete(nextState.items, k)
					deleted = true
				}
			}
			if deleted {
				nextState.currentRev++
			}
			return true, nextState

		case OpTxn:
			if res.Err != nil {
				return false, state
			}
			nextState := st.clone()
			hasChanges := false
			for _, sub := range req.TxnOps {
				switch sub.Type {
				case TxnSubOpPut:
					hasChanges = true
					var createRev int64 = nextState.currentRev + 1
					var ver int64 = 1
					if existing, exists := nextState.items[string(sub.Key)]; exists {
						createRev = existing.CreateRevision
						ver = existing.Version + 1
					}
					nextState.items[string(sub.Key)] = &mvccpb.KeyValue{
						Key:            append([]byte(nil), sub.Key...),
						Value:          append([]byte(nil), sub.Value...),
						CreateRevision: createRev,
						ModRevision:    nextState.currentRev + 1,
						Version:        ver,
					}
				case TxnSubOpDeleteRange:
					deleted := false
					for k := range nextState.items {
						if matchKeyRange(k, sub.Key, sub.End) {
							delete(nextState.items, k)
							deleted = true
						}
					}
					if deleted {
						hasChanges = true
					}
				}
			}
			if hasChanges {
				nextState.currentRev++
			}
			return true, nextState

		case OpCompact:
			if req.Rev > st.currentRev {
				if errors.Is(res.Err, ErrFutureRev) {
					return true, state
				}
				return false, state
			}
			if req.Rev <= st.compactRev {
				if errors.Is(res.Err, ErrCompacted) {
					return true, state
				}
				return false, state
			}
			if res.Err != nil {
				return false, state
			}
			nextState := st.clone()
			nextState.compactRev = req.Rev
			return true, nextState

		case OpGet:
			// 1. Future revision check
			if req.Rev > st.currentRev {
				if errors.Is(res.Err, ErrFutureRev) {
					return true, state
				}
				return false, state
			}
			// 2. Compacted revision check
			if req.Rev > 0 && req.Rev < st.compactRev {
				if errors.Is(res.Err, ErrCompacted) {
					return true, state
				}
				return false, state
			}
			// 3. Concurrent latest read overtaken by compaction
			if req.Rev == 0 && errors.Is(res.Err, ErrCompacted) {
				if st.compactRev > 0 {
					return true, state
				}
				return false, state
			}
			if res.Err != nil {
				return false, state
			}
			// 4. Available historical read (payload validated in phase 2)
			if req.Rev > 0 {
				return true, state
			}

			// 5. Latest state read (req.Rev == 0)
			expectedKV, exists := st.items[string(req.Key)]
			if !exists {
				if len(res.KVs) == 0 {
					return true, state
				}
				return false, state
			}
			if len(res.KVs) != 1 {
				return false, state
			}
			act := res.KVs[0]
			if !bytes.Equal(expectedKV.Key, act.Key) ||
				!bytes.Equal(expectedKV.Value, act.Value) ||
				expectedKV.CreateRevision != act.CreateRevision ||
				expectedKV.ModRevision != act.ModRevision ||
				expectedKV.Version != act.Version {
				return false, state
			}
			return true, state

		case OpRange:
			// 1. Future revision check
			if req.Rev > st.currentRev {
				if errors.Is(res.Err, ErrFutureRev) {
					return true, state
				}
				return false, state
			}
			// 2. Compacted revision check
			if req.Rev > 0 && req.Rev < st.compactRev {
				if errors.Is(res.Err, ErrCompacted) {
					return true, state
				}
				return false, state
			}
			// 3. Concurrent latest read overtaken by compaction
			if req.Rev == 0 && errors.Is(res.Err, ErrCompacted) {
				if st.compactRev > 0 {
					return true, state
				}
				return false, state
			}
			if res.Err != nil {
				return false, state
			}
			// 4. Available historical read (payload validated in phase 2)
			if req.Rev > 0 {
				return true, state
			}

			// 5. Latest state range (req.Rev == 0)
			var expectedKVs []*mvccpb.KeyValue
			for k, v := range st.items {
				if matchKeyRange(k, req.Key, req.End) {
					expectedKVs = append(expectedKVs, v)
				}
			}
			sort.Slice(expectedKVs, func(i, j int) bool {
				return bytes.Compare(expectedKVs[i].Key, expectedKVs[j].Key) < 0
			})

			totalCount := len(expectedKVs)
			if req.CountOnly {
				if len(res.KVs) != 0 || res.Count != totalCount {
					return false, state
				}
				return true, state
			}

			if req.Limit > 0 && int64(len(expectedKVs)) > req.Limit {
				expectedKVs = expectedKVs[:req.Limit]
			}

			if len(expectedKVs) != len(res.KVs) {
				return false, state
			}
			for i := range expectedKVs {
				exp := expectedKVs[i]
				act := res.KVs[i]
				if !bytes.Equal(exp.Key, act.Key) ||
					!bytes.Equal(exp.Value, act.Value) ||
					exp.CreateRevision != act.CreateRevision ||
					exp.ModRevision != act.ModRevision ||
					exp.Version != act.Version {
					return false, state
				}
			}
			return true, state
		default:
			panic("unkown client op")
		}
	},
	Equal: func(s1, s2 any) bool {
		st1 := s1.(*storageLinearState)
		st2 := s2.(*storageLinearState)
		if st1.compactRev != st2.compactRev || st1.currentRev != st2.currentRev {
			return false
		}
		if len(st1.items) != len(st2.items) {
			return false
		}
		for k, v1 := range st1.items {
			v2, ok := st2.items[k]
			if !ok {
				return false
			}
			if !bytes.Equal(v1.Key, v2.Key) || !bytes.Equal(v1.Value, v2.Value) ||
				v1.CreateRevision != v2.CreateRevision || v1.ModRevision != v2.ModRevision || v1.Version != v2.Version {
				return false
			}
		}
		return true
	},
	DescribeOperation: func(input, output any) string {
		req := input.(StorageRequest)
		res := output.(StorageResponse)
		revSuffix := ""
		if req.Rev > 0 {
			revSuffix = fmt.Sprintf(" [rev=%d]", req.Rev)
		}
		switch req.Op {
		case OpPut:
			return fmt.Sprintf("Put(%q, %q)", string(req.Key), string(req.Value))
		case OpDelete:
			return fmt.Sprintf("Delete(%q)", string(req.Key))
		case OpDeleteRange:
			return fmt.Sprintf("DeleteRange(%q, %q)", string(req.Key), string(req.End))
		case OpTxn:
			var subDescs []string
			for _, sub := range req.TxnOps {
				switch sub.Type {
				case TxnSubOpPut:
					subDescs = append(subDescs, fmt.Sprintf("Put(%q,%q)", string(sub.Key), string(sub.Value)))
				case TxnSubOpDeleteRange:
					subDescs = append(subDescs, fmt.Sprintf("Del(%q..%q)", string(sub.Key), string(sub.End)))
				}
			}
			return fmt.Sprintf("Txn([%s])", strings.Join(subDescs, ", "))
		case OpCompact:
			if res.Err != nil {
				return fmt.Sprintf("Compact(%d) -> err:%v", req.Rev, res.Err)
			}
			return fmt.Sprintf("Compact(%d) -> ok", req.Rev)
		case OpGet:
			if res.Err != nil {
				return fmt.Sprintf("Get(%q)%s -> err:%v", string(req.Key), revSuffix, res.Err)
			}
			if len(res.KVs) == 0 {
				return fmt.Sprintf("Get(%q)%s -> <nil>", string(req.Key), revSuffix)
			}
			kv := res.KVs[0]
			return fmt.Sprintf("Get(%q)%s -> %q (c=%d,m=%d,v=%d)", string(req.Key), revSuffix, string(kv.Value), kv.CreateRevision, kv.ModRevision, kv.Version)
		case OpRange:
			if res.Err != nil {
				return fmt.Sprintf("Range(%q..%q)%s -> err:%v", string(req.Key), string(req.End), revSuffix, res.Err)
			}
			if req.CountOnly {
				return fmt.Sprintf("RangeCount(%q..%q)%s -> count:%d", string(req.Key), string(req.End), revSuffix, res.Count)
			}
			var pairs []string
			for _, kv := range res.KVs {
				pairs = append(pairs, fmt.Sprintf("%q:%q(c=%d,m=%d,v=%d)", string(kv.Key), string(kv.Value), kv.CreateRevision, kv.ModRevision, kv.Version))
			}
			limitSuffix := ""
			if req.Limit > 0 {
				limitSuffix = fmt.Sprintf(" [limit=%d]", req.Limit)
			}
			return fmt.Sprintf("Range(%q..%q)%s%s -> [%s]", string(req.Key), string(req.End), limitSuffix, revSuffix, strings.Join(pairs, ", "))
		default:
			return "UnknownOp"
		}
	},
	DescribeState: func(st any) string {
		state := st.(*storageLinearState)
		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("<p style=\"margin: 0.25em 0;\">rev: %d, compactRev: %d</p>", state.currentRev, state.compactRev))
		keys := make([]string, 0, len(state.items))
		for k := range state.items {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		if len(keys) > 0 {
			sb.WriteString("<ul style=\"margin: 0.25em 0;\">")
			for _, k := range keys {
				kv := state.items[k]
				sb.WriteString(fmt.Sprintf("<li><b>%s</b>: %s (c=%d, m=%d, v=%d)</li>",
					k, string(kv.Value), kv.CreateRevision, kv.ModRevision, kv.Version))
			}
			sb.WriteString("</ul>")
		} else {
			sb.WriteString("<p style=\"margin: 0.25em 0;\"><i>empty</i></p>")
		}
		return sb.String()
	},
}

func TestStorageCorrectness(t *testing.T) {
	for _, driver := range DefaultStorageDrivers {
		t.Run(driver.Name, func(t *testing.T) {
			dir := t.TempDir()
			bs, err := driver.Setup(dir)
			require.NoError(t, err)
			defer bs.Close()
			testStorageCorrectness(t, bs.store)
		})
	}
}

type choiceWeight[T any] struct {
	choice T
	weight int
}

func pickRandom[T any](r *rand.Rand, choices []choiceWeight[T]) T {
	sum := 0
	for _, op := range choices {
		sum += op.weight
	}
	roll := r.Intn(sum)
	for _, op := range choices {
		if roll < op.weight {
			return op.choice
		}
		roll -= op.weight
	}
	panic("unexpected")
}

type clientOpType int

const (
	clientPut clientOpType = iota
	clientGet
	clientRange
	clientDelete
	clientDeleteRange
	clientTxn
	clientCompact
	clientReadTxn
)

var defaultOpChoices = []choiceWeight[clientOpType]{
	{choice: clientPut, weight: 25},
	{choice: clientGet, weight: 15},
	{choice: clientRange, weight: 15},
	{choice: clientDelete, weight: 10},
	{choice: clientDeleteRange, weight: 10},
	{choice: clientTxn, weight: 10},
	{choice: clientCompact, weight: 10},
	{choice: clientReadTxn, weight: 10},
}

func testStorageCorrectness(t *testing.T, store WatchableKV) {
	const numClients = 6
	const testDuration = 2 * time.Second
	keys := [][]byte{
		[]byte("key-0"),
		[]byte("key-1"),
		[]byte("key-2"),
		[]byte("key-3"),
		[]byte("key-4"),
	}

	var ops []porcupine.Operation
	var opsMu sync.Mutex
	var valCounter int64
	var maxCommittedRev int64 = 1
	var maxCompactedRev int64 = 0

	var wg sync.WaitGroup
	wg.Add(numClients)

	startGate := make(chan struct{})
	var startTime time.Time
	var stopTime time.Time

	for clientID := 0; clientID < numClients; clientID++ {
		go func(cid int) {
			defer wg.Done()
			<-startGate
			r := rand.New(rand.NewSource(int64(cid*997 + 13)))

			var localOps []porcupine.Operation

			for time.Now().Before(stopTime) {
				k := keys[r.Intn(len(keys))]
				opChoice := pickRandom(r, defaultOpChoices)

				var req StorageRequest
				var res StorageResponse

				callTime := time.Since(startTime).Nanoseconds()

				switch opChoice {
				case clientPut:
					v := []byte(fmt.Sprintf("v%d-c%d", atomic.AddInt64(&valCounter, 1), cid))
					req = StorageRequest{Op: OpPut, Key: k, Value: v}
					tw := store.Write(traceutil.TODO())
					rev := tw.Put(k, v, lease.NoLease)
					tw.End()
					res.Rev = rev
					if rev > atomic.LoadInt64(&maxCommittedRev) {
						atomic.StoreInt64(&maxCommittedRev, rev)
					}

				case clientGet:
					var targetRev int64
					if r.Intn(100) < 60 { // 60% historical query, 40% latest (Rev=0)
						cur := atomic.LoadInt64(&maxCommittedRev)
						delta := int64(r.Intn(21) - 10) // [-10, +10]
						targetRev = cur + delta
						if targetRev < 0 {
							targetRev = 0
						}
					}
					req = StorageRequest{Op: OpGet, Key: k, Rev: targetRev}
					rr, err := store.Range(context.Background(), k, nil, RangeOptions{Rev: targetRev})
					res.Err = err
					if err == nil && rr != nil && len(rr.KVs) > 0 {
						res.KVs = []*mvccpb.KeyValue{rr.KVs[0]}
					}

				case clientRange:
					var startK, endK []byte
					rangeType := r.Intn(10)
					if rangeType < 2 { // 20% single key query via Range
						startK = keys[r.Intn(len(keys))]
						endK = nil
					} else if rangeType < 4 { // 20% unbounded range query (all keys >= startK)
						startK = keys[r.Intn(len(keys))]
						endK = []byte{}
					} else { // 60% bounded range
						startIdx := r.Intn(len(keys) / 2)
						endIdx := startIdx + 1 + r.Intn(len(keys)-startIdx)
						if endIdx >= len(keys) {
							endIdx = len(keys) - 1
						}
						startK = keys[startIdx]
						endK = keys[endIdx]
					}
					limit := int64(0)
					if r.Intn(10) < 4 { // 40% limited range
						limit = int64(1 + r.Intn(3))
					}
					countOnly := r.Intn(10) < 2 // 20% count-only range
					var targetRev int64
					if r.Intn(100) < 60 { // 60% historical query, 40% latest (Rev=0)
						cur := atomic.LoadInt64(&maxCommittedRev)
						delta := int64(r.Intn(21) - 10) // [-10, +10]
						targetRev = cur + delta
						if targetRev < 0 {
							targetRev = 0
						}
					}
					req = StorageRequest{
						Op:        OpRange,
						Key:       startK,
						End:       endK,
						Rev:       targetRev,
						Limit:     limit,
						CountOnly: countOnly,
					}
					rr, err := store.Range(context.Background(), startK, endK, RangeOptions{
						Rev:       targetRev,
						Limit:     limit,
						CountOnly: countOnly,
					})
					res.Err = err
					if err == nil && rr != nil {
						res.KVs = rr.KVs
						res.Count = rr.Count
					}

				case clientDelete:
					req = StorageRequest{Op: OpDelete, Key: k}
					tw := store.Write(traceutil.TODO())
					n, rev := tw.DeleteRange(k, nil)
					tw.End()
					if n > 0 {
						res.Rev = rev
						if rev > atomic.LoadInt64(&maxCommittedRev) {
							atomic.StoreInt64(&maxCommittedRev, rev)
						}
					}

				case clientDeleteRange:
					var startK, endK []byte
					if r.Intn(10) < 2 { // 20% unbounded delete range
						startK = keys[r.Intn(len(keys))]
						endK = []byte{}
					} else {
						startIdx := r.Intn(len(keys) / 2)
						endIdx := startIdx + 1 + r.Intn(len(keys)-startIdx)
						if endIdx >= len(keys) {
							endIdx = len(keys) - 1
						}
						startK = keys[startIdx]
						endK = keys[endIdx]
					}
					req = StorageRequest{Op: OpDeleteRange, Key: startK, End: endK}
					tw := store.Write(traceutil.TODO())
					n, rev := tw.DeleteRange(startK, endK)
					tw.End()
					if n > 0 {
						res.Rev = rev
						if rev > atomic.LoadInt64(&maxCommittedRev) {
							atomic.StoreInt64(&maxCommittedRev, rev)
						}
					}

				case clientTxn:
					numSub := 1 + r.Intn(3)
					subOps := make([]TxnSubOp, numSub)
					tw := store.Write(traceutil.TODO())
					var finalRev int64
					var anyMod bool
					for sIdx := 0; sIdx < numSub; sIdx++ {
						subK := keys[r.Intn(len(keys))]
						if r.Intn(2) == 0 {
							subV := []byte(fmt.Sprintf("v%d-c%d-s%d", atomic.AddInt64(&valCounter, 1), cid, sIdx))
							subOps[sIdx] = TxnSubOp{Type: TxnSubOpPut, Key: subK, Value: subV}
							rev := tw.Put(subK, subV, lease.NoLease)
							finalRev = rev
							anyMod = true
						} else {
							var endK []byte
							if r.Intn(2) == 0 {
								endK = nil
							} else {
								startIdx := r.Intn(len(keys) / 2)
								endIdx := startIdx + 1 + r.Intn(len(keys)-startIdx)
								if endIdx >= len(keys) {
									endIdx = len(keys) - 1
								}
								subK = keys[startIdx]
								endK = keys[endIdx]
							}
							subOps[sIdx] = TxnSubOp{Type: TxnSubOpDeleteRange, Key: subK, End: endK}
							n, rev := tw.DeleteRange(subK, endK)
							if n > 0 {
								finalRev = rev
								anyMod = true
							}
						}
					}

					// Validate in-txn read-your-own-writes before End()
					if anyMod && len(subOps) > 0 {
						lastSub := subOps[len(subOps)-1]
						if lastSub.Type == TxnSubOpPut {
							rr, err := tw.Range(context.Background(), lastSub.Key, nil, RangeOptions{})
							if err == nil && len(rr.KVs) > 0 {
								require.Equal(t, lastSub.Value, rr.KVs[0].Value, "in-txn read must see latest put value")
							}
						}
					}

					tw.End()
					req = StorageRequest{Op: OpTxn, TxnOps: subOps}
					if anyMod && finalRev > 0 {
						res.Rev = finalRev
						if finalRev > atomic.LoadInt64(&maxCommittedRev) {
							atomic.StoreInt64(&maxCommittedRev, finalRev)
						}
					}

				case clientCompact:
					cur := atomic.LoadInt64(&maxCommittedRev)
					prevComp := atomic.LoadInt64(&maxCompactedRev)
					var targetRev int64
					if cur > 1 {
						targetRev = 1 + int64(r.Intn(int(cur)))
					} else {
						targetRev = 1
					}
					req = StorageRequest{Op: OpCompact, Rev: targetRev}
					donec, err := store.Compact(traceutil.TODO(), targetRev)
					if err == nil && donec != nil {
						<-donec
						if targetRev > prevComp {
							atomic.StoreInt64(&maxCompactedRev, targetRev)
						}
					}
					res.Err = err
				case clientReadTxn:
					mode := ConcurrentReadTxMode
					if r.Intn(2) == 0 {
						mode = SharedBufReadTxMode
					}
					readTx := store.Read(mode, traceutil.TODO())
					snapRev := readTx.Rev()

					var startK, endK []byte
					if r.Intn(2) == 0 {
						startK = keys[r.Intn(len(keys))]
						endK = nil
					} else {
						startK = keys[0]
						endK = []byte{}
					}

					rr, err := readTx.Range(context.Background(), startK, endK, RangeOptions{})
					readTx.End()

					req = StorageRequest{
						Op:  OpRange,
						Key: startK,
						End: endK,
						Rev: snapRev,
					}
					res.Err = err
					if err == nil && rr != nil {
						res.KVs = rr.KVs
						res.Count = rr.Count
						for _, kv := range rr.KVs {
							require.LessOrEqual(t, kv.ModRevision, snapRev, "Read transaction must not observe mutations newer than its snapshot revision")
						}
					}
				default:
					panic("unknown client op")
				}

				returnTime := time.Since(startTime).Nanoseconds()

				localOps = append(localOps, porcupine.Operation{
					ClientId: cid,
					Input:    req,
					Call:     callTime,
					Output:   res,
					Return:   returnTime,
				})

				time.Sleep(time.Duration(1+r.Intn(2)) * time.Millisecond)
			}

			opsMu.Lock()
			ops = append(ops, localOps...)
			opsMu.Unlock()
		}(clientID)
	}

	var watchWg sync.WaitGroup
	var watchedEvents []*mvccpb.Event
	var watchMu sync.Mutex
	watchStream := store.NewWatchStream()
	defer watchStream.Close()

	_, err := watchStream.Watch(context.Background(), 1, []byte("key-0"), []byte("key-5"), 0)
	require.NoError(t, err)

	watchDone := make(chan struct{})
	watchWg.Add(1)
	go func() {
		defer watchWg.Done()
		for {
			select {
			case <-watchDone:
				for {
					select {
					case resp := <-watchStream.Chan():
						watchMu.Lock()
						watchedEvents = append(watchedEvents, resp.Events...)
						watchMu.Unlock()
					default:
						return
					}
				}
			case resp := <-watchStream.Chan():
				watchMu.Lock()
				watchedEvents = append(watchedEvents, resp.Events...)
				watchMu.Unlock()
			}
		}
	}()

	startTime = time.Now()
	stopTime = startTime.Add(testDuration)
	close(startGate)

	wg.Wait()
	close(watchDone)
	watchWg.Wait()

	duration := time.Since(startTime)
	totalOps := len(ops)
	qps := float64(totalOps) / duration.Seconds()
	t.Logf("Total Operations: %d, Concurrency: %d, Elapsed: %v, Throughput: %.2f ops/sec", totalOps, numClients, duration, qps)

	res, info := porcupine.CheckOperationsVerbose(storagePorcupineModel, ops, 30*time.Second)
	if res != porcupine.Ok {
		testName := strings.ReplaceAll(t.Name(), "/", "_")
		htmlPath := fmt.Sprintf("%s.html", testName)
		_ = porcupine.VisualizePath(storagePorcupineModel, info, htmlPath)
		t.Logf("Saved Porcupine visualization to %s", htmlPath)
	}
	require.Equal(t, porcupine.Ok, res, "Global storage operations must be linearizable")

	replay := newStorageReplay(ops)
	validateHistoricalOperations(t, replay, ops)

	validateWatchedEvents(t, replay, watchedEvents)
}

func validateWatchedEvents(t *testing.T, replay *StorageReplay, events []*mvccpb.Event) {
	require.NotEmpty(t, events, "Watch stream should have received events")
	require.LessOrEqual(t, len(events), len(replay.expectedEvents), "Watch stream should not receive more events than expected")

	for i, ev := range events {
		expected := replay.expectedEvents[i]
		require.Equal(t, expected.Type, ev.Type, "Event[%d] Type mismatch", i)
		require.Equal(t, expected.Kv.Key, ev.Kv.Key, "Event[%d] Key mismatch", i)
		require.Equal(t, expected.Kv.ModRevision, ev.Kv.ModRevision, "Event[%d] ModRevision mismatch", i)
		if expected.Type == mvccpb.Event_PUT {
			require.Equal(t, expected.Kv.Value, ev.Kv.Value, "Event[%d] Value mismatch at rev %d", i, expected.Kv.ModRevision)
			require.Equal(t, expected.Kv.CreateRevision, ev.Kv.CreateRevision, "Event[%d] CreateRevision mismatch", i)
			require.Equal(t, expected.Kv.Version, ev.Kv.Version, "Event[%d] Version mismatch", i)
		}
	}
}

type StorageReplay struct {
	revisionToState []map[string]*mvccpb.KeyValue
	expectedEvents  []*mvccpb.Event
}

func newStorageReplay(operations []porcupine.Operation) *StorageReplay {
	type writeOp struct {
		rev int64
		req StorageRequest
	}
	var writes []writeOp

	for _, op := range operations {
		req := op.Input.(StorageRequest)
		res := op.Output.(StorageResponse)
		if res.Err != nil {
			continue
		}
		switch req.Op {
		case OpPut, OpDelete, OpDeleteRange, OpTxn:
			if res.Rev > 0 {
				writes = append(writes, writeOp{
					rev: res.Rev,
					req: req,
				})
			}
		}
	}

	sort.Slice(writes, func(i, j int) bool {
		return writes[i].rev < writes[j].rev
	})

	currentState := make(map[string]*mvccpb.KeyValue)
	revisionToState := []map[string]*mvccpb.KeyValue{
		make(map[string]*mvccpb.KeyValue),
		make(map[string]*mvccpb.KeyValue),
	}
	var expectedEvents []*mvccpb.Event

	for _, w := range writes {
		for int64(len(revisionToState)) < w.rev {
			snap := make(map[string]*mvccpb.KeyValue, len(currentState))
			for k, v := range currentState {
				snap[k] = &mvccpb.KeyValue{
					Key:            append([]byte(nil), v.Key...),
					Value:          append([]byte(nil), v.Value...),
					CreateRevision: v.CreateRevision,
					ModRevision:    v.ModRevision,
					Version:        v.Version,
					Lease:          v.Lease,
				}
			}
			revisionToState = append(revisionToState, snap)
		}

		switch w.req.Op {
		case OpPut:
			var createRev int64 = w.rev
			var ver int64 = 1
			if existing, ok := currentState[string(w.req.Key)]; ok {
				createRev = existing.CreateRevision
				ver = existing.Version + 1
			}
			kv := &mvccpb.KeyValue{
				Key:            append([]byte(nil), w.req.Key...),
				Value:          append([]byte(nil), w.req.Value...),
				CreateRevision: createRev,
				ModRevision:    w.rev,
				Version:        ver,
			}
			currentState[string(w.req.Key)] = kv
			expectedEvents = append(expectedEvents, &mvccpb.Event{Type: mvccpb.Event_PUT, Kv: kv})

		case OpDelete:
			if _, ok := currentState[string(w.req.Key)]; ok {
				delKV := &mvccpb.KeyValue{
					Key:         append([]byte(nil), w.req.Key...),
					ModRevision: w.rev,
				}
				expectedEvents = append(expectedEvents, &mvccpb.Event{Type: mvccpb.Event_DELETE, Kv: delKV})
				delete(currentState, string(w.req.Key))
			}

		case OpDeleteRange:
			var toDelete []string
			for k := range currentState {
				if matchKeyRange(k, w.req.Key, w.req.End) {
					toDelete = append(toDelete, k)
				}
			}
			sort.Strings(toDelete)
			for _, k := range toDelete {
				delKV := &mvccpb.KeyValue{
					Key:         []byte(k),
					ModRevision: w.rev,
				}
				expectedEvents = append(expectedEvents, &mvccpb.Event{Type: mvccpb.Event_DELETE, Kv: delKV})
				delete(currentState, k)
			}

		case OpTxn:
			for _, sub := range w.req.TxnOps {
				switch sub.Type {
				case TxnSubOpPut:
					var createRev int64 = w.rev
					var ver int64 = 1
					if existing, ok := currentState[string(sub.Key)]; ok {
						createRev = existing.CreateRevision
						ver = existing.Version + 1
					}
					kv := &mvccpb.KeyValue{
						Key:            append([]byte(nil), sub.Key...),
						Value:          append([]byte(nil), sub.Value...),
						CreateRevision: createRev,
						ModRevision:    w.rev,
						Version:        ver,
					}
					currentState[string(sub.Key)] = kv
					expectedEvents = append(expectedEvents, &mvccpb.Event{Type: mvccpb.Event_PUT, Kv: kv})
				case TxnSubOpDeleteRange:
					var toDel []string
					for k := range currentState {
						if matchKeyRange(k, sub.Key, sub.End) {
							toDel = append(toDel, k)
						}
					}
					sort.Strings(toDel)
					for _, k := range toDel {
						delKV := &mvccpb.KeyValue{
							Key:         []byte(k),
							ModRevision: w.rev,
						}
						expectedEvents = append(expectedEvents, &mvccpb.Event{Type: mvccpb.Event_DELETE, Kv: delKV})
						delete(currentState, k)
					}
				}
			}
		}

		snap := make(map[string]*mvccpb.KeyValue, len(currentState))
		for k, v := range currentState {
			snap[k] = &mvccpb.KeyValue{
				Key:            append([]byte(nil), v.Key...),
				Value:          append([]byte(nil), v.Value...),
				CreateRevision: v.CreateRevision,
				ModRevision:    v.ModRevision,
				Version:        v.Version,
				Lease:          v.Lease,
			}
		}
		if int64(len(revisionToState)) == w.rev {
			revisionToState = append(revisionToState, snap)
		} else if int64(len(revisionToState)) > w.rev {
			revisionToState[w.rev] = snap
		}
	}

	return &StorageReplay{
		revisionToState: revisionToState,
		expectedEvents:  expectedEvents,
	}
}

func (r *StorageReplay) StateForRevision(rev int64) (map[string]*mvccpb.KeyValue, error) {
	if rev <= 0 {
		return nil, errors.New("invalid revision")
	}
	if int(rev) >= len(r.revisionToState) {
		return nil, ErrFutureRev
	}
	return r.revisionToState[rev], nil
}

func validateHistoricalOperations(t *testing.T, replay *StorageReplay, operations []porcupine.Operation) {
	for i, op := range operations {
		req := op.Input.(StorageRequest)
		res := op.Output.(StorageResponse)

		// Only check successful historical reads
		if req.Rev == 0 || res.Err != nil || (req.Op != OpGet && req.Op != OpRange) {
			continue
		}

		expectedState, err := replay.StateForRevision(req.Rev)
		require.NoError(t, err)

		switch req.Op {
		case OpGet:
			expectedKV, exists := expectedState[string(req.Key)]
			if !exists {
				require.Empty(t, res.KVs, "Op[%d] Get(%s, rev=%d) expected empty, got %v", i, string(req.Key), req.Rev, res.KVs)
			} else {
				require.Len(t, res.KVs, 1, "Op[%d] Get(%s, rev=%d) expected 1 KV", i, string(req.Key), req.Rev)
				act := res.KVs[0]
				require.Equal(t, expectedKV.Key, act.Key, "Op[%d] Get(%s, rev=%d) Key mismatch", i, string(req.Key), req.Rev)
				require.Equal(t, expectedKV.Value, act.Value, "Op[%d] Get(%s, rev=%d) Value mismatch", i, string(req.Key), req.Rev)
				require.Equal(t, expectedKV.CreateRevision, act.CreateRevision, "Op[%d] Get(%s, rev=%d) CreateRevision mismatch", i, string(req.Key), req.Rev)
				require.Equal(t, expectedKV.ModRevision, act.ModRevision, "Op[%d] Get(%s, rev=%d) ModRevision mismatch", i, string(req.Key), req.Rev)
				require.Equal(t, expectedKV.Version, act.Version, "Op[%d] Get(%s, rev=%d) Version mismatch", i, string(req.Key), req.Rev)
			}

		case OpRange:
			var expectedKVs []*mvccpb.KeyValue
			for k, v := range expectedState {
				if matchKeyRange(k, req.Key, req.End) {
					expectedKVs = append(expectedKVs, v)
				}
			}
			sort.Slice(expectedKVs, func(a, b int) bool {
				return bytes.Compare(expectedKVs[a].Key, expectedKVs[b].Key) < 0
			})

			totalCount := len(expectedKVs)
			if req.CountOnly {
				require.Empty(t, res.KVs, "Op[%d] CountOnly expected empty KVs", i)
				require.Equal(t, totalCount, res.Count, "Op[%d] CountOnly Count mismatch", i)
				continue
			}

			if req.Limit > 0 && int64(len(expectedKVs)) > req.Limit {
				expectedKVs = expectedKVs[:req.Limit]
			}

			require.Equal(t, len(expectedKVs), len(res.KVs), "Op[%d] Range(%s..%s, rev=%d, limit=%d) length mismatch", i, string(req.Key), string(req.End), req.Rev, req.Limit)
			for j := range expectedKVs {
				exp := expectedKVs[j]
				act := res.KVs[j]
				require.Equal(t, exp.Key, act.Key, "Op[%d] Range Key[%d] mismatch", i, j)
				require.Equal(t, exp.Value, act.Value, "Op[%d] Range Value[%d] mismatch", i, j)
				require.Equal(t, exp.CreateRevision, act.CreateRevision, "Op[%d] Range CreateRevision[%d] mismatch", i, j)
				require.Equal(t, exp.ModRevision, act.ModRevision, "Op[%d] Range ModRevision[%d] mismatch", i, j)
				require.Equal(t, exp.Version, act.Version, "Op[%d] Range Version[%d] mismatch", i, j)
			}
		}
	}
}
