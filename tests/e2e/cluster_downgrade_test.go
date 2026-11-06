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

package e2e

import (
	"context"
	"fmt"
	"math/rand"
	"testing"
	"time"

	"github.com/Masterminds/semver/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	"go.etcd.io/etcd/api/v3/version"
	"go.etcd.io/etcd/client/pkg/v3/fileutil"
	clientv3 "go.etcd.io/etcd/client/v3"
	"go.etcd.io/etcd/server/v3/etcdserver"
	"go.etcd.io/etcd/server/v3/storage/datadir"
	"go.etcd.io/etcd/server/v3/storage/wal"
	"go.etcd.io/etcd/tests/v3/framework/config"
	"go.etcd.io/etcd/tests/v3/framework/e2e"
)

type CancellationState int

const (
	noCancellation CancellationState = iota
	cancelRightBeforeEnable
	cancelRightAfterEnable
	cancelAfterDowngrading
)

func TestDowngradeUpgradeClusterOf1(t *testing.T) {
	testDowngradeUpgrade(t, 1, 1, false, noCancellation)
}

func TestDowngradeUpgrade2InClusterOf3(t *testing.T) {
	testDowngradeUpgrade(t, 2, 3, false, noCancellation)
}

func TestDowngradeUpgradeClusterOf3(t *testing.T) {
	testDowngradeUpgrade(t, 3, 3, false, noCancellation)
}

func TestDowngradeUpgradeClusterOf1WithSnapshot(t *testing.T) {
	testDowngradeUpgrade(t, 1, 1, true, noCancellation)
}

func TestDowngradeUpgradeClusterOf3WithSnapshot(t *testing.T) {
	testDowngradeUpgrade(t, 3, 3, true, noCancellation)
}

func TestDowngradeCancellationWithoutEnablingClusterOf1(t *testing.T) {
	testDowngradeUpgrade(t, 0, 1, false, cancelRightBeforeEnable)
}

func TestDowngradeCancellationRightAfterEnablingClusterOf1(t *testing.T) {
	testDowngradeUpgrade(t, 0, 1, false, cancelRightAfterEnable)
}

func TestDowngradeCancellationWithoutEnablingClusterOf3(t *testing.T) {
	testDowngradeUpgrade(t, 0, 3, false, cancelRightBeforeEnable)
}

func TestDowngradeCancellationRightAfterEnablingClusterOf3(t *testing.T) {
	testDowngradeUpgrade(t, 0, 3, false, cancelRightAfterEnable)
}

func TestDowngradeCancellationAfterDowngrading1InClusterOf3(t *testing.T) {
	testDowngradeUpgrade(t, 1, 3, false, cancelAfterDowngrading)
}

func TestDowngradeCancellationAfterDowngrading2InClusterOf3(t *testing.T) {
	testDowngradeUpgrade(t, 2, 3, false, cancelAfterDowngrading)
}

