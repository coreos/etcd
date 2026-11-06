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

// Integration tests for the ServerHealthCheck feature, covering the
// seven scenarios of the serving-path state machine.
//
// Each test starts a real embed.EtcdServer with the ServerHealthCheck
// feature gate enabled and observes state transitions through a
// grpc.health.v1 client.

package integration

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"

	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	"go.etcd.io/etcd/client/pkg/v3/testutil"
	clientv3 "go.etcd.io/etcd/client/v3"
	"go.etcd.io/etcd/tests/v3/framework/integration"
)

const (
	// healthCheckService is the grpc.health.v1 aggregate entry, the
	// one clients use for endpoint selection. Only reasons that make
	// evicting this member worthwhile flip it; see aggregateEligible
	// in server/etcdserver/api/v3rpc/health.go.
	healthCheckService = ""
	// noLeaderHealthService is the per-check entry no_leader drives.
	// no_leader holds on every member at once, so it is visible here
	// and never on the aggregate entry.
	noLeaderHealthService = "etcd.health/no_leader"
	// linearReadHealthService is the per-check entry the linear_read
	// probe drives. Under no_leader the read-index round trip can never
	// complete, so the entry flips NOT_SERVING while
	// serializable_read stays green — the consensus-path half of the
	// attribution pair.
	linearReadHealthService = "etcd.health/linear_read"
	// applyLagHealthService is the per-check entry apply_lag drives.
	// apply_lag is cluster-wide like no_leader: it never drives the
	// aggregate entry, so a test asserting the aggregate stays
	// SERVING cannot detect an apply_lag false positive at all —
	// the per-check entry is the observable surface for it.
	applyLagHealthService = "etcd.health/apply_lag"
	// dataCorruptionHealthService is the per-check entry the
	// data_corruption check drives. The check fails only on the member
	// a CORRUPT alarm names, so
	// the entry — and the aggregate — flip on the corrupt member
	// while healthy peers keep serving.
	dataCorruptionHealthService = "etcd.health/data_corruption"
	// serializableReadHealthService is the per-check entry the
	// serializable_read check drives. The probe is linear_read minus
	// the read-index quorum wait, so it stays green whenever the
	// local read path works — including during no_leader, where
	// linear_read cannot complete. That contrast is the attribution
	// contract: serializable green + linear red isolates the failure
	// to the consensus path.
	serializableReadHealthService = "etcd.health/serializable_read"
)

// healthCheckClient wraps a gRPC connection dedicated to grpc.health.v1.
type healthCheckClient struct {
	conn   *grpc.ClientConn
	client healthpb.HealthClient
}

// grpcDialAddr converts a etcd endpoint URL (e.g. "unix://localhost:m0" or
// "https://127.0.0.1:2379") to a (network, address) pair suitable for
// grpc.WithContextDialer. etcd's integration.Member listens on a unix domain
// socket by default, which grpc.Dial cannot address without a custom dialer.
func grpcDialAddr(endpoint string) (string, string) {
	// "unix://localhost:m0" or "unixs://localhost:m0"
	if i := strings.Index(endpoint, "://"); i > 0 {
		scheme := endpoint[:i]
		rest := endpoint[i+len("://"):]
		switch scheme {
		case "unix", "unixs":
			return "unix", rest
		}
	}
	// "http://host:port" / "https://host:port" / "host:port" → tcp
	if i := strings.Index(endpoint, "://"); i > 0 {
		return "tcp", endpoint[i+len("://"):]
	}
	return "tcp", endpoint
}

// newHealthCheckClient dials a member's gRPC health endpoint. It returns an
// error instead of asserting so it is also safe to call off the test
// goroutine (require/t.Fatal must run on the test goroutine only).
func newHealthCheckClient(t *testing.T, endpoint string, dialTimeout time.Duration) (*healthCheckClient, error) {
	network, addr := grpcDialAddr(endpoint)
	dialer := func(ctx context.Context, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, network, addr)
	}
	// grpc.NewClient requires the target to be a valid URI with a recognized
	// scheme. We route everything through "passthrough" and rely on
	// WithContextDialer for the actual network/address resolution.
	target := "passthrough:///" + addr
	conn, err := grpc.NewClient(
		target,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(dialer),
	)
	if err != nil {
		return nil, err
	}
	// Force connection establishment within the dial timeout.
	connectCtx, cancel := context.WithTimeout(context.Background(), dialTimeout)
	defer cancel()
	conn.Connect()
	// Wait for the connection to leave Idle (it should transition through
	// Connecting then Ready), or fail on deadline.
	if !conn.WaitForStateChange(connectCtx, connectivity.Idle) {
		_ = conn.Close()
		return nil, fmt.Errorf("grpc client could not reach %s within %v", endpoint, dialTimeout)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return &healthCheckClient{conn: conn, client: healthpb.NewHealthClient(conn)}, nil
}

// mustHealthCheckClient is the asserting variant of newHealthCheckClient for
// callers on the test goroutine.
func mustHealthCheckClient(t *testing.T, endpoint string, dialTimeout time.Duration) *healthCheckClient {
	t.Helper()
	hc, err := newHealthCheckClient(t, endpoint, dialTimeout)
	require.NoErrorf(t, err, "failed to dial %s", endpoint)
	return hc
}

func (h *healthCheckClient) check(service string, timeout time.Duration) (healthpb.HealthCheckResponse_ServingStatus, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	resp, err := h.client.Check(ctx, &healthpb.HealthCheckRequest{Service: service})
	if err != nil {
		return 0, err
	}
	return resp.GetStatus(), nil
}

