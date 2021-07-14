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

package version

import (
	"testing"
	"time"

	"github.com/coreos/go-semver/semver"
	"github.com/stretchr/testify/assert"
	"go.etcd.io/etcd/raft/v3/raftpb"
	"go.etcd.io/etcd/server/v3/mvcc/backend"
	betesting "go.etcd.io/etcd/server/v3/mvcc/backend/testing"
	"go.etcd.io/etcd/server/v3/mvcc/buckets"
	"go.uber.org/zap"
)

var (
	V3_4 = semver.Version{Major: 3, Minor: 4}
	V3_7 = semver.Version{Major: 3, Minor: 7}
)

func TestUpdateStorageVersion(t *testing.T) {
	tcs := []struct {
		name            string
		storageVersion  *semver.Version
		storageMetaKeys [][]byte

		targetVersion *semver.Version

		expectVersion  *semver.Version
		expectError    bool
		expectErrorMsg string
		expectPanic    bool
	}{
		{
			name:           `Upgrading v3.5 to v3.6 should be rejected if confstate is not set`,
			storageVersion: nil,
			targetVersion:  &V3_6,
			expectVersion:  nil,
			expectError:    true,
			expectErrorMsg: `cannot determine storage version: missing "confState" key`,
		},
		{
			name:            `Upgrading v3.5 to v3.6 should be rejected if term is not set`,
			storageVersion:  nil,
			storageMetaKeys: [][]byte{buckets.MetaConfStateName},
			targetVersion:   &V3_6,
			expectVersion:   nil,
			expectError:     true,
			expectErrorMsg:  `cannot determine storage version: missing "term" key`,
		},
		{
			name:            `Upgrading v3.5 to v3.6 should be succeed all required fields are set`,
			storageVersion:  nil,
			storageMetaKeys: [][]byte{buckets.MetaTermKeyName, buckets.MetaConfStateName},
			targetVersion:   &V3_6,
			expectVersion:   &V3_6,
		},
		{
			name:            `Migrate on same v3.6 version should be an no-op`,
			storageVersion:  &V3_6,
			storageMetaKeys: [][]byte{buckets.MetaTermKeyName, buckets.MetaConfStateName, buckets.MetaStorageVersionName},
			targetVersion:   &V3_6,
			expectVersion:   &V3_6,
		},
		{
			name:            "Upgrading 3.6 to v3.7 is not supported",
			storageVersion:  &V3_6,
			storageMetaKeys: [][]byte{buckets.MetaTermKeyName, buckets.MetaConfStateName, buckets.MetaStorageVersionName},
			targetVersion:   &V3_7,
			expectVersion:   &V3_6,
			expectPanic:     true,
		},
		{
			name:            "Downgrading v3.7 to v3.6 is not supported",
			storageVersion:  &V3_7,
			storageMetaKeys: [][]byte{buckets.MetaTermKeyName, buckets.MetaConfStateName, buckets.MetaStorageVersionName, []byte("future-key")},
			targetVersion:   &V3_6,
			expectVersion:   &V3_7,
			expectPanic:     true,
		},
		{
			name:            "Downgrading v3.6 to v3.5 is not supported",
			storageVersion:  &V3_6,
			storageMetaKeys: [][]byte{buckets.MetaTermKeyName, buckets.MetaConfStateName, buckets.MetaStorageVersionName},
			targetVersion:   &V3_5,
			expectVersion:   &V3_6,
			expectPanic:     true,
		},
		{
			name:            "Downgrading v3.5 to v3.4 is not supported",
			storageVersion:  nil,
			storageMetaKeys: [][]byte{buckets.MetaTermKeyName, buckets.MetaConfStateName},
			targetVersion:   &V3_4,
			expectVersion:   nil,
			expectPanic:     true,
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			lg := zap.NewNop()
			be, tmpPath := betesting.NewTmpBackend(t, time.Microsecond, 10)
			tx := be.BatchTx()
			if tx == nil {
				t.Fatal("batch tx is nil")
			}
			tx.Lock()
			buckets.UnsafeCreateMetaBucket(tx)
			for _, k := range tc.storageMetaKeys {
				switch string(k) {
				case string(buckets.MetaConfStateName):
					buckets.MustUnsafeSaveConfStateToBackend(lg, tx, &raftpb.ConfState{})
				case string(buckets.MetaTermKeyName):
					buckets.UnsafeUpdateConsistentIndex(tx, 1, 1, false)
				default:
					tx.UnsafePut(buckets.Meta, k, []byte{})
				}
			}
			if tc.storageVersion != nil {
				buckets.UnsafeSetStorageVersion(tx, tc.storageVersion)
			}
			tx.Unlock()
			be.ForceCommit()
			be.Close()

			b := backend.NewDefaultBackend(tmpPath)
			defer b.Close()
			paniced, err := tryMigrate(lg, b.BatchTx(), *tc.targetVersion)
			if (err != nil) != tc.expectError {
				t.Errorf("Migrate(lg, tx, %q) = %+v, expected error: %v", tc.targetVersion, err, tc.expectError)
			}
			if err != nil && err.Error() != tc.expectErrorMsg {
				t.Errorf("Migrate(lg, tx, %q) = %q, expected error message: %q", tc.targetVersion, err, tc.expectErrorMsg)
			}
			v := buckets.UnsafeReadStorageVersion(b.BatchTx())
			assert.Equal(t, tc.expectVersion, v)
			if (paniced != nil) != tc.expectPanic {
				t.Errorf("Migrate(lg, tx, %q) panic=%q, expected %v", tc.targetVersion, paniced, tc.expectPanic)
			}
		})
	}
}

func tryMigrate(lg *zap.Logger, be backend.BatchTx, target semver.Version) (panic interface{}, err error) {
	defer func() {
		panic = recover()
	}()
	err = Migrate(lg, be, target)
	return panic, err
}
