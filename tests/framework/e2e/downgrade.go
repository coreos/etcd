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

package e2e

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"strings"
	"testing"
	"time"

	"github.com/Masterminds/semver/v3"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"golang.org/x/sync/errgroup"

	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	"go.etcd.io/etcd/api/v3/version"
	"go.etcd.io/etcd/server/v3/etcdserver"
	"go.etcd.io/etcd/tests/v3/framework/testutils"
)

func DowngradeEnable(t *testing.T, epc *EtcdProcessCluster, ver *semver.Version) {
	t.Logf("etcdctl downgrade enable %s", ver.String())
	c := epc.Etcdctl()
	testutils.ExecuteWithTimeout(t, 20*time.Second, func() {
		err := c.DowngradeEnable(t.Context(), ver.String())
		require.NoError(t, err)
	})

	t.Log("Downgrade enabled, validating if cluster is ready for downgrade")
	for i := 0; i < len(epc.Procs); i++ {
		ValidateVersion(t, epc.Cfg, epc.Procs[i], version.Versions{
			Cluster: ver.String(),
			Server:  OffsetMinor(ver, 1).String(),
			Storage: ver.String(),
		})
	}

	t.Log("Cluster is ready for downgrade")
}

func DowngradeCancel(t *testing.T, epc *EtcdProcessCluster) {
	c := epc.Etcdctl()

	var err error
	testutils.ExecuteWithTimeout(t, 1*time.Minute, func() {
		for {
			t.Logf("etcdctl downgrade cancel")
			err = c.DowngradeCancel(t.Context())
			if err != nil {
				if strings.Contains(err.Error(), "no inflight downgrade job") {
					// cancellation has been performed successfully
					t.Log(err)
					err = nil
					break
				}

				t.Logf("etcdctl downgrade error: %v, retrying", err)
				continue
			}

			t.Logf("etcdctl downgrade cancel executed successfully")
			break
		}
	})

	require.NoError(t, err)

	t.Log("Cluster downgrade cancellation is completed")
}

func ValidateDowngradeInfo(t *testing.T, clus *EtcdProcessCluster, expected *pb.DowngradeInfo) {
	cfg := clus.Cfg

	for i := 0; i < len(clus.Procs); i++ {
		member := clus.Procs[i]
		mc := member.Etcdctl()
		mName := member.Config().Name

		testutils.ExecuteWithTimeout(t, 1*time.Minute, func() {
			for {
				statuses, err := mc.Status(t.Context())
				if err != nil {
					cfg.Logger.Warn("failed to get member status and retrying",
						zap.Error(err),
						zap.String("member", mName))

					time.Sleep(time.Second)
					continue
				}

				require.Lenf(t, statuses, 1, "member %s", mName)
				got := (*pb.StatusResponse)(statuses[0]).GetDowngradeInfo()

				if got.GetEnabled() == expected.GetEnabled() && got.GetTargetVersion() == expected.GetTargetVersion() {
					cfg.Logger.Info("DowngradeInfo match", zap.String("member", mName))
					break
				}

				cfg.Logger.Warn("DowngradeInfo didn't match retrying",
					zap.String("member", mName),
					zap.Dict("expected",
						zap.Bool("Enabled", expected.GetEnabled()),
						zap.String("TargetVersion", expected.GetTargetVersion()),
					),
					zap.Dict("got",
						zap.Bool("Enabled", got.GetEnabled()),
						zap.String("TargetVersion", got.GetTargetVersion()),
					),
				)
				time.Sleep(time.Second)
			}
		})
	}
}

func DowngradeUpgradeMembers(t *testing.T, lg *zap.Logger, clus *EtcdProcessCluster, numberOfMembersToChange int, downgradeEnabled bool, currentVersion, targetVersion *semver.Version) error {
	membersToChange := rand.Perm(len(clus.Procs))[:numberOfMembersToChange]
	t.Logf("Elect members for operations on members: %v", membersToChange)

	return DowngradeUpgradeMembersByID(t, lg, clus, membersToChange, downgradeEnabled, currentVersion, targetVersion)
}

