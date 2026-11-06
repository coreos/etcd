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

// Chaos tests for the health state machine — seven scenarios
// approximating real-world faults.
//
// True system-level chaos (traffic control, fio) needs the
// e2e/robustness frameworks; here the scenarios are approximated
// with member manipulation, throttling and failpoint injection
// under the integration framework to verify the correctness of the
// health state machine itself.

package integration

import (
	"context"
	"fmt"
	"runtime"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"

	"go.etcd.io/etcd/client/pkg/v3/testutil"
	"go.etcd.io/etcd/tests/v3/framework/integration"
)

var chaosHealthGates = map[string]bool{
	"ServerHealthCheck": true,
}

// ---------- Chaos scenarios ----------

// Scenario 1: network partition (paused follower).
// Pause freezes the member's raft send/receive while gRPC keeps
// serving: the partitioned follower's raft becomes a candidate after
// the election timeout (Lead()=None), and no_leader flips to
// NOT_SERVING within ~2s (election timeout + 3 failing ticks).
// Ground-truth assertions:
//   - The aggregate entry stays SERVING for the whole pause —
//     no_leader is a cluster-wide reason and never drives the
//     aggregate, and a failing linear_read stays protected by the
//     no_leader veto; if no_leader were wrongly wired into the
//     aggregate, this flips immediately.
//   - etcd.health/no_leader flips NOT_SERVING — the condition is
//     genuinely visible and the debounce (FailureThreshold=3)
//     cannot absorb a sustained partition.
//   - serializable_read stays SERVING — local reads need no
//     leader, so the attribution is right (local reads good /
//     consensus path bad).
//   - After Resume, no_leader recovers SERVING through the
//     SuccessThreshold window.
//
// An older version asserting "aggregate SERVING right after the
// pause" was nearly vacuous: a single immediate observation fails
// to catch up under any implementation.
func TestChaos_NetworkPartitionFollower(t *testing.T) {
	testutil.SkipTestIfShortMode(t, "Cannot start embedded cluster in --short tests")
	integration.BeforeTest(t)

	c := integration.NewCluster(t, &integration.ClusterConfig{
		Size:               3,
		ServerFeatureGates: chaosHealthGates,
	})
	defer c.Terminate(t)

	leaderID := waitForLeaderReady(t, c.Members[0])
	require.NotZerof(t, leaderID, "cluster should have a leader")

	// Pause a follower to simulate a network partition.
	var followerIdx int
	for i, m := range c.Members {
		if uint64(m.ID()) != leaderID {
			followerIdx = i
			break
		}
	}
	c.Members[followerIdx].Pause()

	hc := mustHealthCheckClient(t, c.Members[followerIdx].GRPCURL, 2*time.Second)

	// Pause for 4s: long enough for no_leader to flip (election
	// timeout ~100-300ms + 1.5s debounce), short enough that the
	// candidate's term does not rise into an election storm on
	// resume. Poll the aggregate entry throughout: every single
	// observation must be SERVING.
	pauseDeadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(pauseDeadline) {
		aggStatus, aggErr := hc.check(healthCheckService, 200*time.Millisecond)
		require.NoErrorf(t, aggErr, "aggregate health check rpc failed during pause")
		require.Equalf(t, healthpb.HealthCheckResponse_SERVING, aggStatus,
			"aggregate must stay SERVING while the paused follower has no leader (no_leader never drives the aggregate), got %v", aggStatus)
		srStatus, srErr := hc.check(serializableReadHealthService, 200*time.Millisecond)
		require.NoErrorf(t, srErr, "serializable_read health check rpc failed during pause")
		require.Equalf(t, healthpb.HealthCheckResponse_SERVING, srStatus,
			"serializable_read must stay SERVING during the pause (local reads need no leader), got %v", srStatus)
		time.Sleep(200 * time.Millisecond)
	}

	// The partitioned follower's no_leader entry must have flipped
	// NOT_SERVING (a real condition that the debounce cannot absorb
	// over a sustained partition).
	nlStatus, nlElapsed, nlErr := hc.waitForHealthStatus(noLeaderHealthService, healthpb.HealthCheckResponse_NOT_SERVING, 8*time.Second)
	require.NoErrorf(t, nlErr, "no_leader health status poll failed")
	require.Equalf(t, healthpb.HealthCheckResponse_NOT_SERVING, nlStatus,
		"expected %s NOT_SERVING during the 4s pause, got %v after %v", noLeaderHealthService, nlStatus, nlElapsed)

	// Resume the follower: once the election storm settles,
	// no_leader recovers SERVING through the SuccessThreshold
	// window.
	c.Members[followerIdx].Resume()
	recStatus, recElapsed, recErr := hc.waitForHealthStatus(noLeaderHealthService, healthpb.HealthCheckResponse_SERVING, 15*time.Second)
	require.NoErrorf(t, recErr, "no_leader recovery poll failed")
	require.Equalf(t, healthpb.HealthCheckResponse_SERVING, recStatus,
		"expected %s SERVING after resume, got %v after %v", noLeaderHealthService, recStatus, recElapsed)
}

