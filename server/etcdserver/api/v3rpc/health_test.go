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

package v3rpc

import (
	"context"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"

	"go.etcd.io/etcd/server/v3/etcdserver"
)

// newTestHealthNotifier constructs a healthNotifier suitable for unit
// tests. The package-private constructor is bypassed because
// newHealthNotifier requires a live *etcdserver.EtcdServer, which
// would pull in the entire storage stack. The test instance has all
// feature gates enabled so every code path is reachable.
func newTestHealthNotifier() (*healthNotifier, *health.Server) {
	return newTestHealthNotifierGates(true, true)
}

// newTestHealthNotifierGates constructs a healthNotifier with the two
// defrag-relevant feature gates set explicitly, so tests can pin each
// gate's half of the contract: ServerHealthCheck alone must flip the
// etcd.health/defrag observation entry (upstream #22635), while only
// StopGRPCServiceOnDefrag lets a defrag drive the aggregate entry.
func newTestHealthNotifierGates(stopGRPCServiceOnDefrag, serverHealthCheck bool) (*healthNotifier, *health.Server) {
	hs := health.NewServer()
	hc := &healthNotifier{
		hs:                      hs,
		lg:                      zap.NewNop(),
		reasons:                 make(map[string]time.Time),
		noLeaderVeto:            defaultNoLeaderVetoWindow,
		stopGRPCServiceOnDefrag: stopGRPCServiceOnDefrag,
		serverHealthCheck:       serverHealthCheck,
	}
	hc.initServingStatuses()
	return hc, hs
}

// currentStatus looks up the gRPC health state for the wildcard
// service. health.Server does not expose a public getter, so we
// round-trip through the Check RPC.
func currentStatus(t *testing.T, hs *health.Server) healthpb.HealthCheckResponse_ServingStatus {
	t.Helper()
	return serviceStatus(t, hs, allGRPCServices)
}

// serviceStatus looks up the gRPC health state for an arbitrary
// service entry.
func serviceStatus(t *testing.T, hs *health.Server, service string) healthpb.HealthCheckResponse_ServingStatus {
	t.Helper()
	resp, err := hs.Check(context.TODO(), &healthpb.HealthCheckRequest{Service: service})
	if err != nil {
		t.Fatalf("health.Check(%q): %v", service, err)
	}
	return resp.GetStatus()
}

// perCheckServiceNames pins the per-check grpc.health.v1 service
// entry names. These names are an external contract: once clients
// start calling Check/Watch on them they cannot be renamed, so the
// literals are duplicated here on purpose to catch accidental
// renames in the production constants.
var perCheckServiceNames = []string{
	"etcd.health/linear_read",
	"etcd.health/serializable_read",
	"etcd.health/apply_lag",
	"etcd.health/apply_stall",
	"etcd.health/no_leader",
	"etcd.health/data_corruption",
	"etcd.health/learner",
	"etcd.health/defrag",
}

// TestIsRPCSupportedForLearner_HealthRequests: learners must be
// able to answer gRPC health Check/List so they can honestly
// report NOT_SERVING for the aggregate and per-check entries
// instead of failing the RPC with "rpc not supported for learner"
// (upstream issue #18801).
func TestIsRPCSupportedForLearner_HealthRequests(t *testing.T) {
	if !isRPCSupportedForLearner(&healthpb.HealthCheckRequest{}) {
		t.Fatal("HealthCheckRequest must be allowed for learners")
	}
	if !isRPCSupportedForLearner(&healthpb.HealthListRequest{}) {
		t.Fatal("HealthListRequest must be allowed for learners")
	}
}

// TestIsRPCSupportedForLearnerStream_HealthWatch: the health Watch
// stream must be allowed on learners for the same reason as
// Check/List, while Snapshot stays allowed (regression) and KV
// Watch stays rejected.
func TestIsRPCSupportedForLearnerStream_HealthWatch(t *testing.T) {
	if !isRPCSupportedForLearnerStream("/grpc.health.v1.Health/Watch") {
		t.Fatal("grpc.health.v1.Health/Watch must be allowed for learners")
	}
	if !isRPCSupportedForLearnerStream(snapshotMethod) {
		t.Fatalf("%s must stay allowed for learners", snapshotMethod)
	}
	if isRPCSupportedForLearnerStream("/etcdserverpb.Watch/Watch") {
		t.Fatal("etcdserverpb.Watch/Watch must stay rejected for learners")
	}
}

// TestHealthNotifier_PerCheckEntriesPreRegistered: every per-check
// service entry must exist and report SERVING from startup, before
// any health failure. Without pre-registration a Check on the entry
// fails with NotFound and a Watch immediately streams
// SERVICE_UNKNOWN — both ambiguous to standard clients.
func TestHealthNotifier_PerCheckEntriesPreRegistered(t *testing.T) {
	_, hs := newTestHealthNotifier()
	for _, name := range perCheckServiceNames {
		if got := serviceStatus(t, hs, name); got != healthpb.HealthCheckResponse_SERVING {
			t.Fatalf("entry %q = %v, want SERVING", name, got)
		}
	}
}

// TestHealthNotifier_DataCorruptionDrivesAggregate pins the
// eligibility decision for data_corruption: the check fails only on
// the member a CORRUPT alarm names (or on every member when the alarm
// carries the upstream MemberID=0 "whole cluster" sentinel), so an
// aggregate flip always moves clients to a member that is not named
// corrupt. If it were ever moved into clusterWideReasons, a member
// with known-corrupt data would keep receiving new requests while
// reads served from it hand clients untrustworthy data.
func TestHealthNotifier_DataCorruptionDrivesAggregate(t *testing.T) {
	hc, hs := newTestHealthNotifier()

	hc.HealthCheckFailed("data_corruption")
	if got := serviceStatus(t, hs, "etcd.health/data_corruption"); got != healthpb.HealthCheckResponse_NOT_SERVING {
		t.Fatalf("etcd.health/data_corruption = %v, want NOT_SERVING", got)
	}
	if got := currentStatus(t, hs); got != healthpb.HealthCheckResponse_NOT_SERVING {
		t.Fatalf("aggregate = %v, want NOT_SERVING (data_corruption must stay aggregate-eligible)", got)
	}

	hc.HealthCheckRecovered("data_corruption")
	if got := serviceStatus(t, hs, "etcd.health/data_corruption"); got != healthpb.HealthCheckResponse_SERVING {
		t.Fatalf("etcd.health/data_corruption = %v, want SERVING after recovery", got)
	}
}

// TestHealthNotifier_PerCheckEntryFlipsWithFailure: a health check
// failure must flip its own entry to NOT_SERVING while leaving the
// other per-check entries untouched (per-check isolation mirrors the
// reason refcounting). linear_read is used because it drives the
// aggregate entry; see
// TestHealthNotifier_ClusterWideReasonDoesNotFlipAggregate for the
// reasons that do not.
func TestHealthNotifier_PerCheckEntryFlipsWithFailure(t *testing.T) {
	hc, hs := newTestHealthNotifier()

	hc.HealthCheckFailed("linear_read")
	if got := serviceStatus(t, hs, "etcd.health/linear_read"); got != healthpb.HealthCheckResponse_NOT_SERVING {
		t.Fatalf("etcd.health/linear_read = %v, want NOT_SERVING", got)
	}
	if got := serviceStatus(t, hs, "etcd.health/no_leader"); got != healthpb.HealthCheckResponse_SERVING {
		t.Fatalf("etcd.health/no_leader = %v, want SERVING (isolation)", got)
	}
	if got := currentStatus(t, hs); got != healthpb.HealthCheckResponse_NOT_SERVING {
		t.Fatalf("aggregate = %v, want NOT_SERVING", got)
	}
}

