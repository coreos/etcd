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

package etcdserver

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/coreos/go-semver/semver"
	"github.com/dustin/go-humanize"
	"go.uber.org/zap"

	"go.etcd.io/etcd/api/v3/etcdserverpb"
	"go.etcd.io/etcd/client/pkg/v3/fileutil"
	"go.etcd.io/etcd/client/pkg/v3/types"
	"go.etcd.io/etcd/pkg/v3/pbutil"
	"go.etcd.io/etcd/raft/v3"
	"go.etcd.io/etcd/raft/v3/raftpb"
	"go.etcd.io/etcd/server/v3/config"
	"go.etcd.io/etcd/server/v3/etcdserver/api"
	"go.etcd.io/etcd/server/v3/etcdserver/api/membership"
	"go.etcd.io/etcd/server/v3/etcdserver/api/rafthttp"
	"go.etcd.io/etcd/server/v3/etcdserver/api/snap"
	"go.etcd.io/etcd/server/v3/etcdserver/api/v2discovery"
	"go.etcd.io/etcd/server/v3/etcdserver/api/v2store"
	"go.etcd.io/etcd/server/v3/etcdserver/cindex"
	"go.etcd.io/etcd/server/v3/storage/backend"
	"go.etcd.io/etcd/server/v3/storage/schema"
	"go.etcd.io/etcd/server/v3/wal"
	"go.etcd.io/etcd/server/v3/wal/walpb"
)

func bootstrap(cfg config.ServerConfig) (b *bootstrappedServer, err error) {
	st := v2store.New(StoreClusterPrefix, StoreKeysPrefix)

	if cfg.MaxRequestBytes > recommendedMaxRequestBytes {
		cfg.Logger.Warn(
			"exceeded recommended request limit",
			zap.Uint("max-request-bytes", cfg.MaxRequestBytes),
			zap.String("max-request-size", humanize.Bytes(uint64(cfg.MaxRequestBytes))),
			zap.Int("recommended-request-bytes", recommendedMaxRequestBytes),
			zap.String("recommended-request-size", recommendedMaxRequestBytesString),
		)
	}

	if terr := fileutil.TouchDirAll(cfg.DataDir); terr != nil {
		return nil, fmt.Errorf("cannot access data directory: %v", terr)
	}

	haveWAL := wal.Exist(cfg.WALDir())
	ss := bootstrapSnapshot(cfg)

	be, ci, beExist, beHooks, err := bootstrapBackend(cfg)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			be.Close()
		}
	}()

	prt, err := rafthttp.NewRoundTripper(cfg.PeerTLSInfo, cfg.PeerDialTimeout())
	if err != nil {
		return nil, err
	}

	switch {
	case !haveWAL && !cfg.NewCluster:
		b, err = bootstrapExistingClusterNoWAL(cfg, prt, st, be)
	case !haveWAL && cfg.NewCluster:
		b, err = bootstrapNewClusterNoWAL(cfg, prt, st, be)
	case haveWAL:
		b, err = bootstrapWithWAL(cfg, st, be, ss, beExist, beHooks, ci)
	default:
		be.Close()
		return nil, fmt.Errorf("unsupported bootstrap config")
	}
	if err != nil {
		return nil, err
	}

	if terr := fileutil.TouchDirAll(cfg.MemberDir()); terr != nil {
		return nil, fmt.Errorf("cannot access member directory: %v", terr)
	}
	b.prt = prt
	b.ci = ci
	b.st = st
	b.be = be
	b.ss = ss
	b.beHooks = beHooks
	return b, nil
}

type bootstrappedServer struct {
	raft    *bootstrappedRaft
	remotes []*membership.Member
	prt     http.RoundTripper
	ci      cindex.ConsistentIndexer
	st      v2store.Store
	be      backend.Backend
	ss      *snap.Snapshotter
	beHooks *backendHooks
}

func bootstrapSnapshot(cfg config.ServerConfig) *snap.Snapshotter {
	if err := fileutil.TouchDirAll(cfg.SnapDir()); err != nil {
		cfg.Logger.Fatal(
			"failed to create snapshot directory",
			zap.String("path", cfg.SnapDir()),
			zap.Error(err),
		)
	}

	if err := fileutil.RemoveMatchFile(cfg.Logger, cfg.SnapDir(), func(fileName string) bool {
		return strings.HasPrefix(fileName, "tmp")
	}); err != nil {
		cfg.Logger.Error(
			"failed to remove temp file(s) in snapshot directory",
			zap.String("path", cfg.SnapDir()),
			zap.Error(err),
		)
	}
	return snap.New(cfg.Logger, cfg.SnapDir())
}

