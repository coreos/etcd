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

package snap

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"go.uber.org/zap"

	"go.etcd.io/raft/v3/raftpb"
)

type Snapshotter struct {
	lg       *zap.Logger
	dir      string
	fsyncDir func(string) error

	// pendingDBsMu protects pendingDBs.
	pendingDBsMu sync.Mutex
	// pendingDBs tracks the indices of snapshot database files that were
	// saved on receipt but have not been applied yet. ReleaseSnapDBs must
	// not delete these files: a newer snapshot can arrive before an older
	// one has been applied, and deleting the older file while its apply is
	// pending makes the apply panic with "failed to open snapshot backend".
	// See https://github.com/etcd-io/etcd/issues/18055.
	pendingDBs map[uint64]struct{}
}

func New(lg *zap.Logger, dir string) *Snapshotter {
	if lg == nil {
		lg = zap.NewNop()
	}
	return &Snapshotter{
		lg:         lg,
		dir:        dir,
		fsyncDir:   fsyncSnapDir,
		pendingDBs: make(map[uint64]struct{}),
	}
}

func (s *Snapshotter) ReleaseSnapDBs(snap *raftpb.Snapshot) error {
	dir, err := os.Open(s.dir)
	if err != nil {
		return err
	}
	defer dir.Close()
	filenames, err := dir.Readdirnames(-1)
	if err != nil {
		return err
	}
	for _, filename := range filenames {
		if strings.HasSuffix(filename, ".snap.db") {
			hexIndex := strings.TrimSuffix(filepath.Base(filename), ".snap.db")
			index, err := strconv.ParseUint(hexIndex, 16, 64)
			if err != nil {
				s.lg.Error("failed to parse index from filename", zap.String("path", filename), zap.String("error", err.Error()))
				continue
			}
			if index < snap.Metadata.GetIndex() {
				if s.isPendingDB(index) {
					s.lg.Info("skipping deletion of .snap.db file pending apply", zap.String("path", filename))
					continue
				}
				s.lg.Info("found orphaned .snap.db file; deleting", zap.String("path", filename))
				if rmErr := os.Remove(filepath.Join(s.dir, filename)); rmErr != nil && !os.IsNotExist(rmErr) {
					s.lg.Error("failed to remove orphaned .snap.db file", zap.String("path", filename), zap.String("error", rmErr.Error()))
				}
			}
		}
	}
	return nil
}
