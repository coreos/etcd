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

// Package version implements etcd version parsing and contains latest version
// information.

package etcdserver

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"

	bolt "go.etcd.io/bbolt"
	"go.etcd.io/etcd/api/v3/etcdserverpb"
	"go.etcd.io/etcd/api/v3/version"
	"go.etcd.io/etcd/client/pkg/v3/types"
	"go.etcd.io/etcd/pkg/v3/pbutil"
	"go.etcd.io/etcd/server/v3/config"
	"go.etcd.io/etcd/server/v3/etcdserver/api/membership"
	"go.etcd.io/etcd/server/v3/etcdserver/cindex"
	serverstorage "go.etcd.io/etcd/server/v3/storage"
	"go.etcd.io/etcd/server/v3/storage/datadir"
	"go.etcd.io/etcd/server/v3/storage/schema"
	"go.etcd.io/etcd/server/v3/storage/wal"
	"go.etcd.io/etcd/server/v3/storage/wal/walpb"
	"go.etcd.io/raft/v3/raftpb"
)

func TestBootstrapExistingClusterNoWALMaxLearner(t *testing.T) {
	tests := []struct {
		name          string
		members       []etcdserverpb.Member
		maxLearner    int
		hasError      bool
		expectedError error
	}{
		{
			name: "bootstrap success: maxLearner gt learner count",
			members: []etcdserverpb.Member{
				{ID: 4512484362714696085, PeerURLs: []string{"http://localhost:2380"}},
				{ID: 5321713336100798248, PeerURLs: []string{"http://localhost:2381"}},
				{ID: 5670219998796287055, PeerURLs: []string{"http://localhost:2382"}},
			},
			maxLearner:    1,
			hasError:      false,
			expectedError: nil,
		},
		{
			name: "bootstrap success: maxLearner eq learner count",
			members: []etcdserverpb.Member{
				{ID: 4512484362714696085, PeerURLs: []string{"http://localhost:2380"}, IsLearner: true},
				{ID: 5321713336100798248, PeerURLs: []string{"http://localhost:2381"}},
				{ID: 5670219998796287055, PeerURLs: []string{"http://localhost:2382"}, IsLearner: true},
			},
			maxLearner:    2,
			hasError:      false,
			expectedError: nil,
		},
		{
			name: "bootstrap fail: maxLearner lt learner count",
			members: []etcdserverpb.Member{
				{ID: 4512484362714696085, PeerURLs: []string{"http://localhost:2380"}},
				{ID: 5321713336100798248, PeerURLs: []string{"http://localhost:2381"}, IsLearner: true},
				{ID: 5670219998796287055, PeerURLs: []string{"http://localhost:2382"}, IsLearner: true},
			},
			maxLearner:    1,
			hasError:      true,
			expectedError: membership.ErrTooManyLearners,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cluster, err := types.NewURLsMap("node0=http://localhost:2380,node1=http://localhost:2381,node2=http://localhost:2382")
			require.NoErrorf(t, err, "unexpected error: %v", err)
			cfg := config.ServerConfig{
				Name:               "node0",
				InitialPeerURLsMap: cluster,
				Logger:             zaptest.NewLogger(t),
				MaxLearners:        tt.maxLearner,
			}
			_, err = bootstrapExistingClusterNoWAL(cfg, mockBootstrapRoundTrip(tt.members))
			hasError := err != nil
			if hasError != tt.hasError {
				t.Errorf("expected error: %v got: %v", tt.hasError, err)
			}
			if hasError {
				require.Containsf(t, err.Error(), tt.expectedError.Error(), "expected error to contain: %q, got: %q", tt.expectedError.Error(), err.Error())
			}
		})
	}
}

type roundTripFunc func(r *http.Request) (*http.Response, error)

func (s roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return s(r)
}

