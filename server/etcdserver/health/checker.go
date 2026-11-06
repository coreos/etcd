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
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap"
)

// Notifier is the typed subset of etcdserver.HealthNotifier that
// Aggregator needs. Splitting it out here avoids pulling the whole
// etcdserver package into the health package's public surface.
type Notifier interface {
	HealthCheckFailed(reason string)
	HealthCheckRecovered(reason string)
}

// Checker is the public entry point. EtcdServer.Start launches a
// goroutine that calls Run; the goroutine returns only when ctx is
// canceled (Stop). The self-pause guard never returns: it skips the
// stalled tick, bumps the suppressed counter and keeps looping.
type Checker struct {
	s   EtcdServer
	lg  *zap.Logger
	cfg Config
	agg *Aggregator

	// tickCount gates the asynchronous read probes to every other
	// tick. The probes are asynchronous and never block the tick
	// loop, so the gate is not protecting the tick cadence — it
	// halves the probe frequency (a linear read is a real quorum
	// round trip, 1/s at the default 500ms Interval; a serializable
	// read touches the store mutex and backend read transaction) and,
	// when a read is slow, gives it two full Intervals to finish
	// before the next eligible tick can find it in flight, so the
	// single-flight guards below skip less often.
	mu        sync.Mutex
	tickCount int

	// linearReadInFlight is a single-flight guard. Without it, a
	// linear read that is slower than the configured Interval
	// (e.g. an operator who sets --experimental-health-check-timeout
	// to 60s) would spawn a fresh goroutine on every other tick
	// and pile up unboundedly. The guard collapses concurrent
	// linear reads to at most one; ticks that find a read in
	// flight simply skip and try again on the next eligible tick.
	linearReadInFlight atomic.Bool

	// serializableReadInFlight guards the serializable_read probe the
	// same way; it must be a separate flag from linearReadInFlight so
	// one wedged probe does not suppress the other — they diagnose
	// different failure surfaces (consensus vs local read path).
	serializableReadInFlight atomic.Bool

	// dataCorruptionInFlight guards the data_corruption probe. The
	// alarm-store walk is cheap when uncontended, but AlarmStore.Get
	// serializes with Activate/Deactivate, which hold the store mutex
	// across a backend write; a wedged activation must not stall the
	// tick loop, so the walk runs as an asynchronous probe on every
	// tick instead of inline in the synchronous group.
	dataCorruptionInFlight atomic.Bool

	// applyStallCheck is the stateful apply-stall checkFunc. It is
	// built once here because it carries cross-tick baseline state
	// (see newApplyStallCheck); the other checks are stateless and
	// can be rebuilt per tick.
	applyStallCheck checkFunc

	// probeWG tracks the asynchronously spawned probes (linear_read,
	// serializable_read, data_corruption). Run waits on it before
	// returning so a probe cannot race the server's shutdown sequence
	// into a backend that Cleanup has already closed (Stop cancels
	// ctx, joins s.wg — which this checker goroutine is part of — and
	// only then closes the backend).
	probeWG sync.WaitGroup

	// now is the clock used by the panic-log throttle and the
	// wedged-probe warning. It is nil in production (currentTime
	// falls back to time.Now) and overridden in tests to advance
	// time without sleeping.
	now func() time.Time

	// panicLogLast maps a check name to the time its panic was last
	// logged at Error level; see panicLogThrottleWindow. Guarded by
	// mu: callCheck runs on both the tick goroutine (synchronous
	// checks) and the async probe goroutines.
	panicLogLast map[string]time.Time

	// probes tracks the spawn time and the Warn latch of each async
	// probe for the wedged-probe warning in spawnProbe. It is touched
	// only from the tick goroutine (spawnProbe's only caller is
	// tick), like applyStallCheck's baseline state, so it needs no
	// lock.
	probes map[string]*probeState
}

// probeState is the per-probe bookkeeping for the wedged-probe
// warning: when the current probe was spawned, and whether its wedge
// has already been Warn'd (the Warn fires once per wedge episode and
// re-arms on the next successful spawn).
type probeState struct {
	startedAt time.Time
	warned    bool
}

