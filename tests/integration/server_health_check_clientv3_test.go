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

//go:build !cluster_proxy

package integration

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"

	"go.etcd.io/etcd/client/pkg/v3/testutil"
	clientv3 "go.etcd.io/etcd/client/v3"
	"go.etcd.io/etcd/client/v3/healthcheck"
	"go.etcd.io/etcd/tests/v3/framework/integration"
)

// rangeRequests is enough for round_robin to visit every subconn many
// times over, so a member that stays in rotation cannot be missed by
// luck and one that is dropped cannot be hit by a stale picker.
const rangeRequests = 100

// TestServerHealthCheck_ClientV3DropsNotServingMember closes the loop
// between the server-side self-check and the client-side endpoint
// selection: a member that publishes NOT_SERVING on the aggregate
// grpc.health.v1 entry must stop receiving clientv3 traffic, and the
// remaining members must keep serving without a single failed request.
//
// A learner drives the aggregate entry deterministically and needs no
// failpoints: Checker.tick short-circuits to RecordLearner, which calls
// HealthCheckFailed("learner") on the first tick, and learner is
// member-local so aggregateEligible lets it flip the "" entry.
//
// Which member served a request is read from the response header's
// MemberId rather than from grpc_server_handled_total, so the assertion
// does not depend on scraping each member's metrics endpoint.
func TestServerHealthCheck_ClientV3DropsNotServingMember(t *testing.T) {
	testutil.SkipTestIfShortMode(t, "Cannot start embedded cluster in --short tests")
	integration.BeforeTest(t)

	c := integration.NewCluster(t, &integration.ClusterConfig{
		Size: 2,
		// MemberAddAsLearner is rejected with "unhealthy cluster" while
		// the strict reconfig check still sees the freshly launched
		// peers as inactive.
		DisableStrictReconfigCheck: true,
		ServerFeatureGates:         healthCheckGates,
	})
	defer c.Terminate(t)

	require.NotZerof(t, waitForLeaderReady(t, c.Members[0]), "cluster should have a leader")
	c.AddAndLaunchLearnerMember(t)
	learner := c.Members[len(c.Members)-1]
	learnerID := uint64(learner.ID())
	<-learner.ReadyNotify()

	endpoints := make([]string, 0, len(c.Members))
	for _, m := range c.Members {
		endpoints = append(endpoints, m.GRPCURL)
	}

	hc := mustHealthCheckClient(t, learner.GRPCURL, 2*time.Second)
	status, elapsed, err := hc.waitForHealthStatus(
		healthCheckService, healthpb.HealthCheckResponse_NOT_SERVING, 10*time.Second)
	require.NoErrorf(t, err, "health status poll failed")
	require.Equalf(t, healthpb.HealthCheckResponse_NOT_SERVING, status,
		"learner should publish NOT_SERVING on the aggregate entry, got %v after %v", status, elapsed)

	t.Run("with health check dial options", func(t *testing.T) {
		hits, failed := servedByMember(t, endpoints, healthcheck.GRPCClientHealthCheckOptions())
		t.Logf("requests per member: %v, failed: %d", hits, failed)

		require.Zerof(t, hits[learnerID],
			"a member reporting NOT_SERVING must not receive client traffic")
		require.Zerof(t, failed, "no request should fail once the learner is out of rotation")
		// Two distinct members also proves the helper's service config
		// took effect: round_robin spread rather than a single subconn.
		require.Lenf(t, hits, len(c.Members)-1,
			"the remaining voting members should share the load")
	})

	// Control arm: the same cluster and the same request pattern with no
	// health checking. It pins that the exclusion above comes from the
	// dial options and not from clientv3 refusing learners on its own.
	t.Run("without health check dial options", func(t *testing.T) {
		hits, failed := servedByMember(t, endpoints, nil)
		t.Logf("requests per member: %v, failed: %d", hits, failed)

		require.Positivef(t, hits[learnerID],
			"without health checking the learner must stay in rotation")
	})
}

// servedByMember issues rangeRequests reads through one clientv3 client
// and returns how many each member served, plus how many failed.
//
// The reads are serializable on purpose. A linearizable read sent to a
// learner is rejected and clientv3 retries it onto a voting member, so
// every request succeeds and the response header always names a voter:
// the balancer's choice becomes unobservable and the control arm below
// looks identical to the health-checked one. Serializable reads are
// served locally by any member, learner included, so the header reports
// the member the balancer actually picked.
func servedByMember(t *testing.T, endpoints []string, dialOpts []grpc.DialOption) (map[uint64]int, int) {
	t.Helper()
	cli, err := integration.NewClient(t, clientv3.Config{
		Endpoints:   endpoints,
		DialTimeout: 5 * time.Second,
		DialOptions: dialOpts,
	})
	require.NoError(t, err)
	defer cli.Close()

	hits := make(map[uint64]int, len(endpoints))
	failed := 0
	for i := 0; i < rangeRequests; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		resp, err := cli.Get(ctx, "healthcheck-clientv3", clientv3.WithSerializable())
		cancel()
		if err != nil {
			t.Logf("request %d failed: %v", i, err)
			failed++
			continue
		}
		hits[resp.Header.GetMemberId()]++
	}
	return hits, failed
}