// TestHealthNotifier_ApplyStallDrivesAggregate pins the A2
// eligibility decision: apply_stall is a member-local wedge signal
// (this member cannot complete any new committed entry while the
// cluster moves on), so it must drive the aggregate entry that
// client-side health routing consumes. If it were ever added to
// clusterWideReasons, a wedged member would keep receiving new
// requests and each would hang until its request budget expired.
func TestHealthNotifier_ApplyStallDrivesAggregate(t *testing.T) {
	hc, hs := newTestHealthNotifier()

	hc.HealthCheckFailed("apply_stall")
	if got := serviceStatus(t, hs, "etcd.health/apply_stall"); got != healthpb.HealthCheckResponse_NOT_SERVING {
		t.Fatalf("etcd.health/apply_stall = %v, want NOT_SERVING", got)
	}
	if got := currentStatus(t, hs); got != healthpb.HealthCheckResponse_NOT_SERVING {
		t.Fatalf("aggregate = %v, want NOT_SERVING (apply_stall must stay aggregate-eligible)", got)
	}

	hc.HealthCheckRecovered("apply_stall")
	if got := serviceStatus(t, hs, "etcd.health/apply_stall"); got != healthpb.HealthCheckResponse_SERVING {
		t.Fatalf("etcd.health/apply_stall = %v, want SERVING after recovery", got)
	}
	if got := currentStatus(t, hs); got != healthpb.HealthCheckResponse_SERVING {
		t.Fatalf("aggregate = %v, want SERVING after recovery", got)
	}
}

// TestHealthNotifier_SerializableReadDrivesAggregate pins the
// eligibility decision for serializable_read: a serializable read
// has no consensus component (it is the local read path alone), so
// its failure means the member cannot serve ANY read — the strongest
// member-local eviction signal in the chain. If it were ever moved
// into clusterWideReasons, a member with a wedged local read path on
// a write-idle cluster would never be evicted (apply_stall requires
// committed to advance) and clients would keep sending reads that
// hang until their request budget expires.
func TestHealthNotifier_SerializableReadDrivesAggregate(t *testing.T) {
	hc, hs := newTestHealthNotifier()

	hc.HealthCheckFailed("serializable_read")
	if got := serviceStatus(t, hs, "etcd.health/serializable_read"); got != healthpb.HealthCheckResponse_NOT_SERVING {
		t.Fatalf("etcd.health/serializable_read = %v, want NOT_SERVING", got)
	}
	if got := currentStatus(t, hs); got != healthpb.HealthCheckResponse_NOT_SERVING {
		t.Fatalf("aggregate = %v, want NOT_SERVING (serializable_read must stay aggregate-eligible)", got)
	}

	hc.HealthCheckRecovered("serializable_read")
	if got := serviceStatus(t, hs, "etcd.health/serializable_read"); got != healthpb.HealthCheckResponse_SERVING {
		t.Fatalf("etcd.health/serializable_read = %v, want SERVING after recovery", got)
	}
	if got := currentStatus(t, hs); got != healthpb.HealthCheckResponse_SERVING {
		t.Fatalf("aggregate = %v, want SERVING after recovery", got)
	}
}

// TestHealthNotifier_LinearReadFailsWhileSerializableGreen pins the
// attribution contract that is the reason serializable_read exists
// (the Q4 open question): the two reasons are diagnosed
// independently, and a green serializable_read must NOT rescue the
// aggregate from a failing linear_read — a member that cannot
// complete linearizable reads is useless to clients that need
// linearizability (apiserver), whatever its serializable capability.
// The per-check entries are what let an operator read the split:
// linear_read NOT_SERVING + serializable_read SERVING isolates the
// failure to the consensus path.
func TestHealthNotifier_LinearReadFailsWhileSerializableGreen(t *testing.T) {
	hc, hs := newTestHealthNotifier()

	hc.HealthCheckFailed("linear_read")
	if got := serviceStatus(t, hs, "etcd.health/linear_read"); got != healthpb.HealthCheckResponse_NOT_SERVING {
		t.Fatalf("etcd.health/linear_read = %v, want NOT_SERVING", got)
	}
	if got := serviceStatus(t, hs, "etcd.health/serializable_read"); got != healthpb.HealthCheckResponse_SERVING {
		t.Fatalf("etcd.health/serializable_read = %v, want SERVING (a linear_read failure must not touch it)", got)
	}
	if got := currentStatus(t, hs); got != healthpb.HealthCheckResponse_NOT_SERVING {
		t.Fatal("aggregate must flip on linear_read alone; serializable_read being green is not a rescue signal")
	}
}

// TestHealthNotifier_PerCheckEntryRecovers: recovering a reason must
// flip its entry back to SERVING and, when it was the only reason,
// restore the aggregate too.
func TestHealthNotifier_PerCheckEntryRecovers(t *testing.T) {
	hc, hs := newTestHealthNotifier()
	hc.HealthCheckFailed("linear_read")

	hc.HealthCheckRecovered("linear_read")
	if got := serviceStatus(t, hs, "etcd.health/linear_read"); got != healthpb.HealthCheckResponse_SERVING {
		t.Fatalf("etcd.health/linear_read = %v, want SERVING", got)
	}
	if got := currentStatus(t, hs); got != healthpb.HealthCheckResponse_SERVING {
		t.Fatalf("aggregate = %v, want SERVING", got)
	}
}

// TestHealthNotifier_PerCheckEntryPartialRecovery: with two reasons
// active, recovering one flips only that entry; the other entry and
// the aggregate stay NOT_SERVING.
func TestHealthNotifier_PerCheckEntryPartialRecovery(t *testing.T) {
	hc, hs := newTestHealthNotifier()
	hc.HealthCheckFailed("linear_read")
	hc.HealthCheckFailed("learner")

	hc.HealthCheckRecovered("linear_read")
	if got := serviceStatus(t, hs, "etcd.health/linear_read"); got != healthpb.HealthCheckResponse_SERVING {
		t.Fatalf("etcd.health/linear_read = %v, want SERVING", got)
	}
	if got := serviceStatus(t, hs, "etcd.health/learner"); got != healthpb.HealthCheckResponse_NOT_SERVING {
		t.Fatalf("etcd.health/learner = %v, want NOT_SERVING", got)
	}
	if got := currentStatus(t, hs); got != healthpb.HealthCheckResponse_NOT_SERVING {
		t.Fatalf("aggregate = %v, want NOT_SERVING", got)
	}
}

// TestHealthNotifier_PerCheckDefragEntry: the defrag path drives its
// own etcd.health/defrag entry in lockstep with the aggregate.
func TestHealthNotifier_PerCheckDefragEntry(t *testing.T) {
	hc, hs := newTestHealthNotifier()

	hc.DefragStarted()
	if got := serviceStatus(t, hs, "etcd.health/defrag"); got != healthpb.HealthCheckResponse_NOT_SERVING {
		t.Fatalf("etcd.health/defrag = %v, want NOT_SERVING during defrag", got)
	}
	if got := currentStatus(t, hs); got != healthpb.HealthCheckResponse_NOT_SERVING {
		t.Fatalf("aggregate = %v, want NOT_SERVING during defrag", got)
	}

	hc.DefragFinished()
	if got := serviceStatus(t, hs, "etcd.health/defrag"); got != healthpb.HealthCheckResponse_SERVING {
		t.Fatalf("etcd.health/defrag = %v, want SERVING after defrag", got)
	}
	if got := currentStatus(t, hs); got != healthpb.HealthCheckResponse_SERVING {
		t.Fatalf("aggregate = %v, want SERVING after defrag", got)
	}
}

