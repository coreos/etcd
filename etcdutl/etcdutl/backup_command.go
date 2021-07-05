// Copyright 2021 The etcd Authors
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

package etcdutl

import (
	"os"
	"path"
	"regexp"
	"time"

	"github.com/spf13/cobra"
	"go.etcd.io/etcd/api/v3/etcdserverpb"
	"go.etcd.io/etcd/client/pkg/v3/fileutil"
	"go.etcd.io/etcd/client/pkg/v3/types"
	"go.etcd.io/etcd/pkg/v3/idutil"
	"go.etcd.io/etcd/pkg/v3/pbutil"
	"go.etcd.io/etcd/raft/v3/raftpb"
	"go.etcd.io/etcd/server/v3/datadir"
	"go.etcd.io/etcd/server/v3/etcdserver/api/membership"
	"go.etcd.io/etcd/server/v3/etcdserver/api/snap"
	"go.etcd.io/etcd/server/v3/etcdserver/api/v2store"
	"go.etcd.io/etcd/server/v3/mvcc/backend"
	"go.etcd.io/etcd/server/v3/mvcc/buckets"
	"go.etcd.io/etcd/server/v3/verify"
	"go.etcd.io/etcd/server/v3/wal"
	"go.etcd.io/etcd/server/v3/wal/walpb"

	bolt "go.etcd.io/bbolt"
	"go.uber.org/zap"
)

var (
	withV3       bool
	dataDir      string
	backupDir    string
	walDir       string
	backupWalDir string
)

func NewBackupCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "backup",
		Short: "[legacy] offline backup of etcd directory",

		Long: "Prefer: `etcdctl snapshot save` instead.",
		Run:  doBackup,
	}
	cmd.Flags().StringVar(&dataDir, "data-dir", "", "Path to the etcd data dir")
	cmd.Flags().StringVar(&walDir, "wal-dir", "", "Path to the etcd wal dir")
	cmd.Flags().StringVar(&backupDir, "backup-dir", "", "Path to the backup dir")
	cmd.Flags().StringVar(&backupWalDir, "backup-wal-dir", "", "Path to the backup wal dir")
	cmd.Flags().BoolVar(&withV3, "with-v3", true, "Backup v3 backend data")
	cmd.MarkFlagRequired("data-dir")
	cmd.MarkFlagRequired("backup-dir")
	cmd.MarkFlagDirname("data-dir")
	cmd.MarkFlagDirname("wal-dir")
	cmd.MarkFlagDirname("backup-dir")
	cmd.MarkFlagDirname("backup-wal-dir")
	return cmd
}

func doBackup(cmd *cobra.Command, args []string) {
	HandleBackup(withV3, dataDir, backupDir, walDir, backupWalDir)
}

type desiredCluster struct {
	clusterId types.ID
	nodeId    types.ID
	members   []*membership.Member
	confState raftpb.ConfState
}

func newDesiredCluster() desiredCluster {
	idgen := idutil.NewGenerator(0, time.Now())
	nodeID := idgen.Next()
	clusterID := idgen.Next()

	return desiredCluster{
		clusterId: types.ID(clusterID),
		nodeId:    types.ID(nodeID),
		members: []*membership.Member{
			{
				ID: types.ID(nodeID),
				Attributes: membership.Attributes{
					Name: "etcdctl-v2-backup",
				},
				RaftAttributes: membership.RaftAttributes{
					PeerURLs: []string{"http://use-flag--force-new-cluster:2080"},
				}}},
		confState: raftpb.ConfState{Voters: []uint64{nodeID}},
	}
}

