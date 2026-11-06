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

// Package health exposes the etcd_server_healthcheck_* metric
// family. All metrics are registered unconditionally in init() so
// toggling the ServerHealthCheck feature gate disables the
// *behavior* (the Checker goroutine, the notifier calls) but not
// the metric family, matching the convention of
// etcd_server_health_success / etcd_server_health_failures
// (server/etcdserver/api/etcdhttp/health.go).
//
// Presence splits within the family, and alert rules must respect
// it: the plain gauges (healthcheck_state,
// healthcheck_last_tick_timestamp_seconds, healthcheck_apply_lag,
// healthcheck_learner) are always exposed, but the *Vec families
// only materialize a label combination on first observation
// (prometheus creates Vec children lazily), so a gate-disabled
// cluster exposes no zero series for them. Rules on Vec series
// need absent()/or-vector(0) idioms, not assumed-present zero
// series. The gate-disabled reading of the two operator-facing
// gauges — healthcheck_state = 1 (init() pre-publishes the
// SERVING default, so a gate-off cluster does not read as
// NOT_SERVING) and healthcheck_last_tick = 0 (the "feature
// present, gate off" discriminator) — describes a dedicated
// process; the gauges live in the process-global registry, so
// the shared-process test contract (a gate-off cluster never
// advances either gauge) is enforced by
// tests/integration/server_health_check_test.go
// TestServerHealthCheck_GateOffDoesNotAdvanceMetrics.
package health

import (
	"github.com/prometheus/client_golang/prometheus"
)

var (
	// healthCheckState reflects the most recent decision of the health
	// checker (1 = SERVING, 0 = NOT_SERVING). It is independent of the
	// gRPC health.v1 server so that operators can alert on a divergence
	// between the two.
	healthCheckState = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: "etcd",
		Subsystem: "server",
		Name:      "healthcheck_state",
		Help:      "1 if the most recent server-side health check passed, 0 otherwise.",
	})

	// healthCheckLastTick records the Unix timestamp of the last
	// completed tick. It is the liveness signal for the Checker
	// goroutine: alert when it stops advancing for a few Intervals.
	// There is no in-process stall detector — the gauge is the
	// observable contract. On a server with the ServerHealthCheck
	// gate explicitly off nothing ever advances it; a stuck-at-0
	// reading on such a server is the documented "feature present,
	// gate off" discriminator, not a stall.
	healthCheckLastTick = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: "etcd",
		Subsystem: "server",
		Name:      "healthcheck_last_tick_timestamp_seconds",
		Help:      "Unix timestamp of the last completed health check tick.",
	})

	// healthCheckStateTransitions counts the number of times the
	// health checker's overall state has flipped. A high rate of
	// transitions indicates flapping, which usually means a config
	// mismatch (e.g. linear-read-timeout too low) or upstream noise.
	healthCheckStateTransitions = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "etcd",
		Subsystem: "server",
		Name:      "healthcheck_state_transitions_total",
		Help:      "Total number of health check state transitions, partitioned by from/to labels.",
	}, []string{"from", "to"})

	// healthCheckDuration captures the wall-clock latency of each
	// individual check. Histograms are kept separate so that a slow
	// linear read does not skew apply_lag or no_leader timings.
	healthCheckDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: "etcd",
		Subsystem: "server",
		Name:      "healthcheck_duration_seconds",
		Help:      "Latency of an individual health check invocation.",
		Buckets:   prometheus.ExponentialBuckets(0.001, 2, 10),
	}, []string{"check"})

	// healthCheckFailures and healthCheckSuccesses are simple
	// counters used to compute error rates. They do not require
	// label cardinality management beyond the check name.
	healthCheckFailures = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "etcd",
		Subsystem: "server",
		Name:      "healthcheck_failures_total",
		Help:      "Total number of failed health checks, partitioned by check name.",
	}, []string{"check"})

	healthCheckSuccesses = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "etcd",
		Subsystem: "server",
		Name:      "healthcheck_successes_total",
		Help:      "Total number of successful health checks, partitioned by check name.",
	}, []string{"check"})

	// healthCheckSuppressed counts ticks whose result was dropped
	// without reaching the Aggregator, so the health state could not
	// change on them. Producers: {self_pause,stall} when the ticker
	// goroutine was starved for more than 3 intervals, and
	// {<check>,in_flight} for each asynchronous probe (linear_read,
	// serializable_read, data_corruption) whose previous invocation
	// had not finished. Either counter growing steadily means the
	// checker is not observing the server as often as configured; for
	// the in_flight labels it additionally means that dimension is
	// blind — a probe wedged past the shutdown bound also logs a
	// once-per-episode Warn (spawnProbe), and see ExitProbeLeak for
	// the shutdown-side consequence.
	healthCheckSuppressed = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "etcd",
		Subsystem: "server",
		Name:      "healthcheck_suppressed_total",
		Help:      "Total number of health check ticks dropped without evaluation, partitioned by check and reason.",
	}, []string{"check", "reason"})

	// healthCheckApplyLag mirrors the most recent CommittedIndex -
	// AppliedIndex gap. Recording it on every tick means operators
	// can graph lag even when the threshold is never crossed.
	healthCheckApplyLag = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: "etcd",
		Subsystem: "server",
		Name:      "healthcheck_apply_lag",
		Help:      "Most recent value of committed_index - applied_index.",
	})

	// healthCheckLearner is 1 when the local member is a raft learner.
	// Learners always report NOT_SERVING (consistent with the
	// /readyz/non_learner HTTP probe), so surfacing this explicitly
	// avoids confusion when the gRPC health state is 0 on a learner.
	healthCheckLearner = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: "etcd",
		Subsystem: "server",
		Name:      "healthcheck_learner",
		Help:      "1 if the local member is a raft learner, 0 otherwise.",
	})

	// healthCheckExited counts health-check anomalies by reason.
	// Only "probe_leak" is an actual goroutine exit; the family name
	// predates the other two reasons, which record anomalies on a
	// checker that keeps running. A non-zero value is always
	// anomalous in production: operators should alert on
	// `increase(...[5m]) > 0`. The reason label distinguishes the
	// three known values: "notifier_unwired" (the v3rpc.Server did
	// not install the notifier within the wiring timeout — the
	// checker keeps retrying, so the counter fires exactly once per
	// late wiring, not per poll), "panic" (a checkFunc panicked;
	// callCheck recovered it and scored it as a failure — counted
	// per occurrence, so a persistently panicking check shows up as
	// sustained growth rather than a one-shot blip, and the growth
	// rate conveys persistence) and "probe_leak" (Run's shutdown
	// wait for a context-blind probe exceeded
	// CheckTimeout+Interval and was abandoned; the member is
	// shutting down with the probe goroutine still wedged on a lock
	// section).
	healthCheckExited = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "etcd",
		Subsystem: "server",
		Name:      "healthcheck_exited_total",
		Help:      "Total number of server-side health checker anomalies (goroutine exits, notifier wiring timeouts, recovered check panics), partitioned by reason.",
	}, []string{"reason"})
)

