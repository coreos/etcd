// Copyright 2023 The etcd Authors
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

package scenarios

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"go.etcd.io/etcd/api/v3/version"
	"go.etcd.io/etcd/client/pkg/v3/fileutil"
	"go.etcd.io/etcd/server/v3/etcdserver"
	"go.etcd.io/etcd/tests/v3/framework/e2e"
	"go.etcd.io/etcd/tests/v3/robustness/failpoint"
	"go.etcd.io/etcd/tests/v3/robustness/options"
	"go.etcd.io/etcd/tests/v3/robustness/random"
	"go.etcd.io/etcd/tests/v3/robustness/traffic"
)

type TrafficProfile struct {
	Name    string
	Traffic traffic.Traffic
	Profile traffic.Profile
}

var trafficProfiles = []TrafficProfile{
	{
		Name:    "EtcdHighTraffic",
		Traffic: traffic.EtcdPut,
		Profile: traffic.Profile{
			KeyValue:   &traffic.KeyValueHigh,
			Watch:      &traffic.WatchDefault,
			Compaction: &traffic.CompactionDefault,
		},
	},
	{
		Name:    "EtcdTrafficDeleteLeases",
		Traffic: traffic.EtcdPutDeleteLease,
		Profile: traffic.Profile{
			KeyValue:   &traffic.KeyValueMedium,
			Watch:      &traffic.WatchDefault,
			Compaction: &traffic.CompactionDefault,
		},
	},
	{
		Name:    "KubernetesHighTraffic",
		Traffic: traffic.Kubernetes,
		Profile: traffic.Profile{
			KeyValue:   &traffic.KeyValueHigh,
			Watch:      &traffic.WatchDefault,
			Compaction: &traffic.CompactionDefault,
		},
	},
	{
		Name:    "KubernetesLowTraffic",
		Traffic: traffic.Kubernetes,
		Profile: traffic.Profile{
			KeyValue:   &traffic.KeyValueMedium,
			Watch:      &traffic.WatchDefault,
			Compaction: &traffic.CompactionDefault,
		},
	},
}

type TestScenario struct {
	Name      string
	Failpoint failpoint.Failpoint
	Cluster   e2e.EtcdProcessClusterConfig
	Traffic   traffic.Traffic
	Profile   traffic.Profile
}

func Exploratory(_ *testing.T) []TestScenario {
	randomizableOptions := []e2e.EPClusterOption{
		options.WithClusterOptionGroups(
			options.ClusterOptions{options.WithTickMs(29), options.WithElectionMs(271)},
			options.ClusterOptions{options.WithTickMs(101), options.WithElectionMs(521)},
			options.ClusterOptions{options.WithTickMs(100), options.WithElectionMs(2000)}),
	}

	mixedVersionOptionChoices := []random.ChoiceWeight[options.ClusterOptions]{
		// 60% with all members of current version
		{Choice: options.ClusterOptions{options.WithVersion(e2e.CurrentVersion)}, Weight: 60},
		// 10% with 2 members of current version, 1 member last version, leader is current version
		{Choice: options.ClusterOptions{options.WithVersion(e2e.MinorityLastVersion), options.WithInitialLeaderIndex(0)}, Weight: 10},
		// 10% with 2 members of current version, 1 member last version, leader is last version
		{Choice: options.ClusterOptions{options.WithVersion(e2e.MinorityLastVersion), options.WithInitialLeaderIndex(2)}, Weight: 10},
		// 10% with 2 members of last version, 1 member current version, leader is last version
		{Choice: options.ClusterOptions{options.WithVersion(e2e.QuorumLastVersion), options.WithInitialLeaderIndex(0)}, Weight: 10},
		// 10% with 2 members of last version, 1 member current version, leader is current version
		{Choice: options.ClusterOptions{options.WithVersion(e2e.QuorumLastVersion), options.WithInitialLeaderIndex(2)}, Weight: 10},
	}
	mixedVersionOption := options.WithClusterOptionGroups(random.PickRandom[options.ClusterOptions](mixedVersionOptionChoices))

	baseOptions := []e2e.EPClusterOption{
		options.WithSnapshotCount(50, 100, 1000),
		options.WithSubsetOptions(randomizableOptions...),
		e2e.WithGoFailEnabled(true),
		// Set a low minimal compaction batch limit to allow for triggering multi batch compaction failpoints.
		options.WithCompactionBatchLimit(10, 100, 1000),
		e2e.WithWatchProcessNotifyInterval(100 * time.Millisecond),
	}

	if addr := os.Getenv("TRACING_SERVER_ADDR"); addr != "" {
		baseOptions = append(baseOptions, e2e.WithEnableDistributedTracing(addr))
	}

	if e2e.CouldSetSnapshotCatchupEntries(e2e.BinPath.Etcd) {
		baseOptions = append(baseOptions, options.WithSnapshotCatchUpEntries(100, etcdserver.DefaultSnapshotCatchUpEntries))
	}
	scenarios := []TestScenario{}
	for _, tp := range trafficProfiles {
		name := filepath.Join(tp.Name, "ClusterOfSize1")
		clusterOfSize1Options := baseOptions
		clusterOfSize1Options = append(clusterOfSize1Options, e2e.WithClusterSize(1))
		scenarios = append(scenarios, TestScenario{
			Name:    name,
			Traffic: tp.Traffic,
			Profile: tp.Profile,
			Cluster: *e2e.NewConfig(clusterOfSize1Options...),
		})
	}

	for _, tp := range trafficProfiles {
		name := filepath.Join(tp.Name, "ClusterOfSize3")
		clusterOfSize3Options := baseOptions
		clusterOfSize3Options = append(clusterOfSize3Options, e2e.WithIsPeerTLS(true))
		clusterOfSize3Options = append(clusterOfSize3Options, e2e.WithPeerProxy(true))
		if fileutil.Exist(e2e.BinPath.EtcdLastRelease) {
			clusterOfSize3Options = append(clusterOfSize3Options, mixedVersionOption)
		}
		scenarios = append(scenarios, TestScenario{
			Name:    name,
			Traffic: tp.Traffic,
			Profile: tp.Profile,
			Cluster: *e2e.NewConfig(clusterOfSize3Options...),
		})
	}
	if e2e.BinPath.LazyFSAvailable() {
		newScenarios := scenarios
		for _, s := range scenarios {
			// LazyFS increases the load on the CPU, so we run it with a more lightweight case.
			if s.Profile.KeyValue.MinimalQPS <= 100 && s.Cluster.ClusterSize == 1 {
				lazyfsCluster := s.Cluster
				lazyfsCluster.LazyFSEnabled = true
				profileWithoutCompaction := s.Profile
				profileWithoutCompaction.Compaction = nil
				newScenarios = append(newScenarios, TestScenario{
					Name:      filepath.Join(s.Name, "LazyFS"),
					Failpoint: s.Failpoint,
					Cluster:   lazyfsCluster,
					Traffic:   s.Traffic,
					Profile:   profileWithoutCompaction,
				})
			}
		}
		scenarios = newScenarios
	}
	return scenarios
}