// Scenario 2: no apply_lag false positives under write pressure.
// 400 concurrent writes with the default threshold (10000): after
// the writes complete, both the aggregate entry and the per-check
// entries must still be SERVING — any NOT_SERVING here is a false
// positive. Note the assertion targets: apply_lag is a cluster-wide
// reason and never drives the aggregate, so asserting only the
// aggregate entry against apply_lag false positives is vacuous —
// the etcd.health/apply_lag per-check entry must be asserted as
// well. (A flag value of 0 is the sentinel; healthCheckConfig
// translates it to the package default of 10000, not "any gap
// triggers"; the actual over-threshold behavior is covered by the
// unit tests TestChecker_ApplyLagThreshold*.)
func TestChaos_ApplyLagNoFalsePositiveDuringWrites(t *testing.T) {
	testutil.SkipTestIfShortMode(t, "Cannot start embedded cluster in --short tests")
	integration.BeforeTest(t)

	c := integration.NewCluster(t, &integration.ClusterConfig{
		Size:               1,
		ServerFeatureGates: chaosHealthGates,
	})
	defer c.Terminate(t)

	leaderID := waitForLeaderReady(t, c.Members[0])
	require.NotZerof(t, leaderID, "single node should be its own leader")

	// Write enough data to trigger a potential apply lag.
	var wg sync.WaitGroup
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
				_, err := c.Members[0].Client.Put(ctx, fmt.Sprintf("w%d-k%d", id, i), "v")
				cancel()
				if err != nil {
					return
				}
			}
		}(w)
	}
	wg.Wait()

	// Both the aggregate and the per-check entries must still be
	// SERVING: after the writes finish all 400 puts have applied,
	// so a NOT_SERVING on any entry is a false positive.
	hc := mustHealthCheckClient(t, c.Members[0].GRPCURL, 2*time.Second)
	status, err := hc.check(healthCheckService, 1*time.Second)
	require.NoErrorf(t, err, "health check rpc failed")
	require.Equalf(t, healthpb.HealthCheckResponse_SERVING, status,
		"expected SERVING after 400 writes, got %v", status)
	lagStatus, err := hc.check(applyLagHealthService, 1*time.Second)
	require.NoErrorf(t, err, "apply_lag per-check rpc failed")
	require.Equalf(t, healthpb.HealthCheckResponse_SERVING, lagStatus,
		"expected %s SERVING after 400 writes (apply_lag false positive), got %v", applyLagHealthService, lagStatus)
}

