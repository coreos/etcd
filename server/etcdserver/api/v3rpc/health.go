// Copyright 2023 The etcd Authors
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

package v3rpc

import (
	"sort"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"

	"go.etcd.io/etcd/server/v3/etcdserver"
	etcdhealth "go.etcd.io/etcd/server/v3/etcdserver/health"
	"go.etcd.io/etcd/server/v3/features"
)

const (
	allGRPCServices     = ""
	healthPrefix        = "health:"
	healthServicePrefix = "etcd.health/"
	defragReason        = "defrag is active"

	// reasonLinearRead and friends are the "health:"-namespaced
	// notifier reasons this package derives from the check-name
	// constants owned by the health package (the wire contract both
	// sides share — see etcdhealth's Check* block).
	reasonLinearRead = healthPrefix + etcdhealth.CheckLinearRead
	reasonNoLeader   = healthPrefix + etcdhealth.CheckNoLeader
	reasonApplyLag   = healthPrefix + etcdhealth.CheckApplyLag

	// defaultNoLeaderVetoWindow is the floor for noLeaderVetoWindow,
	// far past a default-timeout election, so the window stays
	// meaningful on clusters with a short election timeout.
	defaultNoLeaderVetoWindow = 30 * time.Second

	// noLeaderVetoFactor scales the veto window with the election
	// timeout: a fixed bound would lift the veto mid-election on a
	// cluster with a long timeout and evict every member at once.
	noLeaderVetoFactor = 10
)

// noLeaderVetoWindow bounds how long a failing no_leader vetoes
// linear_read out of the aggregate entry. A leaderless cluster is
// normally a transient election that resolves in a few election
// timeouts, and evicting every member during it buys nothing. But a
// minority-partition peer stays leaderless forever while the quorum
// side keeps serving: past the window the member is stranded, can
// never complete a linearizable read again, and evicting it moves the
// client to a peer that can.
func noLeaderVetoWindow(electionTimeout time.Duration) time.Duration {
	return max(defaultNoLeaderVetoWindow, noLeaderVetoFactor*electionTimeout)
}

// healthCheckServiceNames lists every per-check grpc.health.v1
// service entry this package publishes. The names after the prefix
// must match the check names emitted by the server-side health
// checker (server/etcdserver/health: "linear_read",
// "serializable_read", "apply_lag", "apply_stall", "no_leader",
// "data_corruption", "learner") plus "defrag" for the defrag-driven
// NOT_SERVING — an observation entry under ServerHealthCheck, and
// also the aggregate-driving drain under the legacy
// StopGRPCServiceOnDefrag gate. The full entry names are an external contract:
// clients may call Check/Watch on them, so they cannot be renamed
// once shipped.
var healthCheckServiceNames = []string{
	etcdhealth.CheckLinearRead,
	etcdhealth.CheckSerializableRead,
	etcdhealth.CheckApplyLag,
	etcdhealth.CheckApplyStall,
	etcdhealth.CheckNoLeader,
	etcdhealth.CheckDataCorruption,
	etcdhealth.CheckLearner,
	"defrag",
}

// clusterWideReasons lists the health reasons that never drive the
// aggregate entry. Evicting a member over one of these can evict
// every member at once, which buys the client nothing: it tears
// down that member's Watch streams and, cluster-wide, turns a
// single ErrNoLeader into a full retry budget on the read path.
// These reasons therefore drive their own etcd.health/* entry
// (probe and observation) and never the aggregate entry
// (client-side endpoint selection).
//
// no_leader is literally simultaneous: a cluster has at most one
// leader, so every member fails it together. apply_lag is
// cluster-correlated rather than simultaneous: under sustained
// write pressure (or shared storage/host jitter) several members
// breach the gap threshold together, so letting it drive the
// aggregate could evict a majority over one degradation. The
// member-local cases are deliberately split off: an apply loop
// frozen while the cluster keeps committing is apply_stall, which
// does drive the aggregate (pinned by
// TestHealthNotifier_ApplyStallDrivesAggregate), while a member
// that is backlogged but still applying keeps serving reads, and
// eviction for it is left to linear_read if reads actually suffer.
var clusterWideReasons = map[string]struct{}{
	reasonNoLeader: {},
	reasonApplyLag: {},
}