func DowngradeUpgradeMembersByID(t *testing.T, lg *zap.Logger, clus *EtcdProcessCluster, membersToChange []int, downgradeEnabled bool, currentVersion, targetVersion *semver.Version) error {
	if lg == nil {
		lg = clus.lg
	}
	isDowngrade := targetVersion.LessThan(currentVersion)
	opString := "upgrading"
	newExecPath := BinPath.Etcd
	if isDowngrade {
		opString = "downgrading"
		newExecPath = BinPath.EtcdLastRelease
	}

	binaryVersion, err := GetVersionFromBinary(newExecPath)
	if err != nil {
		return fmt.Errorf("failed to get binary version from %s: %w", newExecPath, err)
	}

	g := new(errgroup.Group)
	for _, memberID := range membersToChange {
		member := clus.Procs[memberID]
		if member.Config().ExecPath == newExecPath {
			return fmt.Errorf("member:%s is already running with the %s target binary - %s", member.Config().Name, opString, member.Config().ExecPath)
		}
		lg.Info(fmt.Sprintf("%s member", opString), zap.String("member", member.Config().Name))
		if err := member.Stop(); err != nil {
			return err
		}

		// When we downgrade or upgrade a member, we need to re-generate the flags, to convert some non-experimental
		// flags to experimental flags, or vice verse.
		member.Config().Args = convertFlags(member.Config().Args, binaryVersion)

		member.Config().ExecPath = newExecPath
		lg.Info("Restarting member", zap.String("member", member.Config().Name))
		// We shouldn't block on waiting for the member to be ready,
		// otherwise it will be blocked forever if other members are
		// not started yet.
		g.Go(func() error {
			return member.Start(t.Context())
		})
	}
	if err := g.Wait(); err != nil {
		return err
	}

	t.Log("Waiting health interval to make sure the leader propagates version to new processes")
	time.Sleep(etcdserver.HealthInterval)

	lg.Info("Validating versions")
	clusterVersion, err := expectedClusterVersion(clus, downgradeEnabled, currentVersion, targetVersion)
	if err != nil {
		return err
	}

	for _, memberID := range membersToChange {
		member := clus.Procs[memberID]
		ValidateVersion(t, clus.Cfg, member, version.Versions{
			Cluster: clusterVersion.String(),
			Server:  targetVersion.String(),
		})
	}
	return nil
}

func RollingReplaceMembers(t *testing.T, lg *zap.Logger, clus *EtcdProcessCluster, numberOfMembersToChange int, downgradeEnabled bool, currentVersion, targetVersion *semver.Version) error {
	membersToChange := rand.Perm(len(clus.Procs))[:numberOfMembersToChange]
	t.Logf("Elect members for rolling replacement: %v", membersToChange)

	return RollingReplaceMembersByID(t, lg, clus, membersToChange, downgradeEnabled, currentVersion, targetVersion)
}

func RollingReplaceMembersByID(t *testing.T, lg *zap.Logger, clus *EtcdProcessCluster, membersToChange []int, downgradeEnabled bool, currentVersion, targetVersion *semver.Version) error {
	if lg == nil {
		lg = clus.lg
	}
	if len(membersToChange) == 0 {
		return fmt.Errorf("membersToChange must not be empty")
	}

	isDowngrade := targetVersion.LessThan(*currentVersion)
	opString := "upgrading"
	targetExecPath := BinPath.Etcd
	targetClusterVersion := CurrentVersion
	if isDowngrade {
		opString = "downgrading"
		targetExecPath = BinPath.EtcdLastRelease
		targetClusterVersion = LastVersion
	}

	// Resolve indices to EtcdProcess pointers upfront. Each iteration
	// mutates clus.Procs (append a learner, remove a voter) so the
	// indices passed in stop pointing at the same processes after the
	// first step.
	victims := make([]EtcdProcess, len(membersToChange))
	for i, idx := range membersToChange {
		if idx < 0 || idx >= len(clus.Procs) {
			return fmt.Errorf("invalid member index %d: cluster has %d procs", idx, len(clus.Procs))
		}
		member := clus.Procs[idx]
		if member.Config().ExecPath == targetExecPath {
			return fmt.Errorf("member:%s is already running with the %s target binary - %s", member.Config().Name, opString, member.Config().ExecPath)
		}
		victims[i] = member
	}

	newMemberCfg := *clus.Cfg
	newMemberCfg.Version = targetClusterVersion
	newMemberCfg.KeepDataDir = false

	newProcs := make([]EtcdProcess, 0, len(victims))
	for iter, victim := range victims {
		lg.Info(fmt.Sprintf("%s cluster via rolling replacement", opString),
			zap.Int("iteration", iter+1),
			zap.Int("total", len(victims)),
			zap.String("victim", victim.Config().Name),
			zap.String("target", targetExecPath),
		)

		memberID, newProc, err := rollingAddLearner(t, lg, clus, &newMemberCfg)
		if err != nil {
			return fmt.Errorf("rolling replacement iter %d: add learner: %w", iter+1, err)
		}
		newProcs = append(newProcs, newProc)

		if err := rollingPromoteLearner(t, lg, clus, memberID); err != nil {
			return fmt.Errorf("rolling replacement iter %d: promote learner: %w", iter+1, err)
		}

		if err := rollingRemoveMember(t, lg, clus, victim); err != nil {
			return fmt.Errorf("rolling replacement iter %d: remove victim %s: %w", iter+1, victim.Config().Name, err)
		}

		time.Sleep(etcdserver.HealthInterval)
	}

	t.Log("Waiting health interval to make sure the leader propagates version to new processes")
	time.Sleep(etcdserver.HealthInterval)

	return validateReplacedMembers(t, lg, clus, newProcs, downgradeEnabled, currentVersion, targetVersion)
}

