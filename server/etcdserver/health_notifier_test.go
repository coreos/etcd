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

package etcdserver

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"

	"go.etcd.io/etcd/server/v3/config"
	"go.etcd.io/etcd/server/v3/etcdserver/health"
)

// recordingHealthNotifier captures every HealthCheckFailed /
// HealthCheckRecovered / DefragStarted / DefragFinished invocation so
// tests can assert that the MultiNotifier fan-out reaches every
// listener-local notifier.
type recordingHealthNotifier struct {
	mu             sync.Mutex
	failed         []string
	recover        []string
	defragStarts   int
	defragFinishes int
}

func (r *recordingHealthNotifier) HealthCheckFailed(reason string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.failed = append(r.failed, reason)
}

func (r *recordingHealthNotifier) HealthCheckRecovered(reason string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.recover = append(r.recover, reason)
}

func (r *recordingHealthNotifier) DefragStarted() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.defragStarts++
}

func (r *recordingHealthNotifier) DefragFinished() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.defragFinishes++
}

func (r *recordingHealthNotifier) snapshot() (failed, recover []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	failed = append(failed, r.failed...)
	recover = append(recover, r.recover...)
	return failed, recover
}

// defragSnapshot returns the number of DefragStarted / DefragFinished
// invocations recorded so far.
func (r *recordingHealthNotifier) defragSnapshot() (starts, finishes int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.defragStarts, r.defragFinishes
}

// TestMultiNotifier_FanoutToAllNotifiers reproduces the
// dual-listener wiring: embed/serve.go registers a notifier for
// the insecure listener and another for the secure listener.
// The server-side Checker goroutine wraps the two in a
// MultiNotifier; a single HealthCheckFailed("apply_lag") call
// must reach both, so a failing member is observable on every
// gRPC health.v1 endpoint, not just the one that called
// SetHealthNotifier last.
func TestMultiNotifier_FanoutToAllNotifiers(t *testing.T) {
	insecure := &recordingHealthNotifier{}
	secure := &recordingHealthNotifier{}
	multi := NewMultiNotifier([]HealthNotifier{insecure, secure})

	multi.HealthCheckFailed("apply_lag")
	if failed, _ := insecure.snapshot(); len(failed) != 1 || failed[0] != "apply_lag" {
		t.Errorf("insecure notifier failed = %v, want [apply_lag]", failed)
	}
	if failed, _ := secure.snapshot(); len(failed) != 1 || failed[0] != "apply_lag" {
		t.Errorf("secure notifier failed = %v, want [apply_lag]", failed)
	}

	multi.HealthCheckRecovered("apply_lag")
	if _, rec := insecure.snapshot(); len(rec) != 1 || rec[0] != "apply_lag" {
		t.Errorf("insecure notifier recovered = %v, want [apply_lag]", rec)
	}
	if _, rec := secure.snapshot(); len(rec) != 1 || rec[0] != "apply_lag" {
		t.Errorf("secure notifier recovered = %v, want [apply_lag]", rec)
	}
}

// TestMultiNotifier_SingleListenerFansOut verifies the
// single-listener deployment path remains equivalent to the old
// SetHealthNotifier behavior: a single-element slice fans out
// Failed and Recovered to exactly that notifier. This pins the
// regression contract for any deployment that does not use the
// dual-listener configuration.
func TestMultiNotifier_SingleListenerFansOut(t *testing.T) {
	single := &recordingHealthNotifier{}
	multi := NewMultiNotifier([]HealthNotifier{single})

	multi.HealthCheckFailed("no_leader")
	multi.HealthCheckRecovered("no_leader")
	failed, rec := single.snapshot()
	if len(failed) != 1 || failed[0] != "no_leader" {
		t.Errorf("single notifier failed = %v, want [no_leader]", failed)
	}
	if len(rec) != 1 || rec[0] != "no_leader" {
		t.Errorf("single notifier recovered = %v, want [no_leader]", rec)
	}
}

// TestMultiNotifier_EmptySliceIsNoOp covers the defensive
// guard: an empty slice must not panic and must not produce any
// fan-out. The checker goroutine keeps polling until a notifier
// shows up (waitForHealthNotifier); this test pins the
// MultiNotifier side of that contract.
func TestMultiNotifier_EmptySliceIsNoOp(t *testing.T) {
	multi := NewMultiNotifier(nil)
	// Must not panic.
	multi.HealthCheckFailed("apply_lag")
	multi.HealthCheckRecovered("apply_lag")
}

