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

package integration

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"

	clientv3 "go.etcd.io/etcd/client/v3"
	"go.etcd.io/etcd/tests/v3/framework/integration"
)

// leaderAwareClientConfig returns a client config that enables the
// leader-aware balancer with intervals short enough for tests.
func leaderAwareClientConfig(endpoints []string) clientv3.Config {
	return clientv3.Config{
		Endpoints:   endpoints,
		DialTimeout: 5 * time.Second,
	}.WithBalancer(clientv3.LeaderAwareBalancerName).
		WithLeaderAwareRefreshInterval(500 * time.Millisecond).
		WithLeaderAwareRediscoveryDelay(100 * time.Millisecond).
		WithLeaderAwareStatusTimeout(2 * time.Second)
}

// putWithRetry issues a Put and retries on transient errors until
// the deadline expires or the write succeeds.
func putWithRetry(ctx context.Context, t *testing.T, cli *clientv3.Client, key, val string) {
	t.Helper()
	for {
		putCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		_, err := cli.Put(putCtx, key, val)
		cancel()
		if err == nil {
			return
		}
		t.Logf("put %q failed: %v, retrying", key, err)
		if ctx.Err() != nil {
			t.Fatalf("put %q timed out: %v", key, ctx.Err())
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// TestLeaderAwareBalancerPut verifies that the leader-aware balancer
// routes consensus writes and reads correctly in a healthy cluster.
func TestLeaderAwareBalancerPut(t *testing.T) {
	integration.BeforeTest(t)

	clus := integration.NewCluster(t, &integration.ClusterConfig{Size: 3})
	defer clus.Terminate(t)

	clus.WaitLeader(t)

	cli, err := integration.NewClient(t, leaderAwareClientConfig(clus.Endpoints()))
	require.NoError(t, err)
	defer cli.Close()

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	for i := 0; i < 10; i++ {
		key := fmt.Sprintf("k%d", i)
		putWithRetry(ctx, t, cli, key, fmt.Sprintf("v%d", i))
	}

	for i := 0; i < 10; i++ {
		key := fmt.Sprintf("k%d", i)
		getCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		resp, err := cli.Get(getCtx, key)
		cancel()
		require.NoError(t, err)
		require.Len(t, resp.Kvs, 1)
		require.Equal(t, fmt.Sprintf("v%d", i), string(resp.Kvs[0].Value))
	}
}

// TestLeaderAwareBalancerFailover verifies that writes continue to
// succeed after the leader is stopped and a new leader is elected.
func TestLeaderAwareBalancerFailover(t *testing.T) {
	integration.BeforeTest(t)

	clus := integration.NewCluster(t, &integration.ClusterConfig{Size: 3})
	defer clus.Terminate(t)

	oldLeadIdx := clus.WaitLeader(t)
	oldLeadID := uint64(clus.Members[oldLeadIdx].Server.MemberID())

	cli, err := integration.NewClient(t, leaderAwareClientConfig(clus.Endpoints()))
	require.NoError(t, err)
	defer cli.Close()

	// Write before failover to ensure the balancer is working.
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	putWithRetry(ctx, t, cli, "before-failover", "1")

	// Stop the leader to trigger a new election.
	clus.Members[oldLeadIdx].Stop(t)

	// Wait for the remaining members to agree on a new leader.
	newLeadIdx := clus.WaitLeader(t)
	newLeadID := uint64(clus.Members[newLeadIdx].Server.MemberID())
	require.NotEqualf(t, oldLeadID, newLeadID, "leader did not change after stopping the old leader")

	// Writes must still succeed: the balancer invalidates the stale
	// hint and falls back to round_robin, which forwards to the new
	// leader. The tracker then rediscovers the new leader.
	putWithRetry(ctx, t, cli, "after-failover", "2")

	getCtx, cancelGet := context.WithTimeout(ctx, 2*time.Second)
	resp, err := cli.Get(getCtx, "after-failover")
	cancelGet()
	require.NoError(t, err)
	require.Len(t, resp.Kvs, 1)
	require.Equal(t, "2", string(resp.Kvs[0].Value))
}

// TestLeaderAwareBalancerMoveLeader verifies that response headers trigger
// routing to the new leader before the next periodic Status refresh.
func TestLeaderAwareBalancerMoveLeader(t *testing.T) {
	integration.BeforeTest(t)

	clus := integration.NewCluster(t, &integration.ClusterConfig{Size: 3})
	defer clus.Terminate(t)

	oldLeadIdx := clus.WaitLeader(t)
	oldLeadID := uint64(clus.Members[oldLeadIdx].Server.MemberID())

	core, logs := observer.New(zap.DebugLevel)
	cfg := leaderAwareClientConfig(clus.Endpoints()).WithLeaderAwareRefreshInterval(time.Hour)
	cfg.Logger = zap.New(core)
	cli, err := integration.NewClient(t, cfg)
	require.NoError(t, err)
	defer cli.Close()

	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	// Wait for initial discovery so round_robin cannot satisfy the routing
	// check by chance. Periodic polling cannot run during this test.
	require.Eventually(t, func() bool {
		return logs.FilterMessage("refreshed etcd leader").Len() == 1
	}, 5*time.Second, 20*time.Millisecond)
	before, err := cli.Put(ctx, "before-move", "1")
	require.NoError(t, err)
	require.Equal(t, oldLeadID, before.Header.MemberId)

	// Transfer leadership to the next member.
	targetIdx := (oldLeadIdx + 1) % 3
	targetID := uint64(clus.Members[targetIdx].Server.MemberID())
	_, err = clus.Client(oldLeadIdx).MoveLeader(ctx, targetID)
	require.NoError(t, err)

	// Wait for the followers to observe the new leader.
	for i := range clus.Members {
		if i == oldLeadIdx {
			continue
		}
		newID := integration.CheckLeaderTransition(clus.Members[i], oldLeadID)
		require.Equalf(t, targetID, newID, "leader transition did not reach member %d", i)
	}

	// The old leader still accepts writes, so verify the serving member too.
	require.Eventually(t, func() bool {
		resp, putErr := cli.Put(ctx, "after-move", "2")
		return putErr == nil && resp.Header.MemberId == targetID &&
			logs.FilterMessage("refreshed etcd leader").Len() >= 2
	}, 5*time.Second, 20*time.Millisecond)

	getCtx, cancelGet := context.WithTimeout(ctx, 2*time.Second)
	resp, err := cli.Get(getCtx, "after-move")
	cancelGet()
	require.NoError(t, err)
	require.Len(t, resp.Kvs, 1)
	require.Equal(t, "2", string(resp.Kvs[0].Value))
}

// TestLeaderAwareBalancerEndpointChurn verifies that writes continue
// to succeed after the client endpoint list is reordered without
// membership change.
func TestLeaderAwareBalancerEndpointChurn(t *testing.T) {
	integration.BeforeTest(t)

	clus := integration.NewCluster(t, &integration.ClusterConfig{Size: 3})
	defer clus.Terminate(t)

	clus.WaitLeader(t)

	cli, err := integration.NewClient(t, leaderAwareClientConfig(clus.Endpoints()))
	require.NoError(t, err)
	defer cli.Close()

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	putWithRetry(ctx, t, cli, "before-churn", "1")

	// Reordering the endpoints invalidates the leader hint. Writes must
	// continue to succeed while the tracker rediscovers the leader.
	endpoints := clus.Endpoints()
	reordered := []string{endpoints[1], endpoints[0], endpoints[2]}
	cli.SetEndpoints(reordered...)

	putWithRetry(ctx, t, cli, "after-churn", "2")

	getCtx, cancelGet := context.WithTimeout(ctx, 2*time.Second)
	resp, err := cli.Get(getCtx, "after-churn")
	cancelGet()
	require.NoError(t, err)
	require.Len(t, resp.Kvs, 1)
	require.Equal(t, "2", string(resp.Kvs[0].Value))
}
