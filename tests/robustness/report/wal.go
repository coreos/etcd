// Copyright 2024 The etcd Authors
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

package report

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/go-cmp/cmp"
	"go.uber.org/zap"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/testing/protocmp"

	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	"go.etcd.io/etcd/client/pkg/v3/fileutil"
	"go.etcd.io/etcd/server/v3/storage/datadir"
	"go.etcd.io/etcd/server/v3/storage/wal"
	"go.etcd.io/etcd/server/v3/storage/wal/walpb"
	"go.etcd.io/etcd/tests/v3/framework/e2e"
	"go.etcd.io/etcd/tests/v3/robustness/model"
	"go.etcd.io/raft/v3/raftpb"
)

func LoadClusterPersistedRequests(lg *zap.Logger, path string) ([]model.EtcdRequest, error) {
	files, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}
	dataDirs := []string{}
	for _, file := range files {
		if file.IsDir() && strings.HasPrefix(file.Name(), "server-") {
			dataDirs = append(dataDirs, filepath.Join(path, file.Name()))
		}
	}
	return PersistedRequests(lg, dataDirs)
}

func PersistedRequestsCluster(lg *zap.Logger, cluster *e2e.EtcdProcessCluster) ([]model.EtcdRequest, error) {
	dataDirs := []string{}
	for _, proc := range cluster.Procs {
		dataDirs = append(dataDirs, memberDataDir(proc))
	}
	return PersistedRequests(lg, dataDirs)
}

func PersistedRequests(lg *zap.Logger, dataDirs []string) ([]model.EtcdRequest, error) {
	if len(dataDirs) == 0 {
		return nil, errors.New("no data dirs")
	}
	entriesPersistedInWAL := make([][]*raftpb.Entry, len(dataDirs))
	var minCommitIndex uint64 = math.MaxUint64
	for i, dir := range dataDirs {
		state, entries, err := ReadWAL(lg, dir)
		if err != nil {
			lg.Error("Failed to read WAL", zap.Error(err), zap.String("data-dir", dir))
			continue
		}
		minCommitIndex = min(minCommitIndex, state.GetCommit())
		entriesPersistedInWAL[i] = entries
	}
	entries, err := mergeMembersEntries(minCommitIndex, entriesPersistedInWAL)
	if err != nil {
		return nil, err
	}
	persistedRequests := make([]model.EtcdRequest, 0, len(entries))
	for _, e := range entries {
		if e.GetType() != raftpb.EntryNormal {
			continue
		}
		request, err := parseEntryNormal(e)
		if err != nil {
			return nil, err
		}
		if request != nil {
			persistedRequests = append(persistedRequests, *request)
		}
	}
	return persistedRequests, nil
}

