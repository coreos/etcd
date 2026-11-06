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

package health

import (
	"context"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	io_prometheus_client "github.com/prometheus/client_model/go"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"

	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
)

// TestChecker_RunExitsOnContextCancel is the liveness contract for
// the goroutine. Start Run, cancel the context, wait for the
// goroutine to return. If Run does not honor ctx, this test deadlocks
// and the test runner reports it as a hang.
func TestChecker_RunExitsOnContextCancel(t *testing.T) {
	s := newFakeServer()
	cfg := Config{Interval: 50 * time.Millisecond, CheckTimeout: time.Second, ApplyLagThreshold: 10000, FailureThreshold: 3, SuccessThreshold: 2}
	c := NewChecker(s, zap.NewNop(), cfg, &recordingNotifier{})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		c.Run(ctx)
		close(done)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not exit within 2s of context cancel")
	}
}

// TestChecker_RunReturnsWhenProbeWedgedPastBound pins the shutdown
// bound of B1: a probe stuck on a lock section that does not honor
// its context (store.Read acquires s.mu.RLock; the test wedges the
// linear-read one, the bound applies to every async probe) must not
// hang Run — and through s.wg.Wait(), Stop and HardStop — forever.
// Run must return within the CheckTimeout+Interval bound of the
// cancel and fire healthcheck_exited_total{reason="probe_leak"} so
// operators can alert on the abandoned probe.
func TestChecker_RunReturnsWhenProbeWedgedPastBound(t *testing.T) {
	block := make(chan struct{})
	defer close(block) // release the leaked probe so the test binary exits cleanly

	s := newFakeServer()
	s.lead = 1
	s.rangeFn = func(ctx context.Context, req *pb.RangeRequest) (*pb.RangeResponse, error) {
		<-block // ignores ctx: mimics a probe wedged on a lock section
		return &pb.RangeResponse{}, nil
	}
	cfg := Config{Interval: 5 * time.Millisecond, CheckTimeout: 10 * time.Millisecond, ApplyLagThreshold: 10000, FailureThreshold: 3, SuccessThreshold: 2}
	baseLeak := readCounterValue(t, healthCheckExited.WithLabelValues("probe_leak"))
	c := NewChecker(s, zap.NewNop(), cfg, &recordingNotifier{})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { c.Run(ctx); close(done) }()

	time.Sleep(40 * time.Millisecond) // let at least one probe spawn
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run did not return within 1s of cancel despite the probe being wedged past the bound")
	}

	got := readCounterValue(t, healthCheckExited.WithLabelValues("probe_leak"))
	if got-baseLeak != 1 {
		t.Errorf("healthcheck_exited_total{reason=probe_leak} delta = %d, want 1", got-baseLeak)
	}
}

// TestChecker_TickExercisesApplyLagAndNoLeader verifies the happy
// path: a few ticks with a healthy fake server must NOT trip the
// failure threshold. We assert via the notifier that
// HealthCheckFailed is never called.
func TestChecker_TickExercisesApplyLagAndNoLeader(t *testing.T) {
	notif := &recordingNotifier{}
	s := newFakeServer()
	s.lead = 1
	cfg := Config{Interval: 10 * time.Millisecond, CheckTimeout: time.Second, ApplyLagThreshold: 10000, FailureThreshold: 3, SuccessThreshold: 2}
	c := NewChecker(s, zap.NewNop(), cfg, notif)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { c.Run(ctx); close(done) }()

	// Allow several ticks to fire.
	time.Sleep(60 * time.Millisecond)
	cancel()
	<-done

	failed, _ := notif.snapshot()
	if len(failed) != 0 {
		t.Fatalf("healthy server must not generate failure notifications, got %v", failed)
	}
}

// TestChecker_LearnerShortCircuitsToNotServing verifies the learner
// fast path. With IsLearner true the checker must call
// HealthCheckFailed("learner") on every tick.
func TestChecker_LearnerShortCircuitsToNotServing(t *testing.T) {
	notif := &recordingNotifier{}
	s := newFakeServer()
	s.isLearner = true
	cfg := Config{Interval: 5 * time.Millisecond, CheckTimeout: time.Second, ApplyLagThreshold: 10000, FailureThreshold: 3, SuccessThreshold: 2}
	c := NewChecker(s, zap.NewNop(), cfg, notif)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { c.Run(ctx); close(done) }()

	time.Sleep(40 * time.Millisecond)
	cancel()
	<-done

	failed, _ := notif.snapshot()
	if len(failed) == 0 {
		t.Fatal("expected at least one HealthCheckFailed(\"learner\") call on a learner")
	}
	for i, r := range failed {
		if r != "learner" {
			t.Errorf("call %d reason = %q, want \"learner\"", i, r)
		}
	}
}

