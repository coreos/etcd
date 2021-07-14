// Copyright 2018 The etcd Authors
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

package snapshot

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	bolt "go.etcd.io/bbolt"
	"go.etcd.io/etcd/api/v3/etcdserverpb"
	"go.etcd.io/etcd/client/pkg/v3/fileutil"
	"go.etcd.io/etcd/client/pkg/v3/types"
	"go.etcd.io/etcd/client/v3"
	"go.etcd.io/etcd/client/v3/snapshot"
	"go.etcd.io/etcd/raft/v3"
	"go.etcd.io/etcd/raft/v3/raftpb"
	"go.etcd.io/etcd/server/v3/config"
	"go.etcd.io/etcd/server/v3/etcdserver"
	"go.etcd.io/etcd/server/v3/etcdserver/api/membership"
	"go.etcd.io/etcd/server/v3/etcdserver/api/snap"
	"go.etcd.io/etcd/server/v3/etcdserver/api/v2store"
	"go.etcd.io/etcd/server/v3/etcdserver/cindex"
	"go.etcd.io/etcd/server/v3/storage/backend"
	"go.etcd.io/etcd/server/v3/storage/schema"
	"go.etcd.io/etcd/server/v3/verify"
	"go.etcd.io/etcd/server/v3/wal"
	"go.etcd.io/etcd/server/v3/wal/walpb"
	"go.uber.org/zap"
)

// Manager defines snapshot methods.
type Manager interface {
	// Save fetches snapshot from remote etcd server, saves data
	// to target path and returns server version. If the context "ctx" is canceled or timed out,
	// snapshot save stream will error out (e.g. context.Canceled,
	// context.DeadlineExceeded). Make sure to specify only one endpoint
	// in client configuration. Snapshot API must be requested to a
	// selected node, and saved snapshot is the point-in-time state of
	// the selected node.
	Save(ctx context.Context, cfg clientv3.Config, dbPath string) (version string, err error)

	// Status returns the snapshot file information.
	Status(dbPath string) (Status, error)

	// Restore restores a new etcd data directory from given snapshot
	// file. It returns an error if specified data directory already
	// exists, to prevent unintended data directory overwrites.
	Restore(cfg RestoreConfig) error
}

// NewV3 returns a new snapshot Manager for v3.x snapshot.
func NewV3(lg *zap.Logger) Manager {
	if lg == nil {
		lg = zap.NewExample()
	}
	return &v3Manager{lg: lg}
}

type v3Manager struct {
	lg *zap.Logger

	name      string
	srcDbPath string
	walDir    string
	snapDir   string
	cl        *membership.RaftCluster

	skipHashCheck bool
}

// hasChecksum returns "true" if the file size "n"
// has appended sha256 hash digest.
func hasChecksum(n int64) bool {
	// 512 is chosen because it's a minimum disk sector size
	// smaller than (and multiplies to) OS page size in most systems
	return (n % 512) == sha256.Size
}

// Save fetches snapshot from remote etcd server and saves data to target path.
func (s *v3Manager) Save(ctx context.Context, cfg clientv3.Config, dbPath string) (version string, err error) {
	return snapshot.SaveWithVersion(ctx, s.lg, cfg, dbPath)
}

// Status is the snapshot file status.
type Status struct {
	Hash      uint32 `json:"hash"`
	Revision  int64  `json:"revision"`
	TotalKey  int    `json:"totalKey"`
	TotalSize int64  `json:"totalSize"`
	// Version is equal to storageVersion of the snapshot
	// Empty if server does not supports versioned snapshots (<v3.6)
	Version string `json:"version"`
}

// Status returns the snapshot file information.
func (s *v3Manager) Status(dbPath string) (ds Status, err error) {
	if _, err = os.Stat(dbPath); err != nil {
		return ds, err
	}

	db, err := bolt.Open(dbPath, 0400, &bolt.Options{ReadOnly: true})
	if err != nil {
		return ds, err
	}
	defer db.Close()

	h := crc32.New(crc32.MakeTable(crc32.Castagnoli))

	if err = db.View(func(tx *bolt.Tx) error {
		// check snapshot file integrity first
		var dbErrStrings []string
		for dbErr := range tx.Check() {
			dbErrStrings = append(dbErrStrings, dbErr.Error())
		}
		if len(dbErrStrings) > 0 {
			return fmt.Errorf("snapshot file integrity check failed. %d errors found.\n"+strings.Join(dbErrStrings, "\n"), len(dbErrStrings))
		}
		ds.TotalSize = tx.Size()
		v := schema.ReadStorageVersionFromSnapshot(tx)
		if v != nil {
			ds.Version = v.String()
		}
		c := tx.Cursor()
		for next, _ := c.First(); next != nil; next, _ = c.Next() {
			b := tx.Bucket(next)
			if b == nil {
				return fmt.Errorf("cannot get hash of bucket %s", string(next))
			}
			if _, err := h.Write(next); err != nil {
				return fmt.Errorf("cannot write bucket %s : %v", string(next), err)
			}
			iskeyb := (string(next) == "key")
			if err := b.ForEach(func(k, v []byte) error {
				if _, err := h.Write(k); err != nil {
					return fmt.Errorf("cannot write to bucket %s", err.Error())
				}
				if _, err := h.Write(v); err != nil {
					return fmt.Errorf("cannot write to bucket %s", err.Error())
				}
				if iskeyb {
					rev := bytesToRev(k)
					ds.Revision = rev.main
				}
				ds.TotalKey++
				return nil
			}); err != nil {
				return fmt.Errorf("cannot write bucket %s : %v", string(next), err)
			}
		}
		return nil
	}); err != nil {
		return ds, err
	}

	ds.Hash = h.Sum32()
	return ds, nil
}

