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
	"errors"
	"strings"
	"testing"

	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	"go.etcd.io/etcd/client/pkg/v3/types"
	"go.etcd.io/etcd/server/v3/auth"
	"go.etcd.io/raft/v3"
)

// stubAuthStore embeds auth.AuthStore so the mock satisfies the full
// interface. Only WithRoot is called by the health package; any other
// method invocation is a programming error and the test framework
// will report a panic, surfacing accidental coupling at test time.
type stubAuthStore struct {
	auth.AuthStore
	rootCtx context.Context
}

func (s *stubAuthStore) WithRoot(ctx context.Context) context.Context {
	if s.rootCtx != nil {
		return s.rootCtx
	}
	return ctx
}

// rootKey is a private context key used to verify WithRoot flow.
type rootKey struct{}

// fakeServer is a controllable test double for the EtcdServer
// interface. Field-level pointers make each scenario easy to read
// without a builder.
type fakeServer struct {
	isLearner bool
	rangeFn   func(ctx context.Context, req *pb.RangeRequest) (*pb.RangeResponse, error)
	authStore *stubAuthStore
	lead      uint64
	committed uint64
	applied   uint64
	id        types.ID
	alarms    []*pb.AlarmMember

	// alarmsFn, when non-nil, replaces the static alarms field so a
	// test can block inside Alarms(). The production AlarmStore.Get
	// takes the store's exclusive mutex, and Activate/Deactivate hold
	// that mutex across a backend write, so a wedged alarm walk is
	// the shape the asynchronous probe group must survive.
	alarmsFn func() []*pb.AlarmMember

	// indexGate, when non-nil, makes CommittedIndex wait until the
	// channel is closed. CommittedIndex runs in the synchronous part
	// of every tick (apply_lag, apply_stall), so blocking it starves
	// the tick loop and exercises Run's self-pause guard.
	indexGate chan struct{}
}

func (f *fakeServer) IsLearner() bool { return f.isLearner }
func (f *fakeServer) Range(ctx context.Context, req *pb.RangeRequest) (*pb.RangeResponse, error) {
	return f.rangeFn(ctx, req)
}
func (f *fakeServer) AuthStore() auth.AuthStore { return f.authStore }
func (f *fakeServer) Lead() uint64              { return f.lead }
func (f *fakeServer) MemberID() types.ID        { return f.id }
func (f *fakeServer) Alarms() []*pb.AlarmMember {
	if f.alarmsFn != nil {
		return f.alarmsFn()
	}
	return f.alarms
}
func (f *fakeServer) CommittedIndex() uint64 {
	if f.indexGate != nil {
		<-f.indexGate
	}
	return f.committed
}
func (f *fakeServer) AppliedIndex() uint64 { return f.applied }

func newFakeServer() *fakeServer {
	return &fakeServer{
		authStore: &stubAuthStore{},
		rangeFn: func(ctx context.Context, req *pb.RangeRequest) (*pb.RangeResponse, error) {
			return &pb.RangeResponse{}, nil
		},
	}
}