func testDowngradeUpgrade(t *testing.T, numberOfMembersToDowngrade int, clusterSize int, triggerSnapshot bool, triggerCancellation CancellationState) {
	currentEtcdBinary := e2e.BinPath.Etcd
	lastReleaseBinary := e2e.BinPath.EtcdLastRelease
	if !fileutil.Exist(lastReleaseBinary) {
		t.Skipf("%q does not exist", lastReleaseBinary)
	}

	currentVersion, err := e2e.GetVersionFromBinary(currentEtcdBinary)
	require.NoError(t, err)
	// wipe any pre-release suffix like -alpha.0 we see commonly in builds
	currentVersionWithoutPrerelease, err := currentVersion.SetPrerelease("")
	require.NoError(t, err)
	currentVersion = &currentVersionWithoutPrerelease

	lastVersion, err := e2e.GetVersionFromBinary(lastReleaseBinary)
	require.NoError(t, err)

	require.Equalf(t, lastVersion.Minor(), currentVersion.Minor()-1, "unexpected minor version difference")
	currentVersionStr := currentVersion.String()
	lastVersionStr := lastVersion.String()

	lastClusterVersion, err := semver.StrictNewVersion(lastVersionStr)
	require.NoError(t, err)
	lastClusterVersion = semver.New(lastClusterVersion.Major(), lastClusterVersion.Minor(), 0, "", "")

	e2e.BeforeTest(t)

	t.Logf("Create cluster with version %s", currentVersionStr)
	var snapshotCount uint64 = 10
	epc := newCluster(t, clusterSize, snapshotCount)
	for i := 0; i < len(epc.Procs); i++ {
		e2e.ValidateVersion(t, epc.Cfg, epc.Procs[i], version.Versions{
			Cluster: currentVersionStr,
			Server:  e2e.ServerVersion(),
			Storage: currentVersionStr,
		})
	}
	cc := epc.Etcdctl()
	t.Logf("Cluster created")
	if len(epc.Procs) > 1 {
		t.Log("Waiting health interval to required to make membership changes")
		time.Sleep(etcdserver.HealthInterval)
	}

	t.Log("Downgrade should be disabled")
	e2e.ValidateDowngradeInfo(t, epc, &pb.DowngradeInfo{Enabled: false})

	t.Log("Adding member to test membership, but a learner avoid breaking quorum")
	resp, err := cc.MemberAddAsLearner(t.Context(), "fake1", []string{"http://127.0.0.1:1001"})
	require.NoError(t, err)
	if triggerSnapshot {
		t.Logf("Generating snapshot")
		generateSnapshot(t, snapshotCount, cc)
		verifySnapshot(t, epc)
	}
	t.Log("Removing learner to test membership")
	_, err = cc.MemberRemove(t.Context(), resp.Member.ID)
	require.NoError(t, err)
	beforeMembers, beforeKV := getMembersAndKeys(t, cc)

	if triggerCancellation == cancelRightBeforeEnable {
		t.Logf("Cancelling downgrade before enabling")
		e2e.DowngradeCancel(t, epc)
		t.Log("Downgrade cancelled, validating if cluster is in the right state")
		e2e.ValidateMemberVersions(t, epc, generateIdenticalVersions(clusterSize, currentVersion))

		return // No need to perform downgrading, end the test here
	}
	e2e.DowngradeEnable(t, epc, lastVersion)

	t.Log("Downgrade should be enabled")
	e2e.ValidateDowngradeInfo(t, epc, &pb.DowngradeInfo{Enabled: true, TargetVersion: lastClusterVersion.String()})

	if triggerCancellation == cancelRightAfterEnable {
		t.Logf("Cancelling downgrade right after enabling (no node is downgraded yet)")
		e2e.DowngradeCancel(t, epc)
		t.Log("Downgrade cancelled, validating if cluster is in the right state")
		e2e.ValidateMemberVersions(t, epc, generateIdenticalVersions(clusterSize, currentVersion))
		return // No need to perform downgrading, end the test here
	}

	membersToChange := rand.Perm(len(epc.Procs))[:numberOfMembersToDowngrade]
	t.Logf("Elect members for operations on members: %v", membersToChange)

	t.Logf("Starting downgrade process to %q", lastVersionStr)
	err = e2e.DowngradeUpgradeMembersByID(t, nil, epc, membersToChange, true, currentVersion, lastClusterVersion)
	require.NoError(t, err)
	if len(membersToChange) == len(epc.Procs) {
		e2e.AssertProcessLogs(t, epc.Procs[epc.WaitLeader(t)], "the cluster has been downgraded")
	}

	t.Log("Downgrade complete")
	afterMembers, afterKV := getMembersAndKeys(t, cc)
	assert.Equal(t, beforeKV.Kvs, afterKV.Kvs)
	assert.Equal(t, beforeMembers.Members, afterMembers.Members)

	if len(epc.Procs) > 1 {
		t.Log("Waiting health interval to required to make membership changes")
		time.Sleep(etcdserver.HealthInterval)
	}

	if triggerCancellation == cancelAfterDowngrading {
		e2e.DowngradeCancel(t, epc)
		t.Log("Downgrade cancelled, validating if cluster is in the right state")
		e2e.ValidateMemberVersions(t, epc, generatePartialCancellationVersions(clusterSize, membersToChange, lastClusterVersion))
	}

	t.Log("Adding learner to test membership, but avoid breaking quorum")
	resp, err = cc.MemberAddAsLearner(t.Context(), "fake2", []string{"http://127.0.0.1:1002"})
	require.NoError(t, err)
	if triggerSnapshot {
		t.Logf("Generating snapshot")
		generateSnapshot(t, snapshotCount, cc)
		verifySnapshot(t, epc)
	}
	t.Log("Removing learner to test membership")
	_, err = cc.MemberRemove(t.Context(), resp.Member.ID)
	require.NoError(t, err)
	beforeMembers, beforeKV = getMembersAndKeys(t, cc)

	t.Logf("Starting upgrade process to %q", currentVersionStr)
	downgradeEnabled := triggerCancellation == noCancellation && numberOfMembersToDowngrade < clusterSize
	err = e2e.DowngradeUpgradeMembersByID(t, nil, epc, membersToChange, downgradeEnabled, lastClusterVersion, currentVersion)
	require.NoError(t, err)
	t.Log("Upgrade complete")

	if downgradeEnabled {
		t.Log("Downgrade should be still enabled")
		e2e.ValidateDowngradeInfo(t, epc, &pb.DowngradeInfo{Enabled: true, TargetVersion: lastClusterVersion.String()})
	} else {
		t.Log("Downgrade should be disabled")
		e2e.ValidateDowngradeInfo(t, epc, &pb.DowngradeInfo{Enabled: false})
	}

	afterMembers, afterKV = getMembersAndKeys(t, cc)
	assert.Equal(t, beforeKV.Kvs, afterKV.Kvs)
	assert.Equal(t, beforeMembers.Members, afterMembers.Members)
}