// waitForHealthStatus polls until service's gRPC health status
// becomes the target value or the timeout elapses. Returns the
// observed status (may be != target on timeout).
func (h *healthCheckClient) waitForHealthStatus(service string, target healthpb.HealthCheckResponse_ServingStatus, timeout time.Duration) (healthpb.HealthCheckResponse_ServingStatus, time.Duration, error) {
	start := time.Now()
	deadline := start.Add(timeout)
	var last healthpb.HealthCheckResponse_ServingStatus
	for time.Now().Before(deadline) {
		status, err := h.check(service, 200*time.Millisecond)
		if err != nil {
			return last, time.Since(start), err
		}
		last = status
		if last == target {
			return last, time.Since(start), nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return last, time.Since(start), nil
}

// waitForLeaderReady polls until the cluster reports a leader or the timeout
// elapses. Returns the leader ID (0 = none).
func waitForLeaderReady(t *testing.T, m *integration.Member) uint64 {
	t.Helper()
	start := time.Now()
	deadline := start.Add(10 * time.Second)
	var leaderID uint64
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
		resp, err := m.Client.Status(ctx, m.Client.Endpoints()[0])
		cancel()
		if err == nil && resp != nil && resp.Leader != 0 {
			return resp.Leader
		}
		time.Sleep(50 * time.Millisecond)
	}
	return leaderID
}

// healthCheckGates is the common set of feature gates for health tests.
var healthCheckGates = map[string]bool{
	"ServerHealthCheck": true,
}

// ---------- Integration scenarios ----------

// Scenario 1: single-node cluster with the gate enabled reports
// gRPC health SERVING.
func TestServerHealthCheck_1NodeServing(t *testing.T) {
	testutil.SkipTestIfShortMode(t, "Cannot start embedded cluster in --short tests")
	integration.BeforeTest(t)
	c := integration.NewCluster(t, &integration.ClusterConfig{
		Size:               1,
		ServerFeatureGates: healthCheckGates,
	})
	defer c.Terminate(t)

	leaderID := waitForLeaderReady(t, c.Members[0])
	require.NotZerof(t, leaderID, "single node should be its own leader")

	hc := mustHealthCheckClient(t, c.Members[0].GRPCURL, 2*time.Second)
	status, elapsed, err := hc.waitForHealthStatus(healthCheckService, healthpb.HealthCheckResponse_SERVING, 3*time.Second)
	require.NoErrorf(t, err, "health status poll failed")
	require.Equalf(t, healthpb.HealthCheckResponse_SERVING, status,
		"expected SERVING within 3s, got %v after %v", status, elapsed)
}

// Scenario 2: 3-node cluster with the gate enabled; every member
// reports SERVING.
func TestServerHealthCheck_3NodeAllServing(t *testing.T) {
	testutil.SkipTestIfShortMode(t, "Cannot start embedded cluster in --short tests")
	integration.BeforeTest(t)

	c := integration.NewCluster(t, &integration.ClusterConfig{
		Size:               3,
		ServerFeatureGates: healthCheckGates,
	})
	defer c.Terminate(t)

	// Wait for cluster to converge on a leader.
	leaderID := waitForLeaderReady(t, c.Members[0])
	require.NotZerof(t, leaderID, "cluster should have a leader")

	// Poll every member in parallel.
	var wg sync.WaitGroup
	statuses := make([]healthpb.HealthCheckResponse_ServingStatus, len(c.Members))
	elapseds := make([]time.Duration, len(c.Members))
	errs := make([]error, len(c.Members))
	for i, m := range c.Members {
		i, m := i, m
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Error-returning form: require is not allowed off the test goroutine.
			hc, err := newHealthCheckClient(t, m.GRPCURL, 2*time.Second)
			if err != nil {
				errs[i] = err
				return
			}
			statuses[i], elapseds[i], errs[i] = hc.waitForHealthStatus(healthCheckService, healthpb.HealthCheckResponse_SERVING, 5*time.Second)
		}()
	}
	wg.Wait()
	for _, err := range errs {
		require.NoErrorf(t, err, "member health status poll failed")
	}
	for i, s := range statuses {
		require.Equalf(t, healthpb.HealthCheckResponse_SERVING, s,
			"member %d expected SERVING, got %v after %v", i, s, elapseds[i])
	}
}

// Scenario 3: fault injection flips a per-check entry NOT_SERVING.
// Stop the leader of a 2-node cluster to trigger no_leader and verify
// that etcd.health/no_leader flips NOT_SERVING while the aggregate
// entry stays SERVING (no_leader is a cluster-wide condition;
// evicting this member cannot route the client to a better one).
func TestServerHealthCheck_2NodeNoLeaderNotServing(t *testing.T) {
	testutil.SkipTestIfShortMode(t, "Cannot start embedded cluster in --short tests")
	integration.BeforeTest(t)

	c := integration.NewCluster(t, &integration.ClusterConfig{
		Size:               2,
		ServerFeatureGates: healthCheckGates,
	})
	defer c.Terminate(t)

	leaderID := waitForLeaderReady(t, c.Members[0])
	require.NotZerof(t, leaderID, "cluster should have a leader")

	// Find the leader index.
	var leaderIdx int
	for i, m := range c.Members {
		if uint64(m.ID()) == leaderID {
			leaderIdx = i
			break
		}
	}
	// Stop the leader: the 2-node cluster loses quorum and the
	// survivor enters no_leader.
	c.Members[leaderIdx].Stop(t)

	// Poll the survivor for the per-check entry flipping
	// NOT_SERVING (no_leader triggers after 3 consecutive failures,
	// about 1.5s).
	hc := mustHealthCheckClient(t, c.Members[1-leaderIdx].GRPCURL, 2*time.Second)
	status, elapsed, err := hc.waitForHealthStatus(noLeaderHealthService, healthpb.HealthCheckResponse_NOT_SERVING, 8*time.Second)
	require.NoErrorf(t, err, "health status poll failed")
	require.Equalf(t, healthpb.HealthCheckResponse_NOT_SERVING, status,
		"expected %s NOT_SERVING within 8s after leader stop, got %v after %v",
		noLeaderHealthService, status, elapsed)

	// The aggregate entry must stay SERVING: no_leader holds on every
	// member at once, and evicting all of them turns a single
	// ErrNoLeader into a full round of client-side read retries.
	aggStatus, aggErr := hc.check(healthCheckService, 200*time.Millisecond)
	require.NoErrorf(t, aggErr, "aggregate health check rpc failed")
	require.Equalf(t, healthpb.HealthCheckResponse_SERVING, aggStatus,
		"aggregate entry must stay SERVING under no_leader, got %v", aggStatus)

	// Attribution contract, end to end: the serializable_read probe
	// has no consensus component — a local read needs no leader — so
	// it must stay SERVING during no_leader, in contrast with
	// linear_read (which cannot complete without a leader). Operators
	// use this pair of entries to tell "local reads broken" apart
	// from "no consensus". If the serializable probe lost its
	// Serializable flag and degraded into a linear_read clone, it
	// would flip NOT_SERVING here and drag the aggregate down with it.
	srStatus, srErr := hc.check(serializableReadHealthService, 2*time.Second)
	require.NoErrorf(t, srErr, "serializable_read health check rpc failed")
	require.Equalf(t, healthpb.HealthCheckResponse_SERVING, srStatus,
		"serializable_read must stay SERVING under no_leader (local reads need no leader), got %v", srStatus)
}