func mergeMembersEntries(minCommitIndex uint64, memberEntries [][]*raftpb.Entry) ([]*raftpb.Entry, error) {
	for _, entries := range memberEntries {
		var lastIndex uint64
		for _, e := range entries {
			if e.GetIndex() <= lastIndex {
				return nil, fmt.Errorf("raft index should increase, got: %d, previous: %d", e.GetIndex(), lastIndex)
			}
			lastIndex = e.GetIndex()
		}
	}
	memberIndices := make([]int, len(memberEntries))
	mergedHistory := []*raftpb.Entry{}
	var raftIndex uint64
	for {
		// Find entry with raftIndex.
		raftIndex++
		entriesLeft := false
		for i, entries := range memberEntries {
			memberIndex := memberIndices[i]
			for memberIndex < len(entries) && entries[memberIndex].GetIndex() < raftIndex {
				memberIndex++
			}
			if memberIndex < len(entries) {
				entriesLeft = true
			}
			memberIndices[i] = memberIndex
		}
		if !entriesLeft {
			break
		}
		// Entries collects votes from matching entries.
		votes := make([]int, len(memberEntries))
		for i := 0; i < len(memberEntries); i++ {
			if len(memberEntries[i]) <= memberIndices[i] {
				continue
			}
			entry1 := memberEntries[i][memberIndices[i]]
			if entry1.GetIndex() != raftIndex {
				continue
			}
			for j := i; j < len(memberEntries); j++ {
				if i == j {
					votes[i]++
					continue
				}
				if len(memberEntries[j]) <= memberIndices[j] {
					continue
				}
				entry2 := memberEntries[j][memberIndices[j]]
				if entry2.GetIndex() != raftIndex {
					continue
				}
				if areEntriesEqual(entry1, entry2) {
					votes[i]++
					votes[j]++
				}
			}
		}
		// Select entry with most votes
		topVotes := 0
		for _, vote := range votes {
			if vote > topVotes {
				topVotes = vote
			}
		}
		if topVotes == 0 {
			return nil, fmt.Errorf("no entry for raft index %d", raftIndex)
		}
		var entryWithMostVotes *raftpb.Entry
		for i, vote := range votes {
			if vote != topVotes {
				continue
			}
			entry := memberEntries[i][memberIndices[i]]
			if entryWithMostVotes == nil {
				entryWithMostVotes = entry
				continue
			}
			if entryWithMostVotes.GetTerm() != entry.GetTerm() && entry.GetIndex() > minCommitIndex {
				if entryWithMostVotes.GetTerm() < entry.GetTerm() {
					entryWithMostVotes = entry
				}
				continue
			}
			if !areEntriesEqual(entryWithMostVotes, entry) {
				diff := cmp.Diff(transformRaftEntry(entryWithMostVotes), transformRaftEntry(entry), protocmp.Transform(), protocmp.IgnoreDefaultScalars(), cmp.Comparer(bytes.Equal))
				return nil, fmt.Errorf("mismatching entries on raft index %d, diff: %s", raftIndex, diff)
			}
		}
		mergedHistory = append(mergedHistory, proto.Clone(entryWithMostVotes).(*raftpb.Entry))
	}
	if len(mergedHistory) == 0 {
		return nil, errors.New("no WAL entries matched")
	}
	return mergedHistory, nil
}

type decodedRaftEntry struct {
	Index uint64
	Term  uint64
	Type  raftpb.EntryType
	Data  any
}

func transformRaftEntry(e *raftpb.Entry) decodedRaftEntry {
	if e == nil {
		return decodedRaftEntry{}
	}
	res := decodedRaftEntry{
		Index: e.GetIndex(),
		Term:  e.GetTerm(),
		Type:  e.GetType(),
		Data:  e.GetData(),
	}
	switch e.GetType() {
	case raftpb.EntryNormal:
		var req pb.InternalRaftRequest
		if proto.Unmarshal(e.GetData(), &req) == nil {
			res.Data = &req
		}
	case raftpb.EntryConfChange:
		var cc raftpb.ConfChange
		if proto.Unmarshal(e.GetData(), &cc) == nil {
			res.Data = &cc
		}
	case raftpb.EntryConfChangeV2:
		var cc raftpb.ConfChangeV2
		if proto.Unmarshal(e.GetData(), &cc) == nil {
			res.Data = &cc
		}
	}
	return res
}

func areEntriesEqual(e1, e2 *raftpb.Entry) bool {
	if e1 == nil || e2 == nil {
		return e1 == e2
	}
	if e1.GetIndex() != e2.GetIndex() ||
		e1.GetTerm() != e2.GetTerm() ||
		e1.GetType() != e2.GetType() {
		return false
	}
	return areDataEqual(e1.GetType(), e1.GetData(), e2.GetData())
}

func areDataEqual(t raftpb.EntryType, data1, data2 []byte) bool {
	if bytes.Equal(data1, data2) {
		return true
	}
	switch t {
	case raftpb.EntryNormal:
		var req1, req2 pb.InternalRaftRequest
		if proto.Unmarshal(data1, &req1) == nil && proto.Unmarshal(data2, &req2) == nil {
			return cmp.Equal(&req1, &req2, protocmp.Transform(), protocmp.IgnoreDefaultScalars())
		}
	case raftpb.EntryConfChange:
		var cc1, cc2 raftpb.ConfChange
		if proto.Unmarshal(data1, &cc1) == nil && proto.Unmarshal(data2, &cc2) == nil {
			return cmp.Equal(&cc1, &cc2, protocmp.Transform(), protocmp.IgnoreDefaultScalars())
		}
	case raftpb.EntryConfChangeV2:
		var cc1, cc2 raftpb.ConfChangeV2
		if proto.Unmarshal(data1, &cc1) == nil && proto.Unmarshal(data2, &cc2) == nil {
			return cmp.Equal(&cc1, &cc2, protocmp.Transform(), protocmp.IgnoreDefaultScalars())
		}
	}
	return false
}