func Regression(t *testing.T) []TestScenario {
	v, err := e2e.GetVersionFromBinary(e2e.BinPath.Etcd)
	require.NoErrorf(t, err, "Failed checking etcd version binary, binary: %q", e2e.BinPath.Etcd)

	scenarios := []TestScenario{}
	scenarios = append(scenarios, TestScenario{
		Name:      "Issue14370",
		Failpoint: failpoint.RaftBeforeSavePanic,
		Profile: traffic.Profile{
			KeyValue:   &traffic.KeyValueMedium,
			Compaction: &traffic.CompactionDefault,
		},
		Traffic: traffic.EtcdPutDeleteLease,
		Cluster: *e2e.NewConfig(
			e2e.WithClusterSize(1),
			e2e.WithGoFailEnabled(true),
		),
	})
	scenarios = append(scenarios, TestScenario{
		Name:      "Issue14685",
		Failpoint: failpoint.DefragBeforeCopyPanic,
		Profile: traffic.Profile{
			KeyValue:   &traffic.KeyValueMedium,
			Compaction: &traffic.CompactionDefault,
		},
		Traffic: traffic.EtcdPutDeleteLease,
		Cluster: *e2e.NewConfig(
			e2e.WithClusterSize(1),
			e2e.WithGoFailEnabled(true),
			e2e.WithServerFeatureGate("NonBlockingDefrag", false),
		),
	})
	scenarios = append(scenarios, TestScenario{
		Name:      "Issue13766",
		Failpoint: failpoint.KillFailpoint,
		Profile: traffic.Profile{
			KeyValue:   &traffic.KeyValueHigh,
			Compaction: &traffic.CompactionDefault,
		},
		Traffic: traffic.EtcdPut,
		Cluster: *e2e.NewConfig(
			e2e.WithSnapshotCount(100),
		),
	})
	scenarios = append(scenarios, TestScenario{
		Name: "Issue15220",
		Profile: traffic.Profile{
			KeyValue:   &traffic.KeyValueMedium,
			Watch:      &traffic.WatchDefault,
			Compaction: &traffic.CompactionDefault,
		},
		Traffic:   traffic.EtcdPutDeleteLease,
		Failpoint: failpoint.KillFailpoint,
		Cluster: *e2e.NewConfig(
			e2e.WithClusterSize(1),
		),
	})
	scenarios = append(scenarios, TestScenario{
		Name: "Issue17529",
		Profile: traffic.Profile{
			KeyValue:   &traffic.KeyValueHigh,
			Watch:      &traffic.WatchDefault,
			Compaction: &traffic.CompactionDefault,
		},
		Traffic:   traffic.Kubernetes,
		Failpoint: failpoint.SleepBeforeSendWatchResponse,
		Cluster: *e2e.NewConfig(
			e2e.WithClusterSize(1),
			e2e.WithGoFailEnabled(true),
			options.WithSnapshotCount(100),
		),
	})

	scenarios = append(scenarios, TestScenario{
		Name: "Issue17780",
		Profile: traffic.Profile{
			KeyValue: &traffic.KeyValueMedium,
		},
		Failpoint: failpoint.BatchCompactBeforeSetFinishedCompactPanic,
		Traffic:   traffic.Kubernetes,
		Cluster: *e2e.NewConfig(
			e2e.WithClusterSize(1),
			e2e.WithCompactionBatchLimit(300),
			e2e.WithSnapshotCount(1000),
			e2e.WithGoFailEnabled(true),
		),
	})

	// NOTE:
	//
	// 1. All keys have only two revisions: creation and tombstone. With
	// a small compaction batch limit, it's easy to separate a key's two
	// revisions into different batch runs. If the compaction revision is a
	// tombstone and the creation revision was deleted in a previous
	// compaction run, we may encounter issue 19179.
	//
	// 2. It can be easily reproduced when using a lower QPS with a lower
	// burstable value. A higher QPS can generate more new keys than
	// expected, making it difficult to determine an optimal compaction
	// batch limit within a larger key space.
	scenarios = append(scenarios, TestScenario{
		Name: "Issue19179",
		Profile: traffic.Profile{
			KeyValue: &traffic.KeyValueVeryLow,
			Watch:    &traffic.WatchDefault,
		},
		Failpoint: failpoint.BatchCompactBeforeSetFinishedCompactPanic,
		Traffic:   traffic.KubernetesCreateDelete,
		Cluster: *e2e.NewConfig(
			e2e.WithClusterSize(1),
			e2e.WithCompactionBatchLimit(50),
			e2e.WithSnapshotCount(1000),
			e2e.WithGoFailEnabled(true),
		),
	})
	scenarios = append(scenarios, TestScenario{
		Name: "Issue18089",
		Profile: traffic.Profile{
			KeyValue:   &traffic.KeyValueMedium,
			Compaction: &traffic.CompactionFrequent, // Use frequent compaction for high reproduce rate
			Watch:      &traffic.WatchDefault,
		},
		Failpoint: failpoint.SleepBeforeSendWatchResponse,
		Traffic:   traffic.EtcdDelete,
		Cluster: *e2e.NewConfig(
			e2e.WithClusterSize(1),
			e2e.WithGoFailEnabled(true),
		),
	})
	scenarios = append(scenarios, TestScenario{
		Name:      "Issue20221",
		Failpoint: failpoint.BlackholeUntilSnapshot,
		Profile: traffic.Profile{
			KeyValue: &traffic.KeyValueHigh,
			Watch: &traffic.Watch{
				MemberClientCount:   6,
				ClusterClientCount:  2,
				RevisionOffsetRange: traffic.Range{Min: 50, Max: 100},
			},
		},
		Traffic: traffic.EtcdPut,
		Cluster: *e2e.NewConfig(
			e2e.WithSnapshotCount(10),
			e2e.WithPeerProxy(true),
			e2e.WithIsPeerTLS(true),
			e2e.WithWatchProcessNotifyInterval(10*time.Millisecond),
			e2e.WithSnapshotCatchUpEntries(10),
		),
	})
	scenarios = append(scenarios, TestScenario{
		Name:      "Issue22028",
		Failpoint: failpoint.BackendAfterPreCommitHookPanic,
		Profile: traffic.Profile{
			KeyValue:   &traffic.KeyValueMedium,
			Compaction: &traffic.CompactionDefault,
		},
		Traffic: traffic.EtcdPutDeleteLease,
		Cluster: *e2e.NewConfig(
			e2e.WithClusterSize(1),
			e2e.WithGoFailEnabled(true),
		),
	})
	if v.Compare(&version.V3_5) >= 0 {
		opts := []e2e.EPClusterOption{
			e2e.WithSnapshotCount(100),
			e2e.WithPeerProxy(true),
			e2e.WithIsPeerTLS(true),
		}
		if e2e.CouldSetSnapshotCatchupEntries(e2e.BinPath.Etcd) {
			opts = append(opts, e2e.WithSnapshotCatchUpEntries(100))
		}
		scenarios = append(scenarios, TestScenario{
			Name:      "Issue15271",
			Failpoint: failpoint.BlackholeUntilSnapshot,
			Profile: traffic.Profile{
				KeyValue:   &traffic.KeyValueHigh,
				Compaction: &traffic.CompactionDefault,
				Watch:      &traffic.WatchDefault,
			},
			Traffic: traffic.EtcdPut,
			Cluster: *e2e.NewConfig(opts...),
		})
	}
	return scenarios
}