func validateReplacedMembers(t *testing.T, lg *zap.Logger, clus *EtcdProcessCluster, newProcs []EtcdProcess, downgradeEnabled bool, currentVersion, targetVersion *semver.Version) error {
	lg.Info("Validating versions")
	clusterVersion, err := expectedClusterVersion(clus, downgradeEnabled, currentVersion, targetVersion)
	if err != nil {
		return err
	}

	for _, p := range newProcs {
		ValidateVersion(t, clus.Cfg, p, version.Versions{
			Cluster: clusterVersion.String(),
			Server:  targetVersion.String(),
		})
	}
	return nil
}

func expectedClusterVersion(clus *EtcdProcessCluster, downgradeEnabled bool, currentVersion, targetVersion *semver.Version) (*semver.Version, error) {
	if targetVersion.LessThan(*currentVersion) {
		return targetVersion, nil
	}
	// If the downgrade isn't cancelled yet, then the cluster
	// version will always stay at the lower version, no matter
	// what's the binary version of each member.
	if downgradeEnabled {
		return currentVersion, nil
	}
	// If the downgrade has already been cancelled, then the
	// cluster version is the minimal server version.
	minVer, err := clus.MinServerVersion()
	if err != nil {
		return nil, fmt.Errorf("failed to get min server version: %w", err)
	}
	return minVer, nil
}

func rollingAddLearner(t *testing.T, lg *zap.Logger, clus *EtcdProcessCluster, newMemberCfg *EtcdProcessClusterConfig) (memberID uint64, proc EtcdProcess, err error) {
	var serverCfg *EtcdServerProcessConfig
	testutils.ExecuteWithTimeout(t, 1*time.Minute, func() {
		for {
			memberID, serverCfg, err = clus.AddMember(t.Context(), newMemberCfg, t, true)
			if err == nil {
				return
			}
			if strings.Contains(err.Error(), "etcdserver: unhealthy cluster") {
				lg.Info("AddMember not yet accepted, retrying", zap.Error(err))
				time.Sleep(time.Second)
				continue
			}
			return
		}
	})
	if err != nil {
		return 0, nil, err
	}

	lg.Info("Started new learner", zap.String("member", serverCfg.Name), zap.String("exec", serverCfg.ExecPath))
	if err = clus.StartNewProcFromConfig(t.Context(), t, serverCfg); err != nil {
		return 0, nil, fmt.Errorf("failed to start new learner: %w", err)
	}
	proc = clus.Procs[len(clus.Procs)-1]
	return memberID, proc, nil
}

func rollingPromoteLearner(t *testing.T, lg *zap.Logger, clus *EtcdProcessCluster, memberID uint64) error {
	var promoteErr error
	testutils.ExecuteWithTimeout(t, 1*time.Minute, func() {
		for {
			_, promoteErr = clus.Etcdctl().MemberPromote(t.Context(), memberID)
			if promoteErr == nil {
				return
			}
			if strings.Contains(promoteErr.Error(), "can only promote a learner member which is in sync with leader") ||
				strings.Contains(promoteErr.Error(), "etcdserver: unhealthy cluster") ||
				strings.Contains(promoteErr.Error(), "context deadline exceeded") {
				lg.Info("MemberPromote not yet ready, retrying", zap.Uint64("member-id", memberID), zap.Error(promoteErr))
				time.Sleep(1 * time.Second)
				continue
			}
			return
		}
	})
	if promoteErr != nil {
		return promoteErr
	}
	lg.Info("Promoted learner to voting member", zap.Uint64("member-id", memberID))
	return nil
}