// TestChecker_FlappingSuppressedByDebounce reproduces a noisy
// environment: the apply_lag check fails on every tick while
// no_leader stays healthy. With FailureThreshold=3 the debounce
// must fire NOT_SERVING once the 3rd consecutive failure is
// observed, and the notifier must never be told about no_leader.
func TestChecker_FlappingSuppressedByDebounce(t *testing.T) {
	notif := &recordingNotifier{}
	s := newFakeServer()
	s.lead = 1                                 // ensure no_leader passes
	s.committed = defaultApplyLagThreshold + 1 // apply_lag always fails
	cfg := Config{Interval: 5 * time.Millisecond, CheckTimeout: time.Second, ApplyLagThreshold: 10000, FailureThreshold: 3, SuccessThreshold: 2}
	c := NewChecker(s, zap.NewNop(), cfg, notif)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { c.Run(ctx); close(done) }()

	time.Sleep(60 * time.Millisecond)
	cancel()
	<-done

	failed, _ := notif.snapshot()
	if len(failed) < 1 {
		t.Fatalf("expected at least one failure call, got %d", len(failed))
	}
	for i, r := range failed {
		if r != "apply_lag" {
			t.Errorf("call %d reason = %q, want \"apply_lag\"", i, r)
		}
	}
}

// TestChecker_SelfPauseGuardSuppressesStalledTick pins the
// self-pause guard as behavior, not as a formula: when the tick
// loop is starved for several intervals (here by a synchronous
// check blocking on a busy lock), the first tick after the
// starvation measures an elapsed time far past 3*Interval and must
// be skipped — logged and counted in
// healthcheck_suppressed_total{self_pause,stall} — instead of
// evaluating state from a stale snapshot.
func TestChecker_SelfPauseGuardSuppressesStalledTick(t *testing.T) {
	notif := &recordingNotifier{}
	s := newFakeServer()
	s.lead = 1
	gate := make(chan struct{})
	s.indexGate = gate
	cfg := Config{Interval: 10 * time.Millisecond, CheckTimeout: time.Second, ApplyLagThreshold: 10000, FailureThreshold: 3, SuccessThreshold: 2}
	baseSuppressed := readCounterValue(t, healthCheckSuppressed.WithLabelValues("self_pause", "stall"))
	c := NewChecker(s, zap.NewNop(), cfg, notif)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { c.Run(ctx); close(done) }()

	// Hold the first tick for ~9 intervals (the tick blocks inside
	// CommittedIndex), then release. The catch-up tick processes a
	// buffered ticker value from early in the starvation window, so
	// the tick after it measures a large elapsed time and trips the
	// guard.
	time.Sleep(100 * time.Millisecond)
	close(gate)
	time.Sleep(60 * time.Millisecond)
	cancel()
	<-done

	got := readCounterValue(t, healthCheckSuppressed.WithLabelValues("self_pause", "stall"))
	if got-baseSuppressed < 1 {
		t.Fatalf("a tick starved past 3*Interval must be suppressed and counted, delta = %d", got-baseSuppressed)
	}
	failed, _ := notif.snapshot()
	if len(failed) != 0 {
		t.Fatalf("healthy server must not generate failure notifications, got %v", failed)
	}
}

