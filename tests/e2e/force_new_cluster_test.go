// Copyright 2025 The etcd Authors
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

//go:build !cluster_proxy

package e2e

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"go.etcd.io/bbolt"
	"go.etcd.io/etcd/server/v3/etcdserver/api/membership"
	"go.etcd.io/etcd/server/v3/storage/datadir"
	"go.etcd.io/etcd/server/v3/storage/schema"
	"go.etcd.io/etcd/tests/v3/framework/config"
	"go.etcd.io/etcd/tests/v3/framework/e2e"
	"go.etcd.io/etcd/tests/v3/framework/testutils"
)

// TestForceNewCluster verified that etcd works as expected when --force-new-cluster.
// Refer to discussion in https://github.com/etcd-io/etcd/issues/20009.
func TestForceNewCluster(t *testing.T) {
	e2e.BeforeTest(t)

	testCases := []struct {
		name      string
		snapcount int
	}{
		{
			name:      "create a snapshot after promotion",
			snapcount: 10,
		},
		{
			name:      "no snapshot after promotion",
			snapcount: 0,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			epc, promotedMembers := mustCreateNewClusterByPromotingMembers(t, e2e.CurrentVersion, 5,
				e2e.WithKeepDataDir(true), e2e.WithSnapshotCount(uint64(tc.snapcount)))
			require.Len(t, promotedMembers, 4)

			for i := 0; i < tc.snapcount; i++ {
				_, err := epc.Etcdctl().Put(t.Context(), "foo", "bar", config.PutOptions{})
				require.NoError(t, err)
			}

			require.NoError(t, epc.Close())

			m := epc.Procs[0]
			t.Logf("Forcibly create a one-member cluster with member: %s", m.Config().Name)
			m.Config().Args = append(m.Config().Args, "--force-new-cluster")
			require.NoError(t, m.Start(t.Context()))

			t.Log("Restarting the member")
			require.NoError(t, m.Restart(t.Context()))

			t.Log("Closing the member")
			require.NoError(t, m.Close())
		})
	}
}

func TestForceNewCluster_MemberCount(t *testing.T) {
	e2e.BeforeTest(t)

	epc, promotedMembers := mustCreateNewClusterByPromotingMembers(t, e2e.CurrentVersion, 3, e2e.WithKeepDataDir(true))
	require.Len(t, promotedMembers, 2)

	// Wait for the backend TXN to sync/commit the data to disk, to ensure
	// the consistent-index is persisted. Another way is to issue a snapshot
	// command to forcibly commit the backend TXN.
	time.Sleep(time.Second)

	t.Log("Killing all the members")
	require.NoError(t, epc.Kill())
	require.NoError(t, epc.Wait(t.Context()))

	m := epc.Procs[0]
	t.Logf("Forcibly create a one-member cluster with member: %s", m.Config().Name)
	m.Config().Args = append(m.Config().Args, "--force-new-cluster")
	require.NoError(t, m.Start(t.Context()))

	t.Log("Online checking the member count")
	mresp, merr := m.Etcdctl().MemberList(t.Context(), false)
	require.NoError(t, merr)
	require.Len(t, mresp.Members, 1)

	t.Log("Closing the member")
	require.NoError(t, m.Close())
	require.NoError(t, m.Wait(t.Context()))

	t.Log("Offline checking the member count")
	members := mustReadMembersFromBoltDB(t, m.Config().DataDirPath)
	require.Len(t, members, 1)
}