// TestDataCorruptionCheck pins the failure condition of the
// data_corruption check: it fails only when a CORRUPT alarm names
// THIS member, or when it carries the upstream MemberID=0 sentinel
// ("cannot identify the corrupted members, it affects the whole
// cluster" — corrupt.go's compaction hash check). The alarm itself
// replicates through raft into every member's alarm store, so an
// unfiltered any-CORRUPT condition would fail on every member at
// once and evict the whole cluster — the shape the aggregate
// eligibility rules exist to prevent. NOSPACE never enters the
// health decision (it blocks writes only and lands on every member
// simultaneously); it is deliberately not consulted here.
func TestDataCorruptionCheck(t *testing.T) {
	self := types.ID(7)
	cases := []struct {
		name    string
		alarms  []*pb.AlarmMember
		wantErr bool
	}{
		{"no-alarms", nil, false},
		{"corrupt-names-self", []*pb.AlarmMember{{MemberID: uint64(self), Alarm: pb.AlarmType_CORRUPT}}, true},
		{"corrupt-names-other", []*pb.AlarmMember{{MemberID: 42, Alarm: pb.AlarmType_CORRUPT}}, false},
		// MemberID=0 is upstream's explicit "whole cluster affected"
		// sentinel, not a member ID; every member must fail on it.
		{"corrupt-cannot-identify-member-id-zero", []*pb.AlarmMember{{MemberID: 0, Alarm: pb.AlarmType_CORRUPT}}, true},
		{"nospace-names-self", []*pb.AlarmMember{{MemberID: uint64(self), Alarm: pb.AlarmType_NOSPACE}}, false},
		{"nospace-self-and-corrupt-other", []*pb.AlarmMember{{MemberID: uint64(self), Alarm: pb.AlarmType_NOSPACE}, {MemberID: 42, Alarm: pb.AlarmType_CORRUPT}}, false},
		{"nospace-other-and-corrupt-self", []*pb.AlarmMember{{MemberID: 42, Alarm: pb.AlarmType_NOSPACE}, {MemberID: uint64(self), Alarm: pb.AlarmType_CORRUPT}}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := newFakeServer()
			s.id = self
			s.alarms = c.alarms
			err := dataCorruptionCheck(context.Background(), s)
			if c.wantErr && err == nil {
				t.Fatalf("%s: expected failure, got nil", c.name)
			}
			if !c.wantErr && err != nil {
				t.Fatalf("%s: expected nil, got %v", c.name, err)
			}
		})
	}
}

// TestApplyLagCheckWith_BelowThreshold ensures the check returns
// nil when the lag is at or below the threshold. Edge cases (==
// and <) are explicitly covered because the production guard is
// `>` not `>=`. The check is the same function used by the
// production loop (applyLagCheckWith); only the threshold is
// pinned to the package default for clarity.
func TestApplyLagCheckWith_BelowThreshold(t *testing.T) {
	s := newFakeServer()
	check := applyLagCheckWith(defaultApplyLagThreshold)
	cases := []struct {
		name               string
		committed, applied uint64
	}{
		{"equal", 100, 100},
		{"gap-one", 101, 100},
		{"at-threshold", defaultApplyLagThreshold + 100, 100}, // gap == 10000
		// applied can transiently exceed committed during snapshot
		// recovery / restart / compaction windows. A direct
		// uint64 subtraction would wrap to ~2^64 and incorrectly
		// trip the lag check; the production code clamps to gap=0
		// and reports a healthy result.
		{"applied-ahead", 100, 200},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s.committed = c.committed
			s.applied = c.applied
			if err := check(context.Background(), s); err != nil {
				t.Fatalf("expected nil error for gap <= threshold, got %v", err)
			}
		})
	}
}

