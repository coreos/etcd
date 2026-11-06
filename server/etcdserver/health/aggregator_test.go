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
	"errors"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	io_prometheus_client "github.com/prometheus/client_model/go"
)

// recordingNotifier captures every HealthCheckFailed /
// HealthCheckRecovered invocation so tests can assert ordering and
// deduplication without coupling to a real *health.Server.
type recordingNotifier struct {
	mu      sync.Mutex
	failed  []string
	recover []string
}

func (r *recordingNotifier) HealthCheckFailed(reason string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.failed = append(r.failed, reason)
}

func (r *recordingNotifier) HealthCheckRecovered(reason string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.recover = append(r.recover, reason)
}

func (r *recordingNotifier) snapshot() (failed, recover []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	failed = append(failed, r.failed...)
	recover = append(recover, r.recover...)
	return failed, recover
}

func newAggregator(t *testing.T, cfg Config, notif Notifier) *Aggregator {
	t.Helper()
	if notif == nil {
		notif = &recordingNotifier{}
	}
	return NewAggregator(cfg, notif)
}

// TestAggregator_FailureDebounce verifies that the notifier is NOT
// called until exactly FailureThreshold consecutive failures have
// been observed. One or two failures must be silently absorbed.
func TestAggregator_FailureDebounce(t *testing.T) {
	notif := &recordingNotifier{}
	a := newAggregator(t, Config{FailureThreshold: 3, SuccessThreshold: 1}, notif)

	for i := 0; i < 2; i++ {
		a.Evaluate("apply_lag", errors.New("boom"))
	}
	if failed, _ := notif.snapshot(); len(failed) != 0 {
		t.Fatalf("notifier should not be called before FailureThreshold, got %v", failed)
	}

	// Third failure crosses the threshold.
	a.Evaluate("apply_lag", errors.New("boom"))
	failed, _ := notif.snapshot()
	if len(failed) != 1 || failed[0] != "apply_lag" {
		t.Fatalf("expected one HealthCheckFailed(\"apply_lag\"), got %v", failed)
	}
}

// TestAggregator_SuccessDebounce verifies the asymmetric recovery:
// two consecutive successes (SuccessThreshold=2) clear the
// failure, but a single success must not.
func TestAggregator_SuccessDebounce(t *testing.T) {
	notif := &recordingNotifier{}
	a := newAggregator(t, Config{FailureThreshold: 1, SuccessThreshold: 2}, notif)

	// Fail once: failure threshold = 1, so a single failure flips the
	// notifier immediately.
	a.Evaluate("apply_lag", errors.New("boom"))
	if failed, _ := notif.snapshot(); len(failed) != 1 {
		t.Fatalf("expected one failure notification, got %d", len(failed))
	}

	// One success is below threshold; notifier must not be called.
	a.Evaluate("apply_lag", nil)
	if _, rec := notif.snapshot(); len(rec) != 0 {
		t.Fatalf("expected zero recovery notifications, got %d", len(rec))
	}

	// Second success crosses SuccessThreshold; recovery fires.
	a.Evaluate("apply_lag", nil)
	_, rec := notif.snapshot()
	if len(rec) != 1 || rec[0] != "apply_lag" {
		t.Fatalf("expected one HealthCheckRecovered(\"apply_lag\"), got %v", rec)
	}
}

// TestAggregator_FailureResetsSuccessCounter ensures the
// per-check state machine is correctly reset: a single success after
// a long failure streak must not immediately recover.
func TestAggregator_FailureResetsSuccessCounter(t *testing.T) {
	notif := &recordingNotifier{}
	a := newAggregator(t, Config{FailureThreshold: 3, SuccessThreshold: 2}, notif)

	// 3 consecutive failures -> NOT_SERVING
	for i := 0; i < 3; i++ {
		a.Evaluate("apply_lag", errors.New("boom"))
	}
	// 1 success: counter is 0 because the previous successCount was
	// reset on the last failure.
	a.Evaluate("apply_lag", nil)
	if _, rec := notif.snapshot(); len(rec) != 0 {
		t.Fatalf("success streak too short, expected no recovery, got %v", rec)
	}
}