// aggregateEligibleLocked reports whether reason should count towards
// the aggregate entry, given the reasons currently failing on this
// member and the time each one first failed.
//
// linear_read is the one conditional case. It covers both a
// linearizableReadLoop wedged on this member alone (upstream #22214)
// and a cluster-wide read stall; the presence of no_leader is what
// tells them apart. With a leader, a timed-out read is local and
// evicting the member moves the client to a peer that can serve.
// Without a leader every member times out, so the veto keeps the
// aggregate SERVING and leaves the condition visible on
// etcd.health/linear_read.
//
// The veto is bounded by hc.noLeaderVeto: once no_leader has persisted
// past the window the veto lifts and linear_read counts again (see
// noLeaderVetoWindow for why the bound exists).
//
// The veto arms at most once per linear_read failure episode. The
// first evaluation that finds the window elapsed latches it off for
// the rest of the episode, and startServe clears the latch only when
// linear_read itself recovers. Without the latch, a no_leader flap
// (recover, then re-fail with a fresh timestamp) re-shields the
// still-wedged linear_read for another full window, and a cluster
// with leadership churn can pull a bad member back to SERVING
// indefinitely.
//
// Caller must hold hc.mu.
func (hc *healthNotifier) aggregateEligibleLocked(reason string, now time.Time) bool {
	if _, clusterWide := clusterWideReasons[reason]; clusterWide {
		return false
	}
	if reason == defragReason {
		// Defrag drives the aggregate entry (client-side eviction)
		// only under the legacy StopGRPCServiceOnDefrag gate. Under
		// ServerHealthCheck alone it is an observation entry: a
		// defrag must not evict clients that never opted into the
		// drain.
		return hc.stopGRPCServiceOnDefrag
	}
	if reason != reasonLinearRead {
		return true
	}
	if hc.vetoLifted {
		return true
	}
	noLeaderSince, noLeader := hc.reasons[reasonNoLeader]
	if !noLeader {
		return true
	}
	if now.Sub(noLeaderSince) >= hc.noLeaderVeto {
		hc.vetoLifted = true
		return true
	}
	return false
}

// notifier is the internal interface the maintenance server uses to
// mark the gRPC service SERVING / NOT_SERVING. It embeds
// etcdserver.HealthNotifier because the maintenance server holds the
// server-level fan-out notifier (a reloadingNotifier / MultiNotifier)
// rather than the listener-local one, so a Defragment flips every
// registered listener's health.v1 state. The defrag reason string
// ("defrag is active") stays local to this package, which is why the
// exported methods carry no argument.
//
// The interface is kept as a named type (rather than using
// etcdserver.HealthNotifier directly) to preserve the "Alpha stage,
// no API stability promise" contract: any future change to the
// method set is still contained to v3rpc.
type notifier interface {
	etcdserver.HealthNotifier
}

// nopNotifier is a no-op notifier used by callers that do not need
// health-driven SERVING / NOT_SERVING transitions. The in-process
// v3client wrapper (server/etcdserver/api/v3client) plugs a v3rpc
// Maintenance server back into clientv3; that path is meant for
// tests and proxies that already own the underlying state, so a
// nil notifier would panic on the first Defragment() call.
type nopNotifier struct{}

func (nopNotifier) DefragStarted()              {}
func (nopNotifier) DefragFinished()             {}
func (nopNotifier) HealthCheckFailed(string)    {}
func (nopNotifier) HealthCheckRecovered(string) {}

// NopNotifier is the package-exported handle for tests and
// out-of-tree callers that want to construct a MaintenanceServer
// without a live *etcdserver.EtcdServer driving the health state.
var NopNotifier notifier = nopNotifier{}

