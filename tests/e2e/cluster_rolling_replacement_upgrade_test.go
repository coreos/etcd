// Copyright 2026 The etcd Authors
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

package e2e

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.etcd.io/etcd/api/v3/version"
	"go.etcd.io/etcd/client/pkg/v3/fileutil"
	"go.etcd.io/etcd/server/v3/etcdserver"
	"go.etcd.io/etcd/tests/v3/framework/config"
	"go.etcd.io/etcd/tests/v3/framework/e2e"
)

func TestRollingReplacementUpgradeClusterOf1(t *testing.T) {
	testRollingReplacementUpgrade(t, rollingReplacementCase{
		clusterSize:             1,
		numberOfMembersToChange: 1,
		triggerSnapshot:         false,
	})
}

func TestRollingReplacementUpgradeClusterOf3(t *testing.T) {
	testRollingReplacementUpgrade(t, rollingReplacementCase{
		clusterSize:             3,
		numberOfMembersToChange: 3,
		triggerSnapshot:         false,
	})
}

func TestRollingReplacementUpgrade1InClusterOf3(t *testing.T) {
	testRollingReplacementUpgrade(t, rollingReplacementCase{
		clusterSize:             3,
		numberOfMembersToChange: 1,
		triggerSnapshot:         false,
	})
}

func TestRollingReplacementUpgradeClusterOf3WithSnapshot(t *testing.T) {
	testRollingReplacementUpgrade(t, rollingReplacementCase{
		clusterSize:             3,
		numberOfMembersToChange: 3,
		triggerSnapshot:         true,
	})
}

type rollingReplacementCase struct {
	clusterSize             int
	numberOfMembersToChange int
	triggerSnapshot         bool
}

func testRollingReplacementUpgrade(t *testing.T, tc rollingReplacementCase) {
	if !fileutil.Exist(e2e.BinPath.EtcdLastRelease) {
		t.Skipf("%q does not exist", e2e.BinPath.EtcdLastRelease)
	}

	e2e.BeforeTest(t)

	currentVersion, err := e2e.GetVersionFromBinary(e2e.BinPath.Etcd)
	require.NoError(t, err)
	currentVersion.PreRelease = ""

	lastVersion, err := e2e.GetVersionFromBinary(e2e.BinPath.EtcdLastRelease)
	require.NoError(t, err)

	require.Equalf(t, lastVersion.Minor, currentVersion.Minor-1, "unexpected minor version difference")

	t.Logf("Creating cluster at version %s with %d member(s)", lastVersion, tc.clusterSize)
	var snapshotCount uint64 = 10
	epc, err := e2e.NewEtcdProcessCluster(t.Context(), t,
		e2e.WithVersion(e2e.LastVersion),
		e2e.WithClusterSize(tc.clusterSize),
		e2e.WithSnapshotCount(snapshotCount),
	)
	require.NoError(t, err)
	t.Cleanup(func() {
		if cerr := epc.Close(); cerr != nil {
			t.Logf("error closing etcd processes: %v", cerr)
		}
	})

	cc := epc.Etcdctl()

	t.Logf("Populating the cluster with some keys")
	const seededKeys = 5
	for i := 0; i < seededKeys; i++ {
		_, err = cc.Put(t.Context(), fmt.Sprintf("key-%d", i), fmt.Sprintf("val-%d", i), config.PutOptions{})
		require.NoError(t, err)
	}

	if tc.triggerSnapshot {
		t.Logf("Forcing snapshot creation so new learners must receive a snapshot from the leader")
		generateSnapshot(t, snapshotCount, cc)
		verifySnapshot(t, epc)
	}

	if tc.clusterSize > 1 {
		t.Log("Waiting health interval before reconfiguring cluster")
		time.Sleep(etcdserver.HealthInterval)
	}

	beforeMembers, beforeKV := getMembersAndKeys(t, cc)
	require.Lenf(t, beforeMembers.Members, tc.clusterSize, "unexpected starting member count")

	t.Logf("Rolling-replacement upgrade %d of %d members from %s to %s",
		tc.numberOfMembersToChange, tc.clusterSize, lastVersion, currentVersion)
	err = e2e.RollingReplaceMembers(t, nil, epc, tc.numberOfMembersToChange, false, lastVersion, currentVersion)
	require.NoError(t, err)

	t.Log("Verifying cluster size and member binary versions after replacement")
	require.Lenf(t, epc.Procs, tc.clusterSize, "cluster size must stay constant during rolling replacement")

	replacedAtCurrent := 0
	for _, p := range epc.Procs {
		switch p.Config().ExecPath {
		case e2e.BinPath.Etcd:
			replacedAtCurrent++
		case e2e.BinPath.EtcdLastRelease:
		default:
			t.Fatalf("unexpected member binary path %q", p.Config().ExecPath)
		}
	}
	assert.Equalf(t, tc.numberOfMembersToChange, replacedAtCurrent, "number of members on the target binary should match the requested change count")

	t.Log("Verifying the keys that existed before replacement still resolve")
	postCC := epc.Etcdctl()
	afterMembers, afterKV := getMembersAndKeys(t, postCC)
	assert.Equalf(t, beforeKV.Kvs, afterKV.Kvs, "keys must survive rolling replacement")
	assert.Lenf(t, afterMembers.Members, tc.clusterSize, "post-replacement member count must equal starting size")

	t.Log("Verifying every replaced member reports the expected server version")
	for _, p := range epc.Procs {
		if p.Config().ExecPath != e2e.BinPath.Etcd {
			continue
		}
		e2e.ValidateVersion(t, epc.Cfg, p, version.Versions{
			Server: currentVersion.String(),
		})
	}

	t.Log("Ensuring no leftover learner remains; every member is a voting member")
	ensureAllMembersAreVotingMembers(t, epc)
}
