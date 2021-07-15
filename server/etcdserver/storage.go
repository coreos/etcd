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

package etcdserver

import (
	"io"

	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	"go.etcd.io/etcd/client/pkg/v3/types"
	"go.etcd.io/etcd/pkg/v3/pbutil"
	"go.etcd.io/etcd/raft/v3"
	"go.etcd.io/etcd/raft/v3/raftpb"
	"go.etcd.io/etcd/server/v3/config"
	"go.etcd.io/etcd/server/v3/etcdserver/api/snap"
	"go.etcd.io/etcd/server/v3/wal"
	"go.etcd.io/etcd/server/v3/wal/walpb"

	"go.uber.org/zap"
)

type Storage interface {
	// Save function saves ents and state to the underlying stable storage.
	// Save MUST block until st and ents are on stable storage.
	Save(st raftpb.HardState, ents []raftpb.Entry) error
	// SaveSnap function saves snapshot to the underlying stable storage.
	SaveSnap(snap raftpb.Snapshot) error
	// Close closes the Storage and performs finalization.
	Close() error
	// Release releases the locked wal files older than the provided snapshot.
	Release(snap raftpb.Snapshot) error
	// Sync WAL
	Sync() error
}

type storage struct {
	*wal.WAL
	*snap.Snapshotter
}

func NewStorage(w *wal.WAL, s *snap.Snapshotter) Storage {
	return &storage{w, s}
}

// SaveSnap saves the snapshot file to disk and writes the WAL snapshot entry.
func (st *storage) SaveSnap(snap raftpb.Snapshot) error {
	walsnap := walpb.Snapshot{
		Index:     snap.Metadata.Index,
		Term:      snap.Metadata.Term,
		ConfState: &snap.Metadata.ConfState,
	}
	// save the snapshot file before writing the snapshot to the wal.
	// This makes it possible for the snapshot file to become orphaned, but prevents
	// a WAL snapshot entry from having no corresponding snapshot file.
	err := st.Snapshotter.SaveSnap(snap)
	if err != nil {
		return err
	}
	// gofail: var raftBeforeWALSaveSnaphot struct{}

	return st.WAL.SaveSnapshot(walsnap)
}

// Release releases resources older than the given snap and are no longer needed:
// - releases the locks to the wal files that are older than the provided wal for the given snap.
// - deletes any .snap.db files that are older than the given snap.
func (st *storage) Release(snap raftpb.Snapshot) error {
	if err := st.WAL.ReleaseLockTo(snap.Metadata.Index); err != nil {
		return err
	}
	return st.Snapshotter.ReleaseSnapDBs(snap)
}

// bootstrapWALFromSnapshot reads the WAL at the given snap and returns the wal, its latest HardState and cluster ID, and all entries that appear
// after the position of the given snap in the WAL.
// The snap must have been previously saved to the WAL, or this call will panic.
func bootstrapWALFromSnapshot(lg *zap.Logger, waldir string, snapshot *raftpb.Snapshot, unsafeNoFsync bool) *bootstrappedWAL {
	var walsnap walpb.Snapshot
	if snapshot != nil {
		walsnap.Index, walsnap.Term = snapshot.Metadata.Index, snapshot.Metadata.Term
	}
	repaired := false
	for {
		w, err := wal.Open(lg, waldir, walsnap)
		if err != nil {
			lg.Fatal("failed to open WAL", zap.Error(err))
		}
		if unsafeNoFsync {
			w.SetUnsafeNoFsync()
		}
		wmetadata, st, ents, err := w.ReadAll()
		if err != nil {
			w.Close()
			// we can only repair ErrUnexpectedEOF and we never repair twice.
			if repaired || err != io.ErrUnexpectedEOF {
				lg.Fatal("failed to read WAL, cannot be repaired", zap.Error(err))
			}
			if !wal.Repair(lg, waldir) {
				lg.Fatal("failed to repair WAL", zap.Error(err))
			} else {
				lg.Info("repaired WAL", zap.Error(err))
				repaired = true
			}
			continue
		}
		var metadata pb.Metadata
		pbutil.MustUnmarshal(&metadata, wmetadata)
		id := types.ID(metadata.NodeID)
		cid := types.ID(metadata.ClusterID)
		return &bootstrappedWAL{
			lg:       lg,
			w:        w,
			id:       id,
			cid:      cid,
			st:       &st,
			ents:     ents,
			snapshot: snapshot,
		}
	}
}

func bootstrapNewWAL(cfg config.ServerConfig, nodeID, clusterID types.ID) *bootstrappedWAL {
	metadata := pbutil.MustMarshal(
		&pb.Metadata{
			NodeID:    uint64(nodeID),
			ClusterID: uint64(clusterID),
		},
	)
	w, err := wal.Create(cfg.Logger, cfg.WALDir(), metadata)
	if err != nil {
		cfg.Logger.Panic("failed to create WAL", zap.Error(err))
	}
	if cfg.UnsafeNoFsync {
		w.SetUnsafeNoFsync()
	}
	return &bootstrappedWAL{
		lg:  cfg.Logger,
		w:   w,
		id:  nodeID,
		cid: clusterID,
	}
}

type bootstrappedWAL struct {
	lg *zap.Logger

	w        *wal.WAL
	id, cid  types.ID
	st       *raftpb.HardState
	ents     []raftpb.Entry
	snapshot *raftpb.Snapshot
}

func (wal *bootstrappedWAL) MemoryStorage() *raft.MemoryStorage {
	s := raft.NewMemoryStorage()
	if wal.snapshot != nil {
		s.ApplySnapshot(*wal.snapshot)
	}
	if wal.st != nil {
		s.SetHardState(*wal.st)
	}
	if len(wal.ents) != 0 {
		s.Append(wal.ents)
	}
	return s
}

func (wal *bootstrappedWAL) CommitedEntries() []raftpb.Entry {
	for i, ent := range wal.ents {
		if ent.Index > wal.st.Commit {
			wal.lg.Info(
				"discarding uncommitted WAL entries",
				zap.Uint64("entry-index", ent.Index),
				zap.Uint64("commit-index-from-wal", wal.st.Commit),
				zap.Int("number-of-discarded-entries", len(wal.ents)-i),
			)
			return wal.ents[:i]
		}
	}
	return wal.ents
}

func (wal *bootstrappedWAL) ConfigChangeEntries() []raftpb.Entry {
	ids, isLearnerMap := getIDs(wal.lg, wal.snapshot, wal.ents)
	return createConfigChangeEnts(
		wal.lg,
		ids,
		isLearnerMap,
		uint64(wal.id),
		wal.st.Term,
		wal.st.Commit,
	)
}

func (wal *bootstrappedWAL) AppendAndCommitEntries(ents []raftpb.Entry) {
	wal.ents = append(wal.ents, ents...)
	err := wal.w.Save(raftpb.HardState{}, ents)
	if err != nil {
		wal.lg.Fatal("failed to save hard state and entries", zap.Error(err))
	}
	if len(wal.ents) != 0 {
		wal.st.Commit = wal.ents[len(wal.ents)-1].Index
	}
}