// RestoreConfig configures snapshot restore operation.
type RestoreConfig struct {
	// SnapshotPath is the path of snapshot file to restore from.
	SnapshotPath string

	// Name is the human-readable name of this member.
	Name string

	// OutputDataDir is the target data directory to save restored data.
	// OutputDataDir should not conflict with existing etcd data directory.
	// If OutputDataDir already exists, it will return an error to prevent
	// unintended data directory overwrites.
	// If empty, defaults to "[Name].etcd" if not given.
	OutputDataDir string
	// OutputWALDir is the target WAL data directory.
	// If empty, defaults to "[OutputDataDir]/member/wal" if not given.
	OutputWALDir string

	// PeerURLs is a list of member's peer URLs to advertise to the rest of the cluster.
	PeerURLs []string

	// InitialCluster is the initial cluster configuration for restore bootstrap.
	InitialCluster string
	// InitialClusterToken is the initial cluster token for etcd cluster during restore bootstrap.
	InitialClusterToken string

	// SkipHashCheck is "true" to ignore snapshot integrity hash value
	// (required if copied from data directory).
	SkipHashCheck bool
}

// Restore restores a new etcd data directory from given snapshot file.
func (s *v3Manager) Restore(cfg RestoreConfig) error {
	pURLs, err := types.NewURLs(cfg.PeerURLs)
	if err != nil {
		return err
	}
	var ics types.URLsMap
	ics, err = types.NewURLsMap(cfg.InitialCluster)
	if err != nil {
		return err
	}

	srv := config.ServerConfig{
		Logger:              s.lg,
		Name:                cfg.Name,
		PeerURLs:            pURLs,
		InitialPeerURLsMap:  ics,
		InitialClusterToken: cfg.InitialClusterToken,
	}
	if err = srv.VerifyBootstrap(); err != nil {
		return err
	}

	s.cl, err = membership.NewClusterFromURLsMap(s.lg, cfg.InitialClusterToken, ics)
	if err != nil {
		return err
	}

	dataDir := cfg.OutputDataDir
	if dataDir == "" {
		dataDir = cfg.Name + ".etcd"
	}
	if fileutil.Exist(dataDir) && !fileutil.DirEmpty(dataDir) {
		return fmt.Errorf("data-dir %q not empty or could not be read", dataDir)
	}

	walDir := cfg.OutputWALDir
	if walDir == "" {
		walDir = filepath.Join(dataDir, "member", "wal")
	} else if fileutil.Exist(walDir) {
		return fmt.Errorf("wal-dir %q exists", walDir)
	}

	s.name = cfg.Name
	s.srcDbPath = cfg.SnapshotPath
	s.walDir = walDir
	s.snapDir = filepath.Join(dataDir, "member", "snap")
	s.skipHashCheck = cfg.SkipHashCheck

	s.lg.Info(
		"restoring snapshot",
		zap.String("path", s.srcDbPath),
		zap.String("wal-dir", s.walDir),
		zap.String("data-dir", dataDir),
		zap.String("snap-dir", s.snapDir),
		zap.Stack("stack"),
	)

	if err = s.saveDB(); err != nil {
		return err
	}
	hardstate, err := s.saveWALAndSnap()
	if err != nil {
		return err
	}

	if err := s.updateCIndex(hardstate.Commit, hardstate.Term); err != nil {
		return err
	}

	s.lg.Info(
		"restored snapshot",
		zap.String("path", s.srcDbPath),
		zap.String("wal-dir", s.walDir),
		zap.String("data-dir", dataDir),
		zap.String("snap-dir", s.snapDir),
	)

	return verify.VerifyIfEnabled(verify.Config{
		ExactIndex: true,
		Logger:     s.lg,
		DataDir:    dataDir,
	})
}

func (s *v3Manager) outDbPath() string {
	return filepath.Join(s.snapDir, "db")
}

// saveDB copies the database snapshot to the snapshot directory
func (s *v3Manager) saveDB() error {
	err := s.copyAndVerifyDB()
	if err != nil {
		return err
	}

	be := backend.NewDefaultBackend(s.outDbPath())
	defer be.Close()

	err = schema.NewMembershipStore(s.lg, be).TrimMembershipFromBackend()
	if err != nil {
		return err
	}

	return nil
}