func newHealthNotifier(hs *health.Server, s *etcdserver.EtcdServer) notifier {
	if hs == nil {
		panic("unexpected nil gRPC health server")
	}
	hc := &healthNotifier{
		hs:                      hs,
		lg:                      s.Logger(),
		reasons:                 make(map[string]time.Time),
		noLeaderVeto:            noLeaderVetoWindow(s.Cfg.ElectionTimeout()),
		stopGRPCServiceOnDefrag: s.FeatureEnabled(features.StopGRPCServiceOnDefrag),
		serverHealthCheck:       s.FeatureEnabled(features.ServerHealthCheck),
	}
	if hc.serverHealthCheck {
		hc.lg.Info("server health check no_leader veto window",
			zap.Duration("window", hc.noLeaderVeto),
			zap.Duration("election-timeout", s.Cfg.ElectionTimeout()),
		)
		hc.warnOnBlindVeto(s.Cfg.HealthCheckTimeout)
	}
	// Set grpc health server as SERVING blindly since the grpc
	// server will serve iff s.ReadyNotify() is closed.
	hc.initServingStatuses()
	return hc
}

// warnOnBlindVeto logs a startup WARN when an explicitly configured
// per-check timeout is large enough to make the no_leader veto
// structurally blind: a hung linear_read probe runs for its whole
// CheckTimeout budget, so a probe that can outlast the veto window
// means no linear_read failure can ever be recorded while the veto is
// still armed. A no_leader streak that outlives the window then ends
// with every member evicted at once — the thundering herd the veto
// exists to prevent. The derived default (ReqTimeout) stays inside
// the window across election-timeout configurations, so this only
// trips on --experimental-health-check-timeout, which has no
// upper-bound validation.
func (hc *healthNotifier) warnOnBlindVeto(explicitCheckTimeout time.Duration) {
	if explicitCheckTimeout == 0 || explicitCheckTimeout <= hc.noLeaderVeto {
		return
	}
	hc.lg.Warn(
		"explicit health check timeout exceeds the no_leader veto window; a wedged linear read cannot be observed while the veto is armed, so a leaderless period past the window may evict every member at once",
		zap.Duration("check-timeout", explicitCheckTimeout),
		zap.Duration("veto-window", hc.noLeaderVeto),
	)
}

// initServingStatuses publishes the initial SERVING state on the
// aggregate entry and on every pre-registered per-check entry. The
// pre-registration is what makes Check never fail with NotFound and
// Watch never stream SERVICE_UNKNOWN before a check has failed for
// the first time.
func (hc *healthNotifier) initServingStatuses() {
	hc.hs.SetServingStatus(allGRPCServices, healthpb.HealthCheckResponse_SERVING)
	hc.lg.Info(
		"grpc service status changed",
		zap.String("service", allGRPCServices),
		zap.String("status", healthpb.HealthCheckResponse_SERVING.String()),
	)
	for _, name := range healthCheckServiceNames {
		hc.hs.SetServingStatus(healthServicePrefix+name, healthpb.HealthCheckResponse_SERVING)
	}
}