// TestAggregator_SustainedConditionRepeatsNotifier pins the per-tick
// notifier repetition the late-listener path depends on. v3rpc
// registers one notifier per gRPC listener, and a listener that comes
// up after the checker already flipped state only learns that state
// from a later call (the reloading notifier re-reads the registration
// set on every call). Making the notifier edge-triggered would leave
// such a listener permanently reporting the opposite of the truth.
// The repetition must stay cheap on the state side: the gauge and the
// transition counter are edge-triggered even though the notifier is
// not.
func TestAggregator_SustainedConditionRepeatsNotifier(t *testing.T) {
	notif := &recordingNotifier{}
	a := newAggregator(t, Config{FailureThreshold: 1, SuccessThreshold: 1}, notif)
	a.RecordMember()
	baseTr := readTransitions(t)

	const ticks = 5
	for i := 0; i < ticks; i++ {
		a.Evaluate("apply_lag", errors.New("boom"))
	}
	failed, _ := notif.snapshot()
	if len(failed) != ticks {
		t.Fatalf("every tick of a sustained failure must re-notify, got %d calls for %d ticks: %v",
			len(failed), ticks, failed)
	}
	for i, r := range failed {
		if r != "apply_lag" {
			t.Errorf("failure call %d reason = %q, want \"apply_lag\"", i, r)
		}
	}
	if got := readState(t); got != 0 {
		t.Fatalf("sustained failure must hold healthCheckState at 0, got %v", got)
	}
	if tr := readTransitions(t); tr["from=serving,to=not_serving"]-baseTr["from=serving,to=not_serving"] != 1 {
		t.Fatalf("%d failed ticks must record exactly 1 serving->not_serving transition, got delta %d (full: %v)",
			ticks, tr["from=serving,to=not_serving"]-baseTr["from=serving,to=not_serving"], tr)
	}

	for i := 0; i < ticks; i++ {
		a.Evaluate("apply_lag", nil)
	}
	_, rec := notif.snapshot()
	if len(rec) != ticks {
		t.Fatalf("every tick of a sustained recovery must re-notify, got %d calls for %d ticks: %v",
			len(rec), ticks, rec)
	}
	for i, r := range rec {
		if r != "apply_lag" {
			t.Errorf("recovery call %d reason = %q, want \"apply_lag\"", i, r)
		}
	}
	if got := readState(t); got != 1 {
		t.Fatalf("sustained success must hold healthCheckState at 1, got %v", got)
	}
	if tr := readTransitions(t); tr["from=not_serving,to=serving"]-baseTr["from=not_serving,to=serving"] != 1 {
		t.Fatalf("%d successful ticks must record exactly 1 not_serving->serving transition, got delta %d (full: %v)",
			ticks, tr["from=not_serving,to=serving"]-baseTr["from=not_serving,to=serving"], tr)
	}
}

// TestAggregator_RecordLearner covers the learner short-circuit. The
// notifier must be called every tick (idempotently) with the
// "learner" reason, regardless of how many times it is invoked.
func TestAggregator_RecordLearner(t *testing.T) {
	notif := &recordingNotifier{}
	a := newAggregator(t, Config{FailureThreshold: 99, SuccessThreshold: 99}, notif)

	for i := 0; i < 3; i++ {
		a.RecordLearner()
	}
	failed, _ := notif.snapshot()
	if len(failed) != 3 {
		t.Fatalf("expected 3 HealthCheckFailed(\"learner\") calls, got %d (%v)", len(failed), failed)
	}
	for i, r := range failed {
		if r != "learner" {
			t.Errorf("call %d reason = %q, want \"learner\"", i, r)
		}
	}
}

// TestAggregator_RecordLearnerResetsApplyLagGauge verifies that
// the apply-lag gauge is reset to 0 on every RecordLearner call so
// a learner member does not display a stale value inherited from
// its previous voter lifetime. Without the reset, operators
// monitoring etcd_server_healthcheck_apply_lag on a learner would
// see the last voter-era value forever. The gauge is a
// package-global, so the test asserts on the final value after
// the RecordLearner call.
func TestAggregator_RecordLearnerResetsApplyLagGauge(t *testing.T) {
	notif := &recordingNotifier{}
	a := newAggregator(t, Config{FailureThreshold: 99, SuccessThreshold: 99}, notif)

	// Simulate a non-zero apply-lag value inherited from a
	// previous voter lifetime.
	healthCheckApplyLag.Set(12345)
	if got := readApplyLag(t); got != 12345 {
		t.Fatalf("precondition: healthCheckApplyLag = %v, want 12345", got)
	}

	// Promote the member to a learner; the gauge must be reset.
	a.RecordLearner()
	if got := readApplyLag(t); got != 0 {
		t.Fatalf("after RecordLearner, healthCheckApplyLag = %v, want 0", got)
	}
}