// TestHealthNotifier_DefragObservationWithoutLegacyGate pins the
// upstream #22635 contract: with ServerHealthCheck on and the legacy
// StopGRPCServiceOnDefrag gate off (its default), a defrag still
// flips the etcd.health/defrag observation entry — observing a defrag
// must not require the legacy drain opt-in — while the aggregate
// entry stays SERVING, because a drain clients never opted into must
// not evict them.
func TestHealthNotifier_DefragObservationWithoutLegacyGate(t *testing.T) {
	hc, hs := newTestHealthNotifierGates(false, true)

	hc.DefragStarted()
	if got := serviceStatus(t, hs, "etcd.health/defrag"); got != healthpb.HealthCheckResponse_NOT_SERVING {
		t.Fatalf("etcd.health/defrag = %v, want NOT_SERVING during defrag", got)
	}
	if got := currentStatus(t, hs); got != healthpb.HealthCheckResponse_SERVING {
		t.Fatalf("aggregate = %v, want SERVING: defrag without the legacy gate must not drive the aggregate", got)
	}

	hc.DefragFinished()
	if got := serviceStatus(t, hs, "etcd.health/defrag"); got != healthpb.HealthCheckResponse_SERVING {
		t.Fatalf("etcd.health/defrag = %v, want SERVING after defrag", got)
	}
	if got := currentStatus(t, hs); got != healthpb.HealthCheckResponse_SERVING {
		t.Fatalf("aggregate = %v, want SERVING after defrag", got)
	}
}

// TestHealthNotifier_LegacyDefragGateAloneDrainsAggregate: with
// StopGRPCServiceOnDefrag on and ServerHealthCheck off, a defrag
// keeps the legacy behavior — the aggregate entry itself goes
// NOT_SERVING for the defrag window (the drain clients opted into),
// with the per-check entry flipping in lockstep.
func TestHealthNotifier_LegacyDefragGateAloneDrainsAggregate(t *testing.T) {
	hc, hs := newTestHealthNotifierGates(true, false)

	hc.DefragStarted()
	if got := currentStatus(t, hs); got != healthpb.HealthCheckResponse_NOT_SERVING {
		t.Fatalf("aggregate = %v, want NOT_SERVING during defrag under the legacy gate", got)
	}
	if got := serviceStatus(t, hs, "etcd.health/defrag"); got != healthpb.HealthCheckResponse_NOT_SERVING {
		t.Fatalf("etcd.health/defrag = %v, want NOT_SERVING during defrag", got)
	}

	hc.DefragFinished()
	if got := currentStatus(t, hs); got != healthpb.HealthCheckResponse_SERVING {
		t.Fatalf("aggregate = %v, want SERVING after defrag", got)
	}
	if got := serviceStatus(t, hs, "etcd.health/defrag"); got != healthpb.HealthCheckResponse_SERVING {
		t.Fatalf("etcd.health/defrag = %v, want SERVING after defrag", got)
	}
}

// TestHealthNotifier_DefragRequiresEitherGate: with both gates off,
// DefragStarted / DefragFinished must be complete no-ops — no reason
// registered, no entry (aggregate or per-check) flips. This is the
// behavior of a fully legacy configuration that opted into neither
// feature, and it keeps the Started/Finished depth pairing balanced.
func TestHealthNotifier_DefragRequiresEitherGate(t *testing.T) {
	hc, hs := newTestHealthNotifierGates(false, false)

	hc.DefragStarted()
	if got := currentStatus(t, hs); got != healthpb.HealthCheckResponse_SERVING {
		t.Fatalf("aggregate = %v, want SERVING with both defrag gates off", got)
	}
	if got := serviceStatus(t, hs, "etcd.health/defrag"); got != healthpb.HealthCheckResponse_SERVING {
		t.Fatalf("etcd.health/defrag = %v, want SERVING with both defrag gates off", got)
	}

	hc.DefragFinished()
	if got := currentStatus(t, hs); got != healthpb.HealthCheckResponse_SERVING {
		t.Fatalf("aggregate = %v, want SERVING after a gated-off defrag", got)
	}
	if got := serviceStatus(t, hs, "etcd.health/defrag"); got != healthpb.HealthCheckResponse_SERVING {
		t.Fatalf("etcd.health/defrag = %v, want SERVING after a gated-off defrag", got)
	}
}

// TestHealthNotifier_DefragObservationDoesNotMaskHealthFailures: with
// only ServerHealthCheck on, an in-flight defrag neither drives nor
// shields the aggregate: a concurrently failing linear_read keeps the
// aggregate NOT_SERVING across the whole defrag window, and the
// defrag entry recovers independently of it.
func TestHealthNotifier_DefragObservationDoesNotMaskHealthFailures(t *testing.T) {
	hc, hs := newTestHealthNotifierGates(false, true)

	hc.DefragStarted()
	hc.HealthCheckFailed("linear_read")
	if got := currentStatus(t, hs); got != healthpb.HealthCheckResponse_NOT_SERVING {
		t.Fatalf("aggregate = %v, want NOT_SERVING driven by linear_read while defrag only observes", got)
	}

	hc.DefragFinished()
	if got := currentStatus(t, hs); got != healthpb.HealthCheckResponse_NOT_SERVING {
		t.Fatalf("aggregate = %v, want NOT_SERVING until linear_read recovers, independent of defrag", got)
	}
	if got := serviceStatus(t, hs, "etcd.health/defrag"); got != healthpb.HealthCheckResponse_SERVING {
		t.Fatalf("etcd.health/defrag = %v, want SERVING after defrag finished", got)
	}

	hc.HealthCheckRecovered("linear_read")
	if got := currentStatus(t, hs); got != healthpb.HealthCheckResponse_SERVING {
		t.Fatalf("aggregate = %v, want SERVING after linear_read recovered", got)
	}
}

// TestHealthNotifier_DefragRefCount: a defrag Started/Finished pair
// must flip the gRPC state NOT_SERVING -> SERVING.
func TestHealthNotifier_DefragRefCount(t *testing.T) {
	hc, hs := newTestHealthNotifier()
	if got := currentStatus(t, hs); got != healthpb.HealthCheckResponse_SERVING {
		t.Fatalf("initial status = %v, want SERVING", got)
	}

	hc.DefragStarted()
	if got := currentStatus(t, hs); got != healthpb.HealthCheckResponse_NOT_SERVING {
		t.Fatalf("after DefragStarted status = %v, want NOT_SERVING", got)
	}

	hc.DefragFinished()
	if got := currentStatus(t, hs); got != healthpb.HealthCheckResponse_SERVING {
		t.Fatalf("after DefragFinished status = %v, want SERVING", got)
	}
}