// healthNotifier is a reference-counted wrapper around the gRPC
// health server. Each distinct reason that should keep the gRPC
// service NOT_SERVING (defrag in progress, apply_lag exceeded,
// leader lost, learner member, etc.) is recorded in reasons; every
// reason flips its own per-check entry, while the aggregate entry
// tracks only the reasons that aggregateEligible accepts.
//
// Concurrency: startServe / stopServe are called from both the
// maintenance RPC handler (defrag path) and the server-side health
// checker goroutine, so all mutations of reasons are guarded by mu.
// SetServingStatus on the underlying *health.Server is itself
// thread-safe per the grpc-go contract.
type healthNotifier struct {
	hs *health.Server
	lg *zap.Logger

	mu sync.Mutex
	// reasons maps each active failure reason to the time it first
	// failed. The timestamp drives the bounded linear_read veto: a
	// no_leader that has persisted past the veto window stops
	// shielding linear_read from the aggregate entry.
	reasons map[string]time.Time

	// now is the clock used to timestamp reasons and measure the veto
	// window. It is nil in production (currentTime falls back to
	// time.Now) and overridden in tests to drive the window without
	// sleeping.
	now func() time.Time

	// noLeaderVeto is the bound on how long a failing no_leader
	// shields linear_read from the aggregate entry, derived from the
	// election timeout at construction (noLeaderVetoWindow). The zero
	// value disables the veto; production always derives a non-zero
	// value.
	noLeaderVeto time.Duration

	// vetoLifted latches the end of the no_leader veto for the
	// current linear_read failure episode. It is set when a no_leader
	// streak ends (by recovery in startServe, or by window expiry in
	// aggregateEligibleLocked) while linear_read is still failing, and
	// cleared only when linear_read itself recovers: the wedge has
	// survived one full shielded window, so later streaks must not
	// re-shield it. Caller must hold hc.mu.
	vetoLifted bool

	// aggregateNotServing mirrors the published state of the
	// aggregate entry so syncAggregateLocked can skip redundant
	// writes: SetServingStatus pushes to every Watch stream whether
	// or not the value changed. The zero value matches the SERVING
	// state published at startup.
	aggregateNotServing bool

	// defragDepth counts in-flight Defragment RPCs. The handlers
	// serialize only on the backend mutex inside Defragment(), so two
	// of them can overlap around their DefragStarted/DefragFinished
	// pair; the depth keeps the defrag reason registered until the
	// LAST one finishes (a plain set/clear pair would flip the service
	// back to SERVING when the first handler exits). Callers pair the
	// calls structurally (defer in the RPC handler). Caller must hold
	// hc.mu.
	defragDepth int

	// stopGRPCServiceOnDefrag mirrors the legacy StopGRPCServiceOnDefrag
	// feature gate: it is the only path by which a defrag drives the
	// aggregate entry NOT_SERVING (the client-visible drain).
	stopGRPCServiceOnDefrag bool

	// serverHealthCheck mirrors the ServerHealthCheck feature gate: it
	// gates the checker-driven entries and, together with the legacy
	// gate, the etcd.health/defrag observation entry.
	serverHealthCheck bool
}

// currentTime returns the notifier's clock reading, falling back to
// time.Now when now is unset (always the case in production).
func (hc *healthNotifier) currentTime() time.Time {
	if hc.now != nil {
		return hc.now()
	}
	return time.Now()
}

// DefragStarted marks one Defragment RPC in progress. The handlers
// are only serialized on the backend mutex inside Defragment(), so
// overlapping handlers each call this; the depth count keeps the
// service NOT_SERVING until the last one calls DefragFinished.
//
// Either feature gate is enough to register the defrag reason and
// flip the etcd.health/defrag observation entry: observing a defrag
// must not require the legacy drain opt-in (upstream #22635).
// Whether the reason also drives the aggregate entry is decided by
// aggregateEligibleLocked. With both gates off this is a no-op.
func (hc *healthNotifier) DefragStarted() {
	if !hc.stopGRPCServiceOnDefrag && !hc.serverHealthCheck {
		return
	}
	hc.mu.Lock()
	hc.defragDepth++
	hc.mu.Unlock()
	hc.stopServe(defragReason)
}

// DefragFinished marks one Defragment RPC done. The service returns
// to SERVING only when the depth reaches zero, i.e. no other defrag
// handler is still running. The gate check mirrors DefragStarted so
// the depth count stays balanced whichever side was gated off.
func (hc *healthNotifier) DefragFinished() {
	if !hc.stopGRPCServiceOnDefrag && !hc.serverHealthCheck {
		return
	}
	hc.mu.Lock()
	hc.defragDepth--
	stillDefragging := hc.defragDepth > 0
	hc.mu.Unlock()
	if stillDefragging {
		return
	}
	hc.startServe(defragReason)
}

// healthCheckFailed is invoked by the server-side health checker
// (etcdserver/health) with a short, machine-friendly reason
// ("apply_lag", "no_leader", "linear_read", "learner", ...). The
// reason is namespaced with "health:" so the recovery side can
// selectively clear only health-driven failures without touching
// other reasons such as an in-flight defrag.
func (hc *healthNotifier) HealthCheckFailed(reason string) {
	if !hc.serverHealthCheck {
		return
	}
	hc.stopServe(healthPrefix + reason)
}