func ReadWAL(lg *zap.Logger, dataDir string) (state *raftpb.HardState, ents []*raftpb.Entry, err error) {
	walDir := datadir.ToWALDir(dataDir)
	repaired := false
	for {
		state, ents, err = ReadAllWALEntries(lg, walDir)
		if err != nil {
			// we can only repair ErrUnexpectedEOF and we never repair twice.
			if repaired || !errors.Is(err, io.ErrUnexpectedEOF) {
				return state, nil, fmt.Errorf("failed to read WAL, cannot be repaired, err: %w", err)
			}
			if !wal.Repair(lg, walDir) {
				return state, nil, fmt.Errorf("failed to repair WAL, err: %w", err)
			}
			lg.Info("repaired WAL", zap.Error(err))
			repaired = true
			continue
		}
		return state, ents, nil
	}
}

func parseEntryNormal(ent *raftpb.Entry) (*model.EtcdRequest, error) {
	var raftReq pb.InternalRaftRequest
	if len(ent.Data) == 0 {
		return nil, nil
	}
	if err := proto.Unmarshal(ent.Data, &raftReq); err != nil {
		// PR https://github.com/etcd-io/etcd/pull/21263 removed v2 requests
		// in etcd v3.7. However, robustness always uses the latest protobuf
		// definitions to parse WAL entries generated by all previous versions.
		// etcd v3.4 and v3.5 generate v2 requests during bootstrap, which the
		// v3.7 protobuf can no longer parse. As a result, robustness fails when
		// parsing those WAL entries. We intentionally ignore this error here.
		// See https://github.com/etcd-io/etcd/pull/21263#discussion_r2776042340
		if errors.Is(err, proto.Error) {
			return nil, nil
		}
		return nil, err
	}
	switch {
	case raftReq.Put != nil:
		op := model.PutOptions{
			Key:     string(raftReq.Put.Key),
			Value:   model.ToValueOrHash(string(raftReq.Put.Value)),
			LeaseID: raftReq.Put.Lease,
		}
		request := model.EtcdRequest{
			Type: model.Txn,
			Txn: &model.TxnRequest{
				OperationsOnSuccess: []model.EtcdOperation{
					{Type: model.PutOperation, Put: op},
				},
			},
		}
		return &request, nil
	case raftReq.DeleteRange != nil:
		op := model.DeleteOptions{Key: string(raftReq.DeleteRange.Key)}
		request := model.EtcdRequest{
			Type: model.Txn,
			Txn: &model.TxnRequest{
				OperationsOnSuccess: []model.EtcdOperation{
					{Type: model.DeleteOperation, Delete: op},
				},
			},
		}
		return &request, nil
	case raftReq.LeaseRevoke != nil:
		return &model.EtcdRequest{
			Type:        model.LeaseRevoke,
			LeaseRevoke: &model.LeaseRevokeRequest{LeaseID: raftReq.LeaseRevoke.ID},
		}, nil
	case raftReq.LeaseGrant != nil:
		return &model.EtcdRequest{
			Type:       model.LeaseGrant,
			LeaseGrant: &model.LeaseGrantRequest{LeaseID: raftReq.LeaseGrant.ID},
		}, nil
	case raftReq.ClusterMemberAttrSet != nil:
		return nil, nil
	case raftReq.ClusterVersionSet != nil:
		return nil, nil
	case raftReq.DowngradeInfoSet != nil:
		return nil, nil
	case raftReq.Compaction != nil:
		request := model.EtcdRequest{
			Type:    model.Compact,
			Compact: &model.CompactRequest{Revision: raftReq.Compaction.Revision},
		}
		return &request, nil
	case raftReq.Txn != nil:
		txn := model.TxnRequest{
			Conditions:          []model.EtcdCondition{},
			OperationsOnSuccess: []model.EtcdOperation{},
			OperationsOnFailure: []model.EtcdOperation{},
		}
		for _, cmp := range raftReq.Txn.Compare {
			switch {
			case cmp.Result == pb.Compare_EQUAL && cmp.Target == pb.Compare_VERSION:
				txn.Conditions = append(txn.Conditions, model.EtcdCondition{
					Key:             string(cmp.Key),
					ExpectedVersion: cmp.GetVersion(),
				})
			case cmp.Result == pb.Compare_EQUAL && cmp.Target == pb.Compare_MOD:
				txn.Conditions = append(txn.Conditions, model.EtcdCondition{
					Key:              string(cmp.Key),
					ExpectedRevision: cmp.GetModRevision(),
				})
			default:
				panic(fmt.Sprintf("unsupported condition: %+v", cmp))
			}
		}
		for _, op := range raftReq.Txn.Success {
			txn.OperationsOnSuccess = append(txn.OperationsOnSuccess, toEtcdOperation(op))
		}
		for _, op := range raftReq.Txn.Failure {
			txn.OperationsOnFailure = append(txn.OperationsOnFailure, toEtcdOperation(op))
		}
		request := model.EtcdRequest{
			Type: model.Txn,
			Txn:  &txn,
		}
		return &request, nil
	default:
		panic(fmt.Sprintf("Unhandled raft request: %s", raftReq.String()))
	}
}