func bootstrapBackend(cfg config.ServerConfig) (be backend.Backend, ci cindex.ConsistentIndexer, beExist bool, beHooks *backendHooks, err error) {
	beExist = fileutil.Exist(cfg.BackendPath())
	ci = cindex.NewConsistentIndex(nil)
	beHooks = &backendHooks{lg: cfg.Logger, indexer: ci}
	be = openBackend(cfg, beHooks)
	ci.SetBackend(be)
	schema.CreateMetaBucket(be.BatchTx())
	if cfg.ExperimentalBootstrapDefragThresholdMegabytes != 0 {
		err := maybeDefragBackend(cfg, be)
		if err != nil {
			be.Close()
			return nil, nil, false, nil, err
		}
	}
	cfg.Logger.Debug("restore consistentIndex", zap.Uint64("index", ci.ConsistentIndex()))
	return be, ci, beExist, beHooks, nil
}

func maybeDefragBackend(cfg config.ServerConfig, be backend.Backend) error {
	size := be.Size()
	sizeInUse := be.SizeInUse()
	freeableMemory := uint(size - sizeInUse)
	thresholdBytes := cfg.ExperimentalBootstrapDefragThresholdMegabytes * 1024 * 1024
	if freeableMemory < thresholdBytes {
		cfg.Logger.Info("Skipping defragmentation",
			zap.Int64("current-db-size-bytes", size),
			zap.String("current-db-size", humanize.Bytes(uint64(size))),
			zap.Int64("current-db-size-in-use-bytes", sizeInUse),
			zap.String("current-db-size-in-use", humanize.Bytes(uint64(sizeInUse))),
			zap.Uint("experimental-bootstrap-defrag-threshold-bytes", thresholdBytes),
			zap.String("experimental-bootstrap-defrag-threshold", humanize.Bytes(uint64(thresholdBytes))),
		)
		return nil
	}
	return be.Defrag()
}

func bootstrapExistingClusterNoWAL(cfg config.ServerConfig, prt http.RoundTripper, st v2store.Store, be backend.Backend) (*bootstrappedServer, error) {
	if err := cfg.VerifyJoinExisting(); err != nil {
		return nil, err
	}
	cl, err := membership.NewClusterFromURLsMap(cfg.Logger, cfg.InitialClusterToken, cfg.InitialPeerURLsMap)
	if err != nil {
		return nil, err
	}
	existingCluster, gerr := GetClusterFromRemotePeers(cfg.Logger, getRemotePeerURLs(cl, cfg.Name), prt)
	if gerr != nil {
		return nil, fmt.Errorf("cannot fetch cluster info from peer urls: %v", gerr)
	}
	if err := membership.ValidateClusterAndAssignIDs(cfg.Logger, cl, existingCluster); err != nil {
		return nil, fmt.Errorf("error validating peerURLs %s: %v", existingCluster, err)
	}
	if !isCompatibleWithCluster(cfg.Logger, cl, cl.MemberByName(cfg.Name).ID, prt) {
		return nil, fmt.Errorf("incompatible with current running cluster")
	}

	remotes := existingCluster.Members()
	cl.SetID(types.ID(0), existingCluster.ID())
	cl.SetStore(st)
	cl.SetBackend(schema.NewMembershipStore(cfg.Logger, be))
	br := bootstrapRaftFromCluster(cfg, cl, nil)
	cl.SetID(br.wal.id, existingCluster.ID())
	return &bootstrappedServer{
		raft:    br,
		remotes: remotes,
	}, nil
}

func bootstrapNewClusterNoWAL(cfg config.ServerConfig, prt http.RoundTripper, st v2store.Store, be backend.Backend) (*bootstrappedServer, error) {
	if err := cfg.VerifyBootstrap(); err != nil {
		return nil, err
	}
	cl, err := membership.NewClusterFromURLsMap(cfg.Logger, cfg.InitialClusterToken, cfg.InitialPeerURLsMap)
	if err != nil {
		return nil, err
	}
	m := cl.MemberByName(cfg.Name)
	if isMemberBootstrapped(cfg.Logger, cl, cfg.Name, prt, cfg.BootstrapTimeoutEffective()) {
		return nil, fmt.Errorf("member %s has already been bootstrapped", m.ID)
	}
	if cfg.ShouldDiscover() {
		var str string
		str, err = v2discovery.JoinCluster(cfg.Logger, cfg.DiscoveryURL, cfg.DiscoveryProxy, m.ID, cfg.InitialPeerURLsMap.String())
		if err != nil {
			return nil, &DiscoveryError{Op: "join", Err: err}
		}
		var urlsmap types.URLsMap
		urlsmap, err = types.NewURLsMap(str)
		if err != nil {
			return nil, err
		}
		if config.CheckDuplicateURL(urlsmap) {
			return nil, fmt.Errorf("discovery cluster %s has duplicate url", urlsmap)
		}
		if cl, err = membership.NewClusterFromURLsMap(cfg.Logger, cfg.InitialClusterToken, urlsmap); err != nil {
			return nil, err
		}
	}
	cl.SetStore(st)
	cl.SetBackend(schema.NewMembershipStore(cfg.Logger, be))
	br := bootstrapRaftFromCluster(cfg, cl, cl.MemberIDs())
	cl.SetID(br.wal.id, cl.ID())
	return &bootstrappedServer{
		remotes: nil,
		raft:    br,
	}, nil
}