// TestChecker_RunCheckAppliesCheckTimeout pins the per-call timeout
// execution path: runCheck must cap a check that does not return on
// its own at cfg.CheckTimeout and score the resulting context error
// as a failure. The fallback timer inside the check is what makes
// the mutation observable without hanging the test: if runCheck ever
// stops applying the timeout, the check returns nil after 500ms and
// the failure never reaches the notifier.
func TestChecker_RunCheckAppliesCheckTimeout(t *testing.T) {
	notif := &recordingNotifier{}
	cfg := Config{Interval: time.Minute, CheckTimeout: 20 * time.Millisecond, ApplyLagThreshold: 10000, FailureThreshold: 1, SuccessThreshold: 1}
	c := NewChecker(newFakeServer(), zap.NewNop(), cfg, notif)

	blockWithFallback := func(ctx context.Context, s EtcdServer) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
			// Only reachable when runCheck failed to apply the timeout.
			return nil
		}
	}
	start := time.Now()
	c.runCheck(context.Background(), "test_timeout", blockWithFallback)
	if elapsed := time.Since(start); elapsed > 450*time.Millisecond {
		t.Fatalf("runCheck must cap the check at CheckTimeout (20ms), took %v", elapsed)
	}
	failed, _ := notif.snapshot()
	if !slices.Contains(failed, "test_timeout") {
		t.Fatalf("a check hitting CheckTimeout must be scored as a failure, got %v", failed)
	}
}

// TestChecker_RunCheckSkipsEvaluationOnParentCancel pins the
// shutdown-path guard: when the parent context is already
// cancelled, a check result must not be scored. Forwarding it would
// record a spurious NOT_SERVING (or SERVING) transition exactly
// while the server is tearing down.
func TestChecker_RunCheckSkipsEvaluationOnParentCancel(t *testing.T) {
	notif := &recordingNotifier{}
	cfg := Config{Interval: time.Minute, CheckTimeout: time.Second, ApplyLagThreshold: 10000, FailureThreshold: 1, SuccessThreshold: 1}
	c := NewChecker(newFakeServer(), zap.NewNop(), cfg, notif)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	c.runCheck(ctx, "test_ok", func(ctx context.Context, s EtcdServer) error { return nil })
	failed, recovered := notif.snapshot()
	if len(failed) != 0 || len(recovered) != 0 {
		t.Fatalf("ctx-cancelled tick must not score a success, got failed=%v recovered=%v", failed, recovered)
	}

	c.runCheck(ctx, "test_bad", func(ctx context.Context, s EtcdServer) error { return context.Canceled })
	failed, recovered = notif.snapshot()
	if len(failed) != 0 || len(recovered) != 0 {
		t.Fatalf("ctx-cancelled tick must not score a failure, got failed=%v recovered=%v", failed, recovered)
	}
}

// TestChecker_AsyncProbeCadences pins the spawn cadence of every
// asynchronous probe in one run: the read probes (linear_read,
// serializable_read) are gated to every other tick — a linear read
// is a real quorum round trip, so the gate halves the probe
// frequency — while data_corruption runs on every tick. The fake
// Range distinguishes the two read probes by the Serializable flag:
// a wiring regression that stops issuing serializable reads fails
// with zero serializable calls, and one that drops the Serializable
// flag makes every probe count as linear_read instead. The alarm
// walk is counted through alarmsFn.
//
// Tolerances are generous (half the ticks for the read probes, all
// ticks for data_corruption, both ±goroutine scheduling) — the test
// discriminates cadence wiring, not exact rates.
func TestChecker_AsyncProbeCadences(t *testing.T) {
	var linearCalls atomic.Int64
	var serializableCalls atomic.Int64
	var alarmWalks atomic.Int64
	s := newFakeServer()
	s.lead = 1
	s.rangeFn = func(ctx context.Context, req *pb.RangeRequest) (*pb.RangeResponse, error) {
		if req.Serializable {
			serializableCalls.Add(1)
		} else {
			linearCalls.Add(1)
		}
		return &pb.RangeResponse{}, nil
	}
	s.alarmsFn = func() []*pb.AlarmMember {
		alarmWalks.Add(1)
		return nil
	}
	cfg := Config{Interval: 5 * time.Millisecond, CheckTimeout: time.Second, ApplyLagThreshold: 10000, FailureThreshold: 3, SuccessThreshold: 2}
	c := NewChecker(s, zap.NewNop(), cfg, &recordingNotifier{})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { c.Run(ctx); close(done) }()

	// Run ~20 ticks worth of time: ~10 of each read probe and ~20
	// alarm walks expected.
	time.Sleep(100 * time.Millisecond)
	cancel()
	<-done
	// Give any in-flight probes a moment to finish.
	time.Sleep(20 * time.Millisecond)

	sc := serializableCalls.Load()
	if sc < 4 || sc > 20 {
		t.Errorf("serializable read Range calls = %d, expected ~10 (every other tick), out of tolerance 4-20", sc)
	}
	if sc < 1 {
		t.Fatal("serializable read Range was never invoked; the check is not wired into tick()")
	}
	lc := linearCalls.Load()
	if lc < 4 || lc > 20 {
		t.Errorf("linear read Range calls = %d, expected ~10 (every other tick), out of tolerance 4-20", lc)
	}
	if lc < 1 {
		t.Fatal("linear read Range was never invoked; the check is not wired into tick()")
	}
	aw := alarmWalks.Load()
	if aw < 8 || aw > 40 {
		t.Errorf("data_corruption alarm walks = %d, expected ~20 (every tick), out of tolerance 8-40", aw)
	}
	if aw < 1 {
		t.Fatal("data_corruption alarm walk was never invoked; the check is not wired into tick()")
	}
}