func rollingRemoveMember(t *testing.T, lg *zap.Logger, clus *EtcdProcessCluster, victim EtcdProcess) error {
	lg.Info("Removing source-version member",
		zap.String("member", victim.Config().Name),
		zap.String("exec", victim.Config().ExecPath),
	)
	return clus.CloseProc(t.Context(), func(p EtcdProcess) bool {
		return p == victim
	})
}

func ValidateMemberVersions(t *testing.T, epc *EtcdProcessCluster, expect []*version.Versions) {
	for i := 0; i < len(epc.Procs); i++ {
		ValidateVersion(t, epc.Cfg, epc.Procs[i], *expect[i])
	}
	t.Log("Cluster member version validation after downgrade cancellation is completed")
}

func ValidateVersion(t *testing.T, cfg *EtcdProcessClusterConfig, member EtcdProcess, expect version.Versions) {
	testutils.ExecuteWithTimeout(t, 1*time.Minute, func() {
		for {
			result, err := getMemberVersionByCurl(cfg, member)
			if err != nil {
				cfg.Logger.Warn("failed to get member version and retrying", zap.Error(err), zap.String("member", member.Config().Name))
				time.Sleep(time.Second)
				continue
			}
			cfg.Logger.Info("Comparing versions", zap.String("member", member.Config().Name), zap.Any("got", result), zap.Any("want", expect))
			if err := compareMemberVersion(expect, result); err != nil {
				cfg.Logger.Warn("Versions didn't match retrying", zap.Error(err), zap.String("member", member.Config().Name))
				time.Sleep(time.Second)
				continue
			}
			cfg.Logger.Info("Versions match", zap.String("member", member.Config().Name))
			break
		}
	})
}

// OffsetMinor returns the version with offset from the original minor, with the same major.
func OffsetMinor(v *semver.Version, offset int) *semver.Version {
	var minor uint64
	if offset >= 0 {
		minor = v.Minor() + uint64(offset)
	} else {
		diff := uint64(-offset)
		if diff < v.Minor() {
			minor = v.Minor() - diff
		}
	}
	return semver.New(v.Major(), minor, 0, "", "")
}

func majorMinorVersionsEqual(v1, v2 string) (bool, error) {
	ver1, err := semver.NewVersion(v1)
	if err != nil {
		return false, err
	}
	ver2, err := semver.NewVersion(v2)
	if err != nil {
		return false, err
	}
	return ver1.Major() == ver2.Major() && ver1.Minor() == ver2.Minor(), nil
}

func compareMemberVersion(expect version.Versions, target version.Versions) error {
	if expect.Server != "" {
		result, err := majorMinorVersionsEqual(expect.Server, target.Server)
		if err != nil {
			return err
		}
		if !result {
			return fmt.Errorf("expect etcdserver version %v, but got %v", expect.Server, target.Server)
		}
	}

	if expect.Cluster != "" {
		result, err := majorMinorVersionsEqual(expect.Cluster, target.Cluster)
		if err != nil {
			return err
		}
		if !result {
			return fmt.Errorf("expect etcdcluster version %v, but got %v", expect.Cluster, target.Cluster)
		}
	}

	if expect.Storage != "" {
		result, err := majorMinorVersionsEqual(expect.Storage, target.Storage)
		if err != nil {
			return err
		}
		if !result {
			return fmt.Errorf("expect storage version %v, but got %v", expect.Storage, target.Storage)
		}
	}
	return nil
}

func getMemberVersionByCurl(cfg *EtcdProcessClusterConfig, member EtcdProcess) (version.Versions, error) {
	args := CURLPrefixArgsCluster(cfg, member, "GET", CURLReq{Endpoint: "/version"})
	lines, err := RunUtilCompletion(args, nil)
	if err != nil {
		return version.Versions{}, err
	}

	data := strings.Join(lines, "\n")
	result := version.Versions{}
	if err := json.Unmarshal([]byte(data), &result); err != nil {
		return version.Versions{}, fmt.Errorf("failed to unmarshal (%v): %w", data, err)
	}
	return result, nil
}