// panicLogThrottleWindow bounds how often the full-stack Error entry
// for a recovered panic repeats for the same check. The
// healthcheck_exited_total{reason="panic"} counter still fires per
// occurrence — the counter is the sustained-growth signal, the log
// is the debugging signal — so a persistently panicking check at the
// default cadence (up to 2/s) emits at most one stack trace per
// minute instead of drowning the log.
const panicLogThrottleWindow = time.Minute

// NewChecker wires together an Aggregator and a Notifier. The
// EtcdServer is held by reference because the Checker goroutine
// must outlive NewChecker's caller; the convention is that
// EtcdServer.Start owns the lifetime.
func NewChecker(s EtcdServer, lg *zap.Logger, cfg Config, notif Notifier) *Checker {
	return &Checker{
		s:               s,
		lg:              lg,
		cfg:             cfg,
		agg:             NewAggregator(cfg, notif),
		applyStallCheck: newApplyStallCheck(),
		panicLogLast:    make(map[string]time.Time),
		probes:          make(map[string]*probeState),
	}
}

// currentTime returns the checker's clock reading, falling back to
// time.Now when now is unset (always the case in production).
func (c *Checker) currentTime() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

// Run is the long-lived tick loop. It exits cleanly when ctx fires
// and is otherwise self-policing: if more than 3 intervals have
// passed since the previous tick it logs a stall warning,
// increments the suppressed counter, and skips this tick to keep
// the loop responsive.
func (c *Checker) Run(ctx context.Context) {
	ticker := time.NewTicker(c.cfg.Interval)
	defer ticker.Stop()
	// Join in-flight asynchronous read probes before Run returns, but
	// only for a bounded time: see awaitProbe.
	defer c.awaitProbe()

	lastTick := time.Now()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			// self-pause guard. We compare against now rather than
			// time.Now() so that a clock skew between the ticker
			// firing and the goroutine waking up does not trigger a
			// false stall.
			elapsed := now.Sub(lastTick)
			if elapsed > 3*c.cfg.Interval {
				c.lg.Warn(
					"health checker stall detected, holding state",
					zap.Duration("elapsed", elapsed),
					zap.Duration("interval", c.cfg.Interval),
				)
				healthCheckSuppressed.WithLabelValues("self_pause", "stall").Inc()
				lastTick = now
				continue
			}
			lastTick = now
			c.tick(ctx)
			// Update the liveness-of-liveness gauge after the tick
			// so a wedged tick does not leave the gauge stale.
			healthCheckLastTick.Set(float64(time.Now().Unix()))
		}
	}
}

// awaitProbe waits for in-flight asynchronous read probes before Run
// returns, bounded by CheckTimeout + Interval. The bound covers a
// probe that honors its context: each probe runs under a CheckTimeout
// timeout derived from the already-cancelled Run ctx, and it can have
// been spawned up to one Interval before the cancellation. A probe
// still in flight past the bound is wedged on a lock section that
// does not honor its context (store.Read acquires s.mu.RLock), so
// waiting longer cannot help: the wait is abandoned, the
// healthcheck_exited_total{reason="probe_leak"} counter fires, and
// the leaked probe goroutines are left to finish on their own. They
// are inert by then — runCheck skips the Aggregator once the parent
// context is cancelled — and callCheck's recover guards the
// backend-closed race if server cleanup proceeds underneath them.
//
// Abandoning is strictly better than the alternative: an unbounded
// wait here hangs Run, which hangs s.wg.Wait() in Stop, which hangs
// HardStop forever (a pod stuck Terminating until the kubelet
// SIGKILLs the member mid-shutdown).
func (c *Checker) awaitProbe() {
	waited := make(chan struct{})
	go func() {
		c.probeWG.Wait()
		close(waited)
	}()
	bound := c.cfg.CheckTimeout + c.cfg.Interval
	timer := time.NewTimer(bound)
	defer timer.Stop()
	select {
	case <-waited:
	case <-timer.C:
		c.lg.Warn(
			"health check probe still in flight after shutdown bound, abandoning wait",
			zap.Duration("bound", bound),
		)
		RecordCheckerExit(ExitProbeLeak)
	}
}