// HealthCheckRecovered clears the "health:<reason>" entry that was
// previously registered by HealthCheckFailed(reason) and, if no
// other reason remains in the set, flips the gRPC service back to
// SERVING. Concurrent failure reasons are reference-counted
// independently: recovering "apply_lag" never touches a still-
// failing "no_leader" or "linear_read" reason. A recover call for
// a reason that is not currently registered is a no-op.
//
// The serverHealthCheck feature gate is checked symmetrically with
// HealthCheckFailed so a stray Recovered call (e.g. from a path
// the gate does not gate) cannot accidentally clear a
// gate-disabled reason set.
func (hc *healthNotifier) HealthCheckRecovered(reason string) {
	if !hc.serverHealthCheck {
		return
	}
	hc.startServe(healthPrefix + reason)
}

// healthServiceEntry maps an internal reason to the per-check
// grpc.health.v1 service entry it drives. It returns "" for
// reasons without a dedicated entry, in which case only the
// aggregate entry flips.
func healthServiceEntry(reason string) string {
	switch {
	case strings.HasPrefix(reason, healthPrefix):
		return healthServicePrefix + strings.TrimPrefix(reason, healthPrefix)
	case reason == defragReason:
		return healthServicePrefix + "defrag"
	default:
		return ""
	}
}

// aggregateServingLocked reports whether the aggregate entry should
// advertise SERVING given the reasons currently failing. It is a
// full recompute rather than a counter because eligibility is not a
// property of a reason alone: linear_read only counts while
// no_leader is absent or has persisted past the veto window, so both
// registering and clearing a reason can change the answer for the
// reasons that remain.
func (hc *healthNotifier) aggregateServingLocked(now time.Time) bool {
	for reason := range hc.reasons {
		if hc.aggregateEligibleLocked(reason, now) {
			return false
		}
	}
	return true
}

// syncAggregateLocked writes the aggregate entry when the recompute
// changed its desired state, and reports whether it wrote.
func (hc *healthNotifier) syncAggregateLocked(now time.Time) bool {
	notServing := !hc.aggregateServingLocked(now)
	if notServing == hc.aggregateNotServing {
		return false
	}
	hc.aggregateNotServing = notServing
	status := healthpb.HealthCheckResponse_SERVING
	if notServing {
		status = healthpb.HealthCheckResponse_NOT_SERVING
	}
	hc.hs.SetServingStatus(allGRPCServices, status)
	return true
}

// eligibleReasonsLocked returns the failing reasons that currently
// drive the aggregate entry NOT_SERVING, sorted so logs are stable.
// Caller must hold hc.mu.
func (hc *healthNotifier) eligibleReasonsLocked(now time.Time) []string {
	var eligible []string
	for r := range hc.reasons {
		if hc.aggregateEligibleLocked(r, now) {
			eligible = append(eligible, r)
		}
	}
	sort.Strings(eligible)
	return eligible
}

// logAggregateFlipLocked logs a transition of the aggregate entry
// after syncAggregateLocked reports the published state changed. The
// status is read back from aggregateNotServing rather than assumed
// from the call direction: arming the veto recovers the aggregate
// from stopServe (a failure call) and lifting it degrades the
// aggregate from startServe (a recovery call).
//
// The reason field names what drives the new state: the trigger for
// a recovery (the veto-armer or the last recovered reason), and the
// eligible failing reasons for a degradation — a veto expiry arrives
// on the no_leader replay although no_leader itself never drives the
// aggregate; the unmasked linear_read does.
//
// Caller must hold hc.mu.
func (hc *healthNotifier) logAggregateFlipLocked(trigger string, now time.Time) {
	if !hc.aggregateNotServing {
		hc.lg.Info(
			"grpc service status changed",
			zap.String("service", allGRPCServices),
			zap.String("status", healthpb.HealthCheckResponse_SERVING.String()),
			zap.String("reason", trigger),
		)
		return
	}
	hc.lg.Warn(
		"grpc service status changed",
		zap.String("service", allGRPCServices),
		zap.String("status", healthpb.HealthCheckResponse_NOT_SERVING.String()),
		zap.String("reason", strings.Join(hc.eligibleReasonsLocked(now), ",")),
	)
}