// TestForceNewCluster_AddLearner_MemberCount verifies that `--force-new-cluster`
// should always be able to clean up all other members, including learners.
func TestForceNewCluster_AddLearner_MemberCount(t *testing.T) {
	e2e.BeforeTest(t)

	testCases := []struct {
		name      string
		snapcount int
	}{
		{
			name:      "no snapshot after adding learner",
			snapcount: 0,
		},
		{
			name:      "create a snapshot after adding learner",
			snapcount: 5,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := e2e.NewConfig(e2e.WithClusterSize(3))
			epc, err := e2e.NewEtcdProcessCluster(t.Context(), t, e2e.WithConfig(cfg), e2e.WithSnapshotCount(uint64(tc.snapcount)), e2e.WithKeepDataDir(true))
			require.NoError(t, err)

			t.Log("Adding a learner member")
			testutils.ExecuteWithTimeout(t, 1*time.Minute, func() {
				for {
					_, aerr := epc.StartNewProc(t.Context(), nil, t, true)
					if aerr != nil {
						if strings.Contains(aerr.Error(), "etcdserver: unhealthy cluster") {
							time.Sleep(1 * time.Second)
							continue
						}
					}
					break
				}
			})

			for i := 0; i < tc.snapcount; i++ {
				_, werr := epc.Etcdctl().Put(t.Context(), "foo", "bar", config.PutOptions{})
				require.NoError(t, werr)
			}
			require.NoError(t, epc.Close())

			m := epc.Procs[0]
			t.Logf("Forcibly create a one-member cluster with member: %s", m.Config().Name)
			m.Config().Args = append(m.Config().Args, "--force-new-cluster")
			require.NoError(t, m.Start(t.Context()))

			t.Log("Restarting the member")
			require.NoError(t, m.Restart(t.Context()))
			defer func() {
				t.Log("Closing the member")
				require.NoError(t, m.Close())
			}()

			t.Log("Checking member count")
			resp, merr := m.Etcdctl().MemberList(t.Context(), false)
			require.NoError(t, merr)
			require.Len(t, resp.Members, 1)
		})
	}
}

func mustReadMembersFromBoltDB(t *testing.T, dataDir string) []*membership.Member {
	dbPath := datadir.ToBackendFileName(dataDir)
	db, err := bbolt.Open(dbPath, 0o400, &bbolt.Options{ReadOnly: true})
	require.NoError(t, err)
	defer func() {
		require.NoError(t, db.Close())
	}()

	var members []*membership.Member
	_ = db.View(func(tx *bbolt.Tx) error {
		b := tx.Bucket(schema.Members.Name())
		_ = b.ForEach(func(k, v []byte) error {
			m := membership.Member{}
			err := json.Unmarshal(v, &m)
			require.NoError(t, err)
			members = append(members, &m)
			return nil
		})
		return nil
	})

	return members
}

// TestForceNewCluster_BackendConsistentIndexAheadOfWAL ensures that
// --force-new-cluster fails cleanly when the backend consistent index is
// ahead of the last entry in the WAL.
func TestForceNewCluster_BackendConsistentIndexAheadOfWAL(t *testing.T) {
	e2e.BeforeTest(t)

	epc, err := e2e.NewEtcdProcessCluster(t.Context(), t,
		e2e.WithConfig(e2e.NewConfig(e2e.WithClusterSize(3))),
		e2e.WithKeepDataDir(true),
	)
	require.NoError(t, err)

	t.Log("Writing some keys")
	for i := 0; i < 5; i++ {
		_, err = epc.Etcdctl().Put(t.Context(), fmt.Sprintf("key-%d", i), "value", config.PutOptions{})
		require.NoError(t, err)
	}
	require.NoError(t, epc.Close())

	victim := epc.Procs[0]
	t.Log("Simulating a crash where the backend consistent index advanced past the last entry written to the WAL")
	bumpBackendConsistentIndex(t, victim.Config().DataDirPath, 5)

	t.Logf("Forcibly create a one-member cluster with member: %s", victim.Config().Name)
	victim.Config().Args = append(victim.Config().Args, "--force-new-cluster")

	t.Log("The bootstrap must fail cleanly with an error instead of panicking")
	require.Error(t, victim.Start(t.Context()))

	t.Log("Checking the failure is intentional and explained in the member log")
	require.Contains(t, strings.Join(victim.Logs().Lines(), "\n"), "adjusted commit index is ahead of the last WAL entry")
}

// bumpBackendConsistentIndex advances the consistent index persisted in the
// member's backend by delta, without writing the corresponding entries to
// the WAL.
func bumpBackendConsistentIndex(t *testing.T, dataDir string, delta uint64) {
	dbPath := datadir.ToBackendFileName(dataDir)
	db, err := bbolt.Open(dbPath, 0o600, nil)
	require.NoError(t, err)
	defer func() {
		require.NoError(t, db.Close())
	}()
	require.NoError(t, db.Update(func(tx *bbolt.Tx) error {
		b := tx.Bucket(schema.Meta.Name())
		if b == nil {
			return errors.New("meta bucket not found")
		}
		key := schema.MetaConsistentIndexKeyName
		v := b.Get(key)
		if len(v) != 8 {
			return fmt.Errorf("unexpected consistent_index value length: %d", len(v))
		}
		buf := make([]byte, 8)
		binary.BigEndian.PutUint64(buf, binary.BigEndian.Uint64(v)+delta)
		return b.Put(key, buf)
	}))
}
