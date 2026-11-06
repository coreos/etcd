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
	"bytes"
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

	"go.uber.org/zap"
	"google.golang.org/protobuf/proto"

	bolt "go.etcd.io/bbolt"
	"go.etcd.io/etcd/api/v3/etcdserverpb"
	"go.etcd.io/etcd/api/v3/mvccpb"
	"go.etcd.io/etcd/client/pkg/v3/fileutil"
	"go.etcd.io/etcd/client/pkg/v3/types"
	clientv3 "go.etcd.io/etcd/client/v3"
	"go.etcd.io/etcd/client/v3/snapshot"
	"go.etcd.io/etcd/server/v3/config"
	"go.etcd.io/etcd/server/v3/etcdserver/api/membership"
	"go.etcd.io/etcd/server/v3/etcdserver/cindex"
	"go.etcd.io/etcd/server/v3/storage/backend"
	"go.etcd.io/etcd/server/v3/storage/mvcc"
	"go.etcd.io/etcd/server/v3/storage/schema"
	"go.etcd.io/etcd/server/v3/storage/wal"
	"go.etcd.io/etcd/server/v3/storage/wal/walpb"
	"go.etcd.io/etcd/server/v3/verify"
	"go.etcd.io/raft/v3"
	"go.etcd.io/raft/v3/raftpb"
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
	return &v3Manager{lg: lg}
}

type v3Manager struct {
	lg *zap.Logger

	name      string
	srcDbPath string
	walDir    string
	snapDir   string
	cl        *membership.RaftCluster

	skipHashCheck   bool
	initialMmapSize uint64
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

	db, err := bolt.Open(dbPath, 0o400, &bolt.Options{ReadOnly: true})
	if err != nil {
		return ds, err
	}
	defer db.Close()

	h := crc32.New(crc32.MakeTable(crc32.Castagnoli))
	seenKeys := make(map[string]struct{})

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
				return fmt.Errorf("nil bucket: %q", string(next))
			}
			_, err = h.Write(next)
			if err != nil {
				return fmt.Errorf("cannot hash bucket name: %q err: %w", string(next), err)
			}

			iskeyb := (bytes.Equal(next, schema.Key.Name()))
			if err = b.ForEach(func(k, v []byte) error {
				_, err = h.Write(k)
				if err != nil {
					return fmt.Errorf("cannot hash bucket key: %q err: %w", k, err)
				}
				_, err = h.Write(v)
				if err != nil {
					return fmt.Errorf("cannot hash bucket key: %q value: %q err: %w", k, v, err)
				}
				if iskeyb {
					var rev mvcc.Revision
					rev, err = bytesToRev(k)
					if err != nil {
						return fmt.Errorf("cannot parse revision key: %q err: %w", k, err)
					}
					ds.Revision = rev.Main

					var kv mvccpb.KeyValue
					err = proto.Unmarshal(v, &kv)
					if err != nil {
						return fmt.Errorf("cannot unmarshal value, key: %q value: %q err: %w", k, v, err)
					}
					key := string(kv.Key)
					// refer to https://etcd.io/docs/v3.5/learning/data_model/
					if !mvcc.IsTombstone(k) {
						seenKeys[key] = struct{}{}
					} else {
						delete(seenKeys, key)
					}
				}
				return nil
			}); err != nil {
				return fmt.Errorf("error during bucket key iteration, name: %q err: %w", string(next), err)
			}
		}
		return nil
	}); err != nil {
		return ds, err
	}

	ds.TotalKey = len(seenKeys)
	ds.Hash = h.Sum32()
	return ds, nil
}

func bytesToRev(b []byte) (rev mvcc.Revision, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("%s", r)
		}
	}()
	return mvcc.BytesToRev(b), err
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

	// InitialMmapSize is the database initial memory map size.
	InitialMmapSize uint64

	// RevisionBump is the amount to increase the latest revision after restore,
	// to allow administrators to trick clients into thinking that revision never decreased.
	// If 0, revision bumping is skipped.
	// (required if MarkCompacted == true)
	RevisionBump uint64

	// MarkCompacted is "true" to mark the latest revision as compacted.
	// (required if RevisionBump > 0)
	MarkCompacted bool
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
	s.initialMmapSize = cfg.InitialMmapSize

	s.lg.Info(
		"restoring snapshot",
		zap.String("path", s.srcDbPath),
		zap.String("wal-dir", s.walDir),
		zap.String("data-dir", dataDir),
		zap.String("snap-dir", s.snapDir),
		zap.Uint64("initial-memory-map-size", s.initialMmapSize),
	)

	if err = s.saveDB(); err != nil {
		return err
	}

	if cfg.MarkCompacted && cfg.RevisionBump > 0 {
		if err = s.modifyLatestRevision(cfg.RevisionBump); err != nil {
			return err
		}
	}

	hardstate, err := s.saveWALAndSnap()
	if err != nil {
		return err
	}

	if err := s.updateCIndex(hardstate.GetCommit(), hardstate.GetTerm()); err != nil {
		return err
	}

	s.lg.Info(
		"restored snapshot",
		zap.String("path", s.srcDbPath),
		zap.String("wal-dir", s.walDir),
		zap.String("data-dir", dataDir),
		zap.String("snap-dir", s.snapDir),
		zap.Uint64("initial-memory-map-size", s.initialMmapSize),
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

	be := backend.NewDefaultBackend(s.lg, s.outDbPath(), backend.WithMmapSize(s.initialMmapSize))
	defer be.Close()

	err = schema.NewMembershipBackend(s.lg, be).TrimMembershipFromBackend()
	if err != nil {
		return err
	}

	return nil
}