// TestHealthNotifier_ConcurrentDefragsStayNotServingUntilLastFinishes:
// Defragment RPCs serialize only on the backend mutex inside
// Defragment(), so two handlers can hold DefragStarted at the same
// time. The depth count must keep the NOT_SERVING window open until
// the LAST defrag finishes: with a plain set/clear pair, the first
// handler's DefragFinished would report SERVING while the second
// defrag is still running.
func TestHealthNotifier_ConcurrentDefragsStayNotServingUntilLastFinishes(t *testing.T) {
	hc, hs := newTestHealthNotifier()

	hc.DefragStarted()  // defrag A
	hc.DefragStarted()  // defrag B overlaps A around the backend mutex
	hc.DefragFinished() // A ends first; B is still defragmenting
	if got := currentStatus(t, hs); got != healthpb.HealthCheckResponse_NOT_SERVING {
		t.Fatalf("after the first of two concurrent defrags finished, aggregate = %v, want NOT_SERVING", got)
	}
	if got := serviceStatus(t, hs, "etcd.health/defrag"); got != healthpb.HealthCheckResponse_NOT_SERVING {
		t.Fatalf("etcd.health/defrag = %v, want NOT_SERVING until the last defrag finishes", got)
	}

	hc.DefragFinished() // B ends
	if got := currentStatus(t, hs); got != healthpb.HealthCheckResponse_SERVING {
		t.Fatalf("after all defrags finished, aggregate = %v, want SERVING", got)
	}
	if got := serviceStatus(t, hs, "etcd.health/defrag"); got != healthpb.HealthCheckResponse_SERVING {
		t.Fatalf("etcd.health/defrag = %v, want SERVING after all defrags finished", got)
	}
}

// TestHealthNotifier_DefragFansOutAcrossNotifiers reproduces the
// dual-listener topology at unit level: two
// listeners publish two independent *health.Server instances, each
// wrapped in its own healthNotifier registered on the same
// EtcdServer. The server-level MultiNotifier that a Defragment
// drives must flip BOTH health servers' aggregate and
// etcd.health/defrag entries — with the listener-local wiring, defrag
// only reached the notifier of the listener the RPC arrived on and
// the other listener kept reporting SERVING for the whole defrag
// window.
func TestHealthNotifier_DefragFansOutAcrossNotifiers(t *testing.T) {
	hc1, hs1 := newTestHealthNotifier()
	hc2, hs2 := newTestHealthNotifier()
	multi := etcdserver.NewMultiNotifier([]etcdserver.HealthNotifier{hc1, hc2})

	multi.DefragStarted()
	for i, hs := range []*health.Server{hs1, hs2} {
		if got := serviceStatus(t, hs, "etcd.health/defrag"); got != healthpb.HealthCheckResponse_NOT_SERVING {
			t.Fatalf("listener %d etcd.health/defrag = %v, want NOT_SERVING during defrag", i, got)
		}
		if got := currentStatus(t, hs); got != healthpb.HealthCheckResponse_NOT_SERVING {
			t.Fatalf("listener %d aggregate = %v, want NOT_SERVING during defrag", i, got)
		}
	}

	multi.DefragFinished()
	for i, hs := range []*health.Server{hs1, hs2} {
		if got := serviceStatus(t, hs, "etcd.health/defrag"); got != healthpb.HealthCheckResponse_SERVING {
			t.Fatalf("listener %d etcd.health/defrag = %v, want SERVING after defrag", i, got)
		}
		if got := currentStatus(t, hs); got != healthpb.HealthCheckResponse_SERVING {
			t.Fatalf("listener %d aggregate = %v, want SERVING after defrag", i, got)
		}
	}
}

// TestHealthNotifier_HealthRefCountIsolated: a health failure must
// not interfere with a defrag failure, and vice versa. The set of
// reasons is shared; defrag is depth-counted by the notifier itself,
// health reasons each hold their own map entry.
func TestHealthNotifier_HealthRefCountIsolated(t *testing.T) {
	hc, hs := newTestHealthNotifier()

	hc.DefragStarted()
	hc.HealthCheckFailed("linear_read")
	if got := currentStatus(t, hs); got != healthpb.HealthCheckResponse_NOT_SERVING {
		t.Fatalf("expected NOT_SERVING while both reasons active, got %v", got)
	}

	// Clear only the health reason; defrag should keep us NOT_SERVING.
	hc.HealthCheckRecovered("linear_read")
	if got := currentStatus(t, hs); got != healthpb.HealthCheckResponse_NOT_SERVING {
		t.Fatalf("expected NOT_SERVING while defrag still active, got %v", got)
	}

	hc.DefragFinished()
	if got := currentStatus(t, hs); got != healthpb.HealthCheckResponse_SERVING {
		t.Fatalf("expected SERVING after both reasons cleared, got %v", got)
	}
}

// TestHealthNotifier_HealthRecoverDoesNotClearOtherReasons pins the
// multi-reason isolation contract: when several health:* reasons
// are simultaneously active, recovering one must leave the others
// untouched so the gRPC service stays NOT_SERVING until every
// reason has been cleared. Before the fix this test exposed the
// bug where HealthCheckRecovered cleared every health:* entry.
func TestHealthNotifier_HealthRecoverDoesNotClearOtherReasons(t *testing.T) {
	hc, hs := newTestHealthNotifier()

	hc.HealthCheckFailed("linear_read")
	hc.HealthCheckFailed("learner")
	if got := currentStatus(t, hs); got != healthpb.HealthCheckResponse_NOT_SERVING {
		t.Fatalf("expected NOT_SERVING while both reasons active, got %v", got)
	}

	hc.HealthCheckRecovered("linear_read")
	if got := currentStatus(t, hs); got != healthpb.HealthCheckResponse_NOT_SERVING {
		t.Fatalf("expected NOT_SERVING after recovering linear_read (learner still active), got %v", got)
	}

	hc.HealthCheckRecovered("learner")
	if got := currentStatus(t, hs); got != healthpb.HealthCheckResponse_SERVING {
		t.Fatalf("expected SERVING after both reasons cleared, got %v", got)
	}
}

// TestHealthNotifier_HealthRecoverUnknownReasonIsNoop ensures that
// calling HealthCheckRecovered with a reason that was never
// registered leaves the gRPC state untouched.
func TestHealthNotifier_HealthRecoverUnknownReasonIsNoop(t *testing.T) {
	hc, hs := newTestHealthNotifier()

	hc.HealthCheckFailed("linear_read")
	hc.HealthCheckRecovered("never_set")
	if got := currentStatus(t, hs); got != healthpb.HealthCheckResponse_NOT_SERVING {
		t.Fatalf("expected NOT_SERVING, got %v", got)
	}

	hc.HealthCheckRecovered("linear_read")
	if got := currentStatus(t, hs); got != healthpb.HealthCheckResponse_SERVING {
		t.Fatalf("expected SERVING after recovering the only reason, got %v", got)
	}
}

// TestHealthNotifier_ClusterWideReasonDoesNotFlipAggregate pins the
// narrowed aggregate contract: no_leader and apply_lag hold on every
// member of a cluster at the same time, so evicting this member buys
// nothing and only tears down its Watch streams. They must flip
// their own etcd.health/* entry while leaving the aggregate SERVING.
func TestHealthNotifier_ClusterWideReasonDoesNotFlipAggregate(t *testing.T) {
	for _, reason := range []string{"apply_lag", "no_leader"} {
		t.Run(reason, func(t *testing.T) {
			hc, hs := newTestHealthNotifier()

			hc.HealthCheckFailed(reason)
			if got := serviceStatus(t, hs, healthServicePrefix+reason); got != healthpb.HealthCheckResponse_NOT_SERVING {
				t.Fatalf("etcd.health/%s = %v, want NOT_SERVING", reason, got)
			}
			if got := currentStatus(t, hs); got != healthpb.HealthCheckResponse_SERVING {
				t.Fatalf("aggregate = %v, want SERVING for cluster-wide reason %q", got, reason)
			}

			hc.HealthCheckRecovered(reason)
			if got := currentStatus(t, hs); got != healthpb.HealthCheckResponse_SERVING {
				t.Fatalf("aggregate = %v after recovery, want SERVING", got)
			}
		})
	}

	t.Run("both_cluster_wide_at_once", func(t *testing.T) {
		hc, hs := newTestHealthNotifier()
		hc.HealthCheckFailed("no_leader")
		hc.HealthCheckFailed("apply_lag")
		if got := currentStatus(t, hs); got != healthpb.HealthCheckResponse_SERVING {
			t.Fatalf("aggregate = %v, want SERVING while only cluster-wide reasons are failing", got)
		}
	})
}