func newCluster(t *testing.T, clusterSize int, snapshotCount uint64) *e2e.EtcdProcessCluster {
	epc, err := e2e.NewEtcdProcessCluster(t.Context(), t,
		e2e.WithClusterSize(clusterSize),
		e2e.WithSnapshotCount(snapshotCount),
		e2e.WithKeepDataDir(true),
	)
	if err != nil {
		t.Fatalf("could not start etcd process cluster (%v)", err)
	}
	t.Cleanup(func() {
		if errC := epc.Close(); errC != nil {
			t.Fatalf("error closing etcd processes (%v)", errC)
		}
	})
	return epc
}

func generateSnapshot(t *testing.T, snapshotCount uint64, cc *e2e.EtcdctlV3) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	var i uint64
	t.Logf("Adding keys")
	for i = 0; i < snapshotCount*3; i++ {
		_, err := cc.Put(ctx, fmt.Sprintf("%d", i), "1", config.PutOptions{})
		assert.NoError(t, err)
	}
}

func verifySnapshot(t *testing.T, epc *e2e.EtcdProcessCluster) {
	for i := range epc.Procs {
		t.Logf("Verifying snapshot for member %d", i)
		walSnaps, err := wal.ValidSnapshotEntries(epc.Cfg.Logger, datadir.ToWALDir(epc.Procs[i].Config().DataDirPath))
		require.NoError(t, err)
		require.GreaterOrEqual(t, len(walSnaps), 1)
	}
	t.Logf("All members have a valid snapshot")
}

func verifySnapshotMembers(t *testing.T, epc *e2e.EtcdProcessCluster, expectedMembers *clientv3.MemberListResponse) {
	for i := range epc.Procs {
		t.Logf("Verifying snapshot for member %d", i)
		walSnaps, err := wal.ValidSnapshotEntries(epc.Cfg.Logger, datadir.ToWALDir(epc.Procs[i].Config().DataDirPath))
		require.NoError(t, err)
		require.GreaterOrEqual(t, len(walSnaps), 1)
		latestSnap := walSnaps[len(walSnaps)-1]
		voters := latestSnap.GetConfState().GetVoters()
		for _, m := range expectedMembers.Members {
			require.Contains(t, voters, m.ID)
		}
		t.Logf("Verifed snapshot for member %d", i)
	}
	t.Log("All members have a valid snapshot")
}

func getMembersAndKeys(t *testing.T, cc *e2e.EtcdctlV3) (*clientv3.MemberListResponse, *clientv3.GetResponse) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	kvs, err := cc.Get(ctx, "", config.GetOptions{Prefix: true})
	require.NoError(t, err)

	members, err := cc.MemberList(ctx, false)
	assert.NoError(t, err)

	return members, kvs
}

func generateIdenticalVersions(clusterSize int, ver *semver.Version) []*version.Versions {
	ret := make([]*version.Versions, clusterSize)

	for i := range clusterSize {
		ret[i] = &version.Versions{
			Cluster: ver.String(),
			Server:  ver.String(),
			Storage: ver.String(),
		}
	}

	return ret
}

func generatePartialCancellationVersions(clusterSize int, membersToChange []int, ver *semver.Version) []*version.Versions {
	ret := make([]*version.Versions, clusterSize)

	for i := range clusterSize {
		ret[i] = &version.Versions{
			Cluster: ver.String(),
			Server:  e2e.OffsetMinor(ver, 1).String(),
			Storage: "",
		}
	}

	for i := range membersToChange {
		ret[membersToChange[i]].Server = ver.String()
	}

	return ret
}