// Scenario 3b: the no_leader veto over linear_read's aggregate
// eligibility is bounded.
//
// Timeline derivation (integration cluster election timeout
// et = ElectionTicks×TickDuration = 10×10ms = 100ms):
//   - veto window = max(30s, 10×et) = 30s, measured from when
//     no_leader first fires (3 ticks, roughly 1.0-1.5s);
//   - CheckTimeout = ReqTimeout = 5s+2×et ≈ 5.2s. Without a leader
//     the read-index round trip never returns, so linear_read probes
//     fail one per timeout; 3 consecutive failures plus the serial
//     spawn interval flip linear_read at roughly 17s;
//   - that moment is still inside the 30s window, so the aggregate
//     must stay SERVING — during leaderlessness every member's
//     linear_read is timing out, and evicting all of them inside the
//     window just turns one ErrNoLeader into a full round of
//     client-side read retries;
//   - once the window expires (≈ t0+31s), the next linear_read
//     failure re-evaluates the aggregate; vetoLifted latches, and
//     the aggregate flips NOT_SERVING (≈ t0+33-37s): a
//     minority-partitioned member has been stranded for over a full
//     election cycle, and only evicting it moves clients to a peer
//     that can serve.
//
// The assertions follow in three phases: the aggregate stays SERVING
// before t0+30s (the veto holds; without a veto the aggregate would
// flip at the ≈17s linear_read trigger and this phase goes red), and
// flips NOT_SERVING before t0+60s (the veto expires; if the window
// never lifted, this phase goes red). Attribution assertion:
// serializable_read stays SERVING throughout, proving the flip is
// driven by the consensus path, not by broken local reads.
func TestServerHealthCheck_NoLeaderVetoBoundedThenEvicts(t *testing.T) {
	testutil.SkipTestIfShortMode(t, "Cannot start embedded cluster in --short tests")
	integration.BeforeTest(t)

	c := integration.NewCluster(t, &integration.ClusterConfig{
		Size:               2,
		ServerFeatureGates: healthCheckGates,
	})
	defer c.Terminate(t)

	leaderID := waitForLeaderReady(t, c.Members[0])
	require.NotZerof(t, leaderID, "cluster should have a leader")

	var leaderIdx int
	for i, m := range c.Members {
		if uint64(m.ID()) == leaderID {
			leaderIdx = i
			break
		}
	}
	survivor := c.Members[1-leaderIdx]
	hc := mustHealthCheckClient(t, survivor.GRPCURL, 2*time.Second)

	t0 := time.Now()
	c.Members[leaderIdx].Stop(t)

	// Phase 1: the no_leader entry fires (≈1.5s); the veto arms from
	// that timestamp.
	status, elapsed, err := hc.waitForHealthStatus(noLeaderHealthService, healthpb.HealthCheckResponse_NOT_SERVING, 8*time.Second)
	require.NoErrorf(t, err, "health status poll failed")
	require.Equalf(t, healthpb.HealthCheckResponse_NOT_SERVING, status,
		"expected %s NOT_SERVING within 8s after leader stop, got %v after %v",
		noLeaderHealthService, status, elapsed)

	// Phase 2: the aggregate stays SERVING before t0+30s. The earliest
	// possible expiry is t0+31s (no_leader fires at the earliest
	// t0+1.0s, plus the 30s window), so 30s leaves ≥1s of margin;
	// linear_read fires at ≈17s, making the second half of the window
	// (17s-30s) the discriminating interval.
	vetoHold := 30 * time.Second
	for time.Since(t0) < vetoHold {
		aggStatus, aggErr := hc.check(healthCheckService, 200*time.Millisecond)
		require.NoErrorf(t, aggErr, "aggregate health check rpc failed")
		require.Equalf(t, healthpb.HealthCheckResponse_SERVING, aggStatus,
			"aggregate must stay SERVING inside the no_leader veto window (t=%v after leader stop), got %v",
			time.Since(t0).Round(time.Millisecond), aggStatus)
		time.Sleep(200 * time.Millisecond)
	}

	// Phase 2 precondition: linear_read really did fire inside the
	// window (≈17s; this should return immediately. If the probe
	// mechanism is broken this times out and goes red, and phase 2's
	// discriminating interval is void).
	lrStatus, lrElapsed, lrErr := hc.waitForHealthStatus(linearReadHealthService, healthpb.HealthCheckResponse_NOT_SERVING, 15*time.Second)
	require.NoErrorf(t, lrErr, "linear_read health status poll failed")
	require.Equalf(t, healthpb.HealthCheckResponse_NOT_SERVING, lrStatus,
		"expected %s NOT_SERVING within the veto window, got %v after %v",
		linearReadHealthService, lrStatus, lrElapsed)

	// Phase 3: after the veto expires the aggregate flips
	// NOT_SERVING. Expiry is ≈ t0+31s, the next probe failure
	// ≈ t0+37s; 60s is the absolute bound (measured from t0,
	// independent of how long the earlier phases took). The poll
	// tolerates transient RPC errors and fails only at the deadline.
	deadline := t0.Add(60 * time.Second)
	var lastAgg healthpb.HealthCheckResponse_ServingStatus
	for {
		if time.Now().After(deadline) {
			t.Fatalf("aggregate never flipped NOT_SERVING after the veto window expired, last=%v at t=%v",
				lastAgg, time.Since(t0).Round(time.Millisecond))
		}
		lastAgg, err = hc.check(healthCheckService, 200*time.Millisecond)
		if err == nil && lastAgg == healthpb.HealthCheckResponse_NOT_SERVING {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}

	// Attribution: serializable_read stays SERVING throughout — local
	// reads need no leader, so the aggregate flip is driven by
	// linear_read (the consensus path). If the serializable probe lost
	// its Serializable flag and degraded into a linear_read clone, it
	// would flip NOT_SERVING here.
	srStatus, srErr := hc.check(serializableReadHealthService, 2*time.Second)
	require.NoErrorf(t, srErr, "serializable_read health check rpc failed")
	require.Equalf(t, healthpb.HealthCheckResponse_SERVING, srStatus,
		"serializable_read must stay SERVING throughout the leaderless period, got %v", srStatus)

	t.Logf("veto timeline: no_leader=%v, aggregate flipped NOT_SERVING at t=%v",
		elapsed.Round(time.Millisecond), time.Since(t0).Round(time.Millisecond))
}

// Scenario 4: recovery → SERVING.
// A 3-node cluster: stop one follower, restart it, and verify the
// member recovers to SERVING.
func TestServerHealthCheck_RecoversToServing(t *testing.T) {
	testutil.SkipTestIfShortMode(t, "Cannot start embedded cluster in --short tests")
	integration.BeforeTest(t)

	c := integration.NewCluster(t, &integration.ClusterConfig{
		Size:               3,
		ServerFeatureGates: healthCheckGates,
	})
	defer c.Terminate(t)

	leaderID := waitForLeaderReady(t, c.Members[0])
	require.NotZerof(t, leaderID, "cluster should have a leader")

	// Pick a follower.
	var followerIdx int
	for i, m := range c.Members {
		if uint64(m.ID()) != leaderID {
			followerIdx = i
			break
		}
	}
	c.Members[followerIdx].Stop(t)

	// Wait, then restart the follower.
	time.Sleep(1 * time.Second)
	require.NoError(t, c.Members[followerIdx].Restart(t))
	c.WaitMembersForLeader(t, c.Members)

	// Verify the restarted member reports SERVING (re-dial the
	// GRPCURL because Restart replaces the listener).
	hc := mustHealthCheckClient(t, c.Members[followerIdx].GRPCURL, 2*time.Second)
	status, elapsed, err := hc.waitForHealthStatus(healthCheckService, healthpb.HealthCheckResponse_SERVING, 15*time.Second)
	require.NoErrorf(t, err, "health status poll failed")
	require.Equalf(t, healthpb.HealthCheckResponse_SERVING, status,
		"expected SERVING within 15s after restart, got %v after %v", status, elapsed)
}

// Regression: a server stopped before publishV3 closed readych must not
// deadlock HardStop. The ServerHealthCheck goroutine waits on ReadyNotify;
// without a stopping branch it blocks wg.Wait forever, so Terminate hangs.
// Stop a member before it is ready (the Restart window), then stop again.
func TestServerHealthCheck_StopBeforeReadyDoesNotHang(t *testing.T) {
	testutil.SkipTestIfShortMode(t, "Cannot start embedded cluster in --short tests")
	integration.BeforeTest(t)

	c := integration.NewCluster(t, &integration.ClusterConfig{
		Size:               3,
		ServerFeatureGates: healthCheckGates,
	})
	defer c.Terminate(t)

	leaderID := waitForLeaderReady(t, c.Members[0])
	require.NotZerof(t, leaderID, "cluster should have a leader")

	var followerIdx int
	for i, m := range c.Members {
		if uint64(m.ID()) != leaderID {
			followerIdx = i
			break
		}
	}
	c.Members[followerIdx].Stop(t)
	time.Sleep(1 * time.Second)
	require.NoError(t, c.Members[followerIdx].Restart(t))

	// Terminate while the restarted member may still be pre-ready. If the
	// goroutine is stuck on ReadyNotify, m.Close() -> HardStop blocks on
	// wg.Wait and this test times out instead of failing fast.
	done := make(chan struct{})
	go func() {
		c.Members[followerIdx].Stop(t)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("member Stop deadlocked: server stopped before ready never returned")
	}
}

// Scenario 5: before the first publish during startup, gRPC health
// must already report SERVING.
// Invariant: between Server.ReadyNotify returning and the first
// health check tick, gRPC health is still SERVING (pre-seeded by
// initServingStatuses at registration time).
func TestServerHealthCheck_InitialServingBeforeReadyNotify(t *testing.T) {
	testutil.SkipTestIfShortMode(t, "Cannot start embedded cluster in --short tests")
	integration.BeforeTest(t)

	c := integration.NewCluster(t, &integration.ClusterConfig{
		Size:               1,
		ServerFeatureGates: healthCheckGates,
	})
	defer c.Terminate(t)

	// Check immediately — before the first health check tick fires.
	hc := mustHealthCheckClient(t, c.Members[0].GRPCURL, 2*time.Second)
	// The first few polls (earlier than the 500ms tick interval)
	// expect SERVING (pre-seeded by initServingStatuses).
	for i := 0; i < 3; i++ {
		status, err := hc.check(healthCheckService, 200*time.Millisecond)
		require.NoErrorf(t, err, "health check rpc failed")
		require.Equalf(t, healthpb.HealthCheckResponse_SERVING, status,
			"iteration %d: expected SERVING before first tick, got %v", i, status)
		time.Sleep(20 * time.Millisecond)
	}
}

// Scenario 6: during defrag, with StopGRPCServiceOnDefrag=true,
// expect NOT_SERVING.
// On the small data sets of integration tests a defrag can finish
// faster than any RPC polling interval, so the RPC layer only
// verifies the connection stays usable; the deterministic evidence
// of the drain window is the log pair: DefragStarted's WARN
// (NOT_SERVING) and DefragFinished's INFO (SERVING), both with
// reason "defrag is active".
func TestServerHealthCheck_DuringDefragNotServing(t *testing.T) {
	testutil.SkipTestIfShortMode(t, "Cannot start embedded cluster in --short tests")
	integration.BeforeTest(t)

	c := integration.NewCluster(t, &integration.ClusterConfig{
		Size: 1,
		ServerFeatureGates: map[string]bool{
			"ServerHealthCheck":       true,
			"StopGRPCServiceOnDefrag": true,
		},
	})
	defer c.Terminate(t)

	leaderID := waitForLeaderReady(t, c.Members[0])
	require.NotZerof(t, leaderID, "single node should be its own leader")

	// Write enough data that defrag takes visible time (repeated
	// deletes of the same key range generate fragmentation).
	for round := 0; round < 5; round++ {
		for i := 0; i < 200; i++ {
			_, err := c.Members[0].Client.Put(context.Background(), fmt.Sprintf("r%d-k%d", round, i), "v")
			require.NoError(t, err)
		}
		// Delete the previous round's keys to punch holes and force
		// defrag to rewrite pages.
		if round > 0 {
			_, err := c.Members[0].Client.Delete(context.Background(), fmt.Sprintf("r%d-", round-1), clientv3.WithPrefix())
			require.NoError(t, err)
		}
	}

	// Start the defrag on another goroutine.
	go func() {
		_, _ = c.Members[0].Client.Defragment(context.Background(), c.Members[0].GRPCURL)
	}()

	// Poll gRPC health: the connection must stay usable
	// (StopGRPCServiceOnDefrag does not reject connections). The
	// NOT_SERVING window itself may be shorter than the polling
	// interval, so it is not forced to be observed over RPC — the
	// log assertions below are the deterministic evidence of the
	// drain window.
	hc := mustHealthCheckClient(t, c.Members[0].GRPCURL, 2*time.Second)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		_, err := hc.check(healthCheckService, 200*time.Millisecond)
		require.NoErrorf(t, err, "health check rpc failed during defrag")
		time.Sleep(50 * time.Millisecond)
	}

	// Deterministic assertion: DefragStarted must flip the aggregate
	// entry NOT_SERVING (WARN, reason "defrag is active") and
	// DefragFinished must flip it back to SERVING (INFO). Logs cannot
	// miss the window — this pair is the audit evidence that the
	// drain actually happened and recovered, independent of how long
	// defrag takes on a small data set.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	logs, err := c.Members[0].LogObserver.Expect(ctx, "defrag is active", 2)
	require.NoErrorf(t, err, "expected the defrag NOT_SERVING/SERVING log pair within 5s")
	require.Len(t, logs, 2)
	require.Containsf(t, logs[0], "NOT_SERVING",
		"first defrag log must be the NOT_SERVING flip, got %s", logs[0])
	require.NotContainsf(t, logs[1], "NOT_SERVING",
		"second defrag log must be the SERVING recovery, got %s", logs[1])
}