// TestHealthNotifier_MemberLocalReasonsFlipAggregate covers the
// reasons that do justify eviction: learner and defrag are local to
// this member, so a client that moves away gets a strictly better
// peer.
func TestHealthNotifier_MemberLocalReasonsFlipAggregate(t *testing.T) {
	hc, hs := newTestHealthNotifier()

	hc.HealthCheckFailed("learner")
	if got := currentStatus(t, hs); got != healthpb.HealthCheckResponse_NOT_SERVING {
		t.Fatalf("aggregate = %v, want NOT_SERVING for learner", got)
	}
	hc.HealthCheckRecovered("learner")
	if got := currentStatus(t, hs); got != healthpb.HealthCheckResponse_SERVING {
		t.Fatalf("aggregate = %v, want SERVING after learner cleared", got)
	}

	hc.DefragStarted()
	if got := currentStatus(t, hs); got != healthpb.HealthCheckResponse_NOT_SERVING {
		t.Fatalf("aggregate = %v, want NOT_SERVING during defrag", got)
	}
	hc.DefragFinished()
	if got := currentStatus(t, hs); got != healthpb.HealthCheckResponse_SERVING {
		t.Fatalf("aggregate = %v, want SERVING after defrag", got)
	}
}

// TestHealthNotifier_LinearReadVetoedByNoLeader covers the one
// conditional rule: a stuck linearizable read is worth evicting for
// when a leader exists (the problem is local, upstream #22214), but
// not when the cluster has no leader (all three members time out and
// evicting them all turns one ErrNoLeader into 100 client retries).
func TestHealthNotifier_LinearReadVetoedByNoLeader(t *testing.T) {
	for name, order := range map[string][2]string{
		"no_leader_first":   {"no_leader", "linear_read"},
		"linear_read_first": {"linear_read", "no_leader"},
	} {
		t.Run(name, func(t *testing.T) {
			hc, hs := newTestHealthNotifier()

			hc.HealthCheckFailed(order[0])
			hc.HealthCheckFailed(order[1])
			if got := serviceStatus(t, hs, healthServicePrefix+"linear_read"); got != healthpb.HealthCheckResponse_NOT_SERVING {
				t.Fatalf("etcd.health/linear_read = %v, want NOT_SERVING", got)
			}
			if got := currentStatus(t, hs); got != healthpb.HealthCheckResponse_SERVING {
				t.Fatalf("aggregate = %v, want SERVING while no_leader vetoes linear_read", got)
			}
		})
	}
}

// TestHealthNotifier_LinearReadFlipsAggregateAfterNoLeaderRecovers
// covers the recovery recompute: clearing the veto must re-evaluate
// the aggregate from the reasons still failing, not just remove the
// recovered one. A pure delete would leave a wedged-read member
// advertised as SERVING for as long as the read stays stuck.
func TestHealthNotifier_LinearReadFlipsAggregateAfterNoLeaderRecovers(t *testing.T) {
	hc, hs := newTestHealthNotifier()

	hc.HealthCheckFailed("linear_read")
	hc.HealthCheckFailed("no_leader")
	if got := currentStatus(t, hs); got != healthpb.HealthCheckResponse_SERVING {
		t.Fatalf("aggregate = %v, want SERVING while no_leader is active", got)
	}

	hc.HealthCheckRecovered("no_leader")
	if got := serviceStatus(t, hs, healthServicePrefix+"linear_read"); got != healthpb.HealthCheckResponse_NOT_SERVING {
		t.Fatalf("etcd.health/linear_read = %v, want NOT_SERVING", got)
	}
	if got := currentStatus(t, hs); got != healthpb.HealthCheckResponse_NOT_SERVING {
		t.Fatalf("aggregate = %v, want NOT_SERVING once the no_leader veto is gone", got)
	}

	// The symmetric case: clearing the local reason while the veto
	// stands must keep the aggregate SERVING.
	hc.HealthCheckRecovered("linear_read")
	if got := currentStatus(t, hs); got != healthpb.HealthCheckResponse_SERVING {
		t.Fatalf("aggregate = %v, want SERVING after all reasons cleared", got)
	}
}

// fakeClock returns a clock function and an advance helper so tests
// can drive noLeaderVetoWindow deterministically instead of sleeping.
func fakeClock() (func() time.Time, func(time.Duration)) {
	current := time.Unix(1700000000, 0)
	return func() time.Time { return current },
		func(d time.Duration) { current = current.Add(d) }
}

// TestHealthNotifier_NoLeaderVetoExpires pins the bounded veto: a
// member still leaderless past noLeaderVetoWindow is a minority
// partition peer, not a transient election, so a still-failing
// linear_read must count towards the aggregate entry again. The flip
// is driven by the checker's per-tick replay of HealthCheckFailed,
// which is why a repeat call for an already-recorded reason must
// still recompute the aggregate rather than early-returning.
func TestHealthNotifier_NoLeaderVetoExpires(t *testing.T) {
	hc, hs := newTestHealthNotifier()
	clock, advance := fakeClock()
	hc.now = clock

	hc.HealthCheckFailed("linear_read")
	hc.HealthCheckFailed("no_leader")
	if got := currentStatus(t, hs); got != healthpb.HealthCheckResponse_SERVING {
		t.Fatalf("aggregate = %v, want SERVING while the veto is armed", got)
	}

	advance(defaultNoLeaderVetoWindow - time.Second)
	hc.HealthCheckFailed("linear_read")
	if got := currentStatus(t, hs); got != healthpb.HealthCheckResponse_SERVING {
		t.Fatalf("aggregate = %v, want SERVING before the veto window elapses", got)
	}

	advance(time.Second)
	hc.HealthCheckFailed("linear_read")
	if got := currentStatus(t, hs); got != healthpb.HealthCheckResponse_NOT_SERVING {
		t.Fatalf("aggregate = %v, want NOT_SERVING once the veto window elapsed", got)
	}

	// no_leader recovering afterwards must not resurrect the veto:
	// linear_read is still failing and still eligible.
	hc.HealthCheckRecovered("no_leader")
	if got := currentStatus(t, hs); got != healthpb.HealthCheckResponse_NOT_SERVING {
		t.Fatalf("aggregate = %v, want NOT_SERVING while linear_read still fails", got)
	}
}