// tick is one iteration of the loop. It runs the cheap checks
// synchronously and the read probes (linear_read, serializable_read)
// asynchronously every other tick.
func (c *Checker) tick(ctx context.Context) {
	// Learners: short-circuit to NOT_SERVING. We must call
	// RecordLearner/RecordMember on every tick so the gauge and
	// the notifier are always in sync with the cluster's current
	// view of the local member.
	if c.s.IsLearner() {
		c.agg.RecordLearner()
		return
	}
	c.agg.RecordMember()

	// Synchronous group invariant: a check may run inline in tick()
	// only if it is a non-blocking memory read — atomics, or a mutex
	// held just long enough to copy the data it protects. These
	// cannot fail spuriously and cannot stretch the tick, so they run
	// on every tick. Anything that can block — a quorum round trip
	// (linear_read), disk IO, a lock a request path holds while it
	// works (the alarm-store mutex data_corruption walks is held
	// across a backend write by Activate/Deactivate) — belongs in the
	// asynchronous group below; if a check here ever grows a blocking
	// dependency, the fix is to move it into the async group, NOT to
	// widen CheckTimeout. A stalled synchronous check stalls the whole
	// loop: every other check stops sampling, and Run's self-pause
	// guard starts suppressing ticks.
	//
	// Current members, and the memory-read shape that admits each:
	//   - apply_lag: two atomic loads plus a subtraction. The
	//     threshold is captured by closure at construction time so
	//     the --experimental-health-check-apply-lag flag (and the
	//     Config.ApplyLagThreshold it writes to) takes effect.
	//   - no_leader: one atomic load.
	//   - apply_stall: two atomic loads plus a comparison; its
	//     cross-tick baseline state is held by the Checker, not
	//     rebuilt per tick.
	c.runCheck(ctx, CheckApplyLag, applyLagCheckWith(c.cfg.ApplyLagThreshold))
	c.runCheck(ctx, CheckNoLeader, noLeaderCheck)
	c.runCheck(ctx, CheckApplyStall, c.applyStallCheck)

	// The probes are asynchronous, so their latency cannot stretch
	// the tick. data_corruption runs on every tick: the walk is a
	// cheap in-memory read when the alarm store is uncontended, and
	// its own single-flight guard below is the pile-up protection
	// when the store mutex is held elsewhere.
	//
	// The read probes are additionally gated to every other tick
	// (tickCount) to probe at half the tick cadence — a linear read
	// is a real quorum round trip (1/s at the default 500ms
	// Interval) — and to let a slow read finish within two Intervals
	// before the next eligible tick can find it in flight, so the
	// single-flight guards skip less often.
	//
	// serializable_read is a blocking read (store mutex, backend
	// read transaction); it is the local half of linear_read with
	// the read-index round trip skipped, and its failure drives the
	// aggregate entry (see serializableReadCheck).
	c.spawnProbe(ctx, CheckDataCorruption, &c.dataCorruptionInFlight, dataCorruptionCheck)

	c.mu.Lock()
	c.tickCount++
	shouldRunReadProbes := c.tickCount%2 == 0
	c.mu.Unlock()
	if shouldRunReadProbes {
		c.spawnProbe(ctx, CheckLinearRead, &c.linearReadInFlight, linearReadCheck)
		c.spawnProbe(ctx, CheckSerializableRead, &c.serializableReadInFlight, serializableReadCheck)
	}
}

// spawnProbe launches fn as an asynchronous probe guarded by the
// single-flight flag guard: a tick that finds the previous probe
// still in flight records the suppression and returns. Probes never
// queue, so a wedged probe costs its own dimension's sampling only —
// never the tick loop, whose synchronous checks keep running.
//
// A suppression past the shutdown bound (CheckTimeout+Interval, the
// same budget awaitProbe waits) upgrades to a single Warn per wedge
// episode: the healthcheck_suppressed counter is the steady signal,
// but a probe wedged that long has blinded its check's whole
// dimension and deserves a log line an operator will actually see.
// The episode state is tick-goroutine-only (see probes), and the
// Warn re-arms when the probe returns and the next spawn succeeds.
func (c *Checker) spawnProbe(ctx context.Context, name string, guard *atomic.Bool, fn checkFunc) {
	if !guard.CompareAndSwap(false, true) {
		healthCheckSuppressed.WithLabelValues(name, "in_flight").Inc()
		if st := c.probes[name]; st != nil && !st.warned {
			bound := c.cfg.CheckTimeout + c.cfg.Interval
			if inFlight := c.currentTime().Sub(st.startedAt); inFlight > bound {
				st.warned = true
				c.lg.Warn(
					"health check probe wedged past shutdown bound; sampling for this check is blind until it returns",
					zap.String("check", name),
					zap.Duration("in-flight", inFlight),
					zap.Duration("bound", bound),
				)
			}
		}
		return
	}
	c.probes[name] = &probeState{startedAt: c.currentTime()}
	c.probeWG.Add(1)
	go func() {
		defer c.probeWG.Done()
		defer guard.Store(false)
		c.runCheck(ctx, name, fn)
	}()
}