// TestChecker_RunCheckRecoversPanic ensures that a panicking
// checkFunc neither tears down the long-lived ticker goroutine nor
// loses its result. The check is exercised via runCheck (the same
// path used by the apply_lag / no_leader / linear_read checks); a
// recovery guard must (a) absorb the panic, (b) increment the
// healthcheck_exited_total{reason="panic"} counter, (c) score the
// panic as a failure on that check's own dimension so the debounce
// counters keep moving, and (d) leave a subsequent runCheck call free
// to evaluate its result normally.
func TestChecker_RunCheckRecoversPanic(t *testing.T) {
	notif := &recordingNotifier{}
	s := newFakeServer()
	cfg := Config{Interval: time.Minute, CheckTimeout: time.Second, ApplyLagThreshold: 10000, FailureThreshold: 1, SuccessThreshold: 1}
	c := NewChecker(s, zap.NewNop(), cfg, notif)

	basePanic := readCounterValue(t, healthCheckExited.WithLabelValues(ExitPanic))

	panicFn := func(ctx context.Context, s EtcdServer) error {
		panic("boom")
	}
	// runCheck is unexported, but the test lives in the same package.
	c.runCheck(context.Background(), "test_panic", panicFn)

	got := readCounterValue(t, healthCheckExited.WithLabelValues(ExitPanic))
	if got-basePanic != 1 {
		t.Errorf("healthcheck_exited_total{reason=panic} delta = %d, want 1", got-basePanic)
	}

	// The panic must be scored as a failure rather than dropped. This
	// is the freeze the conversion exists to prevent: a panic that
	// skipped Evaluate left failureCount/successCount/failedChecks at
	// their pre-panic values, so a member already NOT_SERVING could
	// never come back and one still SERVING could never be pulled,
	// whatever the underlying condition did afterwards.
	failed, _ := notif.snapshot()
	if !slices.Contains(failed, "test_panic") {
		t.Fatalf("a panicking check must reach the aggregator as a failure, got %v", failed)
	}

	// Same dimension, no longer panicking: it must recover. Only
	// reachable because the failure above was recorded against
	// "test_panic" instead of vanishing.
	goodFn := func(ctx context.Context, s EtcdServer) error { return nil }
	c.runCheck(context.Background(), "test_panic", goodFn)
	_, recovered := notif.snapshot()
	if !slices.Contains(recovered, "test_panic") {
		t.Fatalf("a check that stops panicking must recover, got %v", recovered)
	}

	// A subsequent non-panicking check on a fresh dimension must
	// still drive the aggregator; the recovered panic must not
	// short-circuit future calls.
	c.runCheck(context.Background(), "test_ok", goodFn)
	failed, _ = notif.snapshot()
	if slices.Contains(failed, "test_ok") {
		t.Fatalf("test_ok should not produce a failure notification, got %v", failed)
	}
}