// Scenario 6b: during defrag, with only ServerHealthCheck=true (the
// legacy StopGRPCServiceOnDefrag gate off, its default), the
// etcd.health/defrag observation entry must still flip — observing a
// defrag must not require the legacy drain opt-in (upstream #22635)
// — while the aggregate entry stays SERVING: a drain clients never
// opted into must not evict them.
//
// Deterministic evidence is the log pair: both lines carry
// service=etcd.health/defrag. An aggregate flip would log service=""
// (logAggregateFlipLocked) and break the two-line count, so the
// count plus the service field is the regression assertion for the
// guard; the aggregate polling loop additionally checks every live
// observation stays SERVING.
func TestServerHealthCheck_DuringDefragObservationOnly(t *testing.T) {
	testutil.SkipTestIfShortMode(t, "Cannot start embedded cluster in --short tests")
	integration.BeforeTest(t)

	c := integration.NewCluster(t, &integration.ClusterConfig{
		Size:               1,
		ServerFeatureGates: healthCheckGates,
	})
	defer c.Terminate(t)

	leaderID := waitForLeaderReady(t, c.Members[0])
	require.NotZerof(t, leaderID, "single node should be its own leader")

	// Same fragmentation workload as Scenario 6: enough data that
	// defrag takes visible time.
	for round := 0; round < 5; round++ {
		for i := 0; i < 200; i++ {
			_, err := c.Members[0].Client.Put(context.Background(), fmt.Sprintf("r%d-k%d", round, i), "v")
			require.NoError(t, err)
		}
		if round > 0 {
			_, err := c.Members[0].Client.Delete(context.Background(), fmt.Sprintf("r%d-", round-1), clientv3.WithPrefix())
			require.NoError(t, err)
		}
	}

	go func() {
		_, _ = c.Members[0].Client.Defragment(context.Background(), c.Members[0].GRPCURL)
	}()

	// Poll the aggregate entry throughout the defrag window: unlike
	// Scenario 6 there is no drain, so every observation must be
	// SERVING. A NOT_SERVING here means the defrag escaped its
	// observation-only role.
	hc := mustHealthCheckClient(t, c.Members[0].GRPCURL, 2*time.Second)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		status, err := hc.check(healthCheckService, 200*time.Millisecond)
		require.NoErrorf(t, err, "health check rpc failed during defrag")
		require.Equalf(t, healthpb.HealthCheckResponse_SERVING, status,
			"aggregate must stay SERVING during defrag without the legacy gate, got %v", status)
		time.Sleep(50 * time.Millisecond)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	logs, err := c.Members[0].LogObserver.Expect(ctx, "defrag is active", 2)
	require.NoErrorf(t, err, "expected the defrag NOT_SERVING/SERVING log pair within 5s")
	require.Len(t, logs, 2)
	require.Containsf(t, logs[0], "NOT_SERVING",
		"first defrag log must be the NOT_SERVING flip, got %s", logs[0])
	require.Containsf(t, logs[0], "etcd.health/defrag",
		"first defrag log must flip the observation entry (service=etcd.health/defrag), not the aggregate, got %s", logs[0])
	require.NotContainsf(t, logs[1], "NOT_SERVING",
		"second defrag log must be the SERVING recovery, got %s", logs[1])
	require.Containsf(t, logs[1], "etcd.health/defrag",
		"second defrag log must recover the observation entry (service=etcd.health/defrag), got %s", logs[1])
}