// modifyLatestRevision can increase the latest revision by the given amount and sets the scheduled compaction
// to that revision so that the server will consider this revision compacted.
func (s *v3Manager) modifyLatestRevision(bumpAmount uint64) error {
	be := backend.NewDefaultBackend(s.lg, s.outDbPath())
	defer func() {
		be.ForceCommit()
		be.Close()
	}()

	tx := be.BatchTx()
	tx.LockOutsideApply()
	defer tx.Unlock()

	latest, err := s.unsafeGetLatestRevision(tx)
	if err != nil {
		return err
	}

	latest = s.unsafeBumpBucketsRevision(tx, latest, int64(bumpAmount))
	s.unsafeMarkRevisionCompacted(tx, latest)

	return nil
}

func (s *v3Manager) unsafeBumpBucketsRevision(tx backend.UnsafeWriter, latest mvcc.Revision, amount int64) mvcc.Revision {
	s.lg.Info(
		"bumping latest revision",
		zap.Int64("latest-revision", latest.Main),
		zap.Int64("bump-amount", amount),
		zap.Int64("new-latest-revision", latest.Main+amount),
	)

	latest.Main += amount
	latest.Sub = 0
	k := mvcc.NewRevBytes()
	k = mvcc.RevToBytes(latest, k)
	tx.UnsafePut(schema.Key, k, []byte{})

	return latest
}

func (s *v3Manager) unsafeMarkRevisionCompacted(tx backend.UnsafeWriter, latest mvcc.Revision) {
	s.lg.Info(
		"marking revision compacted",
		zap.Int64("revision", latest.Main),
	)

	mvcc.UnsafeSetScheduledCompact(tx, latest.Main)
}

func (s *v3Manager) unsafeGetLatestRevision(tx backend.UnsafeReader) (mvcc.Revision, error) {
	var latest mvcc.Revision
	err := tx.UnsafeForEach(schema.Key, func(k, _ []byte) error {
		rev, err := bytesToRev(k)
		if err != nil {
			return fmt.Errorf("cannot parse revision key: %q err: %w", k, err)
		}

		if rev.GreaterThan(latest) {
			latest = rev
		}

		return nil
	})
	return latest, err
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

	if err := fileutil.CreateDirAll(s.lg, s.snapDir); err != nil {
		return err
	}

	outDbPath := s.outDbPath()

	db, dberr := os.OpenFile(outDbPath, os.O_RDWR|os.O_CREATE, 0o600)
	if dberr != nil {
		return dberr
	}
	defer db.Close()

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

	return nil
}

// saveWALAndSnap creates a WAL for the initial cluster
//
// TODO: This code ignores learners !!!
func (s *v3Manager) saveWALAndSnap() (*raftpb.HardState, error) {
	if err := fileutil.CreateDirAll(s.lg, s.walDir); err != nil {
		return nil, err
	}

	// add members again to persist them to the backend we create.
	be := backend.NewDefaultBackend(s.lg, s.outDbPath(), backend.WithMmapSize(s.initialMmapSize))
	defer be.Close()
	s.cl.SetBackend(schema.NewMembershipBackend(s.lg, be))
	for _, m := range s.cl.Members() {
		s.cl.AddMember(m, true)
	}

	m := s.cl.MemberByName(s.name) //nolint:staticcheck // See https://github.com/dominikh/go-tools/issues/1698
	md := &etcdserverpb.Metadata{NodeID: new(uint64(m.ID)), ClusterID: new(uint64(s.cl.ID()))}
	metadata, merr := proto.Marshal(md)
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

	ents := make([]*raftpb.Entry, len(peers))
	nodeIDs := make([]uint64, len(peers))
	for i, p := range peers {
		nodeIDs[i] = p.ID
		cc := raftpb.ConfChange{
			Type:    raftpb.ConfChangeAddNode.Enum(),
			NodeId:  new(p.ID),
			Context: p.Context,
		}
		d, err := proto.Marshal(&cc)
		if err != nil {
			return nil, err
		}
		ents[i] = &raftpb.Entry{
			Type:  raftpb.EntryConfChange.Enum(),
			Term:  new(uint64(1)),
			Index: new(uint64(i + 1)),
			Data:  d,
		}
	}

	commit, term := uint64(len(ents)), uint64(1)
	hardState := raftpb.HardState{
		Term:   new(term),
		Vote:   new(peers[0].ID),
		Commit: new(commit),
	}
	if err := w.Save(&hardState, ents); err != nil {
		return nil, err
	}

	confState := &raftpb.ConfState{
		Voters: nodeIDs,
	}
	snapshot := walpb.Snapshot{Index: new(commit), Term: new(term), ConfState: confState}
	return &hardState, w.SaveSnapshot(&snapshot)
}

func (s *v3Manager) updateCIndex(commit uint64, term uint64) error {
	be := backend.NewDefaultBackend(s.lg, s.outDbPath(), backend.WithMmapSize(s.initialMmapSize))
	defer be.Close()

	cindex.UpdateConsistentIndexForce(be.BatchTx(), commit, term)
	return nil
}