// TestChecker_ApplyStallFiresThroughDebounceAndRecovers wires the
// stateful check into the real tick path and pins the full contract:
// the check name the notifier receives is "apply_stall", the
// Aggregator's FailureThreshold debounce (3 stalled ticks here)
// gates the notification, and resuming apply recovers it through the
// SuccessThreshold path. Ticks are driven synchronously (c.tick) so
// the sample sequence is deterministic instead of sleep-tolerant.
func TestChecker_ApplyStallFiresThroughDebounceAndRecovers(t *testing.T) {
	notif := &recordingNotifier{}
	s := newFakeServer()
	s.lead = 1
	s.committed, s.applied = 100, 100
	cfg := Config{Interval: time.Minute, CheckTimeout: time.Second, ApplyLagThreshold: 10000, FailureThreshold: 3, SuccessThreshold: 2}
	c := NewChecker(s, zap.NewNop(), cfg, notif)
	ctx := context.Background()

	// Tick 1: baseline sample, everything healthy.
	c.tick(ctx)

	// Three consecutive stalled windows cross the failure threshold.
	// The gap stays at 1-3 entries so apply_lag stays silent and the
	// notification is attributable to apply_stall alone.
	for i := 0; i < 3; i++ {
		s.committed++
		c.tick(ctx)
	}
	failed, _ := notif.snapshot()
	if !slices.Contains(failed, "apply_stall") {
		t.Fatalf("expected HealthCheckFailed(\"apply_stall\") after 3 stalled ticks, got %v", failed)
	}

	// Apply catches up: two healthy ticks recover the reason.
	s.applied = s.committed
	c.tick(ctx)
	c.tick(ctx)
	_, recovered := notif.snapshot()
	if !slices.Contains(recovered, "apply_stall") {
		t.Fatalf("expected HealthCheckRecovered(\"apply_stall\") after apply resumed, got %v", recovered)
	}
}

// TestChecker_ApplyStallIdleClusterDoesNotFire: with committed frozen
// alongside applied (idle cluster, or a cluster-wide pause), the
// tick loop must never produce an apply_stall failure.
func TestChecker_ApplyStallIdleClusterDoesNotFire(t *testing.T) {
	notif := &recordingNotifier{}
	s := newFakeServer()
	s.lead = 1
	s.committed, s.applied = 100, 100
	cfg := Config{Interval: time.Minute, CheckTimeout: time.Second, ApplyLagThreshold: 10000, FailureThreshold: 3, SuccessThreshold: 2}
	c := NewChecker(s, zap.NewNop(), cfg, notif)

	for i := 0; i < 8; i++ {
		c.tick(context.Background())
	}
	failed, _ := notif.snapshot()
	if slices.Contains(failed, "apply_stall") {
		t.Fatalf("idle cluster must not fire apply_stall, got %v", failed)
	}
}

// TestChecker_DataCorruptionFiresThroughDebounceAndRecovers wires the
// alarm check into the real tick path. Alarm activation and disarm
// both go through raft apply into this member's alarm store; the
// check must fail through the FailureThreshold debounce under the
// reason name "data_corruption" and recover through SuccessThreshold
// once the operator disarms the alarm. The probe runs asynchronously
// (AlarmStore.Get serializes with alarm activation across a backend
// write), so each tick is followed by waitProbeIdle to make the
// sample sequence deterministic.
func TestChecker_DataCorruptionFiresThroughDebounceAndRecovers(t *testing.T) {
	notif := &recordingNotifier{}
	s := newFakeServer()
	s.lead = 1
	s.id = 7
	cfg := Config{Interval: time.Minute, CheckTimeout: time.Second, ApplyLagThreshold: 10000, FailureThreshold: 3, SuccessThreshold: 2}
	c := NewChecker(s, zap.NewNop(), cfg, notif)
	ctx := context.Background()

	// Healthy: no alarms in the store.
	c.tick(ctx)
	waitProbeIdle(t, &c.dataCorruptionInFlight)
	failed, _ := notif.snapshot()
	if slices.Contains(failed, "data_corruption") {
		t.Fatalf("no alarms must not fire data_corruption, got %v", failed)
	}

	// A CORRUPT alarm naming this member lands via raft apply.
	s.alarms = []*pb.AlarmMember{{MemberID: 7, Alarm: pb.AlarmType_CORRUPT}}
	for i := 0; i < 3; i++ {
		c.tick(ctx)
		waitProbeIdle(t, &c.dataCorruptionInFlight)
	}
	failed, _ = notif.snapshot()
	if !slices.Contains(failed, "data_corruption") {
		t.Fatalf("expected HealthCheckFailed(\"data_corruption\") after 3 ticks with a self-naming CORRUPT alarm, got %v", failed)
	}

	// The operator disarms the alarm; the check recovers.
	s.alarms = nil
	c.tick(ctx)
	waitProbeIdle(t, &c.dataCorruptionInFlight)
	c.tick(ctx)
	waitProbeIdle(t, &c.dataCorruptionInFlight)
	_, recovered := notif.snapshot()
	if !slices.Contains(recovered, "data_corruption") {
		t.Fatalf("expected HealthCheckRecovered(\"data_corruption\") after disarm, got %v", recovered)
	}
}

