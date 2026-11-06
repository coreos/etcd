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

// TestE2E_ServerHealthCheck_ApplyLagNotServing is the end-to-end
// counterpart of the chaos write-burst scenario and the unit family
// TestApplyLagCheckWith_*: it drives the server-side health
// self-check to report apply_lag on a real cluster by making apply
// SLOW BUT CONTINUOUS — the beforeApplyOneEntryNormal failpoint
// sleeps 100ms per entry while 30 concurrent Puts land — so
// committed advances well ahead of applied for several seconds
// without ever freezing.
//
// The "slow but progressing" shape is load-bearing for what the test
// pins:
//   - etcd.health/apply_lag must flip NOT_SERVING: the gap stays
//     above the threshold (1) through the FailureThreshold debounce.
//     A single Put cannot do this — the check requires gap >
//     threshold, so gap=1 never fires.
//   - etcd.health/apply_stall must STAY SERVING: applied advances
//     every 100ms, so the frozen-applied discriminator never fires.
//     A full 2s apply stall (the original design) deterministically
//     trips apply_stall, which drives the aggregate and contradicts
//     the aggregate assertion below.
//   - the aggregate entry must STAY SERVING: apply_lag is
//     cluster-correlated (under sustained write pressure several
//     members breach together), so it drives only its per-check
//     entry and never evicts the member from client-side endpoint
//     selection. See aggregateEligible in
//     server/etcdserver/api/v3rpc/health.go.
//
// After the burst drains, the gap closes and the asymmetric debounce
// returns the entry to SERVING, closing the recovery path on a real
// cluster.

package e2e

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"

	clientv3 "go.etcd.io/etcd/client/v3"
	"go.etcd.io/etcd/tests/v3/framework/e2e"
)

const (
	// applyLagEntryDelay is the per-entry sleep the
	// beforeApplyOneEntryNormal failpoint injects on the target
	// member's apply loop. 100ms keeps apply progressing (an entry
	// lands every 100ms) while accumulating a multi-second backlog
	// from the write burst.
	applyLagEntryDelay = 100 * time.Millisecond

	// applyLagBurstWrites is the number of concurrent Puts in the
	// burst. At 100ms per entry the target needs ~3s to drain them,
	// which sustains gap > threshold far past the FailureThreshold
	// (3) × Interval (500ms) debounce.
	applyLagBurstWrites = 30

	// applyLagTestCheckTimeout is the per-poll deadline for the
	// gRPC health.v1 Check RPC.
	applyLagTestCheckTimeout = 1 * time.Second

	// applyLagObserveTimeout bounds the wait for the NOT_SERVING
	// transition (~1.5s debounce inside a ~3s backlog window) and
	// the subsequent recovery, with headroom for slow CI.
	applyLagObserveTimeout = 15 * time.Second

	// aggregateHealthService is the grpc.health.v1 wildcard entry
	// that clients use for endpoint selection; applyLagHealthService
	// and applyStallHealthService are the per-check entries the
	// burst drives (and must not drive, respectively).
	aggregateHealthService  = ""
	applyLagHealthService   = "etcd.health/apply_lag"
	applyStallHealthService = "etcd.health/apply_stall"
)

// healthCheckV1Client is a minimal gRPC health.v1 client used
// only by this test. The integration package has a richer
// variant; the e2e package keeps its own copy so the test file
// stays self-contained and avoids an import cycle.
type healthCheckV1Client struct {
	conn   *grpc.ClientConn
	client healthpb.HealthClient
}

func newHealthCheckV1Client(t *testing.T, clientURL string) *healthCheckV1Client {
	t.Helper()
	// ClientURL carries a scheme ("http://host:port") that a raw TCP
	// dial cannot use; dial the parsed host:port instead.
	u, err := url.Parse(clientURL)
	require.NoErrorf(t, err, "parsing client URL %q", clientURL)
	endpoint := u.Host
	target := "passthrough:///" + endpoint
	conn, err := grpc.NewClient(
		target,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "tcp", endpoint)
		}),
	)
	require.NoError(t, err)
	connectCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	conn.Connect()
	if !conn.WaitForStateChange(connectCtx, connectivity.Idle) {
		_ = conn.Close()
		t.Fatalf("grpc health client could not reach %s within 2s", endpoint)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return &healthCheckV1Client{conn: conn, client: healthpb.NewHealthClient(conn)}
}

func (h *healthCheckV1Client) check(t *testing.T, service string) healthpb.HealthCheckResponse_ServingStatus {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), applyLagTestCheckTimeout)
	defer cancel()
	resp, err := h.client.Check(ctx, &healthpb.HealthCheckRequest{Service: service})
	require.NoError(t, err)
	return resp.GetStatus()
}

// waitForHealthStatus polls until service's status becomes target or
// the timeout elapses, returning the last observed status and the
// elapsed time. Polling cadence matches the Checker tick
// interval so we never miss a transition window.
func (h *healthCheckV1Client) waitForHealthStatus(
	t *testing.T,
	service string,
	target healthpb.HealthCheckResponse_ServingStatus,
	timeout time.Duration,
) (healthpb.HealthCheckResponse_ServingStatus, time.Duration) {
	t.Helper()
	start := time.Now()
	deadline := start.Add(timeout)
	var last healthpb.HealthCheckResponse_ServingStatus
	for time.Now().Before(deadline) {
		last = h.check(t, service)
		if last == target {
			return last, time.Since(start)
		}
		time.Sleep(250 * time.Millisecond)
	}
	return last, time.Since(start)
}