func bootstrapWithWAL(cfg config.ServerConfig, st v2store.Store, be backend.Backend, ss *snap.Snapshotter, beExist bool, beHooks *backendHooks, ci cindex.ConsistentIndexer) (*bootstrappedServer, error) {
	if err := fileutil.IsDirWriteable(cfg.MemberDir()); err != nil {
		return nil, fmt.Errorf("cannot write to member directory: %v", err)
	}

	if err := fileutil.IsDirWriteable(cfg.WALDir()); err != nil {
		return nil, fmt.Errorf("cannot write to WAL directory: %v", err)
	}

	if cfg.ShouldDiscover() {
		cfg.Logger.Warn(
			"discovery token is ignored since cluster already initialized; valid logs are found",
			zap.String("wal-dir", cfg.WALDir()),
		)
	}

	// Find a snapshot to start/restart a raft node
	walSnaps, err := wal.ValidSnapshotEntries(cfg.Logger, cfg.WALDir())
	if err != nil {
		return nil, err
	}
	// snapshot files can be orphaned if etcd crashes after writing them but before writing the corresponding
	// wal log entries
	snapshot, err := ss.LoadNewestAvailable(walSnaps)
	if err != nil && err != snap.ErrNoSnapshot {
		return nil, err
	}

	if snapshot != nil {
		if err = st.Recovery(snapshot.Data); err != nil {
			cfg.Logger.Panic("failed to recover from snapshot", zap.Error(err))
		}

		if err = assertNoV2StoreContent(cfg.Logger, st, cfg.V2Deprecation); err != nil {
			cfg.Logger.Error("illegal v2store content", zap.Error(err))
			return nil, err
		}

		cfg.Logger.Info(
			"recovered v2 store from snapshot",
			zap.Uint64("snapshot-index", snapshot.Metadata.Index),
			zap.String("snapshot-size", humanize.Bytes(uint64(snapshot.Size()))),
		)

		if be, err = recoverSnapshotBackend(cfg, be, *snapshot, beExist, beHooks); err != nil {
			cfg.Logger.Panic("failed to recover v3 backend from snapshot", zap.Error(err))
		}
		s1, s2 := be.Size(), be.SizeInUse()
		cfg.Logger.Info(
			"recovered v3 backend from snapshot",
			zap.Int64("backend-size-bytes", s1),
			zap.String("backend-size", humanize.Bytes(uint64(s1))),
			zap.Int64("backend-size-in-use-bytes", s2),
			zap.String("backend-size-in-use", humanize.Bytes(uint64(s2))),
		)
		if beExist {
			// TODO: remove kvindex != 0 checking when we do not expect users to upgrade
			// etcd from pre-3.0 release.
			kvindex := ci.ConsistentIndex()
			if kvindex < snapshot.Metadata.Index {
				if kvindex != 0 {
					return nil, fmt.Errorf("database file (%v index %d) does not match with snapshot (index %d)", cfg.BackendPath(), kvindex, snapshot.Metadata.Index)
				}
				cfg.Logger.Warn(
					"consistent index was never saved",
					zap.Uint64("snapshot-index", snapshot.Metadata.Index),
				)
			}
		}
	} else {
		cfg.Logger.Info("No snapshot found. Recovering WAL from scratch!")
	}

	r := &bootstrappedServer{}
	if !cfg.ForceNewCluster {
		r.raft = bootstrapRaftFromWal(cfg, snapshot)
	} else {
		r.raft = bootstrapRaftFromWalStandalone(cfg, snapshot)
	}

	r.raft.cl.SetStore(st)
	r.raft.cl.SetBackend(schema.NewMembershipStore(cfg.Logger, be))
	r.raft.cl.Recover(api.UpdateCapability)
	if r.raft.cl.Version() != nil && !r.raft.cl.Version().LessThan(semver.Version{Major: 3}) && !beExist {
		bepath := cfg.BackendPath()
		os.RemoveAll(bepath)
		return nil, fmt.Errorf("database file (%v) of the backend is missing", bepath)
	}
	return r, nil
}