// waitProbeIdle blocks until the single-flight guard reports no probe
// in flight. The guard is cleared by the probe goroutine's deferred
// Store AFTER runCheck has returned, so guard==false guarantees the
// probe's evaluation reached the Aggregator — the deterministic join
// point for tests that drive ticks synchronously.
func waitProbeIdle(t *testing.T, guard *atomic.Bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for guard.Load() {
		if time.Now().After(deadline) {
			t.Fatal("probe still in flight after 2s")
		}
		time.Sleep(time.Millisecond)
	}
}

// TestChecker_DataCorruptionAlarmWalkDoesNotStallTick pins the
// synchronous-group invariant for data_corruption. AlarmStore.Get
// takes the alarm store's exclusive mutex, and Activate/Deactivate
// hold that mutex across a backend batch-tx write — so the walk is
// NOT a non-blocking memory read and must not run inline in tick().
// The observable contract: with the alarm walk wedged forever (an
// activation stuck on a wedged backend commit), the tick loop keeps
// sampling the other checks — here apply_lag, whose failure fires
// through the debounce while Alarms() never returns.
func TestChecker_DataCorruptionAlarmWalkDoesNotStallTick(t *testing.T) {
	notif := &recordingNotifier{}
	block := make(chan struct{})
	defer close(block) // release the leaked probe so the test binary exits cleanly

	s := newFakeServer()
	s.lead = 1
	s.committed = 200000 // gap > threshold: apply_lag fails on every tick
	s.alarmsFn = func() []*pb.AlarmMember {
		<-block // ignores ctx: mimics a walk wedged on the alarm-store mutex
		return nil
	}
	cfg := Config{Interval: 5 * time.Millisecond, CheckTimeout: 50 * time.Millisecond, ApplyLagThreshold: 10000, FailureThreshold: 3, SuccessThreshold: 2}
	c := NewChecker(s, zap.NewNop(), cfg, notif)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { c.Run(ctx); close(done) }()

	deadline := time.After(2 * time.Second)
	for {
		failed, _ := notif.snapshot()
		if slices.Contains(failed, "apply_lag") {
			break
		}
		select {
		case <-deadline:
			t.Fatal("tick loop stalled: apply_lag failure never fired while the alarm walk was wedged")
		case <-time.After(5 * time.Millisecond):
		}
	}
	cancel()
	<-done
}

// readCounterValue fetches the current value of a prometheus.Counter.
// Mirrors the readState helper used for gauges, but for a Counter.
func readCounterValue(t *testing.T, c prometheus.Counter) uint64 {
	t.Helper()
	m := &io_prometheus_client.Metric{}
	if err := c.Write(m); err != nil {
		t.Fatalf("read counter: %v", err)
	}
	if m.Counter == nil {
		t.Fatal("counter is nil")
	}
	return uint64(m.Counter.GetValue())
}