// TestHealthNotifier_NoLeaderVetoDoesNotRearmAfterStreakEnds pins the
// latch for the streak that ends by recovery: a no_leader streak that
// ends while linear_read is still failing has exhausted the veto for
// this linear_read episode. A later streak must not re-shield the
// still-wedged read with a fresh window — without the latch, a cluster
// with leadership churn (streaks shorter than the window) can pull a
// bad member back to SERVING indefinitely.
func TestHealthNotifier_NoLeaderVetoDoesNotRearmAfterStreakEnds(t *testing.T) {
	hc, hs := newTestHealthNotifier()
	clock, advance := fakeClock()
	hc.now = clock

	hc.HealthCheckFailed("linear_read")
	if got := currentStatus(t, hs); got != healthpb.HealthCheckResponse_NOT_SERVING {
		t.Fatalf("aggregate = %v, want NOT_SERVING with only linear_read failing", got)
	}
	hc.HealthCheckFailed("no_leader")
	if got := currentStatus(t, hs); got != healthpb.HealthCheckResponse_SERVING {
		t.Fatalf("aggregate = %v, want SERVING once the veto armed", got)
	}
	advance(defaultNoLeaderVetoWindow - time.Second)
	hc.HealthCheckRecovered("no_leader")
	if got := currentStatus(t, hs); got != healthpb.HealthCheckResponse_NOT_SERVING {
		t.Fatalf("aggregate = %v, want NOT_SERVING once the streak ended and lifted the veto", got)
	}

	hc.HealthCheckFailed("no_leader")
	if got := currentStatus(t, hs); got != healthpb.HealthCheckResponse_NOT_SERVING {
		t.Fatalf("aggregate = %v, want NOT_SERVING; a new streak must not re-shield the still-failing linear_read", got)
	}
}

// TestHealthNotifier_NoLeaderVetoDoesNotRearmAfterExpiry pins the same
// latch for the streak that ends by expiry: once the window elapsed
// and the member was exposed to the aggregate, a no_leader flap
// (recover, re-fail) must not hand the still-failing linear_read
// another full window.
func TestHealthNotifier_NoLeaderVetoDoesNotRearmAfterExpiry(t *testing.T) {
	hc, hs := newTestHealthNotifier()
	clock, advance := fakeClock()
	hc.now = clock

	hc.HealthCheckFailed("linear_read")
	hc.HealthCheckFailed("no_leader")
	advance(defaultNoLeaderVetoWindow + time.Second)
	hc.HealthCheckFailed("linear_read") // per-tick replay drives the expiry
	if got := currentStatus(t, hs); got != healthpb.HealthCheckResponse_NOT_SERVING {
		t.Fatalf("aggregate = %v, want NOT_SERVING after the first veto expired", got)
	}

	hc.HealthCheckRecovered("no_leader")
	hc.HealthCheckFailed("no_leader")
	if got := currentStatus(t, hs); got != healthpb.HealthCheckResponse_NOT_SERVING {
		t.Fatalf("aggregate = %v, want NOT_SERVING; a flap must not re-shield the still-failing linear_read", got)
	}
}

// TestHealthNotifier_NoLeaderVetoRearmsForFreshEpisode pins the other
// side of the latch: the veto still applies to a *new* linear_read
// failure episode. linear_read recovering clears the latch, so a later
// no_leader streak shields a later linear_read failure as usual — the
// latch must not degrade a healthy member's election protection.
func TestHealthNotifier_NoLeaderVetoRearmsForFreshEpisode(t *testing.T) {
	hc, hs := newTestHealthNotifier()
	clock, _ := fakeClock()
	hc.now = clock

	hc.HealthCheckFailed("linear_read")
	hc.HealthCheckFailed("no_leader")
	hc.HealthCheckRecovered("linear_read")
	hc.HealthCheckRecovered("no_leader")
	if got := currentStatus(t, hs); got != healthpb.HealthCheckResponse_SERVING {
		t.Fatalf("aggregate = %v, want SERVING after full recovery", got)
	}

	hc.HealthCheckFailed("no_leader")
	hc.HealthCheckFailed("linear_read")
	if got := currentStatus(t, hs); got != healthpb.HealthCheckResponse_SERVING {
		t.Fatalf("aggregate = %v, want SERVING; a fresh episode must get a fresh veto", got)
	}
}

// TestNoLeaderVetoWindowScalesWithElectionTimeout pins the derivation:
// the veto has to outlast a normal election, so a cluster configured
// with a long election timeout gets a proportionally longer window
// instead of the floor. Lifting the veto mid-election would flip the
// aggregate entry on every member at once — the harm the veto exists
// to prevent. The default 1s election timeout stays on the floor.
func TestNoLeaderVetoWindowScalesWithElectionTimeout(t *testing.T) {
	for _, electionTimeout := range []time.Duration{0, time.Second, 3 * time.Second} {
		if got := noLeaderVetoWindow(electionTimeout); got != defaultNoLeaderVetoWindow {
			t.Fatalf("noLeaderVetoWindow(%v) = %v, want the %v floor", electionTimeout, got, defaultNoLeaderVetoWindow)
		}
	}
	if got := noLeaderVetoWindow(12500 * time.Millisecond); got != 125*time.Second {
		t.Fatalf("noLeaderVetoWindow(12.5s) = %v, want 125s", got)
	}
}

// TestHealthNotifier_NoLeaderReplayDrivesVetoExpiry pins the
// load-bearing replay path: no_leader fires HealthCheckFailed on
// every 500ms checker tick, and it is that replay — not a
// linear_read probe completion — which recomputes eligibility after
// the veto window elapses and flips the aggregate. It also guards
// the timestamp semantics: the replay must not refresh the reason's
// first-failure time, or the window would never elapse.
//
// The expiry flip's log attribution is asserted too: the WARN that
// publishes the degradation must name the reasons that drive the
// aggregate (the unmasked linear_read), not the no_leader replay
// that merely triggered the recompute. An operator who saw
// reason=no_leader here would triage a leaderless cluster instead
// of a wedged read loop.
func TestHealthNotifier_NoLeaderReplayDrivesVetoExpiry(t *testing.T) {
	hc, hs := newTestHealthNotifier()
	clock, advance := fakeClock()
	hc.now = clock
	core, recorded := observer.New(zapcore.InfoLevel)
	hc.lg = zap.New(core)

	hc.HealthCheckFailed("linear_read")
	hc.HealthCheckFailed("no_leader")
	advance(defaultNoLeaderVetoWindow + time.Second)

	hc.HealthCheckFailed("no_leader")
	if got := currentStatus(t, hs); got != healthpb.HealthCheckResponse_NOT_SERVING {
		t.Fatalf("no_leader replay past the window must flip the aggregate to NOT_SERVING, got %v", got)
	}

	// Every flip in this sequence is an aggregate flip (the per-check
	// entries never log here), so the three lines are: linear_read
	// degrades, no_leader arms the veto and recovers the aggregate,
	// and the replay expiry degrades it again.
	entries := recorded.FilterMessage("grpc service status changed").All()
	if len(entries) != 3 {
		t.Fatalf("expected 3 aggregate-flip log lines, got %d", len(entries))
	}
	expiry := entries[2].ContextMap()
	if expiry["service"] != allGRPCServices {
		t.Errorf("expiry flip: service = %v, want %q", expiry["service"], allGRPCServices)
	}
	if expiry["status"] != healthpb.HealthCheckResponse_NOT_SERVING.String() {
		t.Errorf("expiry flip: status = %v, want %v", expiry["status"], healthpb.HealthCheckResponse_NOT_SERVING)
	}
	if expiry["reason"] != "health:linear_read" {
		t.Errorf("expiry flip: reason = %v, want %q (the unmasked eligible driver, not the replaying no_leader)", expiry["reason"], "health:linear_read")
	}
}