func toEtcdOperation(op *pb.RequestOp) (operation model.EtcdOperation) {
	switch {
	case op.GetRequestRange() != nil:
		rangeOp := op.GetRequestRange()
		operation = model.EtcdOperation{
			Type: model.RangeOperation,
			Range: model.RangeOptions{
				Start:    string(rangeOp.Key),
				End:      string(rangeOp.RangeEnd),
				Limit:    rangeOp.Limit,
				KeysOnly: rangeOp.GetKeysOnly(),
			},
		}
	case op.GetRequestPut() != nil:
		putOp := op.GetRequestPut()
		operation = model.EtcdOperation{
			Type: model.PutOperation,
			Put: model.PutOptions{
				Key:   string(putOp.Key),
				Value: model.ToValueOrHash(string(putOp.Value)),
			},
		}
	case op.GetRequestDeleteRange() != nil:
		deleteOp := op.GetRequestDeleteRange()
		operation = model.EtcdOperation{
			Type: model.DeleteOperation,
			Delete: model.DeleteOptions{
				Key: string(deleteOp.Key),
			},
		}
	default:
		panic(fmt.Sprintf("Unknown op type %v", op))
	}
	return operation
}

func ReadAllWALEntries(lg *zap.Logger, dirpath string) (state *raftpb.HardState, ents []*raftpb.Entry, err error) {
	names, err := fileutil.ReadDir(dirpath)
	if err != nil {
		return state, nil, err
	}
	files := make([]fileutil.FileReader, 0, len(names))
	for _, name := range names {
		if !strings.HasSuffix(name, ".wal") {
			continue
		}
		p := filepath.Join(dirpath, name)
		var f *os.File
		f, err = os.OpenFile(p, os.O_RDONLY, fileutil.PrivateFileMode)
		if err != nil {
			return state, nil, fmt.Errorf("os.OpenFile failed (%q): %w", p, err)
		}
		defer f.Close()
		files = append(files, fileutil.NewFileReader(f))
	}
	rec := &walpb.Record{}
	decoder := wal.NewDecoder(files...)
	for err = decoder.Decode(rec); err == nil; err = decoder.Decode(rec) {
		switch rec.GetType() {
		case wal.EntryType:
			e := wal.MustUnmarshalEntry(rec.Data)
			i := len(ents)
			for ; i > 0 && ents[i-1].GetIndex() >= e.GetIndex(); i-- {
			}
			// The line below is potentially overriding some 'uncommitted' entries.
			ents = append(ents[:i], e)
		case wal.StateType:
			state = wal.MustUnmarshalState(rec.Data)
		case wal.MetadataType:
		case wal.CrcType:
			crc := decoder.LastCRC()
			// current crc of decoder must match the crc of the record.
			// do no need to match 0 crc, since the decoder is a new one at this case.
			if crc != 0 && rec.Validate(crc) != nil {
				state.Reset()
				return state, nil, wal.ErrCRCMismatch
			}
			decoder.UpdateCRC(rec.GetCrc())
		case wal.SnapshotType:
		default:
			return state, nil, fmt.Errorf("unexpected block type %d", rec.GetType())
		}
	}
	if err != nil && !errors.Is(err, io.EOF) {
		return state, nil, err
	}
	return state, ents, nil
}