// Scenario 7: ClusterConfig.HealthCheckApplyLagThreshold is passed
// through to the running Checker. Verification: server.go logs
// "server health check started" at startup with an
// apply-lag-threshold field. An end-to-end NOT_SERVING check is not
// reliable here: on a single node with clientv3.Put's synchronous
// semantics, apply follows commit immediately, the gap almost never
// stays above 1, and the 500ms tick cannot catch a 1-entry window.
// The end-to-end NOT_SERVING behavior is covered by the unit tests
// TestChecker_ApplyLagThreshold*.
func TestServerHealthCheck_ApplyLagThreshold(t *testing.T) {
	testutil.SkipTestIfShortMode(t, "Cannot start embedded cluster in --short tests")
	integration.BeforeTest(t)

	c := integration.NewCluster(t, &integration.ClusterConfig{
		Size:                         1,
		ServerFeatureGates:           healthCheckGates,
		HealthCheckApplyLagThreshold: 1,
	})
	defer c.Terminate(t)

	leaderID := waitForLeaderReady(t, c.Members[0])
	require.NotZerof(t, leaderID, "single node should be its own leader")

	// Wait for the server health check startup log. After ReadyNotify,
	// server.Start still waits on GetHealthNotifier (≤5s), so the log
	// may appear a few hundred milliseconds after the leader is
	// elected. Wait 5s with LogObserver.Expect.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	logs, err := c.Members[0].LogObserver.Expect(ctx, "server health check started", 1)
	require.NoErrorf(t, err, "expected 'server health check started' log within 5s")
	require.NotEmpty(t, logs)

	// Verify "apply-lag-threshold": 1 (pass-through worked) rather
	// than the default 10000 (pass-through failed). The zap field
	// format is "name": value, not name=value.
	found := false
	for _, l := range logs {
		if strings.Contains(l, `"apply-lag-threshold": 1`) {
			found = true
			break
		}
	}
	require.Truef(t, found,
		"expected \"apply-lag-threshold\": 1 (ClusterConfig.HealthCheckApplyLagThreshold passed through), got logs: %v", logs)
}