func mockBootstrapRoundTrip(members []etcdserverpb.Member) roundTripFunc {
	return func(r *http.Request) (*http.Response, error) {
		switch {
		case strings.Contains(r.URL.String(), "/members"):
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(mockMembersJSON(members))),
				Header:     http.Header{"X-Etcd-Cluster-Id": []string{"f4588138892a16b0"}},
			}, nil
		case strings.Contains(r.URL.String(), "/version"):
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(mockVersionJSON())),
			}, nil
		case strings.Contains(r.URL.String(), DowngradeEnabledPath):
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(`false`)),
			}, nil
		}
		return nil, nil
	}
}

func mockVersionJSON() string {
	v := version.Versions{Server: "3.7.0", Cluster: "3.7.0"}
	version, _ := json.Marshal(v)
	return string(version)
}

func mockMembersJSON(m []etcdserverpb.Member) string {
	members, _ := json.Marshal(m)
	return string(members)
}

func TestBootstrapBackend(t *testing.T) {
	tests := []struct {
		name                  string
		prepareData           func(config.ServerConfig) error
		expectedConsistentIdx uint64
		expectedError         error
	}{
		{
			name:                  "bootstrap backend success: no data files",
			prepareData:           nil,
			expectedConsistentIdx: 0,
			expectedError:         nil,
		},
		{
			name:                  "bootstrap backend success: have data files and snapshot db file",
			prepareData:           prepareData,
			expectedConsistentIdx: 5,
			expectedError:         nil,
		},
		// TODO(ahrtr): add more test cases
		// https://github.com/etcd-io/etcd/issues/13507
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dataDir, err := createDataDir(t)
			require.NoErrorf(t, err, "Failed to create the data dir, unexpected error: %v", err)

			cfg := config.ServerConfig{
				Name:                "demoNode",
				DataDir:             dataDir,
				BackendFreelistType: bolt.FreelistArrayType,
				Logger:              zaptest.NewLogger(t),
			}

			if tt.prepareData != nil {
				err = tt.prepareData(cfg)
				require.NoErrorf(t, err, "failed to prepare data, unexpected error: %v", err)
			}

			haveWAL := wal.Exist(cfg.WALDir())
			backend, err := bootstrapBackend(cfg, haveWAL)
			defer t.Cleanup(func() {
				backend.Close()
			})

			hasError := err != nil
			expectedHasError := tt.expectedError != nil
			if hasError != expectedHasError {
				t.Errorf("expected error: %v got: %v", expectedHasError, err)
			}
			if hasError {
				require.Containsf(t, err.Error(), tt.expectedError.Error(), "expected error to contain: %q, got: %q", tt.expectedError.Error(), err.Error())
			}

			if backend.ci.ConsistentIndex() != tt.expectedConsistentIdx {
				t.Errorf("expected consistent index: %d, got: %d", tt.expectedConsistentIdx, backend.ci.ConsistentIndex())
			}
		})
	}
}

func createDataDir(t *testing.T) (string, error) {
	var err error

	// create the temporary data dir
	dataDir := t.TempDir()

	// create ${dataDir}/member/snap
	if err = os.MkdirAll(datadir.ToSnapDir(dataDir), 0o700); err != nil {
		return "", err
	}

	// create ${dataDir}/member/wal
	err = os.MkdirAll(datadir.ToWALDir(dataDir), 0o700)
	if err != nil {
		return "", err
	}

	return dataDir, nil
}

// prepare data for the test case
func prepareData(cfg config.ServerConfig) error {
	var snapshotTerm, snapshotIndex uint64 = 2, 5

	if err := createWALFileWithSnapshotRecord(cfg, snapshotTerm, snapshotIndex); err != nil {
		return err
	}

	return createSnapshotAndBackendDB(cfg, snapshotTerm, snapshotIndex)
}

func createWALFileWithSnapshotRecord(cfg config.ServerConfig, snapshotTerm, snapshotIndex uint64) (err error) {
	var w *wal.WAL
	if w, err = wal.Create(cfg.Logger, cfg.WALDir(), []byte("somedata")); err != nil {
		return err
	}

	defer func() {
		err = w.Close()
	}()

	walSnap := walpb.Snapshot{
		Index: &snapshotIndex,
		Term:  &snapshotTerm,
		ConfState: &raftpb.ConfState{
			Voters:    []uint64{0x00ffca74},
			AutoLeave: new(false),
		},
	}

	if err = w.SaveSnapshot(&walSnap); err != nil {
		return err
	}

	return w.Save(&raftpb.HardState{Term: &snapshotTerm, Vote: new(uint64(3)), Commit: &snapshotIndex}, nil)
}

