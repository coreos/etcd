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

package e2e

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	clientv3 "go.etcd.io/etcd/client/v3"
	"go.etcd.io/etcd/tests/v3/framework/e2e"
)

// leaderAwareE2EConfig returns a clientv3 config that enables the
// leader-aware balancer with intervals short enough for tests.
func leaderAwareE2EConfig(endpoints []string) clientv3.Config {
	return clientv3.Config{
		Endpoints:   endpoints,
		DialTimeout: 5 * time.Second,
	}.WithBalancer(clientv3.LeaderAwareBalancerName).
		WithLeaderAwareRefreshInterval(500 * time.Millisecond).
		WithLeaderAwareRediscoveryDelay(100 * time.Millisecond).
		WithLeaderAwareStatusTimeout(2 * time.Second)
}

// putWithRetryE2E issues a Put and retries on transient errors.
func putWithRetryE2E(ctx context.Context, t *testing.T, cli *clientv3.Client, key, val string) {
	t.Helper()
	for {
		putCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		_, err := cli.Put(putCtx, key, val)
		cancel()
		if err == nil {
			return
		}
		t.Logf("put %q failed: %v, retrying", key, err)
		if ctx.Err() != nil {
			t.Fatalf("put %q timed out: %v", key, ctx.Err())
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// TestLeaderAwareBalancerE2E verifies that the leader-aware balancer
// routes writes and reads correctly against a real etcd process cluster.
func TestLeaderAwareBalancerE2E(t *testing.T) {
	e2e.BeforeTest(t)

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	epc, err := e2e.NewEtcdProcessCluster(ctx, t, e2e.WithClusterSize(3))
	require.NoError(t, err)
	defer epc.Close()

	epc.WaitLeader(t)

	cli, err := clientv3.New(leaderAwareE2EConfig(epc.EndpointsGRPC()))
	require.NoError(t, err)
	defer cli.Close()

	for i := 0; i < 10; i++ {
		key := fmt.Sprintf("k%d", i)
		putWithRetryE2E(ctx, t, cli, key, fmt.Sprintf("v%d", i))
	}

	for i := 0; i < 10; i++ {
		key := fmt.Sprintf("k%d", i)
		getCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		resp, err := cli.Get(getCtx, key)
		cancel()
		require.NoError(t, err)
		require.Len(t, resp.Kvs, 1)
		require.Equal(t, fmt.Sprintf("v%d", i), string(resp.Kvs[0].Value))
	}
}

// TestLeaderAwareBalancerFailoverE2E verifies that writes continue to
// succeed after the leader process is stopped and a new leader is elected.
func TestLeaderAwareBalancerFailoverE2E(t *testing.T) {
	e2e.BeforeTest(t)

	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()

	epc, err := e2e.NewEtcdProcessCluster(ctx, t, e2e.WithClusterSize(3))
	require.NoError(t, err)
	defer epc.Close()

	leadIdx := epc.WaitLeader(t)

	cli, err := clientv3.New(leaderAwareE2EConfig(epc.EndpointsGRPC()))
	require.NoError(t, err)
	defer cli.Close()

	putWithRetryE2E(ctx, t, cli, "before-failover", "1")

	// Stop the leader process to trigger a new election.
	require.NoError(t, epc.Procs[leadIdx].Stop())

	// Wait for the remaining members to elect a new leader.
	newLeadIdx := epc.WaitLeader(t)
	require.NotEqualf(t, leadIdx, newLeadIdx, "leader did not change after stopping the old leader")

	// Writes must still succeed after failover.
	putWithRetryE2E(ctx, t, cli, "after-failover", "2")

	getCtx, cancelGet := context.WithTimeout(ctx, 3*time.Second)
	resp, err := cli.Get(getCtx, "after-failover")
	cancelGet()
	require.NoError(t, err)
	require.Len(t, resp.Kvs, 1)
	require.Equal(t, "2", string(resp.Kvs[0].Value))
}

// TestLeaderAwareBalancerMoveLeaderE2E verifies that writes continue
// to succeed after a leadership transfer via MoveLeader.
func TestLeaderAwareBalancerMoveLeaderE2E(t *testing.T) {
	e2e.BeforeTest(t)

	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()

	epc, err := e2e.NewEtcdProcessCluster(ctx, t, e2e.WithClusterSize(3))
	require.NoError(t, err)
	defer epc.Close()

	leadIdx := epc.WaitLeader(t)

	cli, err := clientv3.New(leaderAwareE2EConfig(epc.EndpointsGRPC()))
	require.NoError(t, err)
	defer cli.Close()

	putWithRetryE2E(ctx, t, cli, "before-move", "1")

	// Transfer leadership to the next member.
	targetIdx := (leadIdx + 1) % epc.Cfg.ClusterSize
	require.NoError(t, epc.MoveLeader(ctx, t, targetIdx))

	newLeadIdx := epc.WaitLeader(t)
	require.Equalf(t, targetIdx, newLeadIdx, "leader was not transferred to the target member")

	// Writes must still succeed after the leadership transfer.
	putWithRetryE2E(ctx, t, cli, "after-move", "2")

	getCtx, cancelGet := context.WithTimeout(ctx, 3*time.Second)
	resp, err := cli.Get(getCtx, "after-move")
	cancelGet()
	require.NoError(t, err)
	require.Len(t, resp.Kvs, 1)
	require.Equal(t, "2", string(resp.Kvs[0].Value))
}