// TestGetHealthNotifier_ReloadingSeesLateRegistration pins the
// reloading contract of GetHealthNotifier: the notifier handed to
// the health checker must re-read the registration set on every
// call instead of holding a snapshot taken at wiring
// time. In a dual-listener deployment the two v3rpc.Server
// constructions race the checker's first poll (the secure
// listener also loads TLS material before registering), so a
// snapshot would permanently miss the second listener's notifier:
// its gRPC health.v1 server would stay stuck at the
// construction-time SERVING state while the member is actually
// failing checks.
func TestGetHealthNotifier_ReloadingSeesLateRegistration(t *testing.T) {
	// Zero value is sufficient: AddHealthNotifier and
	// GetHealthNotifier only touch the healthNotifiers field.
	s := &EtcdServer{}

	// Before any registration the checker must see nil ("not ready
	// yet, keep polling").
	require.Nilf(t, s.GetHealthNotifier(), "no notifier registered yet must yield nil")

	// Insecure listener registers first; the checker picks up the
	// notifier immediately, before the secure listener is up.
	insecure := &recordingHealthNotifier{}
	s.AddHealthNotifier(insecure)
	notif := s.GetHealthNotifier()
	require.NotNil(t, notif)

	// The secure listener registers late — after the checker has
	// already obtained its notifier. A snapshot-based
	// GetHealthNotifier would have baked in the one-element
	// registration set above and never see this one.
	secure := &recordingHealthNotifier{}
	s.AddHealthNotifier(secure)

	notif.HealthCheckFailed("apply_lag")
	if failed, _ := insecure.snapshot(); len(failed) != 1 || failed[0] != "apply_lag" {
		t.Errorf("insecure notifier failed = %v, want [apply_lag]", failed)
	}
	if failed, _ := secure.snapshot(); len(failed) != 1 || failed[0] != "apply_lag" {
		t.Errorf("late-registered secure notifier failed = %v, want [apply_lag]", failed)
	}

	notif.HealthCheckRecovered("apply_lag")
	if _, rec := insecure.snapshot(); len(rec) != 1 || rec[0] != "apply_lag" {
		t.Errorf("insecure notifier recovered = %v, want [apply_lag]", rec)
	}
	if _, rec := secure.snapshot(); len(rec) != 1 || rec[0] != "apply_lag" {
		t.Errorf("late-registered secure notifier recovered = %v, want [apply_lag]", rec)
	}
}

// TestMultiNotifier_DefragFansOutToAllNotifiers pins the defrag half
// of the server-level fan-out: a Defragment RPC
// must flip every registered listener's health.v1 state, not only
// the listener the RPC arrived on. Dual-listener deployments register
// one notifier per listener, so the MultiNotifier the maintenance
// server drives must deliver the defrag events to all of them.
func TestMultiNotifier_DefragFansOutToAllNotifiers(t *testing.T) {
	insecure := &recordingHealthNotifier{}
	secure := &recordingHealthNotifier{}
	multi := NewMultiNotifier([]HealthNotifier{insecure, secure})

	multi.DefragStarted()
	if starts, _ := insecure.defragSnapshot(); starts != 1 {
		t.Errorf("insecure notifier DefragStarted calls = %d, want 1", starts)
	}
	if starts, _ := secure.defragSnapshot(); starts != 1 {
		t.Errorf("secure notifier DefragStarted calls = %d, want 1", starts)
	}

	multi.DefragFinished()
	if _, finishes := insecure.defragSnapshot(); finishes != 1 {
		t.Errorf("insecure notifier DefragFinished calls = %d, want 1", finishes)
	}
	if _, finishes := secure.defragSnapshot(); finishes != 1 {
		t.Errorf("secure notifier DefragFinished calls = %d, want 1", finishes)
	}
}

// TestGetHealthNotifier_ReloadingDefragReachesLateRegistration: the
// maintenance server holds the reloadingNotifier returned by
// GetHealthNotifier, so a notifier registered after that construction
// — the exact race the reloading design exists for — must also
// receive defrag events, not only health-check events.
func TestGetHealthNotifier_ReloadingDefragReachesLateRegistration(t *testing.T) {
	// Zero value is sufficient: AddHealthNotifier and
	// GetHealthNotifier only touch the healthNotifiers field.
	s := &EtcdServer{}

	insecure := &recordingHealthNotifier{}
	s.AddHealthNotifier(insecure)
	notif := s.GetHealthNotifier()
	require.NotNil(t, notif)

	secure := &recordingHealthNotifier{}
	s.AddHealthNotifier(secure)

	notif.DefragStarted()
	if starts, _ := insecure.defragSnapshot(); starts != 1 {
		t.Errorf("insecure notifier DefragStarted = %d, want 1", starts)
	}
	if starts, _ := secure.defragSnapshot(); starts != 1 {
		t.Errorf("late-registered notifier DefragStarted = %d, want 1", starts)
	}

	notif.DefragFinished()
	if _, finishes := insecure.defragSnapshot(); finishes != 1 {
		t.Errorf("insecure notifier DefragFinished = %d, want 1", finishes)
	}
	if _, finishes := secure.defragSnapshot(); finishes != 1 {
		t.Errorf("late-registered notifier DefragFinished = %d, want 1", finishes)
	}
}