// TestHealthNotifier_AggregateFlipLogsPublishedStatusAndDriver pins
// the log contract for aggregate flips: the logged status is the
// state that was actually published, not the direction of the call
// that triggered the recompute, and the reason field names what
// drives the new state. Three flips are exercised: linear_read alone
// degrades the aggregate (reason = linear_read), a fresh no_leader
// arms the veto and recovers it (status = SERVING, reason = the
// no_leader that armed the veto), and no_leader recovering again
// lifts the veto and re-degrades it with linear_read still failing
// (reason = linear_read, not the recovered no_leader).
func TestHealthNotifier_AggregateFlipLogsPublishedStatusAndDriver(t *testing.T) {
	hc, hs := newTestHealthNotifier()
	core, recorded := observer.New(zapcore.InfoLevel)
	hc.lg = zap.New(core)

	hc.HealthCheckFailed("linear_read")
	hc.HealthCheckFailed("no_leader")
	if got := currentStatus(t, hs); got != healthpb.HealthCheckResponse_SERVING {
		t.Fatalf("veto should keep the aggregate SERVING, got %v", got)
	}
	hc.HealthCheckRecovered("no_leader")
	if got := currentStatus(t, hs); got != healthpb.HealthCheckResponse_NOT_SERVING {
		t.Fatalf("veto lift should degrade the aggregate with linear_read still failing, got %v", got)
	}

	entries := recorded.FilterMessage("grpc service status changed").All()
	if len(entries) != 3 {
		t.Fatalf("expected 3 aggregate-flip log lines, got %d", len(entries))
	}
	want := []struct{ status, reason string }{
		{healthpb.HealthCheckResponse_NOT_SERVING.String(), "health:linear_read"},
		{healthpb.HealthCheckResponse_SERVING.String(), "health:no_leader"},
		{healthpb.HealthCheckResponse_NOT_SERVING.String(), "health:linear_read"},
	}
	for i, w := range want {
		got := entries[i].ContextMap()
		if got["service"] != allGRPCServices {
			t.Errorf("flip %d: service = %v, want %q", i, got["service"], allGRPCServices)
		}
		if got["status"] != w.status {
			t.Errorf("flip %d: status = %v, want %v", i, got["status"], w.status)
		}
		if got["reason"] != w.reason {
			t.Errorf("flip %d: reason = %v, want %v", i, got["reason"], w.reason)
		}
	}
}

// TestHealthNotifier_WarnOnBlindVeto pins the B3 startup
// cross-check: an explicit per-check timeout larger than the
// no_leader veto window makes the veto structurally blind — a hung
// linear_read probe runs for its whole CheckTimeout budget, so no
// failure can be recorded while the veto is still armed, and a
// leaderless streak past the window ends with every member evicted
// at once. The unset sentinel (newHealthNotifier passes 0 when the
// flag is not set; the derived ReqTimeout default stays inside the
// window across election-timeout configurations) and sub-window
// explicit values must stay silent.
func TestHealthNotifier_WarnOnBlindVeto(t *testing.T) {
	for _, tc := range []struct {
		name     string
		explicit time.Duration
		wantWarn bool
	}{
		{"unset sentinel", 0, false},
		{"below window", 10 * time.Second, false},
		{"exactly window", defaultNoLeaderVetoWindow, false},
		{"above window", defaultNoLeaderVetoWindow + time.Second, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hc, _ := newTestHealthNotifier()
			core, recorded := observer.New(zapcore.WarnLevel)
			hc.lg = zap.New(core)

			hc.warnOnBlindVeto(tc.explicit)

			entries := recorded.FilterMessageSnippet("explicit health check timeout exceeds").All()
			if tc.wantWarn && len(entries) != 1 {
				t.Fatalf("expected 1 blind-veto WARN, got %d", len(entries))
			}
			if !tc.wantWarn {
				if len(entries) != 0 {
					t.Fatalf("expected no WARN, got %d", len(entries))
				}
				return
			}
			fields := entries[0].ContextMap()
			if got, _ := fields["check-timeout"].(time.Duration); got != tc.explicit {
				t.Errorf("WARN check-timeout = %v, want %v", got, tc.explicit)
			}
			if got, _ := fields["veto-window"].(time.Duration); got != defaultNoLeaderVetoWindow {
				t.Errorf("WARN veto-window = %v, want %v", got, defaultNoLeaderVetoWindow)
			}
		})
	}
}

// TestHealthNotifier_FirstTimeEligibleReasonLogsPerCheckEntry pins the
// causal accuracy of the no-flip WARN: when the aggregate is already
// NOT_SERVING from an earlier eligible reason, a first-time eligible
// reason drives only its own per-check entry, and the WARN must name
// that entry. A service="" line here would read as an aggregate flip
// that never happened.
func TestHealthNotifier_FirstTimeEligibleReasonLogsPerCheckEntry(t *testing.T) {
	hc, hs := newTestHealthNotifier()
	core, recorded := observer.New(zapcore.InfoLevel)
	hc.lg = zap.New(core)

	hc.HealthCheckFailed("learner")
	if got := currentStatus(t, hs); got != healthpb.HealthCheckResponse_NOT_SERVING {
		t.Fatalf("aggregate = %v, want NOT_SERVING for learner", got)
	}
	hc.HealthCheckFailed("apply_stall")
	if got := serviceStatus(t, hs, healthServicePrefix+"apply_stall"); got != healthpb.HealthCheckResponse_NOT_SERVING {
		t.Fatalf("etcd.health/apply_stall = %v, want NOT_SERVING", got)
	}
	if got := currentStatus(t, hs); got != healthpb.HealthCheckResponse_NOT_SERVING {
		t.Fatalf("aggregate = %v, want NOT_SERVING (learner still active)", got)
	}

	entries := recorded.FilterMessage("grpc service status changed").All()
	if len(entries) != 2 {
		t.Fatalf("expected 2 log lines (learner aggregate flip + apply_stall per-check WARN), got %d", len(entries))
	}
	got := entries[1].ContextMap()
	if got["service"] != healthServicePrefix+"apply_stall" {
		t.Errorf("apply_stall WARN service = %v, want %q (the entry it actually flipped)", got["service"], healthServicePrefix+"apply_stall")
	}
	if got["status"] != healthpb.HealthCheckResponse_NOT_SERVING.String() {
		t.Errorf("apply_stall WARN status = %v, want NOT_SERVING", got["status"])
	}
	if got["reason"] != healthPrefix+"apply_stall" {
		t.Errorf("apply_stall WARN reason = %v, want %q", got["reason"], healthPrefix+"apply_stall")
	}
}