// Gate-off guard: with ServerHealthCheck disabled, the
// FeatureEnabled guard in the Start layer must stop the checker
// goroutine from starting. If the guard were removed, gate-off
// clusters would still start the checker (idle ticks plus health
// metric noise) while the notifier-side gate still discards the
// flips — the two sides' gate semantics would split. Observable
// surface: the checker always logs "server health check started"
// on startup; not seeing it within 3s (far beyond the log's normal
// latency when the gate is on) proves the goroutine never started.
// TestServerHealthCheck_DisabledByDefault pins the default-off contract:
// no feature-gate flag anywhere, the checker must not start. While the
// chain is under development, enabling is a conscious act
// (--feature-gates=ServerHealthCheck=true, e.g. rendered by the operator
// for the clusters it manages); upgrading the binary alone must not
// activate the chain. Complements TestServerHealthCheck_GateOffDoesNot
// StartChecker (explicit opt-out) by pinning the unspecified-flag default.
func TestServerHealthCheck_DisabledByDefault(t *testing.T) {
	testutil.SkipTestIfShortMode(t, "Cannot start embedded cluster in --short tests")
	integration.BeforeTest(t)

	// No ServerFeatureGates: rely on the registry default (off while
	// under development).
	c := integration.NewCluster(t, &integration.ClusterConfig{Size: 1})
	defer c.Terminate(t)

	leaderID := waitForLeaderReady(t, c.Members[0])
	require.NotZerof(t, leaderID, "single node should be its own leader")

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	logs, err := c.Members[0].LogObserver.Expect(ctx, "server health check started", 1)
	if err == nil {
		t.Fatalf("checker must not start under the default (no flag) configuration, observed log: %v", logs)
	}
	require.ErrorIs(t, err, context.DeadlineExceeded)
}