// TestApplyLagCheckWith_AboveThreshold covers the failure path.
// The error message must surface both the actual gap and the
// threshold so the logs are immediately actionable without
// re-reading the source.
func TestApplyLagCheckWith_AboveThreshold(t *testing.T) {
	s := newFakeServer()
	s.committed = defaultApplyLagThreshold + 1
	s.applied = 0
	check := applyLagCheckWith(defaultApplyLagThreshold)
	err := check(context.Background(), s)
	if err == nil {
		t.Fatal("expected error when gap > threshold, got nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, "apply lag") {
		t.Errorf("error message missing 'apply lag' prefix: %q", msg)
	}
	if !strings.Contains(msg, "threshold=10000") {
		t.Errorf("error message missing 'threshold=10000' label: %q", msg)
	}
}

// TestApplyLagCheckWith_ErrorMessageUsesConfiguredThreshold locks
// the M2 contract: when the operator lowers the flag below the
// package default, the error message must reflect the new value
// (not the hard-coded 10000). Without this a misconfigured
// threshold would never show up in the logs.
func TestApplyLagCheckWith_ErrorMessageUsesConfiguredThreshold(t *testing.T) {
	s := newFakeServer()
	s.committed = 200
	s.applied = 0
	check := applyLagCheckWith(100)
	err := check(context.Background(), s)
	if err == nil {
		t.Fatal("expected error when gap (200) > threshold (100), got nil")
	}
	if !strings.Contains(err.Error(), "threshold=100") {
		t.Errorf("error message should report configured threshold=100, got %q", err.Error())
	}
	if strings.Contains(err.Error(), "threshold=10000") {
		t.Errorf("error message should not leak the package default, got %q", err.Error())
	}
}

// TestApplyLagCheckWith_CustomThreshold exercises the parameterized
// variant used by tests. A caller-supplied threshold is honored
// exactly; the package default does not bleed in.
func TestApplyLagCheckWith_CustomThreshold(t *testing.T) {
	s := newFakeServer()
	s.committed = 50
	s.applied = 0

	check := applyLagCheckWith(10)
	if err := check(context.Background(), s); err == nil {
		t.Fatal("expected error when gap (50) > threshold (10), got nil")
	}

	s.committed = 5
	s.applied = 0
	if err := check(context.Background(), s); err != nil {
		t.Fatalf("expected nil error for gap (5) <= threshold (10), got %v", err)
	}
}

// TestApplyStallCheck_FiresWhenAppliedFrozenAndCommittedAdvances is
// the core A2 discriminator: the check must fail while this member's
// applied index is frozen and the cluster's committed index has moved
// past the point it was at when the member last applied — the locally
// observable signature of a wedged apply loop (stuck backend commit,
// snapshot install, scheduler starvation) on a cluster that is
// otherwise making progress.
func TestApplyStallCheck_FiresWhenAppliedFrozenAndCommittedAdvances(t *testing.T) {
	s := newFakeServer()
	s.committed, s.applied = 100, 100
	check := newApplyStallCheck()

	// The first call only establishes the baseline; it must be
	// healthy even though applied has not "advanced" from the zero
	// value of the closure state (a fresh member must not fire
	// before its first sample).
	if err := check(context.Background(), s); err != nil {
		t.Fatalf("first call must establish the baseline and be healthy, got %v", err)
	}

	// Applied frozen, one new committed entry: failure. The error
	// must name the check and carry both indices so operators can
	// correlate it with the apply_lag gauge.
	s.committed = 101
	err := check(context.Background(), s)
	if err == nil {
		t.Fatal("expected error when applied is frozen while committed advances")
	}
	for _, want := range []string{"apply stall", "applied=100", "committed=101"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error message %q missing %q", err.Error(), want)
		}
	}

	// Sustained wedge: keeps failing so the notifier replay and the
	// recovery debounce keep seeing the condition.
	s.committed = 102
	if err := check(context.Background(), s); err == nil {
		t.Fatal("expected sustained failure while applied stays frozen")
	}
}

// TestApplyStallCheck_IdleClusterDoesNotFire pins the other half of
// the discriminator: with committed also frozen the member has
// nothing new to apply, which is an idle cluster — not a wedge. The
// same holds for a cluster-wide raft/persistence pause, which is
// exactly why this check must not be a plain "applied frozen"
// detector.
func TestApplyStallCheck_IdleClusterDoesNotFire(t *testing.T) {
	s := newFakeServer()
	s.committed, s.applied = 100, 100
	check := newApplyStallCheck()

	if err := check(context.Background(), s); err != nil {
		t.Fatalf("first call must establish the baseline, got %v", err)
	}
	for i := 0; i < 5; i++ {
		if err := check(context.Background(), s); err != nil {
			t.Fatalf("idle cluster (committed frozen) must not fire, iteration %d: %v", i, err)
		}
	}
}