// HandleBackup handles a request that intends to do a backup.
func HandleBackup(withV3 bool, srcDir string, destDir string, srcWAL string, destWAL string) error {
	lg := GetLogger()

	srcSnap := datadir.ToSnapDir(srcDir)
	destSnap := datadir.ToSnapDir(destDir)

	if srcWAL == "" {
		srcWAL = datadir.ToWalDir(srcDir)
	}

	if destWAL == "" {
		destWAL = datadir.ToWalDir(destDir)
	}

	if err := fileutil.CreateDirAll(destSnap); err != nil {
		lg.Fatal("failed creating backup snapshot dir", zap.String("dest-snap", destSnap), zap.Error(err))
	}

	destDbPath := datadir.ToBackendFileName(destDir)
	srcDbPath := datadir.ToBackendFileName(srcDir)
	desired := newDesiredCluster()

	walsnap := saveSnap(lg, destSnap, srcSnap, &desired)
	metadata, state, ents := translateWAL(lg, srcWAL, walsnap, withV3)
	saveDB(lg, destDbPath, srcDbPath, state.Commit, state.Term, &desired, withV3)

	neww, err := wal.Create(lg, destWAL, pbutil.MustMarshal(&metadata))
	if err != nil {
		lg.Fatal("wal.Create failed", zap.Error(err))
	}
	defer neww.Close()
	if err := neww.Save(state, ents); err != nil {
		lg.Fatal("wal.Save failed ", zap.Error(err))
	}
	if err := neww.SaveSnapshot(walsnap); err != nil {
		lg.Fatal("SaveSnapshot", zap.Error(err))
	}

	verify.MustVerifyIfEnabled(verify.Config{
		Logger:     lg,
		DataDir:    destDir,
		ExactIndex: false,
	})

	return nil
}

func saveSnap(lg *zap.Logger, destSnap, srcSnap string, desired *desiredCluster) (walsnap walpb.Snapshot) {
	ss := snap.New(lg, srcSnap)
	snapshot, err := ss.Load()
	if err != nil && err != snap.ErrNoSnapshot {
		lg.Fatal("saveSnap(Snapshoter.Load) failed", zap.Error(err))
	}
	if snapshot != nil {
		walsnap.Index, walsnap.Term, walsnap.ConfState = snapshot.Metadata.Index, snapshot.Metadata.Term, &desired.confState
		newss := snap.New(lg, destSnap)
		snapshot.Metadata.ConfState = desired.confState
		snapshot.Data = mustTranslateV2store(lg, snapshot.Data, desired)
		if err = newss.SaveSnap(*snapshot); err != nil {
			lg.Fatal("saveSnap(Snapshoter.SaveSnap) failed", zap.Error(err))
		}
	}
	return walsnap
}

// mustTranslateV2store processes storeData such that they match 'desiredCluster'.
// In particular the method overrides membership information.
func mustTranslateV2store(lg *zap.Logger, storeData []byte, desired *desiredCluster) []byte {
	st := v2store.New()
	if err := st.Recovery(storeData); err != nil {
		lg.Panic("cannot translate v2store", zap.Error(err))
	}

	raftCluster := membership.NewClusterFromMembers(lg, desired.clusterId, desired.members)
	raftCluster.SetID(desired.nodeId, desired.clusterId)
	raftCluster.SetStore(st)
	raftCluster.PushMembershipToStorage()

	outputData, err := st.Save()
	if err != nil {
		lg.Panic("cannot save v2store", zap.Error(err))
	}
	return outputData
}