// readApplyLag fetches the current value of healthCheckApplyLag
// for assertion. Mirrors readState but for the apply-lag gauge.
func readApplyLag(t *testing.T) float64 {
	t.Helper()
	m := &io_prometheus_client.Metric{}
	if err := healthCheckApplyLag.Write(m); err != nil {
		t.Fatalf("read healthCheckApplyLag: %v", err)
	}
	if m.Gauge == nil {
		t.Fatal("healthCheckApplyLag gauge is nil")
	}
	return m.Gauge.GetValue()
}

// TestAggregator_RecordMember is the corresponding positive path: a
// voting member must call HealthCheckRecovered("learner") so a
// transition out of learner state is reflected immediately. The call
// is gated on a "learner" entry actually being present: a steady-state
// voter (never a learner) must not drive the notifier on every tick.
func TestAggregator_RecordMember(t *testing.T) {
	notif := &recordingNotifier{}
	a := newAggregator(t, Config{}, notif)

	// No learner history: RecordMember must be a notifier no-op.
	for i := 0; i < 3; i++ {
		a.RecordMember()
	}
	if _, rec := notif.snapshot(); len(rec) != 0 {
		t.Fatalf("steady-state voter must not call HealthCheckRecovered, got %d calls", len(rec))
	}

	// Learner -> member demotion: exactly one recovery call, on the
	// first RecordMember tick after the demotion.
	a.RecordLearner()
	a.RecordMember()
	a.RecordMember()
	_, rec := notif.snapshot()
	if len(rec) != 1 || rec[0] != "learner" {
		t.Fatalf("expected exactly 1 HealthCheckRecovered(\"learner\") call after demotion, got %v", rec)
	}
}

// TestAggregator_PerCheckStateIndependence ensures counters for one
// check name do not affect another. apply_lag failing while
// no_leader succeeds must not report any failure for no_leader.
func TestAggregator_PerCheckStateIndependence(t *testing.T) {
	notif := &recordingNotifier{}
	a := newAggregator(t, Config{FailureThreshold: 2, SuccessThreshold: 1}, notif)

	for i := 0; i < 5; i++ {
		a.Evaluate("apply_lag", errors.New("boom"))
		a.Evaluate("no_leader", nil)
	}
	failed, _ := notif.snapshot()
	if len(failed) == 0 {
		t.Fatalf("expected at least one HealthCheckFailed(\"apply_lag\"), got none")
	}
	for i, r := range failed {
		if r != "apply_lag" {
			t.Errorf("call %d reason = %q, want only \"apply_lag\"; no_leader leaks: %v", i, r, failed)
		}
	}
}

// readState fetches the current value of healthCheckState for
// assertion. The gauge is package-global, so the helper also
// snapshots the value to avoid races with concurrent tests.
func readState(t *testing.T) float64 {
	t.Helper()
	m := &io_prometheus_client.Metric{}
	if err := healthCheckState.Write(m); err != nil {
		t.Fatalf("read healthCheckState: %v", err)
	}
	if m.Gauge == nil {
		t.Fatal("healthCheckState gauge is nil")
	}
	return m.Gauge.GetValue()
}

// readTransitions fetches the from/to label counter for
// healthCheckStateTransitions, summing across all label values.
func readTransitions(t *testing.T) map[string]uint64 {
	t.Helper()
	ch := make(chan prometheus.Metric, 16)
	healthCheckStateTransitions.Collect(ch)
	close(ch)
	out := map[string]uint64{}
	for m := range ch {
		dto := &io_prometheus_client.Metric{}
		if err := m.Write(dto); err != nil {
			t.Fatalf("decode transition: %v", err)
		}
		labels := make([]string, 0, 4)
		for _, l := range dto.Label {
			labels = append(labels, l.GetName()+"="+l.GetValue())
		}
		sort.Strings(labels)
		key := strings.Join(labels, ",")
		if dto.Counter == nil {
			continue
		}
		out[key] = uint64(dto.Counter.GetValue())
	}
	return out
}