func bootstrapRaftFromCluster(cfg config.ServerConfig, cl *membership.RaftCluster, ids []types.ID) *bootstrappedRaft {
	member := cl.MemberByName(cfg.Name)
	id := member.ID
	wal := bootstrapNewWAL(cfg, id, cl.ID())
	peers := make([]raft.Peer, len(ids))
	for i, id := range ids {
		var ctx []byte
		ctx, err := json.Marshal((*cl).Member(id))
		if err != nil {
			cfg.Logger.Panic("failed to marshal member", zap.Error(err))
		}
		peers[i] = raft.Peer{ID: uint64(id), Context: ctx}
	}
	cfg.Logger.Info(
		"starting local member",
		zap.String("local-member-id", id.String()),
		zap.String("cluster-id", cl.ID().String()),
	)
	s := wal.MemoryStorage()
	return &bootstrappedRaft{
		lg:        cfg.Logger,
		heartbeat: time.Duration(cfg.TickMs) * time.Millisecond,
		cl:        cl,
		config:    raftConfig(cfg, uint64(wal.id), s),
		peers:     peers,
		storage:   s,
		wal:       wal,
	}
}

func bootstrapRaftFromWal(cfg config.ServerConfig, snapshot *raftpb.Snapshot) *bootstrappedRaft {
	wal := bootstrapWALFromSnapshot(cfg.Logger, cfg.WALDir(), snapshot, cfg.UnsafeNoFsync)

	cfg.Logger.Info(
		"restarting local member",
		zap.String("cluster-id", wal.cid.String()),
		zap.String("local-member-id", wal.id.String()),
		zap.Uint64("commit-index", wal.st.Commit),
	)
	cl := membership.NewCluster(cfg.Logger)
	cl.SetID(wal.id, wal.cid)
	s := wal.MemoryStorage()
	return &bootstrappedRaft{
		lg:        cfg.Logger,
		heartbeat: time.Duration(cfg.TickMs) * time.Millisecond,
		cl:        cl,
		config:    raftConfig(cfg, uint64(wal.id), s),
		storage:   s,
		wal:       wal,
	}
}

func bootstrapRaftFromWalStandalone(cfg config.ServerConfig, snapshot *raftpb.Snapshot) *bootstrappedRaft {
	wal := bootstrapWALFromSnapshot(cfg.Logger, cfg.WALDir(), snapshot, cfg.UnsafeNoFsync)

	// discard the previously uncommitted entries
	wal.ents = wal.CommitedEntries()
	entries := wal.ConfigChangeEntries()
	// force commit config change entries
	wal.AppendAndCommitEntries(entries)

	cfg.Logger.Info(
		"forcing restart member",
		zap.String("cluster-id", wal.cid.String()),
		zap.String("local-member-id", wal.id.String()),
		zap.Uint64("commit-index", wal.st.Commit),
	)

	cl := membership.NewCluster(cfg.Logger)
	cl.SetID(wal.id, wal.cid)
	s := wal.MemoryStorage()
	return &bootstrappedRaft{
		lg:        cfg.Logger,
		heartbeat: time.Duration(cfg.TickMs) * time.Millisecond,
		cl:        cl,
		config:    raftConfig(cfg, uint64(wal.id), s),
		storage:   s,
		wal:       wal,
	}
}

func raftConfig(cfg config.ServerConfig, id uint64, s *raft.MemoryStorage) *raft.Config {
	return &raft.Config{
		ID:              id,
		ElectionTick:    cfg.ElectionTicks,
		HeartbeatTick:   1,
		Storage:         s,
		MaxSizePerMsg:   maxSizePerMsg,
		MaxInflightMsgs: maxInflightMsgs,
		CheckQuorum:     true,
		PreVote:         cfg.PreVote,
		Logger:          NewRaftLoggerZap(cfg.Logger.Named("raft")),
	}
}

type bootstrappedRaft struct {
	lg        *zap.Logger
	heartbeat time.Duration

	peers   []raft.Peer
	config  *raft.Config
	cl      *membership.RaftCluster
	storage *raft.MemoryStorage
	wal     *bootstrappedWAL
}

func (b *bootstrappedRaft) newRaftNode(ss *snap.Snapshotter) *raftNode {
	var n raft.Node
	if len(b.peers) == 0 {
		n = raft.RestartNode(b.config)
	} else {
		n = raft.StartNode(b.config, b.peers)
	}
	raftStatusMu.Lock()
	raftStatus = n.Status
	raftStatusMu.Unlock()
	return newRaftNode(
		raftNodeConfig{
			lg:          b.lg,
			isIDRemoved: func(id uint64) bool { return b.cl.IsIDRemoved(types.ID(id)) },
			Node:        n,
			heartbeat:   b.heartbeat,
			raftStorage: b.storage,
			storage:     NewStorage(b.wal.w, ss),
		},
	)
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
		var metadata etcdserverpb.Metadata
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
		&etcdserverpb.Metadata{
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
	return createConfigChangeEnts(
		wal.lg,
		getIDs(wal.lg, wal.snapshot, wal.ents),
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