func (s *v3Manager) copyAndVerifyDB() error {
	srcf, ferr := os.Open(s.srcDbPath)
	if ferr != nil {
		return ferr
	}
	defer srcf.Close()

	// get snapshot integrity hash
	if _, err := srcf.Seek(-sha256.Size, io.SeekEnd); err != nil {
		return err
	}
	sha := make([]byte, sha256.Size)
	if _, err := srcf.Read(sha); err != nil {
		return err
	}
	if _, err := srcf.Seek(0, io.SeekStart); err != nil {
		return err
	}

	if err := fileutil.CreateDirAll(s.snapDir); err != nil {
		return err
	}

	outDbPath := s.outDbPath()

	db, dberr := os.OpenFile(outDbPath, os.O_RDWR|os.O_CREATE, 0600)
	if dberr != nil {
		return dberr
	}
	dbClosed := false
	defer func() {
		if !dbClosed {
			db.Close()
			dbClosed = true
		}
	}()
	if _, err := io.Copy(db, srcf); err != nil {
		return err
	}

	// truncate away integrity hash, if any.
	off, serr := db.Seek(0, io.SeekEnd)
	if serr != nil {
		return serr
	}
	hasHash := hasChecksum(off)
	if hasHash {
		if err := db.Truncate(off - sha256.Size); err != nil {
			return err
		}
	}

	if !hasHash && !s.skipHashCheck {
		return fmt.Errorf("snapshot missing hash but --skip-hash-check=false")
	}

	if hasHash && !s.skipHashCheck {
		// check for match
		if _, err := db.Seek(0, io.SeekStart); err != nil {
			return err
		}
		h := sha256.New()
		if _, err := io.Copy(h, db); err != nil {
			return err
		}
		dbsha := h.Sum(nil)
		if !reflect.DeepEqual(sha, dbsha) {
			return fmt.Errorf("expected sha256 %v, got %v", sha, dbsha)
		}
	}

	// db hash is OK, can now modify DB so it can be part of a new cluster
	db.Close()
	return nil
}

// saveWALAndSnap creates a WAL for the initial cluster
//
// TODO: This code ignores learners !!!
func (s *v3Manager) saveWALAndSnap() (*raftpb.HardState, error) {
	if err := fileutil.CreateDirAll(s.walDir); err != nil {
		return nil, err
	}

	// add members again to persist them to the store we create.
	st := v2store.New(etcdserver.StoreClusterPrefix, etcdserver.StoreKeysPrefix)
	s.cl.SetStore(st)
	be := backend.NewDefaultBackend(s.outDbPath())
	defer be.Close()
	s.cl.SetBackend(schema.NewMembershipStore(s.lg, be))
	for _, m := range s.cl.Members() {
		s.cl.AddMember(m, true)
	}

	m := s.cl.MemberByName(s.name)
	md := &etcdserverpb.Metadata{NodeID: uint64(m.ID), ClusterID: uint64(s.cl.ID())}
	metadata, merr := md.Marshal()
	if merr != nil {
		return nil, merr
	}
	w, walerr := wal.Create(s.lg, s.walDir, metadata)
	if walerr != nil {
		return nil, walerr
	}
	defer w.Close()

	peers := make([]raft.Peer, len(s.cl.MemberIDs()))
	for i, id := range s.cl.MemberIDs() {
		ctx, err := json.Marshal((*s.cl).Member(id))
		if err != nil {
			return nil, err
		}
		peers[i] = raft.Peer{ID: uint64(id), Context: ctx}
	}

	ents := make([]raftpb.Entry, len(peers))
	nodeIDs := make([]uint64, len(peers))
	for i, p := range peers {
		nodeIDs[i] = p.ID
		cc := raftpb.ConfChange{
			Type:    raftpb.ConfChangeAddNode,
			NodeID:  p.ID,
			Context: p.Context,
		}
		d, err := cc.Marshal()
		if err != nil {
			return nil, err
		}
		ents[i] = raftpb.Entry{
			Type:  raftpb.EntryConfChange,
			Term:  1,
			Index: uint64(i + 1),
			Data:  d,
		}
	}

	commit, term := uint64(len(ents)), uint64(1)
	hardState := raftpb.HardState{
		Term:   term,
		Vote:   peers[0].ID,
		Commit: commit,
	}
	if err := w.Save(hardState, ents); err != nil {
		return nil, err
	}

	b, berr := st.Save()
	if berr != nil {
		return nil, berr
	}
	confState := raftpb.ConfState{
		Voters: nodeIDs,
	}
	raftSnap := raftpb.Snapshot{
		Data: b,
		Metadata: raftpb.SnapshotMetadata{
			Index:     commit,
			Term:      term,
			ConfState: confState,
		},
	}
	sn := snap.New(s.lg, s.snapDir)
	if err := sn.SaveSnap(raftSnap); err != nil {
		return nil, err
	}
	snapshot := walpb.Snapshot{Index: commit, Term: term, ConfState: &confState}
	return &hardState, w.SaveSnapshot(snapshot)
}

func (s *v3Manager) updateCIndex(commit uint64, term uint64) error {
	be := backend.NewDefaultBackend(s.outDbPath())
	defer be.Close()

	cindex.UpdateConsistentIndex(be.BatchTx(), commit, term, false)
	return nil
}