func TestE2E_ServerHealthCheck_ApplyLagNotServing(t *testing.T) {
	e2e.SkipInShortMode(t)
	e2e.BeforeTest(t)

	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()

	// Cluster topology: 3 voting members so quorum stays healthy
	// while one member's apply loop is slowed by the failpoint. The
	// apply-lag threshold is 1: the check fires on gap > 1, so the
	// write burst must accumulate a backlog of at least 2 entries
	// (it accumulates ~25).
	clus, err := e2e.NewEtcdProcessCluster(ctx, t,
		e2e.WithClusterSize(3),
		e2e.WithGoFailEnabled(true),
		e2e.WithServerFeatureGate("ServerHealthCheck", true),
		e2e.WithHealthCheckApplyLagThreshold(1),
	)
	require.NoError(t, err)
	defer func() { require.NoError(t, clus.Close()) }()

	// Pick any serving member as the target — apply_lag is local to
	// the member whose apply loop trails its committed index, so
	// leadership is irrelevant. The Checker only starts after
	// ReadyNotify fires, and the wildcard entry reads SERVING only
	// after that.
	var targetIdx int
	require.Eventuallyf(t, func() bool {
		for i, p := range clus.Procs {
			hc := newHealthCheckV1Client(t, p.Config().ClientURL)
			if hc.check(t, aggregateHealthService) == healthpb.HealthCheckResponse_SERVING {
				targetIdx = i
				return true
			}
		}
		return false
	}, 10*time.Second, 200*time.Millisecond, "expected a serving member within 10s")

	targetProc := clus.Procs[targetIdx]
	writerProc := clus.Procs[(targetIdx+1)%len(clus.Procs)]
	hc := newHealthCheckV1Client(t, targetProc.Config().ClientURL)

	// Sanity: target reports SERVING before the failpoint.
	require.Equalf(t, healthpb.HealthCheckResponse_SERVING, hc.check(t, applyLagHealthService),
		"target should be SERVING before failpoint, got %v", hc.check(t, applyLagHealthService))

	// Slow the target's apply loop to 100ms per entry, then land a
	// burst of concurrent writes through another member. The burst
	// must go through an in-process clientv3 client: 30 etcdctl
	// process spawns trickle proposals in slower than the slowed
	// apply drains them, so no backlog ever forms and the gap never
	// crosses the threshold. A single client issues all 30 Puts
	// within milliseconds, they commit together, and the target's
	// raft hands the whole batch to its apply loop at once: committed
	// advances ~30 entries ahead of applied for ~3s without ever
	// freezing.
	require.NoError(t, targetProc.Failpoints().SetupHTTP(ctx,
		"beforeApplyOneEntryNormal",
		fmt.Sprintf(`sleep("%s")`, applyLagEntryDelay)))

	writer, err := clientv3.New(clientv3.Config{
		Endpoints: writerProc.EndpointsGRPC(),
		Logger:    zap.NewNop(),
	})
	require.NoError(t, err)
	defer func() { require.NoError(t, writer.Close()) }()

	var wg sync.WaitGroup
	errCh := make(chan error, applyLagBurstWrites)
	for i := 0; i < applyLagBurstWrites; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			putCtx, putCancel := context.WithTimeout(ctx, 20*time.Second)
			defer putCancel()
			if _, err := writer.Put(putCtx, fmt.Sprintf("apply-lag-key-%d", i), "v"); err != nil {
				errCh <- err
			}
		}(i)
	}
	wg.Wait()
	select {
	case err := <-errCh:
		t.Fatalf("burst Put failed: %v", err)
	default:
	}

	// The target's apply_lag entry must flip NOT_SERVING inside the
	// backlog window. apply_stall must stay SERVING the whole time —
	// applied advances every 100ms, so a flip here would mean the
	// frozen-applied discriminator misclassifies slow-but-progressing
	// apply. The aggregate must stay SERVING: apply_lag never evicts.
	status, elapsed := hc.waitForHealthStatus(t, applyLagHealthService, healthpb.HealthCheckResponse_NOT_SERVING, applyLagObserveTimeout)
	require.Equalf(t, healthpb.HealthCheckResponse_NOT_SERVING, status,
		"expected %s NOT_SERVING within %v, got %v after %v",
		applyLagHealthService, applyLagObserveTimeout, status, elapsed)
	require.Equalf(t, healthpb.HealthCheckResponse_SERVING, hc.check(t, applyStallHealthService),
		"slow-but-progressing apply must not trip apply_stall, got %v",
		hc.check(t, applyStallHealthService))
	require.Equalf(t, healthpb.HealthCheckResponse_SERVING, hc.check(t, aggregateHealthService),
		"aggregate entry must stay SERVING while only apply_lag is failing, got %v",
		hc.check(t, aggregateHealthService))

	// The backlog drains at 100ms per remaining entry; with no new
	// writes the gap closes and the SuccessThreshold debounce
	// returns the entry to SERVING. Deactivate the failpoint so
	// residual slowness cannot stretch the recovery window.
	require.NoError(t, targetProc.Failpoints().DeactivateHTTP(ctx, "beforeApplyOneEntryNormal"))

	status, elapsed = hc.waitForHealthStatus(t, applyLagHealthService, healthpb.HealthCheckResponse_SERVING, applyLagObserveTimeout)
	require.Equalf(t, healthpb.HealthCheckResponse_SERVING, status,
		"expected %s to recover to SERVING within %v after the backlog drained, got %v after %v",
		applyLagHealthService, applyLagObserveTimeout, status, elapsed)
}