// TestHealthNotifier_RecoveryLogsPerCheckEntry pins the recovery-side
// mirror of the firstTime WARN in stopServe: when clearing a reason
// does not transition the aggregate (SERVING before and after), the
// only entry this recovery changed is the reason's own per-check
// entry, and an Info must name it. Without this line the per-check
// NOT_SERVING WARN has no recovery counterpart, so an operator
// watching etcd.health/apply_lag sees the failure arrive in the logs
// but never clear. A recovery that does flip the aggregate logs
// exactly one aggregate line — a second line for the per-check entry
// would double-report the same call.
func TestHealthNotifier_RecoveryLogsPerCheckEntry(t *testing.T) {
	hc, hs := newTestHealthNotifier()
	core, recorded := observer.New(zapcore.InfoLevel)
	hc.lg = zap.New(core)

	// apply_lag is clusterWide, so failing and recovering it never
	// touches the aggregate: its recovery is the pure per-check case.
	hc.HealthCheckFailed("apply_lag")
	hc.HealthCheckRecovered("apply_lag")
	if got := serviceStatus(t, hs, healthServicePrefix+"apply_lag"); got != healthpb.HealthCheckResponse_SERVING {
		t.Fatalf("etcd.health/apply_lag = %v, want SERVING after recovery", got)
	}

	entries := recorded.FilterMessage("grpc service status changed").All()
	if len(entries) != 2 {
		t.Fatalf("expected 2 log lines (apply_lag failure WARN + apply_lag recovery Info), got %d", len(entries))
	}
	got := entries[1].ContextMap()
	if got["service"] != healthServicePrefix+"apply_lag" {
		t.Errorf("recovery Info service = %v, want %q (the entry it actually flipped back)", got["service"], healthServicePrefix+"apply_lag")
	}
	if got["status"] != healthpb.HealthCheckResponse_SERVING.String() {
		t.Errorf("recovery Info status = %v, want SERVING", got["status"])
	}
	if got["reason"] != healthPrefix+"apply_lag" {
		t.Errorf("recovery Info reason = %v, want %q", got["reason"], healthPrefix+"apply_lag")
	}

	// The flip case: no_leader recovering while linear_read still
	// fails lifts the veto and degrades the aggregate. That recovery
	// must log exactly one aggregate line; a per-check line for
	// etcd.health/no_leader on top of it would double-report.
	hc.HealthCheckFailed("linear_read")
	hc.HealthCheckFailed("no_leader")
	hc.HealthCheckRecovered("no_leader")
	if got := currentStatus(t, hs); got != healthpb.HealthCheckResponse_NOT_SERVING {
		t.Fatalf("aggregate = %v, want NOT_SERVING after veto lift", got)
	}

	entries = recorded.FilterMessage("grpc service status changed").All()
	// 2 from the apply_lag pair + linear_read aggregate flip + veto
	// arming flip + veto lifting flip.
	if len(entries) != 5 {
		t.Fatalf("expected 5 log lines total, got %d", len(entries))
	}
	for i, e := range entries[2:] {
		fields := e.ContextMap()
		if fields["service"] == healthServicePrefix+"no_leader" && fields["status"] == healthpb.HealthCheckResponse_SERVING.String() {
			t.Errorf("log %d: no_leader recovery double-reported a per-check line; the aggregate flip line is the single log for that call", i+2)
		}
	}
	flip := entries[4].ContextMap()
	if flip["service"] != allGRPCServices || flip["status"] != healthpb.HealthCheckResponse_NOT_SERVING.String() {
		t.Errorf("veto-lift flip log = service %v status %v, want aggregate NOT_SERVING", flip["service"], flip["status"])
	}
}

// TestHealthNotifier_GateDisabledIgnoresHealthFailed covers the
// ServerHealthCheck feature gate short-circuit. When the gate is
// off, HealthCheckFailed and HealthCheckRecovered must both be
// no-ops: neither the aggregate entry nor any per-check entry may
// flip, otherwise a gate-disabled legacy cluster could surface
// health transitions from a checker that was never supposed to
// run.
func TestHealthNotifier_GateDisabledIgnoresHealthFailed(t *testing.T) {
	hs := health.NewServer()
	hc := &healthNotifier{
		hs:                      hs,
		lg:                      zap.NewNop(),
		reasons:                 make(map[string]time.Time),
		stopGRPCServiceOnDefrag: true,
		serverHealthCheck:       false, // gate disabled
	}
	hc.initServingStatuses()

	hc.HealthCheckFailed("apply_lag")
	hc.HealthCheckRecovered("apply_lag")
	if got := currentStatus(t, hs); got != healthpb.HealthCheckResponse_SERVING {
		t.Fatalf("gate disabled should ignore HealthCheckFailed/Recovered, aggregate = %v, want SERVING", got)
	}
	for _, name := range perCheckServiceNames {
		if got := serviceStatus(t, hs, name); got != healthpb.HealthCheckResponse_SERVING {
			t.Fatalf("gate disabled: entry %q = %v, want SERVING", name, got)
		}
	}
}

// TestHealthNotifier_StopServeRepeatSuppressesLog covers the repeat
// path of stopServe: the aggregate recompute still runs (eligibility
// is time-dependent) but the WARN is suppressed while the reason
// persists. Repeated HealthCheckFailed("linear_read") must (a) leave
// the gRPC state at NOT_SERVING, (b) leave the reasons set with a
// single entry, and (c) emit exactly one WARN transition log. The
// third assertion is the regression target: the previous
// implementation logged the transition on every call, producing
// ~7200 identical WARN lines per hour on a learner member (500ms
// tick).
func TestHealthNotifier_StopServeRepeatSuppressesLog(t *testing.T) {
	core, recorded := observer.New(zapcore.WarnLevel)
	hs := health.NewServer()
	hs.SetServingStatus(allGRPCServices, healthpb.HealthCheckResponse_SERVING)
	hc := &healthNotifier{
		hs:                      hs,
		lg:                      zap.New(core),
		reasons:                 make(map[string]time.Time),
		stopGRPCServiceOnDefrag: true,
		serverHealthCheck:       true,
	}

	// First call flips state to NOT_SERVING and emits one WARN.
	hc.HealthCheckFailed("linear_read")
	if got := currentStatus(t, hs); got != healthpb.HealthCheckResponse_NOT_SERVING {
		t.Fatalf("first HealthCheckFailed should flip to NOT_SERVING, got %v", got)
	}

	// Second call for the same reason: no state change, no extra WARN.
	hc.HealthCheckFailed("linear_read")
	if got := currentStatus(t, hs); got != healthpb.HealthCheckResponse_NOT_SERVING {
		t.Fatalf("repeated HealthCheckFailed should stay NOT_SERVING, got %v", got)
	}

	// A different reason is independent: must still emit a WARN and
	// the gRPC state stays NOT_SERVING.
	hc.HealthCheckFailed("learner")

	entries := recorded.FilterMessage("grpc service status changed").All()
	if len(entries) != 2 {
		t.Fatalf("expected exactly 2 transition WARN logs (one per distinct reason), got %d", len(entries))
	}
	if entries[0].ContextMap()["reason"] != "health:linear_read" {
		t.Errorf("first WARN reason = %v, want health:linear_read", entries[0].ContextMap()["reason"])
	}
	if entries[1].ContextMap()["reason"] != "health:learner" {
		t.Errorf("second WARN reason = %v, want health:learner", entries[1].ContextMap()["reason"])
	}

	// Recovery still works with a single call after duplicates.
	hc.HealthCheckRecovered("linear_read")
	if got := currentStatus(t, hs); got != healthpb.HealthCheckResponse_NOT_SERVING {
		t.Fatalf("recovering only one reason should keep NOT_SERVING, got %v", got)
	}
	hc.HealthCheckRecovered("learner")
	if got := currentStatus(t, hs); got != healthpb.HealthCheckResponse_SERVING {
		t.Fatalf("after both reasons cleared, state should be SERVING, got %v", got)
	}
}

// TestHealthNotifier_ConcurrentSafety stresses the notifier from
// many goroutines. Run with -race to flush out unsynchronized
// access to `reasons` or the underlying *health.Server.
func TestHealthNotifier_ConcurrentSafety(t *testing.T) {
	hc, hs := newTestHealthNotifier()
	const goroutines = 32
	const opsPerGoroutine = 200

	var wg sync.WaitGroup
	deadline := time.Now().Add(2 * time.Second)

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(seed int) {
			defer wg.Done()
			for j := 0; j < opsPerGoroutine; j++ {
				switch (seed + j) % 4 {
				case 0:
					hc.DefragStarted()
				case 1:
					hc.DefragFinished()
				case 2:
					hc.HealthCheckFailed("linear_read")
				case 3:
					hc.HealthCheckRecovered("linear_read")
				}
				if time.Now().After(deadline) {
					return
				}
			}
		}(i)
	}
	wg.Wait()

	// No assertion on the final state — the workload is intentionally
	// racy to exercise the mutex. The test passes if -race does not
	// report a data race and the runtime does not panic.
	_ = currentStatus(t, hs)
}
