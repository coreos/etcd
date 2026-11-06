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

import "sync"

// Aggregator is the debounce brain of the health check loop. It
// tracks consecutive failure/success counters per check name and
// only calls into the notifier once a threshold is crossed.
type Aggregator struct {
	cfg Config

	mu           sync.Mutex
	failureCount map[string]int
	successCount map[string]int
	// failedChecks is the set of check names that have crossed
	// the failure threshold and not yet been recovered. Members
	// are added by Evaluate (debounced failure path) and
	// RecordLearner (unconditional); they are removed by
	// Evaluate (debounced recovery path) and RecordMember
	// (only while a "learner" entry is present). The health
	// checker's "perceived" state is
	// SERVING iff failedChecks is empty; healthCheckState mirrors
	// that view so operators can alert on a divergence from the
	// gRPC health.v1 state.
	failedChecks map[string]struct{}
	notifier     Notifier
}

// NewAggregator returns a ready-to-use Aggregator. cfg supplies the
// failure/success thresholds; notif is the sink that ultimately
// flips the gRPC health state.
// The initial healthCheckState is published as SERVING because no
// check has yet crossed the failure threshold.
func NewAggregator(cfg Config, notif Notifier) *Aggregator {
	healthCheckState.Set(1)
	return &Aggregator{
		cfg:          cfg,
		failureCount: make(map[string]int),
		successCount: make(map[string]int),
		failedChecks: make(map[string]struct{}),
		notifier:     notif,
	}
}

// Evaluate ingests the result of a single check. It records the
// per-check metric counters, then routes the result through the
// debounce logic.
//
// The asymmetry between the failure and success thresholds
// (FailureThreshold vs SuccessThreshold) is deliberate: we want to
// flap into NOT_SERVING only after sustained badness, but recover
// quickly so a single transient blip on a previously healthy
// server does not leave it pinned at NOT_SERVING.
//
// Once a check has crossed its threshold, the notifier keeps being
// called on every subsequent tick while the condition persists.
// This repetition is load-bearing, not waste: v3rpc.Server registers
// one notifier per listener and a listener that registers after the
// checker started (dual insecure/secure listener deployments) only
// learns the current state from a later call. The reloading
// notifier re-reads the registration set on every call. Do
// not "optimize" this into edge-only calls without adding an
// explicit re-sync on listener registration. The per-call cost is
// one mutex + map lookup + early return in stopServe/startServe.
func (a *Aggregator) Evaluate(checkName string, err error) {
	// 1. Always record the per-check metric counters so error rate
	//    dashboards work regardless of the debounce outcome.
	if err != nil {
		healthCheckFailures.WithLabelValues(checkName).Inc()
	} else {
		healthCheckSuccesses.WithLabelValues(checkName).Inc()
	}

	// 2. Debounced state transition. The counter bookkeeping runs
	//    under a.mu, but the notifier call is made outside it
	//    (matching RecordLearner/RecordMember) so a.mu is never held
	//    across a callback into the gRPC health subsystem. markCheck*
	//    re-takes a.mu because it mutates failedChecks and the gauges.
	if err != nil {
		a.mu.Lock()
		a.failureCount[checkName]++
		a.successCount[checkName] = 0
		crossed := a.failureCount[checkName] >= a.cfg.FailureThreshold
		a.mu.Unlock()
		if crossed {
			a.notifier.HealthCheckFailed(checkName)
			a.mu.Lock()
			a.markCheckFailed(checkName)
			a.mu.Unlock()
		}
		return
	}
	a.mu.Lock()
	a.successCount[checkName]++
	a.failureCount[checkName] = 0
	crossed := a.successCount[checkName] >= a.cfg.SuccessThreshold
	a.mu.Unlock()
	if crossed {
		a.notifier.HealthCheckRecovered(checkName)
		a.mu.Lock()
		a.markCheckRecovered(checkName)
		a.mu.Unlock()
	}
}

// markCheckFailed registers checkName in the failed set and, if
// the overall perceived state transitions to NOT_SERVING, updates
// the healthCheckState gauge and increments the
// healthCheckStateTransitions counter. Idempotent: a check that
// is already in the set is a no-op, and a transition counter
// increment only fires when the overall state actually flips
// from SERVING to NOT_SERVING (i.e. the failed-set was empty
// before this call). Caller must hold a.mu.
func (a *Aggregator) markCheckFailed(checkName string) {
	if _, ok := a.failedChecks[checkName]; ok {
		return
	}
	a.failedChecks[checkName] = struct{}{}
	// The state only flips on the first failed check; subsequent
	// concurrent failures are idempotent on the gauge and must
	// not double-count the transition.
	healthCheckState.Set(0)
	if len(a.failedChecks) == 1 {
		healthCheckStateTransitions.WithLabelValues("serving", "not_serving").Inc()
	}
}

// markCheckRecovered removes checkName from the failed set and,
// if the set becomes empty (i.e. the overall perceived state
// transitions back to SERVING), updates the healthCheckState
// gauge and the transition counter. Idempotent: a check that is
// not in the set is a no-op. Caller must hold a.mu.
func (a *Aggregator) markCheckRecovered(checkName string) {
	if _, ok := a.failedChecks[checkName]; !ok {
		return
	}
	delete(a.failedChecks, checkName)
	if len(a.failedChecks) == 0 {
		healthCheckState.Set(1)
		healthCheckStateTransitions.WithLabelValues("not_serving", "serving").Inc()
	}
}

// RecordLearner is called once per tick when the local member is a
// raft learner. Learners must report NOT_SERVING so that
// load-balancers do not route client traffic to them; the existing
// /readyz/non_learner HTTP probe already enforces this and we
// need the gRPC health state to match. The notifier call repeats
// every tick for the late-listener self-healing reason documented
// on Evaluate; stopServe is idempotent while the reason is set.
func (a *Aggregator) RecordLearner() {
	healthCheckLearner.Set(1)
	// Reset the apply-lag gauge so a learner member does not
	// inherit a stale value from its previous voter lifetime.
	// Without this reset the gauge reflects the value at the
	// time of the voter->learner transition and never advances,
	// misleading operators who alert on
	// etcd_server_healthcheck_apply_lag while monitoring a
	// learner. The voting-member tick path in checks.go
	// continues to update the gauge as usual.
	healthCheckApplyLag.Set(0)
	a.notifier.HealthCheckFailed(CheckLearner)
	a.mu.Lock()
	a.markCheckFailed(CheckLearner)
	a.mu.Unlock()
}

// RecordMember clears the learner health reason when the local member is
// a voting member again. The notifier is only driven when there is an
// actual "learner" failure to clear: HealthCheckRecovered goes through
// the reloading notifier (which re-reads GetHealthNotifier and rebuilds
// its fan-out set on every call), and a steady-state voter calls this
// every tick, so an unconditional call would pay that rebuild on every
// tick to clear a reason that was never set. When the reason is
// present, the call clears it immediately on the very next tick after a
// learner->member demotion, without waiting for a failure streak to age
// out; healthNotifier.HealthCheckRecovered("learner") only deletes the
// "health:learner" entry from the reasons set and leaves every other
// "health:*" reason untouched (e.g. a still-failing "apply_lag" or
// "no_leader" check is preserved).
func (a *Aggregator) RecordMember() {
	healthCheckLearner.Set(0)
	a.mu.Lock()
	_, isLearner := a.failedChecks[CheckLearner]
	a.mu.Unlock()
	if isLearner {
		a.notifier.HealthCheckRecovered(CheckLearner)
		a.mu.Lock()
		a.markCheckRecovered(CheckLearner)
		a.mu.Unlock()
	}
}