// TestAggregator_StateMetricTracksFailedSet verifies that
// healthCheckState and healthCheckStateTransitions are driven by
// the failed-set, not the gRPC notifier, so operators can detect
// divergence between the two views. The test uses FailureThreshold
// = 1 / SuccessThreshold = 1 so each tick crosses the threshold
// immediately.
//
// healthCheckState and healthCheckStateTransitions are package
// globals, so the test asserts on the *delta* caused by each
// step rather than the absolute value, and the starting state is
// established by a fresh RecordMember so a learner Set from a
// prior test cannot leak.
func TestAggregator_StateMetricTracksFailedSet(t *testing.T) {
	notif := &recordingNotifier{}
	a := newAggregator(t, Config{FailureThreshold: 1, SuccessThreshold: 1}, notif)

	// Establish a clean baseline: NewAggregator publishes SERVING,
	// but a previous test may have left a learner gate open. Push
	// the failed-set empty so healthCheckState is exactly 1 and
	// the from->to transition counters are zeroed by the recorded
	// recovery.
	a.RecordMember()
	baseState := readState(t)
	if baseState != 1 {
		t.Fatalf("baseline healthCheckState = %v, want 1 (SERVING)", baseState)
	}
	baseTr := readTransitions(t)
	baseFailed := baseTr["from=serving,to=not_serving"]
	baseRecovered := baseTr["from=not_serving,to=serving"]

	// First failure crosses threshold: state -> NOT_SERVING.
	a.Evaluate("apply_lag", errors.New("boom"))
	if got := readState(t); got != 0 {
		t.Fatalf("after 1 failure, healthCheckState = %v, want 0 (NOT_SERVING)", got)
	}
	tr := readTransitions(t)
	if tr["from=serving,to=not_serving"]-baseFailed != 1 {
		t.Fatalf("expected +1 serving->not_serving transition, got delta %d (full: %v)",
			tr["from=serving,to=not_serving"]-baseFailed, tr)
	}

	// A second distinct failure is idempotent on the state: the
	// failed-set is unchanged, the gauge stays at 0, and no
	// new transition is recorded.
	a.Evaluate("no_leader", errors.New("boom"))
	if got := readState(t); got != 0 {
		t.Fatalf("after 2 concurrent failures, healthCheckState = %v, want 0 (NOT_SERVING)", got)
	}
	tr = readTransitions(t)
	if tr["from=serving,to=not_serving"]-baseFailed != 1 {
		t.Fatalf("second failure should not record a new transition, got delta %d (full: %v)",
			tr["from=serving,to=not_serving"]-baseFailed, tr)
	}

	// Recovering one check is not enough: state stays NOT_SERVING
	// because apply_lag is still in the failed-set.
	a.Evaluate("no_leader", nil)
	if got := readState(t); got != 0 {
		t.Fatalf("partial recovery should keep state NOT_SERVING, got %v", got)
	}
	tr = readTransitions(t)
	if tr["from=not_serving,to=serving"]-baseRecovered != 0 {
		t.Fatalf("partial recovery should not flip state, got delta %d (full: %v)",
			tr["from=not_serving,to=serving"]-baseRecovered, tr)
	}

	// Recovering the last failed check transitions back to SERVING.
	a.Evaluate("apply_lag", nil)
	if got := readState(t); got != 1 {
		t.Fatalf("after full recovery, healthCheckState = %v, want 1 (SERVING)", got)
	}
	tr = readTransitions(t)
	if tr["from=not_serving,to=serving"]-baseRecovered != 1 {
		t.Fatalf("expected +1 not_serving->serving transition, got delta %d (full: %v)",
			tr["from=not_serving,to=serving"]-baseRecovered, tr)
	}
}

// TestAggregator_RecordLearnerUpdatesState verifies that the
// unconditional learner/member path also drives the perceived
// state metric, so a learner transition alone is observable
// without crossing the debounce threshold. Uses the delta-based
// pattern from TestAggregator_StateMetricTracksFailedSet because
// the gauges are package globals.
func TestAggregator_RecordLearnerUpdatesState(t *testing.T) {
	notif := &recordingNotifier{}
	a := newAggregator(t, Config{FailureThreshold: 99, SuccessThreshold: 99}, notif)

	// Establish baseline: empty failed-set, state = SERVING.
	a.RecordMember()
	baseTr := readTransitions(t)
	baseFailed := baseTr["from=serving,to=not_serving"]
	baseRecovered := baseTr["from=not_serving,to=serving"]

	if got := readState(t); got != 1 {
		t.Fatalf("baseline healthCheckState = %v, want 1", got)
	}
	a.RecordLearner()
	if got := readState(t); got != 0 {
		t.Fatalf("after RecordLearner, healthCheckState = %v, want 0", got)
	}
	a.RecordMember()
	if got := readState(t); got != 1 {
		t.Fatalf("after RecordMember, healthCheckState = %v, want 1", got)
	}
	tr := readTransitions(t)
	if tr["from=serving,to=not_serving"]-baseFailed < 1 {
		t.Fatalf("learner should record a serving->not_serving transition, got delta %d (full: %v)",
			tr["from=serving,to=not_serving"]-baseFailed, tr)
	}
	if tr["from=not_serving,to=serving"]-baseRecovered < 1 {
		t.Fatalf("member should record a not_serving->serving transition, got delta %d (full: %v)",
			tr["from=not_serving,to=serving"]-baseRecovered, tr)
	}
}