func translateWAL(lg *zap.Logger, srcWAL string, walsnap walpb.Snapshot, v3 bool) (etcdserverpb.Metadata, raftpb.HardState, []raftpb.Entry) {
	w, err := wal.OpenForRead(lg, srcWAL, walsnap)
	if err != nil {
		lg.Fatal("wal.OpenForRead failed", zap.Error(err))
	}
	defer w.Close()
	wmetadata, state, ents, err := w.ReadAll()
	switch err {
	case nil:
	case wal.ErrSnapshotNotFound:
		lg.Warn("failed to find the match snapshot record", zap.Any("walsnap", walsnap), zap.String("srcWAL", srcWAL))
		lg.Warn("etcdctl will add it back. Start auto fixing...")
	default:
		lg.Fatal("unexpected error while reading WAL", zap.Error(err))
	}

	re := path.Join(membership.StoreMembersPrefix, "[[:xdigit:]]{1,16}", "attributes")
	memberAttrRE := regexp.MustCompile(re)

	for i := 0; i < len(ents); i++ {

		// Replacing WAL entries with 'dummy' entries allows to avoid
		// complicated entries shifting and risk of other data (like consistent_index)
		// running out of sync.
		// Also moving entries and computing offsets would get complicated if
		// TERM changes (so there are superflous entries from previous term).

		if ents[i].Type == raftpb.EntryConfChange {
			lg.Info("ignoring EntryConfChange raft entry")
			raftEntryToNoOp(&ents[i])
			continue
		}

		var raftReq etcdserverpb.InternalRaftRequest
		var v2Req *etcdserverpb.Request
		if pbutil.MaybeUnmarshal(&raftReq, ents[i].Data) {
			v2Req = raftReq.V2
		} else {
			v2Req = &etcdserverpb.Request{}
			pbutil.MustUnmarshal(v2Req, ents[i].Data)
		}

		if v2Req != nil && v2Req.Method == "PUT" && memberAttrRE.MatchString(v2Req.Path) {
			lg.Info("ignoring member attribute update on",
				zap.Stringer("entry", &ents[i]),
				zap.String("v2Req.Path", v2Req.Path))
			raftEntryToNoOp(&ents[i])
			continue
		}

		if v2Req != nil {
			lg.Debug("preserving log entry", zap.Stringer("entry", &ents[i]))
		}

		if raftReq.ClusterMemberAttrSet != nil {
			lg.Info("ignoring cluster_member_attr_set")
			raftEntryToNoOp(&ents[i])
			continue
		}

		if v3 || raftReq.Header == nil {
			lg.Debug("preserving log entry", zap.Stringer("entry", &ents[i]))
			continue
		}
		lg.Info("ignoring v3 raft entry")
		raftEntryToNoOp(&ents[i])
	}
	var metadata etcdserverpb.Metadata
	pbutil.MustUnmarshal(&metadata, wmetadata)
	return metadata, state, ents
}

func raftEntryToNoOp(entry *raftpb.Entry) {
	// Empty (dummy) entries are send by RAFT when new leader is getting elected.
	// They do not cary any change to data-model so its safe to replace entries
	// to be ignored with them.
	*entry = raftpb.Entry{Term: entry.Term, Index: entry.Index, Type: raftpb.EntryNormal, Data: nil}
}

// saveDB copies the v3 backend and strips cluster information.
func saveDB(lg *zap.Logger, destDB, srcDB string, idx uint64, term uint64, desired *desiredCluster, v3 bool) {

	// open src db to safely copy db state
	if v3 {
		var src *bolt.DB
		ch := make(chan *bolt.DB, 1)
		go func() {
			db, err := bolt.Open(srcDB, 0444, &bolt.Options{ReadOnly: true})
			if err != nil {
				lg.Fatal("bolt.Open FAILED", zap.Error(err))
			}
			ch <- db
		}()
		select {
		case src = <-ch:
		case <-time.After(time.Second):
			lg.Fatal("timed out waiting to acquire lock on", zap.String("srcDB", srcDB))
			src = <-ch
		}
		defer src.Close()

		tx, err := src.Begin(false)
		if err != nil {
			lg.Fatal("bbolt.BeginTx failed", zap.Error(err))
		}

		// copy srcDB to destDB
		dest, err := os.Create(destDB)
		if err != nil {
			lg.Fatal("creation of destination file failed", zap.String("dest", destDB), zap.Error(err))
		}
		if _, err := tx.WriteTo(dest); err != nil {
			lg.Fatal("bbolt write to destination file failed", zap.String("dest", destDB), zap.Error(err))
		}
		dest.Close()
		if err := tx.Rollback(); err != nil {
			lg.Fatal("bbolt tx.Rollback failed", zap.String("dest", destDB), zap.Error(err))
		}
	}

	be := backend.NewDefaultBackend(destDB)
	defer be.Close()
	ms := buckets.NewMembershipBackend(lg, be)
	if err := ms.TrimClusterFromBackend(); err != nil {
		lg.Fatal("bbolt tx.Membership failed", zap.Error(err))
	}

	raftCluster := membership.NewClusterFromMembers(lg, desired.clusterId, desired.members)
	raftCluster.SetID(desired.nodeId, desired.clusterId)
	raftCluster.SetBackend(ms)
	raftCluster.PushMembershipToStorage()

	if !v3 {
		tx := be.BatchTx()
		tx.Lock()
		defer tx.Unlock()
		buckets.UnsafeCreateMetaBucket(tx)
		buckets.UnsafeUpdateConsistentIndex(tx, idx, term, false)
	} else {
		// Thanks to translateWAL not moving entries, but just replacing them with
		// 'empty', there is no need to update the consistency index.
	}

}