// Explicit opt-out contract: --feature-gates=ServerHealthCheck=false
// disables the whole chain.
func TestServerHealthCheck_GateOffDoesNotStartChecker(t *testing.T) {
	testutil.SkipTestIfShortMode(t, "Cannot start embedded cluster in --short tests")
	integration.BeforeTest(t)

	c := integration.NewCluster(t, &integration.ClusterConfig{
		Size: 1,
		ServerFeatureGates: map[string]bool{
			"ServerHealthCheck": false,
		},
	})
	defer c.Terminate(t)

	leaderID := waitForLeaderReady(t, c.Members[0])
	require.NotZerof(t, leaderID, "single node should be its own leader")

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	logs, err := c.Members[0].LogObserver.Expect(ctx, "server health check started", 1)
	if err == nil {
		t.Fatalf("checker must not start when ServerHealthCheck gate is off, observed log: %v", logs)
	}
	require.ErrorIs(t, err, context.DeadlineExceeded)
}

// Gate-off metric contract. The two etcd_server_healthcheck_* gauges
// are global variables in the process-level prometheus registry,
// shared by clusters started one after another in the same process;
// gate-on healthcheck/chaos tests advance last_tick, so "absolute
// value 0" only holds with an exclusive process. The behavioral
// discriminator that holds in a shared process: after a gate-off
// cluster starts, the two gauges stay frozen — no Checker is
// advancing them. This dimension complements
// TestChaos_CheckerLivenessByLastTick (gate-on: last_tick must
// advance).
func TestServerHealthCheck_GateOffDoesNotAdvanceMetrics(t *testing.T) {
	testutil.SkipTestIfShortMode(t, "Cannot start embedded cluster in --short tests")
	integration.BeforeTest(t)

	c := integration.NewCluster(t, &integration.ClusterConfig{
		Size: 1,
		ServerFeatureGates: map[string]bool{
			"ServerHealthCheck": false,
		},
	})
	defer c.Terminate(t)

	readGauge := func(name string) float64 {
		v, err := c.Members[0].Metric(name)
		require.NoErrorf(t, err, "metric %s not exposed", name)
		f, err := strconv.ParseFloat(v, 64)
		require.NoErrorf(t, err, "unparseable %s %q", name, v)
		return f
	}

	tickBefore := readGauge("etcd_server_healthcheck_last_tick_timestamp_seconds")
	stateBefore := readGauge("etcd_server_healthcheck_state")

	// 2s = 4 default tick periods; if the opt-out were broken (gate
	// actually on), the Checker would advance last_tick every ~500ms,
	// enough for the difference to show.
	time.Sleep(2 * time.Second)

	require.InDeltaf(t, tickBefore, readGauge("etcd_server_healthcheck_last_tick_timestamp_seconds"), 0,
		"gate-off cluster must not advance etcd_server_healthcheck_last_tick_timestamp_seconds")
	require.InDeltaf(t, stateBefore, readGauge("etcd_server_healthcheck_state"), 0,
		"gate-off cluster must not touch etcd_server_healthcheck_state")
}

