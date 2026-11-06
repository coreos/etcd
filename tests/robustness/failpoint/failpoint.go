// Copyright 2022 The etcd Authors
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

package failpoint

import (
	"context"
	"fmt"
	"math/rand"
	"testing"
	"time"

	"go.uber.org/zap"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"

	clientv3 "go.etcd.io/etcd/client/v3"
	"go.etcd.io/etcd/tests/v3/framework/e2e"
	"go.etcd.io/etcd/tests/v3/robustness/identity"
	"go.etcd.io/etcd/tests/v3/robustness/report"
	"go.etcd.io/etcd/tests/v3/robustness/traffic"
)

const (
	triggerTimeout = time.Minute
)

var allFailpoints = []Failpoint{
	KillFailpoint, BeforeCommitPanic, AfterCommitPanic, RaftBeforeSavePanic, RaftAfterSavePanic,
	DefragBeforeCopyPanic, DefragNonBlockBeforeCopyPanic, DefragNonBlockBeforeCatchup, DefragBeforeRenamePanic,
	BackendBeforePreCommitHookPanic, BackendAfterPreCommitHookPanic,
	BackendBeforeStartDBTxnPanic, BackendAfterStartDBTxnPanic, BackendBeforeWritebackBufPanic,
	BackendAfterWritebackBufPanic,
	// CompactBeforeCommitScheduledCompactPanic, CompactAfterCommitScheduledCompactPanic,
	// CompactBeforeSetFinishedCompactPanic, CompactAfterSetFinishedCompactPanic, CompactBeforeCommitBatchPanic,
	// CompactAfterCommitBatchPanic,
	RaftBeforeLeaderSendPanic, BlackholePeerNetwork, DelayPeerNetwork,
	RaftBeforeFollowerSendPanic, RaftBeforeApplySnapPanic, RaftAfterApplySnapPanic, RaftAfterWALReleasePanic,
	RaftBeforeSaveSnapPanic, RaftAfterSaveSnapPanic, BlackholeUntilSnapshot,
	BeforeApplyOneConfChangeSleep,
	MemberReplace,
	MemberDowngrade,
	MemberDowngradeUpgrade,
	DropPeerNetwork,
	RaftBeforeSaveSleep,
	RaftAfterSaveSleep,
	ApplyBeforeOpenSnapshot,
	SleepBeforeSendWatchResponse,
	SleepBeforeSyncWatchers,
	SleepBeforeMoveVictims,
	SleepBeforeProgressIfSync,
}

func PickRandom(clus *e2e.EtcdProcessCluster, profile traffic.Profile) (Failpoint, error) {
	availableFailpoints := make([]Failpoint, 0, len(allFailpoints))
	for _, failpoint := range allFailpoints {
		err := Validate(clus, failpoint, profile)
		if err != nil {
			continue
		}
		availableFailpoints = append(availableFailpoints, failpoint)
	}
	if len(availableFailpoints) == 0 {
		return nil, fmt.Errorf("no available failpoints")
	}
	return availableFailpoints[rand.Int()%len(availableFailpoints)], nil
}

func Validate(clus *e2e.EtcdProcessCluster, failpoint Failpoint, profile traffic.Profile) error {
	for _, proc := range clus.Procs {
		if !failpoint.Available(*clus.Cfg, proc, profile) {
			return fmt.Errorf("failpoint %q not available on %s", failpoint.Name(), proc.Config().Name)
		}
	}
	return nil
}

func Inject(ctx context.Context, t *testing.T, lg *zap.Logger, clus *e2e.EtcdProcessCluster, failpoint Failpoint, baseTime time.Time, ids identity.Provider) (*report.FailpointReport, error) {
	timeout := triggerTimeout
	if timeoutObj, ok := failpoint.(TimeoutInterface); ok {
		timeout = timeoutObj.Timeout()
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var err error

	if err = verifyClusterHealth(ctx, t, clus); err != nil {
		return nil, fmt.Errorf("failed to verify cluster health before failpoint injection, err: %w", err)
	}
	lg.Info("Triggering failpoint", zap.String("failpoint", failpoint.Name()))
	start := time.Since(baseTime)
	clientReport, err := failpoint.Inject(ctx, t, lg, clus, baseTime, ids)
	if err != nil {
		lg.Error("Failed to trigger failpoint", zap.String("failpoint", failpoint.Name()), zap.Error(err))
		return nil, fmt.Errorf("failed triggering failpoint, err: %w", err)
	}
	if err = verifyClusterHealth(ctx, t, clus); err != nil {
		return nil, fmt.Errorf("failed to verify cluster health after failpoint injection, err: %w", err)
	}
	lg.Info("Finished triggering failpoint", zap.String("failpoint", failpoint.Name()))
	end := time.Since(baseTime)

	return &report.FailpointReport{
		FailpointInjection: report.FailpointInjection{
			Start: start,
			End:   end,
			Name:  failpoint.Name(),
		},
		Client: clientReport,
	}, nil
}

func verifyClusterHealth(ctx context.Context, _ *testing.T, clus *e2e.EtcdProcessCluster) error {
	for i := 0; i < len(clus.Procs); i++ {
		clusterClient, err := clientv3.New(clientv3.Config{
			Endpoints:            clus.Procs[i].EndpointsGRPC(),
			Logger:               zap.NewNop(),
			DialKeepAliveTime:    10 * time.Second,
			DialKeepAliveTimeout: 100 * time.Millisecond,
		})
		if err != nil {
			return fmt.Errorf("Error creating client for cluster %s: %w", clus.Procs[i].Config().Name, err)
		}
		defer clusterClient.Close()

		cli := healthpb.NewHealthClient(clusterClient.ActiveConnection())
		resp, err := cli.Check(ctx, &healthpb.HealthCheckRequest{})
		if err != nil {
			return fmt.Errorf("Error checking member %s health: %w", clus.Procs[i].Config().Name, err)
		}
		if resp.Status != healthpb.HealthCheckResponse_SERVING {
			return fmt.Errorf("Member %s health status expected %s, got %s",
				clus.Procs[i].Config().Name,
				healthpb.HealthCheckResponse_SERVING,
				resp.Status)
		}
	}
	return nil
}

type Failpoint interface {
	Inject(ctx context.Context, t *testing.T, lg *zap.Logger, clus *e2e.EtcdProcessCluster, baseTime time.Time, ids identity.Provider) ([]report.ClientReport, error)
	Name() string
	AvailabilityChecker
}

type AvailabilityChecker interface {
	Available(e2e.EtcdProcessClusterConfig, e2e.EtcdProcess, traffic.Profile) bool
}

type TimeoutInterface interface {
	Timeout() time.Duration
}
