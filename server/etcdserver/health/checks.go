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

	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	"go.etcd.io/etcd/client/pkg/v3/types"
	"go.etcd.io/etcd/server/v3/auth"
	"go.etcd.io/raft/v3"
)

// EtcdServer is the minimal surface the health checker needs from
// an etcd server. It is satisfied by *etcdserver.EtcdServer; the
// indirection is required to avoid a circular import between the
// etcdserver package and this sub-package.
type EtcdServer interface {
	IsLearner() bool
	Range(ctx context.Context, req *pb.RangeRequest) (*pb.RangeResponse, error)
	AuthStore() auth.AuthStore
	Lead() uint64
	CommittedIndex() uint64
	AppliedIndex() uint64
	MemberID() types.ID
	Alarms() []*pb.AlarmMember
}

// checkFunc is the signature implemented by every concrete check. The
// function MUST honor ctx cancellation and return promptly when ctx
// fires; the Checker applies a per-call timeout on top of ctx.
type checkFunc func(ctx context.Context, s EtcdServer) error

// Check names are a cross-package wire contract: the checker and the
// aggregator emit them as notifier reasons, and v3rpc maps them to
// the etcd.health/<name> grpc.health.v1 service entries and to
// aggregate-entry eligibility. Both sides must reference these
// constants — a typo'd string literal on the v3rpc side compiles
// green and silently changes which reasons can evict the member from
// client-side endpoint selection.
const (
	CheckLinearRead       = "linear_read"
	CheckSerializableRead = "serializable_read"
	CheckApplyLag         = "apply_lag"
	CheckNoLeader         = "no_leader"
	CheckApplyStall       = "apply_stall"
	CheckDataCorruption   = "data_corruption"
	CheckLearner          = "learner"
)

// linearReadCheck performs a Range(KeysOnly, Limit=1) under the
// server's root identity. It mirrors the existing HTTP /readyz
// behavior: WithRoot bypasses RBAC so a misconfigured auth setup
// does not by itself make the server look unhealthy, while Range
// still exercises the MVCC read path and (transitively) the linear
// read barrier in v3_server.go.
//
// The check is intentionally cheap (Limit=1, KeysOnly) and is run
// asynchronously from the main tick loop so a slow read does not
// stall the apply_lag / no_leader measurements.
func linearReadCheck(ctx context.Context, s EtcdServer) error {
	ctx = s.AuthStore().WithRoot(ctx)
	_, err := s.Range(ctx, &pb.RangeRequest{KeysOnly: true, Limit: 1})
	return err
}

// serializableReadCheck is linearReadCheck minus the read-index quorum
// round trip: the same KeysOnly+Limit=1 Range with Serializable=true
// executes the identical local read path (store mutex, backend read
// transaction, in-memory index — see txn/range.go and
// kvstore_txn.go), only the LinearizableReadNotify step in
// v3_server.go is skipped. Its failure therefore has no consensus
// component: it means this member cannot serve ANY read, which is why
// the reason drives the aggregate entry.
//
// Paired with linear_read it provides the attribution the HTTP
// /readyz surface already has (etcdhttp/health.go: "serializable_read
// checks if local read is ok; linearizable_read checks if there is
// consensus"): linear_read failing while serializable_read stays
// green isolates the failure to the quorum path — the member can
// still serve serializable traffic — while both failing means the
// local read path itself is broken. It is also the only check that
// can fire on a write-idle cluster whose local read path wedges:
// apply_stall structurally requires committed to advance, and a
// leaderless veto on linear_read cannot suppress this check.
//
// The check has no cluster-correlated failure mode: an election or
// quorum loss leaves local reads working everywhere, so it can never
// evict a quorum of members at once — the property the aggregate
// eligibility rules exist to protect.
func serializableReadCheck(ctx context.Context, s EtcdServer) error {
	ctx = s.AuthStore().WithRoot(ctx)
	_, err := s.Range(ctx, &pb.RangeRequest{KeysOnly: true, Limit: 1, Serializable: true})
	return err
}