// TestChecker_LinearReadInFlightGuard exercises the in-flight
// single-flight guard. The fake Range blocks for longer than the
// configured Interval so without a guard the checker would spawn
// a fresh linear-read goroutine on every other tick. The guard
// must collapse the in-flight reads to at most one, so a slow
// downstream cannot pile up goroutines even when an operator
// sets an unusually large --experimental-health-check-timeout.
//
// The threshold is intentionally a generous 2 to absorb the
// first race where the previous Range returns and the next tick
// fires before the flag is reset; what we are guarding against
// is unbounded growth, not strict single-flight.
func TestChecker_LinearReadInFlightGuard(t *testing.T) {
	var inFlight atomic.Int64
	var maxConcurrent atomic.Int64
	s := newFakeServer()
	s.lead = 1
	s.rangeFn = func(ctx context.Context, req *pb.RangeRequest) (*pb.RangeResponse, error) {
		cur := inFlight.Add(1)
		defer inFlight.Add(-1)
		// Track the high-water mark with a CAS loop.
		for {
			m := maxConcurrent.Load()
			if cur <= m {
				break
			}
			if maxConcurrent.CompareAndSwap(m, cur) {
				break
			}
		}
		// Block for longer than the tick interval so a missing
		// guard would pile up additional goroutines.
		select {
		case <-ctx.Done():
		case <-time.After(100 * time.Millisecond):
		}
		return &pb.RangeResponse{}, nil
	}
	cfg := Config{Interval: 5 * time.Millisecond, CheckTimeout: time.Second, ApplyLagThreshold: 10000, FailureThreshold: 3, SuccessThreshold: 2}
	c := NewChecker(s, zap.NewNop(), cfg, &recordingNotifier{})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { c.Run(ctx); close(done) }()

	time.Sleep(250 * time.Millisecond)
	cancel()
	<-done

	if max := maxConcurrent.Load(); max > 2 {
		t.Errorf("max concurrent linear reads = %d, want <= 2 (in-flight guard missing)", max)
	}
	if max := maxConcurrent.Load(); max < 1 {
		t.Errorf("max concurrent linear reads = %d, want >= 1 (no linear reads fired)", max)
	}
}

// TestChecker_ConcurrentNotify is a race-detector smoke test. Many
// goroutines call Evaluate concurrently; the test framework's
// `-race` flag will fail if any internal state is unsynchronized.
func TestChecker_ConcurrentNotify(t *testing.T) {
	a := newAggregator(t, Config{FailureThreshold: 1000, SuccessThreshold: 1}, &recordingNotifier{})

	var wg sync.WaitGroup
	var iters atomic.Int64
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				a.Evaluate("apply_lag", nil)
				iters.Add(1)
			}
		}()
	}
	wg.Wait()
	if got := iters.Load(); got != 16*50 {
		t.Fatalf("iter count = %d, want 800", got)
	}
}

// TestChecker_PanicLogThrottledPerCheck pins the panic-log throttle:
// the Error entry with the stack fires at most once per check name
// per throttle window, while healthcheck_exited_total{reason=panic}
// keeps counting per occurrence (the counter is the sustained-growth
// signal; the log is the debugging signal). A persistently panicking
// check at the default cadence would otherwise emit a full stack
// trace on every tick and drown the log.
func TestChecker_PanicLogThrottledPerCheck(t *testing.T) {
	core, recorded := observer.New(zapcore.DebugLevel)
	notif := &recordingNotifier{}
	cfg := Config{Interval: time.Minute, CheckTimeout: time.Second, ApplyLagThreshold: 10000, FailureThreshold: 1, SuccessThreshold: 1}
	c := NewChecker(newFakeServer(), zap.New(core), cfg, notif)

	basePanic := readCounterValue(t, healthCheckExited.WithLabelValues(ExitPanic))
	cur := time.Now()
	c.now = func() time.Time { return cur }

	panicFn := func(ctx context.Context, s EtcdServer) error { panic("boom") }
	errorLogs := func() int {
		n := 0
		for _, e := range recorded.FilterMessage("health check panic recovered").All() {
			if e.Level == zapcore.ErrorLevel {
				n++
			}
		}
		return n
	}

	// First panic on a name logs the full Error entry.
	c.runCheck(context.Background(), "pa", panicFn)
	if got := errorLogs(); got != 1 {
		t.Fatalf("error logs after first panic = %d, want 1", got)
	}
	// Two more panics on the same name inside the window are
	// throttled; the counter still advances per occurrence.
	c.runCheck(context.Background(), "pa", panicFn)
	c.runCheck(context.Background(), "pa", panicFn)
	if got := errorLogs(); got != 1 {
		t.Fatalf("error logs after repeated panics in window = %d, want 1 (throttle missing)", got)
	}
	// A different check name has its own window.
	c.runCheck(context.Background(), "pb", panicFn)
	if got := errorLogs(); got != 2 {
		t.Fatalf("error logs after first panic on second name = %d, want 2", got)
	}
	// Past the throttle window the same name logs again.
	cur = cur.Add(time.Minute + time.Second)
	c.runCheck(context.Background(), "pa", panicFn)
	if got := errorLogs(); got != 3 {
		t.Fatalf("error logs after window elapsed = %d, want 3", got)
	}

	if got := readCounterValue(t, healthCheckExited.WithLabelValues(ExitPanic)); got-basePanic != 5 {
		t.Fatalf("healthcheck_exited_total{reason=panic} delta = %d, want 5 (counter must stay per-occurrence)", got-basePanic)
	}
	// The debounce contract is untouched: every panicking call still
	// reached the aggregator as a failure.
	failed, _ := notif.snapshot()
	if !slices.Contains(failed, "pa") || !slices.Contains(failed, "pb") {
		t.Fatalf("panicking checks must reach the aggregator as failures, got %v", failed)
	}
}