// runCheck applies the per-call timeout, records metrics, and
// forwards the result to the Aggregator. The returned error from
// fn is intentionally not logged here: Aggregator already drives
// the failure counter and would double-count if we logged it
// again.
//
// If the parent context is already cancelled when the check
// returns, the in-flight fn will typically surface
// context.Canceled as its error. Forwarding that into
// Aggregator.Evaluate would (a) record a misleading "failure"
// on the shutdown path, and (b) cause a spurious NOT_SERVING
// transition just as the server is going down. The
// `etcdserver/health_check: ctx cancelled mid-tick` log line
// helps operators distinguish an in-flight cancellation from a
// genuine failure when triaging the gauges.
func (c *Checker) runCheck(ctx context.Context, name string, fn checkFunc) {
	checkCtx, cancel := context.WithTimeout(ctx, c.cfg.CheckTimeout)
	defer cancel()

	start := time.Now()
	err := c.callCheck(checkCtx, name, fn)
	duration := time.Since(start)
	// Skip metric and aggregator updates when the parent context
	// has been cancelled. A ctx-cancelled result is not a real
	// health signal; it just means the loop is tearing down.
	if ctx.Err() != nil {
		c.lg.Debug("health check: ctx cancelled mid-tick, skipping state update",
			zap.String("check", name),
			zap.Error(err),
		)
		return
	}
	healthCheckDuration.WithLabelValues(name).Observe(duration.Seconds())

	c.agg.Evaluate(name, err)
}

// callCheck runs fn and converts a panic into an error, so a future
// regression in one checkFunc can neither tear down the long-lived
// ticker goroutine nor leave the Aggregator without a result.
//
// Scoring the panic as a failure is what keeps the debounce honest.
// A panic that skipped Evaluate entirely would freeze this check's
// counters at their pre-panic values: a member already NOT_SERVING
// could never come back, and one still SERVING could never be pulled,
// no matter what the underlying condition did afterwards. Treating
// "health unknown" as failure routes traffic away from the member,
// and healthcheck_exited_total{reason="panic"} is the alert that says
// why (a non-zero value is always anomalous, counted per occurrence
// so sustained growth conveys persistence).
//
// The Error entry with the stack is throttled to once per check name
// per panicLogThrottleWindow; throttled occurrences keep a Debug
// entry so the panic value stays traceable without re-emitting the
// stack every tick.
//
// The recover scope is deliberately fn alone. Wrapping the Evaluate
// call as well would mean a panic inside Evaluate re-panics from the
// deferred handler, where nothing is left to catch it.
func (c *Checker) callCheck(ctx context.Context, name string, fn checkFunc) (err error) {
	defer func() {
		if r := recover(); r != nil {
			if c.shouldLogPanic(name) {
				c.lg.Error(
					"health check panic recovered",
					zap.String("check", name),
					zap.Any("recover", r),
					zap.Stack("stack"),
				)
			} else {
				c.lg.Debug(
					"health check panic recovered (throttled)",
					zap.String("check", name),
					zap.Any("recover", r),
				)
			}
			RecordCheckerExit(ExitPanic)
			err = fmt.Errorf("panic: %v", r)
		}
	}()
	return fn(ctx, c.s)
}

// shouldLogPanic reports whether this panic is the first for name in
// the current throttle window, recording the window when it is.
func (c *Checker) shouldLogPanic(name string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.currentTime()
	if last, ok := c.panicLogLast[name]; ok && now.Sub(last) < panicLogThrottleWindow {
		return false
	}
	c.panicLogLast[name] = now
	return true
}