// data_corruption semantics: a CORRUPT alarm is replicated through
// raft into every member's alarmStore, but the check fails only on
// the member the alarm names — the named member flips the aggregate
// entry and etcd.health/data_corruption, while every other member
// keeps both entries SERVING (their read paths are intact, and the
// drain routes read traffic to them). After disarm, the named member
// recovers through the SuccessThreshold window; injecting only
// NOSPACE flips nothing on any member — NOSPACE is deliberately
// excluded from the health verdict.
func TestServerHealthCheck_DataCorruptionEvictsNamedMemberOnly(t *testing.T) {
	testutil.SkipTestIfShortMode(t, "Cannot start embedded cluster in --short tests")
	integration.BeforeTest(t)

	c := integration.NewCluster(t, &integration.ClusterConfig{
		Size:               3,
		ServerFeatureGates: healthCheckGates,
	})
	defer c.Terminate(t)

	named := c.Members[1]
	namedID := uint64(named.ID())
	// The Alarm RPC injects the alarm through the same raft path the
	// corruption checker uses.
	mt := integration.ToGRPC(c.Client(0)).Maintenance
	_, err := mt.Alarm(t.Context(), &pb.AlarmRequest{
		MemberID: namedID,
		Action:   pb.AlarmRequest_ACTIVATE,
		Alarm:    pb.AlarmType_CORRUPT,
	})
	require.NoError(t, err)

	// Named member: both entries flip within 1.5s (FailureThreshold=3
	// × 500ms).
	hcNamed := mustHealthCheckClient(t, named.GRPCURL, 2*time.Second)
	status, _, err := hcNamed.waitForHealthStatus(healthCheckService, healthpb.HealthCheckResponse_NOT_SERVING, 4*time.Second)
	require.NoErrorf(t, err, "waiting for named member aggregate NOT_SERVING")
	require.Equalf(t, healthpb.HealthCheckResponse_NOT_SERVING, status,
		"named member aggregate = %v, want NOT_SERVING", status)
	status, err = hcNamed.check(dataCorruptionHealthService, time.Second)
	require.NoErrorf(t, err, "named member data_corruption check rpc failed")
	require.Equalf(t, healthpb.HealthCheckResponse_NOT_SERVING, status,
		"named member %s = %v, want NOT_SERVING", dataCorruptionHealthService, status)

	// Every other member keeps both entries SERVING: the alarm does
	// not name them. Their read paths do not go through the applier
	// (CORRUPT only rejects raft-committed writes), so their
	// linear_read/apply_lag/apply_stall stay healthy and the
	// aggregate entry does not move.
	for i, m := range c.Members {
		if uint64(m.ID()) == namedID {
			continue
		}
		hc := mustHealthCheckClient(t, m.GRPCURL, 2*time.Second)
		aggStatus, aggErr := hc.check(healthCheckService, time.Second)
		require.NoErrorf(t, aggErr, "member %d aggregate check rpc failed", i)
		require.Equalf(t, healthpb.HealthCheckResponse_SERVING, aggStatus,
			"member %d aggregate must stay SERVING (B semantics: the alarm names another member)", i)
		dcStatus, dcErr := hc.check(dataCorruptionHealthService, time.Second)
		require.NoErrorf(t, dcErr, "member %d data_corruption check rpc failed", i)
		require.Equalf(t, healthpb.HealthCheckResponse_SERVING, dcStatus,
			"member %d %s must stay SERVING", i, dataCorruptionHealthService)
	}

	// Disarm: the named member recovers through the SuccessThreshold
	// window.
	_, err = mt.Alarm(t.Context(), &pb.AlarmRequest{
		MemberID: namedID,
		Action:   pb.AlarmRequest_DEACTIVATE,
		Alarm:    pb.AlarmType_CORRUPT,
	})
	require.NoError(t, err)
	status, _, err = hcNamed.waitForHealthStatus(healthCheckService, healthpb.HealthCheckResponse_SERVING, 4*time.Second)
	require.NoErrorf(t, err, "waiting for named member aggregate SERVING after disarm")
	require.Equalf(t, healthpb.HealthCheckResponse_SERVING, status,
		"named member aggregate = %v after disarm, want SERVING", status)

	// NOSPACE-only injection flips nothing on any member: NOSPACE
	// rejects writes but leaves reads intact, and it is set on all
	// members at once, so draining would amplify into a full outage.
	_, err = mt.Alarm(t.Context(), &pb.AlarmRequest{
		MemberID: namedID,
		Action:   pb.AlarmRequest_ACTIVATE,
		Alarm:    pb.AlarmType_NOSPACE,
	})
	require.NoError(t, err)
	time.Sleep(2 * time.Second)
	for i, m := range c.Members {
		hc := mustHealthCheckClient(t, m.GRPCURL, 2*time.Second)
		aggStatus, aggErr := hc.check(healthCheckService, time.Second)
		require.NoErrorf(t, aggErr, "member %d aggregate check rpc failed", i)
		require.Equalf(t, healthpb.HealthCheckResponse_SERVING, aggStatus,
			"member %d aggregate must stay SERVING under NOSPACE (deliberately excluded)", i)
		dcStatus, dcErr := hc.check(dataCorruptionHealthService, time.Second)
		require.NoErrorf(t, dcErr, "member %d data_corruption check rpc failed", i)
		require.Equalf(t, healthpb.HealthCheckResponse_SERVING, dcStatus,
			"member %d %s must stay SERVING under NOSPACE", i, dataCorruptionHealthService)
	}

	_, err = mt.Alarm(t.Context(), &pb.AlarmRequest{
		MemberID: namedID,
		Action:   pb.AlarmRequest_DEACTIVATE,
		Alarm:    pb.AlarmType_NOSPACE,
	})
	require.NoError(t, err)
}

// defragFanoutRecorder records DefragStarted / DefragFinished
// delivery for the server-level fan-out test below.
type defragFanoutRecorder struct {
	mu       sync.Mutex
	starts   int
	finishes int
}

func (r *defragFanoutRecorder) HealthCheckFailed(string)    {}
func (r *defragFanoutRecorder) HealthCheckRecovered(string) {}
func (r *defragFanoutRecorder) DefragStarted() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.starts++
}
func (r *defragFanoutRecorder) DefragFinished() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.finishes++
}
func (r *defragFanoutRecorder) snapshot() (starts, finishes int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.starts, r.finishes
}

// Defrag events must fan out at server level. A recording notifier
// is registered late on a running member — in a dual-listener
// deployment the second listener's notifier appears exactly this
// way, after the maintenanceServer is constructed — and then a real
// Defragment RPC triggers the defrag. When the maintenanceServer
// holds the server-level reloadingNotifier, the late-registered
// notifier still receives DefragStarted / DefragFinished; if it
// still held the listener-local notifier, the late registrant would
// never receive anything. After Defragment returns, the deferred
// defragFinished has already run, so both counters can be asserted
// race-free.
func TestServerHealthCheck_DefragFansOutToAllRegisteredNotifiers(t *testing.T) {
	testutil.SkipTestIfShortMode(t, "Cannot start embedded cluster in --short tests")
	integration.BeforeTest(t)

	c := integration.NewCluster(t, &integration.ClusterConfig{
		Size: 1,
		ServerFeatureGates: map[string]bool{
			"ServerHealthCheck":       true,
			"StopGRPCServiceOnDefrag": true,
		},
	})
	defer c.Terminate(t)

	leaderID := waitForLeaderReady(t, c.Members[0])
	require.NotZerof(t, leaderID, "single node should be its own leader")

	rec := &defragFanoutRecorder{}
	c.Members[0].Server.AddHealthNotifier(rec)

	_, err := c.Members[0].Client.Defragment(context.Background(), c.Members[0].GRPCURL)
	require.NoError(t, err)

	starts, finishes := rec.snapshot()
	require.Equalf(t, 1, starts,
		"late-registered notifier must receive DefragStarted exactly once (server-level fan-out)")
	require.Equalf(t, 1, finishes,
		"late-registered notifier must receive DefragFinished exactly once")

	// This listener's own health status must return to SERVING once
	// the defrag finishes.
	hc := mustHealthCheckClient(t, c.Members[0].GRPCURL, 2*time.Second)
	status, err := hc.check(healthCheckService, time.Second)
	require.NoErrorf(t, err, "aggregate check rpc failed after defrag")
	require.Equalf(t, healthpb.HealthCheckResponse_SERVING, status,
		"aggregate must return to SERVING after defrag finished, got %v", status)
}