func init() {
	prometheus.MustRegister(
		healthCheckState,
		healthCheckLastTick,
		healthCheckStateTransitions,
		healthCheckDuration,
		healthCheckFailures,
		healthCheckSuccesses,
		healthCheckSuppressed,
		healthCheckApplyLag,
		healthCheckLearner,
		healthCheckExited,
	)
	// Publish the SERVING default so a cluster with the
	// ServerHealthCheck gate disabled (NewAggregator never runs, so
	// its Set(1) never fires) does not sit at Prometheus's zero
	// default, which reads as NOT_SERVING and trips false alerts.
	healthCheckState.Set(1)
}

// ExitReason values are passed to RecordCheckerExit. They are kept
// as exported constants so server.go can refer to them without
// sprinkling string literals across the codebase.
const (
	// ExitNotifierUnwired: the v3rpc.Server did not install the
	// notifier within the wiring timeout of ReadyNotify firing. The
	// checker does NOT exit on this: it keeps retrying and fires the
	// counter exactly once per late wiring.
	ExitNotifierUnwired = "notifier_unwired"
	// ExitPanic: a checkFunc panicked. callCheck recovers it and
	// returns it as an error, so the long-lived ticker keeps running
	// and the Aggregator still scores the tick (as a failure, since
	// health unknown must not be silently treated as healthy). The
	// counter fires per occurrence: a persistently panicking check
	// produces sustained growth, while a single blip means a transient
	// regression in any checkFunc showed up there instead of
	// quietly dropping a health dimension. The full-stack Error log
	// is throttled to once per check per panicLogThrottleWindow; the
	// counter — not the log — is the per-occurrence signal.
	ExitPanic = "panic"
	// ExitProbeLeak: Run's shutdown wait for an in-flight probe
	// exceeded CheckTimeout+Interval, meaning the probe is
	// wedged on a lock section that ignores its context (store.Read
	// acquires s.mu.RLock). Run abandons the wait so the server's
	// Stop/HardStop cannot hang; the leaked probe is inert (its
	// results are dropped once the parent ctx is cancelled) and its
	// goroutine is left to finish on its own. A non-zero value means
	// the member shut down with a wedged probe — investigate the lock
	// contention (defrag, backend commit stall) that outlived it.
	ExitProbeLeak = "probe_leak"
)

// RecordCheckerExit increments the healthcheck_exited_total
// counter for the given reason. Callers: the EtcdServer wiring
// when the notifier wiring timeout fires (the goroutine keeps
// retrying; once per late wiring), Run's shutdown path for a
// leaked probe (the one true goroutine exit), and callCheck for
// each recovered checkFunc panic (per occurrence).
func RecordCheckerExit(reason string) {
	healthCheckExited.WithLabelValues(reason).Inc()
}