// TestChecker_WedgedProbeWarnsOncePastBound pins the in-flight
// suppression upgrade: a probe wedged longer than the shutdown bound
// (CheckTimeout+Interval, the same budget awaitProbe waits) must log
// exactly one Warn per wedge episode — the healthcheck_suppressed
// counter grows silently otherwise, and a check whose probe never
// returns has a permanently blind dimension. The Warn re-arms when
// the probe returns and a new wedge episode begins.
func TestChecker_WedgedProbeWarnsOncePastBound(t *testing.T) {
	core, recorded := observer.New(zapcore.WarnLevel)
	cfg := Config{Interval: 50 * time.Millisecond, CheckTimeout: 200 * time.Millisecond, ApplyLagThreshold: 10000, FailureThreshold: 3, SuccessThreshold: 2}
	c := NewChecker(newFakeServer(), zap.New(core), cfg, &recordingNotifier{})

	cur := time.Now()
	c.now = func() time.Time { return cur }

	release1 := make(chan struct{})
	wedge := func(ctx context.Context, s EtcdServer) error {
		<-release1
		return nil
	}

	// Episode 1: the probe spawns and wedges (fn ignores ctx, like a
	// real store.Read stuck on s.mu).
	c.spawnProbe(context.Background(), CheckLinearRead, &c.linearReadInFlight, wedge)
	// Suppression far inside the bound: counter only, no Warn.
	cur = cur.Add(10 * time.Millisecond)
	c.spawnProbe(context.Background(), CheckLinearRead, &c.linearReadInFlight, wedge)
	if got := len(recorded.All()); got != 0 {
		t.Fatalf("warn logs inside the bound = %d, want 0", got)
	}
	// Suppression past the bound: exactly one Warn...
	cur = cur.Add(241 * time.Millisecond) // 251ms > 200ms+50ms
	c.spawnProbe(context.Background(), CheckLinearRead, &c.linearReadInFlight, wedge)
	if got := len(recorded.All()); got != 1 {
		t.Fatalf("warn logs past the bound = %d, want 1", got)
	}
	// ...and it does not repeat while the wedge persists.
	cur = cur.Add(10 * time.Millisecond)
	c.spawnProbe(context.Background(), CheckLinearRead, &c.linearReadInFlight, wedge)
	if got := len(recorded.All()); got != 1 {
		t.Fatalf("warn logs after repeated suppression = %d, want 1 (latch missing)", got)
	}

	// The probe returns; the guard frees; a fresh spawn starts a new
	// episode whose Warn is armed again.
	close(release1)
	deadline := time.Now().Add(2 * time.Second)
	for c.linearReadInFlight.Load() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if c.linearReadInFlight.Load() {
		t.Fatal("probe guard never freed after release")
	}
	release2 := make(chan struct{})
	wedge2 := func(ctx context.Context, s EtcdServer) error {
		<-release2
		return nil
	}
	cur = cur.Add(time.Second)
	c.spawnProbe(context.Background(), CheckLinearRead, &c.linearReadInFlight, wedge2)
	cur = cur.Add(251 * time.Millisecond)
	c.spawnProbe(context.Background(), CheckLinearRead, &c.linearReadInFlight, wedge2)
	if got := len(recorded.All()); got != 2 {
		t.Fatalf("warn logs after re-wedge = %d, want 2 (re-arm missing)", got)
	}
	close(release2)
	deadline = time.Now().Add(2 * time.Second)
	for c.linearReadInFlight.Load() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
}