func createSnapshotAndBackendDB(cfg config.ServerConfig, snapshotTerm, snapshotIndex uint64) error {
	var err error

	confState := raftpb.ConfState{
		Voters: []uint64{1, 2, 3},
	}

	// create snapshot db file: "%016x.snap.db"
	be := serverstorage.OpenBackend(cfg, nil)
	schema.CreateMetaBucket(be.BatchTx())
	schema.UnsafeUpdateConsistentIndex(be.BatchTx(), snapshotIndex, snapshotTerm)
	schema.MustUnsafeSaveConfStateToBackend(cfg.Logger, be.BatchTx(), &confState)
	if err = be.Close(); err != nil {
		return err
	}
	sdb := filepath.Join(cfg.SnapDir(), fmt.Sprintf("%016x.snap.db", snapshotIndex))
	if err = os.Rename(cfg.BackendPath(), sdb); err != nil {
		return err
	}

	// create backend db file
	be = serverstorage.OpenBackend(cfg, nil)
	schema.CreateMetaBucket(be.BatchTx())
	schema.UnsafeUpdateConsistentIndex(be.BatchTx(), 1, 1)
	return be.Close()
}

func TestBootstrapWALFromSnapshotBackendAheadOfWAL(t *testing.T) {
	dataDir, err := createDataDir(t)
	require.NoError(t, err)

	cfg := config.ServerConfig{
		Logger:          zaptest.NewLogger(t),
		DataDir:         dataDir,
		ForceNewCluster: true,
	}

	var ents []*raftpb.Entry
	for idx := uint64(28); idx <= 31; idx++ {
		ents = append(ents, &raftpb.Entry{
			Type:  raftpb.EntryNormal.Enum(),
			Data:  []byte("foo"),
			Term:  new(uint64(2)),
			Index: new(idx),
		})
	}

	metadata := pbutil.MustMarshalMessage(
		&etcdserverpb.Metadata{
			NodeID:    new(uint64(1)),
			ClusterID: new(uint64(1)),
		},
	)
	w, err := wal.Create(cfg.Logger, cfg.WALDir(), metadata)
	require.NoError(t, err)
	snapshot := &raftpb.Snapshot{
		Metadata: &raftpb.SnapshotMetadata{
			ConfState: &raftpb.ConfState{Voters: []uint64{1, 2, 3}},
			Index:     new(uint64(27)),
			Term:      new(uint64(2)),
		},
	}
	require.NoError(t, w.SaveSnapshot(&walpb.Snapshot{
		Index:     snapshot.Metadata.Index,
		Term:      snapshot.Metadata.Term,
		ConfState: snapshot.Metadata.ConfState,
	}))
	err = w.Save(&raftpb.HardState{Term: new(uint64(2)), Commit: new(uint64(28))}, ents)
	require.NoError(t, err)
	require.NoError(t, w.Close())

	bwal, err := bootstrapWALFromSnapshot(cfg, snapshot, cindex.NewFakeConsistentIndex(49))
	require.Error(t, err)
	require.Nil(t, bwal)
	require.Contains(t, err.Error(), "exceeds the last WAL entry index")

	// the WAL must not have been touched: no entries past the gap are
	// appended, and the hard state is left as it was
	w2, err := wal.Open(cfg.Logger, cfg.WALDir(), &walpb.Snapshot{
		Index: snapshot.Metadata.Index,
		Term:  snapshot.Metadata.Term,
	})
	require.NoError(t, err)
	defer w2.Close()
	_, _, ents2, err := w2.ReadAll()
	require.NoError(t, err)
	require.Len(t, ents2, 4)
	require.Equal(t, uint64(31), ents2[len(ents2)-1].GetIndex())
}