// Scenario 3: leader transition (the etcdctl move-leader
// equivalent). A 3-member cluster with the leader stopped. Three
// things are asserted:
//   - A new leader really is elected (and != the old one) — this is
//     the evidence the "transition" happened; otherwise the test
//     degrades into scenario 6's stopped follower;
//   - The survivor's aggregate entry stays SERVING from the moment
//     the leader stops — the no_leader failures inside the election
//     window are absorbed by the debounce plus the cluster-wide
//     exemption; if no_leader were wrongly wired into the aggregate
//     and the election (under load) exceeded 1.5s, this flips;
//   - The linear_read per-check entry is eventually SERVING — the
//     consensus path recovers.
func TestChaos_LeaderTransition(t *testing.T) {
	testutil.SkipTestIfShortMode(t, "Cannot start embedded cluster in --short tests")
	integration.BeforeTest(t)

	c := integration.NewCluster(t, &integration.ClusterConfig{
		Size:               3,
		ServerFeatureGates: chaosHealthGates,
	})
	defer c.Terminate(t)

	oldLeaderID := waitForLeaderReady(t, c.Members[0])
	require.NotZerof(t, oldLeaderID, "cluster should have a leader")

	// Find the leader index.
	var leaderIdx int
	for i, m := range c.Members {
		if uint64(m.ID()) == oldLeaderID {
			leaderIdx = i
			break
		}
	}

	survivorIdx := (leaderIdx + 1) % len(c.Members)
	hc := mustHealthCheckClient(t, c.Members[survivorIdx].GRPCURL, 2*time.Second)

	// Stop the leader and poll the aggregate entry from the instant
	// it stops: every observation inside the election window must be
	// SERVING (a transient no_leader must not drain — draining all
	// members only amplifies a single ErrNoLeader into a full round
	// of client retries).
	c.Members[leaderIdx].Stop(t)
	electionDeadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(electionDeadline) {
		aggStatus, aggErr := hc.check(healthCheckService, 200*time.Millisecond)
		require.NoErrorf(t, aggErr, "aggregate health check rpc failed during election")
		require.Equalf(t, healthpb.HealthCheckResponse_SERVING, aggStatus,
			"aggregate must stay SERVING through the leader transition, got %v", aggStatus)
		time.Sleep(100 * time.Millisecond)
	}

	// A new leader must have been elected, and it is not the stopped
	// member.
	newLeaderID := uint64(0)
	for _, m := range c.Members {
		if m == c.Members[leaderIdx] {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		resp, err := m.Client.Status(ctx, m.Client.Endpoints()[0])
		cancel()
		if err == nil && resp != nil && resp.Leader != 0 {
			newLeaderID = resp.Leader
			break
		}
	}
	require.NotZerof(t, newLeaderID, "no new leader elected within 5s of stopping the old leader")
	require.NotEqualf(t, oldLeaderID, newLeaderID,
		"the stopped member %d is still reported as leader; the transition never happened", oldLeaderID)

	// The consensus path recovers: the survivor's linear_read entry
	// is SERVING.
	lrStatus, lrElapsed, lrErr := hc.waitForHealthStatus(linearReadHealthService, healthpb.HealthCheckResponse_SERVING, 10*time.Second)
	require.NoErrorf(t, lrErr, "linear_read health status poll failed")
	require.Equalf(t, healthpb.HealthCheckResponse_SERVING, lrStatus,
		"expected %s SERVING after election, got %v after %v", linearReadHealthService, lrStatus, lrElapsed)
}

// Scenario 4: goroutine leak — the Checker exits after Stop().
// Invariant under test: after Terminate the pprof goroutine count
// returns close to baseline. (The framework's leak detection does
// this too; here it gets an explicit assertion.)
func TestChaos_NoGoroutineLeakOnStop(t *testing.T) {
	testutil.SkipTestIfShortMode(t, "Cannot start embedded cluster in --short tests")
	integration.BeforeTest(t)

	// Baseline: goroutine count before startup.
	runtime.GC()
	base := runtime.NumGoroutine()

	c := integration.NewCluster(t, &integration.ClusterConfig{
		Size:               3,
		ServerFeatureGates: chaosHealthGates,
	})

	// Wait for the Checker goroutine to start.
	time.Sleep(1 * time.Second)
	// After the cluster is created the goroutine count should rise.
	mid := runtime.NumGoroutine()
	require.Greaterf(t, mid, base, "expected more goroutines after cluster start")

	// Terminate the cluster.
	c.Terminate(t)

	// Give goroutines some time to exit.
	time.Sleep(2 * time.Second)
	runtime.GC()
	after := runtime.NumGoroutine()

	// Allow a tolerance of 5 (the framework itself may keep
	// non-healthcheck resident goroutines).
	delta := after - base
	if delta < 0 {
		delta = 0
	}
	require.LessOrEqualf(t, delta, 5,
		"potential goroutine leak: baseline=%d after=%d (delta=%d)", base, after, delta)
}

// Scenario 5: an observable liveness criterion for the Checker —
// the last_tick timestamp advancing. An older version pretended to
// detect stalls by polling "aggregate stays SERVING", but the
// SERVING preset by init() also stays when the Checker deadlocks,
// so that assertion cannot distinguish the two worlds. The real
// criterion is etcd_server_healthcheck_last_tick_timestamp_seconds:
// the gauge is only updated inside the tick loop in checker.go, so
// the value advancing proves the loop is alive.
func TestChaos_CheckerLivenessByLastTick(t *testing.T) {
	testutil.SkipTestIfShortMode(t, "Cannot start embedded cluster in --short tests")
	integration.BeforeTest(t)

	c := integration.NewCluster(t, &integration.ClusterConfig{
		Size:               1,
		ServerFeatureGates: chaosHealthGates,
	})
	defer c.Terminate(t)

	leaderID := waitForLeaderReady(t, c.Members[0])
	require.NotZerof(t, leaderID, "single node should be its own leader")

	readLastTick := func() (float64, error) {
		v, err := c.Members[0].Metric("etcd_server_healthcheck_last_tick_timestamp_seconds")
		if err != nil {
			return 0, err
		}
		f, perr := strconv.ParseFloat(v, 64)
		if perr != nil {
			return 0, fmt.Errorf("unparseable etcd_server_healthcheck_last_tick_timestamp_seconds %q", v)
		}
		return f, nil
	}

	// Wait for the first non-zero tick (the checker starts with the
	// server, so it should be running around leader-ready). If the
	// Checker never ticks, this times out — that is the stall
	// criterion itself, not an indirect state.
	var first float64
	for i := 0; ; i++ {
		f, err := readLastTick()
		require.NoErrorf(t, err, "scraping last_tick metric failed")
		if f > 0 {
			first = f
			break
		}
		require.Lessf(t, i, 25,
			"last_tick stayed 0 for 5s (25×200ms) — checker loop never ticked (stalled?)")
		time.Sleep(200 * time.Millisecond)
	}

	// 2s >= 4 ticks (Interval 500ms); the value must advance at
	// Unix-second resolution.
	time.Sleep(2 * time.Second)
	second, err := readLastTick()
	require.NoErrorf(t, err, "scraping last_tick metric failed")
	require.Greaterf(t, second, first,
		"last_tick did not advance over 2s (first=%f second=%f) — checker loop stalled", first, second)
}

// Scenario 6: stop one of three nodes — a degraded cluster whose
// survivors must stay SERVING. Similar to the member-removal
// scenario, but simulating member failure by stopping the follower
// directly, the more common shape of member failure in etcd (it
// avoids the occasional "etcdserver: unhealthy cluster" false
// failure that MemberRemove hits under the integration framework;
// hence the scenario name StopOne rather than RemoveOne).
func TestChaos_3NodeStopOneDegraded(t *testing.T) {
	testutil.SkipTestIfShortMode(t, "Cannot start embedded cluster in --short tests")
	integration.BeforeTest(t)

	c := integration.NewCluster(t, &integration.ClusterConfig{
		Size:               3,
		ServerFeatureGates: chaosHealthGates,
	})
	defer c.Terminate(t)

	leaderID := waitForLeaderReady(t, c.Members[0])
	require.NotZerof(t, leaderID, "cluster should have a leader")

	// Find a follower and stop it (simulating member failure).
	var followerIdx int
	for i, m := range c.Members {
		if uint64(m.ID()) != leaderID {
			followerIdx = i
			break
		}
	}
	c.Members[followerIdx].Stop(t)

	// The 2-node quorum survives, so the leader must stay SERVING.
	// Poll from a member that is not the stopped follower.
	pollIdx := (followerIdx + 1) % len(c.Members)
	hc := mustHealthCheckClient(t, c.Members[pollIdx].GRPCURL, 2*time.Second)
	status, elapsed, err := hc.waitForHealthStatus(healthCheckService, healthpb.HealthCheckResponse_SERVING, 5*time.Second)
	require.NoErrorf(t, err, "health status poll failed")
	require.Equalf(t, healthpb.HealthCheckResponse_SERVING, status,
		"survivor expected SERVING after member stop, got %v after %v", status, elapsed)
}

// Scenario 7 was originally a "transient-suppression timeout". That
// suppression is gone: InflightSnapshots counts the snapshots this
// node is *sending*, which blames the wrong member (the receiver is
// the one blocked on apply) at the wrong phase (snapshot creation
// blocks apply synchronously while the count has not advanced yet),
// so a suppression window would only mask real failures. What the
// scenario keeps verifying is the invariant that actually mattered:
// no apply_lag false NOT_SERVING during a write burst. apply_lag is
// a cluster-wide reason and never drives the aggregate entry, so
// the invariant is observed on the etcd.health/apply_lag per-check
// entry (the aggregate entry is counted separately and covers the
// aggregate-eligible reasons such as linear_read).
func TestChaos_NoFalseNotServingDuringWriteBurst(t *testing.T) {
	testutil.SkipTestIfShortMode(t, "Cannot start embedded cluster in --short tests")
	integration.BeforeTest(t)

	c := integration.NewCluster(t, &integration.ClusterConfig{
		Size:               1,
		ServerFeatureGates: chaosHealthGates,
	})
	defer c.Terminate(t)

	leaderID := waitForLeaderReady(t, c.Members[0])
	require.NotZerof(t, leaderID, "single node should be its own leader")

	// Write hard for 5 seconds, simulating I/O jitter.
	stop := make(chan struct{})
	go func() {
		i := 0
		for {
			select {
			case <-stop:
				return
			default:
			}
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			_, _ = c.Members[0].Client.Put(ctx, fmt.Sprintf("k-%d", i), "v")
			cancel()
			i++
		}
	}()

	// SERVING is expected for the whole 5 seconds. apply_lag is a
	// cluster-wide reason: it never flips the aggregate entry itself,
	// so the real observation surface for apply_lag false positives
	// during the burst is the etcd.health/apply_lag per-check entry —
	// the aggregate entry keeps verifying that aggregate-eligible
	// reasons (linear_read, apply_stall, ...) stay free of false
	// positives.
	hc := mustHealthCheckClient(t, c.Members[0].GRPCURL, 1*time.Second)
	deadline := time.Now().Add(5 * time.Second)
	notServingCount := 0
	applyLagNotServingCount := 0
	for time.Now().Before(deadline) {
		status, err := hc.check(healthCheckService, 200*time.Millisecond)
		require.NoErrorf(t, err, "health check rpc failed")
		if status != healthpb.HealthCheckResponse_SERVING {
			notServingCount++
		}
		lagStatus, err := hc.check(applyLagHealthService, 200*time.Millisecond)
		require.NoErrorf(t, err, "apply_lag per-check rpc failed")
		if lagStatus != healthpb.HealthCheckResponse_SERVING {
			applyLagNotServingCount++
		}
		time.Sleep(100 * time.Millisecond)
	}
	close(stop)
	require.Zerof(t, notServingCount,
		"expected aggregate SERVING throughout burst, got %d NOT_SERVING observations (false positive)", notServingCount)
	require.Zerof(t, applyLagNotServingCount,
		"expected %s SERVING throughout burst, got %d NOT_SERVING observations (apply_lag false positive)", applyLagHealthService, applyLagNotServingCount)
}