// TestApplyStallCheck_AppliedAdvancingResets: a member that resumes
// applying must immediately return to healthy, and the baseline must
// re-arm so a later freeze fires again instead of being absorbed by
// the stale baseline.
func TestApplyStallCheck_AppliedAdvancingResets(t *testing.T) {
	s := newFakeServer()
	s.committed, s.applied = 100, 100
	check := newApplyStallCheck()

	if err := check(context.Background(), s); err != nil {
		t.Fatalf("baseline call: %v", err)
	}
	s.committed = 101
	if err := check(context.Background(), s); err == nil {
		t.Fatal("expected failure while frozen")
	}

	// Catch up: healthy, baseline re-armed at the new position.
	s.applied = 101
	if err := check(context.Background(), s); err != nil {
		t.Fatalf("member that resumed applying must be healthy, got %v", err)
	}

	// Freeze again with a fresh commit: must fire again.
	s.committed = 102
	if err := check(context.Background(), s); err == nil {
		t.Fatal("expected re-armed failure on the second freeze")
	}
}

// TestApplyStallCheck_AppliedBackwardsResets: applied can move
// backwards across a restore/restart. Any change from the baseline
// (either direction) re-arms; a frozen applied at a lower value with
// committed at/below the new baseline must not fire.
func TestApplyStallCheck_AppliedBackwardsResets(t *testing.T) {
	s := newFakeServer()
	s.committed, s.applied = 100, 100
	check := newApplyStallCheck()

	if err := check(context.Background(), s); err != nil {
		t.Fatalf("baseline call: %v", err)
	}
	s.committed = 101
	if err := check(context.Background(), s); err == nil {
		t.Fatal("expected failure while frozen")
	}

	s.applied, s.committed = 50, 51
	if err := check(context.Background(), s); err != nil {
		t.Fatalf("applied moving backwards (restore) must re-arm, got %v", err)
	}
	if err := check(context.Background(), s); err != nil {
		t.Fatalf("frozen at the new baseline with no new commits must not fire, got %v", err)
	}
}

// TestNoLeaderCheck verifies the binary outcome: any value other than
// raft.None means a leader exists.
func TestNoLeaderCheck(t *testing.T) {
	s := newFakeServer()

	// No leader
	s.lead = raft.None
	if err := noLeaderCheck(context.Background(), s); err == nil {
		t.Fatal("expected error when Lead == raft.None, got nil")
	}

	// Has leader (any non-zero id)
	s.lead = 1
	if err := noLeaderCheck(context.Background(), s); err != nil {
		t.Fatalf("expected nil error when Lead != raft.None, got %v", err)
	}
}

// TestLinearReadCheck_RangeError ensures Range errors propagate
// unchanged. The check must not wrap, swallow, or log; Aggregator
// owns the error-rate accounting.
func TestLinearReadCheck_RangeError(t *testing.T) {
	want := errors.New("boom")
	s := newFakeServer()
	s.rangeFn = func(ctx context.Context, req *pb.RangeRequest) (*pb.RangeResponse, error) {
		return nil, want
	}
	got := linearReadCheck(context.Background(), s)
	if !errors.Is(got, want) {
		t.Fatalf("linearReadCheck should propagate Range error verbatim, got %v", got)
	}
}

// TestLinearReadCheck_AppliesWithRoot verifies the production
// invariant that linearReadCheck wraps the supplied ctx in the
// server's root identity before issuing Range. The root context
// must propagate to the Range call so RBAC does not mask a real
// MVCC / linear-read failure as a permission error.
func TestLinearReadCheck_AppliesWithRoot(t *testing.T) {
	want := context.WithValue(context.Background(), rootKey{}, "sentinel")
	s := newFakeServer()
	s.authStore = &stubAuthStore{rootCtx: want}
	var gotCtx context.Context
	s.rangeFn = func(ctx context.Context, req *pb.RangeRequest) (*pb.RangeResponse, error) {
		gotCtx = ctx
		return &pb.RangeResponse{}, nil
	}
	if err := linearReadCheck(context.Background(), s); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotCtx == nil {
		t.Fatal("Range was not invoked")
	}
	if v, _ := gotCtx.Value(rootKey{}).(string); v != "sentinel" {
		t.Errorf("Range ctx did not carry the root identity, got value %q", v)
	}
}

