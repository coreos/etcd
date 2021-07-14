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
	"fmt"

	"github.com/coreos/go-semver/semver"
	"go.uber.org/zap"

	"go.etcd.io/etcd/server/v3/mvcc/backend"
	"go.etcd.io/etcd/server/v3/mvcc/buckets"
)

var (
	V3_5 = semver.Version{Major: 3, Minor: 5}
	V3_6 = semver.Version{Major: 3, Minor: 6}
)

// Migrate updates storage version to provided target version.
func Migrate(lg *zap.Logger, tx backend.BatchTx, target semver.Version) error {
	ver, err := detectStorageVersion(lg, tx)
	if err != nil {
		return fmt.Errorf("cannot determine storage version: %w", err)
	}
	if ver.Major != target.Major {
		lg.Panic("Changing major storage version is not supported",
			zap.String("storage-version", ver.String()),
			zap.String("target-storage-version", target.String()),
		)
	}
	if ver.Minor > target.Minor {
		lg.Panic("Target version is lower than the current version, specific higher version, downgrades are not yet supported",
			zap.String("storage-version", ver.String()),
			zap.String("target-storage-version", target.String()),
		)
	}
	for ver.Minor != target.Minor {
		upgrade := ver.Minor < target.Minor
		next, err := migrateOnce(lg, tx, *ver, upgrade)
		if err != nil {
			return err
		}
		ver = next
		lg.Info("upgraded storage version", zap.String("storage-version", ver.String()))
	}
	return nil
}

func detectStorageVersion(lg *zap.Logger, tx backend.ReadTx) (*semver.Version, error) {
	tx.Lock()
	defer tx.Unlock()
	v := buckets.UnsafeReadStorageVersion(tx)
	if v != nil {
		return v, nil
	}
	confstate := buckets.UnsafeConfStateFromBackend(lg, tx)
	if confstate == nil {
		return nil, fmt.Errorf("missing %q key", buckets.MetaConfStateName)
	}
	_, term := buckets.UnsafeReadConsistentIndex(tx)
	if term == 0 {
		return nil, fmt.Errorf("missing %q key", buckets.MetaTermKeyName)
	}
	copied := V3_5
	return &copied, nil
}

func migrateOnce(lg *zap.Logger, tx backend.BatchTx, current semver.Version, upgrade bool) (*semver.Version, error) {
	var target semver.Version
	if upgrade {
		target = semver.Version{Major: current.Major, Minor: current.Minor + 1}
	} else {
		target = semver.Version{Major: current.Major, Minor: current.Minor - 1}
	}
	ms := migrations(lg, current, target)
	err := runMigrations(tx, target, ms, upgrade)
	if err != nil {
		return nil, err
	}
	return &target, nil
}

func migrations(lg *zap.Logger, current semver.Version, target semver.Version) []Migration {
	var schemaKey semver.Version
	// Migrations should be taken from higher version
	if current.LessThan(target) {
		schemaKey = target
	} else {
		schemaKey = current
	}
	ms, found := schema[schemaKey]
	if !found {
		lg.Panic("version is not supported", zap.String("storage-version", target.String()))
	}
	return ms
}

var schema = map[semver.Version][]Migration{
	V3_6: {
		&newField{bucket: buckets.Meta, fieldName: buckets.MetaStorageVersionName, fieldValue: []byte("")},
	},
}

type Migration interface {
	UnsafeUpgrade(backend.BatchTx) error
	UnsafeDowngrade(backend.BatchTx) error
}

type newField struct {
	bucket     backend.Bucket
	fieldName  []byte
	fieldValue []byte
}

func (m *newField) UnsafeUpgrade(tx backend.BatchTx) error {
	tx.UnsafePut(m.bucket, m.fieldName, m.fieldValue)
	return nil
}
func (m *newField) UnsafeDowngrade(tx backend.BatchTx) error {
	tx.UnsafeDelete(m.bucket, m.fieldName)
	return nil
}

func runMigrations(tx backend.BatchTx, target semver.Version, ms []Migration, upgrade bool) error {
	var err error
	tx.Lock()
	defer tx.Unlock()
	for _, m := range ms {
		if upgrade {
			err = m.UnsafeUpgrade(tx)
		} else {
			err = m.UnsafeDowngrade(tx)
		}
		if err != nil {
			return err
		}
	}
	// Storage version is available since v3.6, downgrading to v3.5 should clean this field.
	if !target.LessThan(V3_6) {
		buckets.UnsafeSetStorageVersion(tx, &target)
	}
	return nil
}