// healthcheckExitedValue reads the current value of
// etcd_server_healthcheck_exited_total{reason="notifier_unwired"} from
// the default registry. The counter lives in the health package
// unexported, so tests in this package can only observe it through the
// registry.
func healthcheckExitedValue(t *testing.T) float64 {
	t.Helper()
	mfs, err := prometheus.DefaultGatherer.Gather()
	require.NoError(t, err)
	for _, mf := range mfs {
		if mf.GetName() != "etcd_server_healthcheck_exited_total" {
			continue
		}
		for _, m := range mf.GetMetric() {
			for _, lbl := range m.GetLabel() {
				if lbl.GetName() == "reason" && lbl.GetValue() == health.ExitNotifierUnwired {
					return m.GetCounter().GetValue()
				}
			}
		}
	}
	return 0
}

// TestWaitForHealthNotifierRetriesPastTimeout pins the persistent-retry
// contract: when the v3rpc.Server wires the notifier later than the
// timeout, the wait must NOT give up. A permanent exit would leave the
// gRPC health.v1 server stuck at its construction-time SERVING state
// (v3rpc/health.go), reporting healthy on a server whose checks never
// ran. The timeout instead fires a one-time Error log (no flooding) and
// the one-time healthcheck_exited_total{reason="notifier_unwired"}
// anomaly counter, and the poll loop keeps going until the notifier
// arrives.
func TestWaitForHealthNotifierRetriesPastTimeout(t *testing.T) {
	core, logs := observer.New(zapcore.ErrorLevel)
	lg := zap.New(core)

	stop := make(chan struct{})
	t.Cleanup(func() { close(stop) })

	before := int(healthcheckExitedValue(t))

	var delivered atomic.Bool
	get := func() HealthNotifier {
		if delivered.Load() {
			return NewMultiNotifier(nil)
		}
		return nil
	}

	// Deliver only after the retry Error has been observed, so the
	// one-time-log assertion cannot race with notifier arrival. The 5s
	// deadline is a safety valve: if the log never fires, delivery
	// happens anyway and the log assertion below fails instead of the
	// test hanging.
	go func() {
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if logs.FilterMessage("health check: notifier not injected, retrying").Len() > 0 {
				break
			}
			time.Sleep(2 * time.Millisecond)
		}
		delivered.Store(true)
	}()

	notif := waitForHealthNotifier(lg, get, stop, 25*time.Millisecond, 5*time.Millisecond)
	require.NotNilf(t, notif, "a late notifier must still be picked up")

	errs := logs.FilterMessage("health check: notifier not injected, retrying")
	require.Equalf(t, 1, errs.Len(),
		"the timeout must log exactly one Error: one-time, never flooding")
	require.Equal(t, 25*time.Millisecond, errs.All()[0].ContextMap()["timeout"])

	require.Equalf(t, before+1, int(healthcheckExitedValue(t)),
		"the unwired anomaly must bump healthcheck_exited_total exactly once")
}

// TestWaitForHealthNotifierImmediateDelivery pins the healthy path: a
// notifier that is already wired returns without any Error log and
// without touching the anomaly counter.
func TestWaitForHealthNotifierImmediateDelivery(t *testing.T) {
	core, logs := observer.New(zapcore.ErrorLevel)
	lg := zap.New(core)

	stop := make(chan struct{})
	t.Cleanup(func() { close(stop) })

	before := int(healthcheckExitedValue(t))

	notif := NewMultiNotifier(nil)
	got := waitForHealthNotifier(lg, func() HealthNotifier { return notif }, stop, 25*time.Millisecond, 5*time.Millisecond)
	require.Same(t, notif, got)
	require.Zerof(t, logs.Len(), "the healthy path must not log anything")
	require.Equal(t, before, int(healthcheckExitedValue(t)))
}

// TestWaitForHealthNotifierStops pins the shutdown path: a server that
// stops before the notifier arrives must return nil promptly, not keep
// polling forever.
func TestWaitForHealthNotifierStops(t *testing.T) {
	stop := make(chan struct{})
	close(stop)

	got := waitForHealthNotifier(zap.NewNop(), func() HealthNotifier { return nil }, stop, time.Hour, 5*time.Millisecond)
	require.Nil(t, got)
}