// TestLinearReadCheck_ForwardsKeysOnlyAndLimit ensures the production
// request shape is preserved. Operators rely on the KeysOnly+Limit=1
// property for the read's cost; an accidental change here would make
// the check 1000x more expensive.
func TestLinearReadCheck_ForwardsKeysOnlyAndLimit(t *testing.T) {
	var captured *pb.RangeRequest
	s := newFakeServer()
	s.rangeFn = func(ctx context.Context, req *pb.RangeRequest) (*pb.RangeResponse, error) {
		captured = req
		return &pb.RangeResponse{}, nil
	}
	if err := linearReadCheck(context.Background(), s); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if captured == nil {
		t.Fatal("Range was not invoked")
	}
	if !captured.KeysOnly {
		t.Error("RangeRequest.KeysOnly must be true to keep the read cheap")
	}
	if captured.Limit != 1 {
		t.Errorf("RangeRequest.Limit = %d, want 1", captured.Limit)
	}
}

// TestSerializableReadCheck_ForwardsSerializableKeysOnlyLimit pins
// the one field that distinguishes this probe from linearReadCheck:
// Serializable=true. Everything else in the request matches the
// linear read (KeysOnly, Limit=1) so the two probes execute the
// identical local read path; without the Serializable flag the check
// would silently degrade into a duplicate linear_read probe and the
// attribution contract (local-read-broken vs no-consensus) would be
// lost. It is the reason this test exists — the flag is the check.
func TestSerializableReadCheck_ForwardsSerializableKeysOnlyLimit(t *testing.T) {
	var captured *pb.RangeRequest
	s := newFakeServer()
	s.rangeFn = func(ctx context.Context, req *pb.RangeRequest) (*pb.RangeResponse, error) {
		captured = req
		return &pb.RangeResponse{}, nil
	}
	if err := serializableReadCheck(context.Background(), s); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if captured == nil {
		t.Fatal("Range was not invoked")
	}
	if !captured.Serializable {
		t.Error("RangeRequest.Serializable must be true; without it this check is a duplicate linear_read probe")
	}
	if !captured.KeysOnly {
		t.Error("RangeRequest.KeysOnly must be true to keep the read cheap")
	}
	if captured.Limit != 1 {
		t.Errorf("RangeRequest.Limit = %d, want 1", captured.Limit)
	}
}

// TestSerializableReadCheck_RangeError mirrors the linear_read
// contract: Range errors propagate verbatim so Aggregator owns the
// error-rate accounting.
func TestSerializableReadCheck_RangeError(t *testing.T) {
	want := errors.New("boom")
	s := newFakeServer()
	s.rangeFn = func(ctx context.Context, req *pb.RangeRequest) (*pb.RangeResponse, error) {
		return nil, want
	}
	got := serializableReadCheck(context.Background(), s)
	if !errors.Is(got, want) {
		t.Fatalf("serializableReadCheck should propagate Range error verbatim, got %v", got)
	}
}

// TestSerializableReadCheck_AppliesWithRoot: the probe must run under
// the server's root identity so RBAC misconfiguration cannot mask a
// real local read failure, exactly as linearReadCheck does.
func TestSerializableReadCheck_AppliesWithRoot(t *testing.T) {
	want := context.WithValue(context.Background(), rootKey{}, "sentinel")
	s := newFakeServer()
	s.authStore = &stubAuthStore{rootCtx: want}
	var gotCtx context.Context
	s.rangeFn = func(ctx context.Context, req *pb.RangeRequest) (*pb.RangeResponse, error) {
		gotCtx = ctx
		return &pb.RangeResponse{}, nil
	}
	if err := serializableReadCheck(context.Background(), s); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotCtx == nil {
		t.Fatal("Range was not invoked")
	}
	if v, _ := gotCtx.Value(rootKey{}).(string); v != "sentinel" {
		t.Errorf("Range ctx did not carry the root identity, got value %q", v)
	}
}