// applyLagCheckWith reports failure when the gap between the
// committed and applied indices exceeds the supplied threshold.
// The default threshold (defaultApplyLagThreshold, 10000) matches
// maxPriorityGap in v3_server.go so that a node that has applied
// at least one entry for every committed index is never reported
// unhealthy due to the check itself. The threshold is plumbed in
// from the Checker so the --experimental-health-check-apply-lag
// flag takes effect; the error message embeds the same value
// the gauge used for the comparison so operators can correlate
// logs with the configured value.
func applyLagCheckWith(threshold uint64) checkFunc {
	return func(ctx context.Context, s EtcdServer) error {
		// Guard the subtraction: during snapshot recovery, server
		// restart, or a brief compaction window, AppliedIndex can
		// transiently exceed CommittedIndex. A direct
		// `committed - applied` on uint64 would wrap to a huge
		// positive value and incorrectly trip the lag check, so we
		// clamp the gap to 0 in that direction. A lag of 0 is the
		// honest answer: the node is at least as caught up as the
		// committed frontier.
		committed := s.CommittedIndex()
		applied := s.AppliedIndex()
		var gap uint64
		if committed > applied {
			gap = committed - applied
		}
		// Always export the gauge so dashboards can chart lag even
		// when the threshold is never crossed.
		healthCheckApplyLag.Set(float64(gap))
		if gap > threshold {
			return fmt.Errorf("apply lag: gap=%d threshold=%d", gap, threshold)
		}
		return nil
	}
}

// newApplyStallCheck returns a stateful checkFunc that fails while
// this member's applied index is frozen and the cluster's committed
// index has moved past the point it was at when the member last
// applied: raft is confirmed making progress elsewhere while this
// member cannot complete any new entry. That is a member-local
// wedge (stuck backend commit, snapshot install, starved apply
// goroutine), which is why the reason drives the aggregate entry —
// unlike apply_lag, whose sustained-backlog form is cluster-correlated
// and is deliberately kept out of it.
//
// The complementary halves of the predicate:
//   - committed frozen too means an idle cluster or a cluster-wide
//     raft/persistence pause — not a member-local signal, no failure;
//   - applied advancing (in either direction; restore can move it
//     backwards) re-arms the baseline and reports healthy.
//
// The cross-tick window is the Aggregator's own consecutive-failure
// debounce (FailureThreshold ticks), so a member must be frozen for
// that many consecutive samples with commits flowing before the
// notifier fires. A wedged member blocks its linearizable reads on
// the same frozen apply, so eviction here only accelerates what
// linear_read would do ~20s later; conditions that legitimately
// block apply (defrag, snapshot install) leave the member equally
// unable to serve and are correctly reported NOT_SERVING.
//
// The closure state is only touched from the Checker's tick
// goroutine, which invokes the synchronous checks inline, so it
// needs no lock.
func newApplyStallCheck() checkFunc {
	var lastApplied, lastCommitted uint64
	started := false
	return func(ctx context.Context, s EtcdServer) error {
		applied, committed := s.AppliedIndex(), s.CommittedIndex()
		if !started || applied != lastApplied {
			started = true
			lastApplied, lastCommitted = applied, committed
			return nil
		}
		if committed <= lastCommitted {
			return nil
		}
		return fmt.Errorf("apply stall: applied=%d frozen while committed=%d advanced", applied, committed)
	}
}

// noLeaderCheck is a single atomic read. It cannot fail spuriously
// because Lead() is updated synchronously by the raft loop; the
// only way this returns an error is if the cluster genuinely has
// no leader (election in progress or quorum lost).
func noLeaderCheck(ctx context.Context, s EtcdServer) error {
	if s.Lead() == raft.None {
		return fmt.Errorf("no leader")
	}
	return nil
}

// dataCorruptionCheck fails when a CORRUPT alarm names THIS member —
// or when it carries MemberID=0, the sentinel the compaction hash
// check raises when it "cannot identify the corrupted members, so it
// affects the whole cluster" (corrupt.go); every member must fail on
// that one.
//
// The alarm is raft-replicated into every member's alarm store, and
// once any CORRUPT alarm exists every member's applier is wrapped in
// applierV3Corrupt (raft-ized writes fail cluster-wide). An
// unfiltered any-CORRUPT condition would therefore fail on all
// members at once and evict the whole cluster from the aggregate —
// the shape the aggregate eligibility rules exist to prevent.
// Filtering by MemberID pins the failure to the corrupted member(s)
// so healthy peers stay SERVING for reads, which still work there:
// reads bypass the applier (read-index + local mvcc), so only the
// corrupted member's reads are untrustworthy anyway.
//
// NOSPACE never enters the health decision: it blocks writes only,
// lands on every member simultaneously, and reads must keep flowing
// while operators drain the space.
func dataCorruptionCheck(ctx context.Context, s EtcdServer) error {
	for _, m := range s.Alarms() {
		if m.Alarm != pb.AlarmType_CORRUPT {
			continue
		}
		if m.MemberID == 0 || m.MemberID == uint64(s.MemberID()) {
			return fmt.Errorf("data corruption: CORRUPT alarm active for member %s", types.ID(m.MemberID))
		}
	}
	return nil
}