// startServe is the inverse of stopServe: it removes reason from the
// reasons set, flips the reason's per-check service entry back to
// SERVING and recomputes the aggregate entry from what remains.
//
// The recompute runs in both directions. Clearing no_leader while
// linear_read is still failing removes the veto, so the aggregate
// has to flip to NOT_SERVING even though this call is a recovery.
//
// The veto latch is maintained here: clearing no_leader while
// linear_read is still failing latches the veto off for the rest of
// the linear_read episode (see vetoLifted), while clearing linear_read
// itself starts a fresh episode and re-arms the veto.
//
// The "reason not in set" branch is intentionally a no-op rather
// than an error: the recovery side is called from paths (notably
// DefragFinished) that may run after the failure side was a
// no-op (e.g. both defrag gates are off, so DefragStarted
// never inserted the reason). The "delete then recompute" shape
// also makes the transition robust if a future call site clears the
// same reason twice in a row.
func (hc *healthNotifier) startServe(reason string) {
	hc.mu.Lock()
	defer hc.mu.Unlock()
	if _, exists := hc.reasons[reason]; !exists {
		return
	}
	delete(hc.reasons, reason)
	if reason == reasonLinearRead {
		hc.vetoLifted = false
	}
	if reason == reasonNoLeader {
		if _, stillFailing := hc.reasons[reasonLinearRead]; stillFailing {
			hc.vetoLifted = true
		}
	}
	if entry := healthServiceEntry(reason); entry != "" {
		hc.hs.SetServingStatus(entry, healthpb.HealthCheckResponse_SERVING)
	}
	now := hc.currentTime()
	if !hc.syncAggregateLocked(now) {
		// The aggregate did not transition, so this recovery changed
		// only the reason's own per-check entry; log that entry —
		// the recovery-side mirror of the firstTime WARN in
		// stopServe, so a per-check failure that arrived in the logs
		// also visibly clears. When the aggregate does transition,
		// logAggregateFlipLocked covers the call and a second line
		// here would double-report it.
		if entry := healthServiceEntry(reason); entry != "" {
			hc.lg.Info(
				"grpc service status changed",
				zap.String("service", entry),
				zap.String("status", healthpb.HealthCheckResponse_SERVING.String()),
				zap.String("reason", reason),
			)
		}
		return
	}
	hc.logAggregateFlipLocked(reason, now)
}

// stopServe records reason in the reasons set (keyed by the time it
// first started failing), flips the reason's per-check service entry
// to NOT_SERVING and recomputes the aggregate entry.
//
// The aggregate recompute is *not* skipped for an already-present
// reason: eligibility is time-dependent, so a linear_read that has
// been vetoed by no_leader becomes eligible once no_leaderVetoWindow
// elapses, and the checker's per-tick replay is what drives that
// transition. Only the log line is suppressed on a repeat, which is
// the flood guard: HealthCheckFailed fires on every checker tick
// (e.g. a learner member is recorded as NOT_SERVING at the 500ms
// cadence) and logging every tick produces ~7200 identical WARN lines
// per hour.
//
// The WARN names the entry the reason actually drove: for a
// first-time reason whose aggregate did not transition (it was
// already NOT_SERVING from an earlier eligible reason, or the reason
// is not aggregate-eligible at all), that is the per-check entry.
// Aggregate transitions themselves are logged by
// logAggregateFlipLocked with the status that was actually published
// and the reason that drives the new state.
func (hc *healthNotifier) stopServe(reason string) {
	hc.mu.Lock()
	defer hc.mu.Unlock()
	now := hc.currentTime()
	firstTime := false
	if _, exists := hc.reasons[reason]; !exists {
		hc.reasons[reason] = now
		firstTime = true
		if entry := healthServiceEntry(reason); entry != "" {
			hc.hs.SetServingStatus(entry, healthpb.HealthCheckResponse_NOT_SERVING)
		}
	}
	flipped := hc.syncAggregateLocked(now)
	if !firstTime && !flipped {
		return
	}
	if flipped {
		hc.logAggregateFlipLocked(reason, now)
		return
	}
	// firstTime && !flipped: the aggregate was already NOT_SERVING,
	// so this reason drove only its own per-check entry. Naming the
	// aggregate entry here would log a flip that never happened.
	service := healthServiceEntry(reason)
	hc.lg.Warn(
		"grpc service status changed",
		zap.String("service", service),
		zap.String("status", healthpb.HealthCheckResponse_NOT_SERVING.String()),
		zap.String("reason", reason),
	)
}