// TestHealthCheckConfigTimeoutFollowsReqTimeout pins the one mapping in
// healthCheckConfig that deliberately departs from the health package
// defaults: with --experimental-health-check-timeout unset the checker
// must judge a linear read by the same deadline a client read gets
// (ReqTimeout = 5s + 2 election timeouts), not by the package's 2s
// fallback. Otherwise a slow-but-healthy node is flipped to NOT_SERVING
// while reads against it are still succeeding, and the false positive
// gets worse the longer the election timeout is. An explicit flag still
// wins, so an operator can tighten the budget on purpose.
func TestHealthCheckConfigTimeoutFollowsReqTimeout(t *testing.T) {
	fast := &config.ServerConfig{ElectionTicks: 10, TickMs: 100}
	slow := &config.ServerConfig{ElectionTicks: 50, TickMs: 250}

	for _, cfg := range []*config.ServerConfig{fast, slow} {
		got := healthCheckConfig(cfg)
		require.Equalf(t, cfg.ReqTimeout(), got.CheckTimeout,
			"unset --experimental-health-check-timeout must derive from ReqTimeout")
		require.Equal(t, health.DefaultConfig().Interval, got.Interval)
		require.Equal(t, health.DefaultConfig().ApplyLagThreshold, got.ApplyLagThreshold)
		require.Equal(t, health.DefaultConfig().FailureThreshold, got.FailureThreshold)
		require.Equal(t, health.DefaultConfig().SuccessThreshold, got.SuccessThreshold)
	}
	require.Equal(t, 7*time.Second, healthCheckConfig(fast).CheckTimeout)
	require.Equal(t, 30*time.Second, healthCheckConfig(slow).CheckTimeout)

	pinned := *fast
	pinned.HealthCheckTimeout = 250 * time.Millisecond
	require.Equal(t, 250*time.Millisecond, healthCheckConfig(&pinned).CheckTimeout)

	full := *fast
	full.HealthCheckInterval = time.Second
	full.HealthCheckApplyLagThreshold = 42
	full.HealthCheckFailureThreshold = 5
	full.HealthCheckSuccessThreshold = 4
	got := healthCheckConfig(&full)
	require.Equal(t, time.Second, got.Interval)
	require.Equal(t, uint64(42), got.ApplyLagThreshold)
	require.Equal(t, 5, got.FailureThreshold)
	require.Equal(t, 4, got.SuccessThreshold)
}

// TestHealthCheckConfigWarnsBelowApplyLagFloor pins the startup
// guard: --experimental-health-check-apply-lag
// below the package floor logs exactly one Warn carrying the actual
// and the floor value, while the floor itself and an unset flag stay
// silent. apply_lag no longer drives the aggregate entry
// (clusterWideReasons), so a low threshold cannot evict anyone; the
// Warn exists because the etcd.health/apply_lag observation entry
// still flips on members that are backlogged but still applying —
// and, below maxPriorityGap, still accepting priority requests.
func TestHealthCheckConfigWarnsBelowApplyLagFloor(t *testing.T) {
	floor := health.DefaultConfig().ApplyLagThreshold

	newCfg := func(threshold uint64, lg *zap.Logger) *config.ServerConfig {
		return &config.ServerConfig{
			Logger:                       lg,
			ElectionTicks:                10,
			TickMs:                       100,
			HealthCheckApplyLagThreshold: threshold,
		}
	}

	core, logs := observer.New(zapcore.WarnLevel)
	got := healthCheckConfig(newCfg(500, zap.New(core)))
	require.Equalf(t, uint64(500), got.ApplyLagThreshold,
		"the operator's value must be honored, not clamped")
	warns := logs.FilterMessage("health check apply-lag threshold is below the recommended floor; the etcd.health/apply_lag observation entry will report failures while the member still accepts some request classes")
	require.Equalf(t, 1, warns.Len(),
		"below-floor threshold must warn exactly once, got %d", warns.Len())
	require.Equal(t, uint64(500), warns.All()[0].ContextMap()["actual"])
	require.Equal(t, floor, warns.All()[0].ContextMap()["floor"])

	coreOK, logsOK := observer.New(zapcore.WarnLevel)
	_ = healthCheckConfig(newCfg(floor, zap.New(coreOK)))
	_ = healthCheckConfig(newCfg(0, zap.New(coreOK)))
	require.Zerof(t, logsOK.Len(),
		"the floor itself and an unset flag must not warn")
}
