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

package leader

import (
	"context"
	"errors"
	"sync"
	"testing"

	"google.golang.org/grpc/balancer"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/connectivity"
	estats "google.golang.org/grpc/experimental/stats"
	"google.golang.org/grpc/resolver"
	"google.golang.org/grpc/status"

	"go.etcd.io/etcd/api/v3/v3rpc/rpctypes"
)

// stubPicker records picks and returns a fixed result or error.
type stubPicker struct {
	picks int
	err   error
}

func (p *stubPicker) Pick(balancer.PickInfo) (balancer.PickResult, error) {
	p.picks++
	if p.err != nil {
		return balancer.PickResult{}, p.err
	}
	return balancer.PickResult{}, nil
}

type failureRecord struct {
	calls   int
	hintID  uint64
	address string
}

func (f *failureRecord) callback() func(uint64, string) {
	return func(hintID uint64, address string) {
		f.calls++
		f.hintID = hintID
		f.address = address
	}
}

func markedCtx(f *failureRecord) context.Context {
	return MarkMutation(context.Background(), f.callback())
}

func TestRoutingPickerPinnedEndpoint(t *testing.T) {
	endpoint := &stubPicker{}
	roundRobin := &stubPicker{}
	p := &routingPicker{
		roundRobin: roundRobin,
		endpoints:  map[string]balancer.Picker{"10.0.0.1:2379": endpoint},
	}

	if _, err := p.Pick(balancer.PickInfo{Ctx: PinEndpoint(context.Background(), "10.0.0.1:2379")}); err != nil {
		t.Fatalf("pinned pick failed: %v", err)
	}
	if endpoint.picks != 1 || roundRobin.picks != 0 {
		t.Fatalf("pinned probe: endpoint picks=%d round_robin picks=%d, want 1 and 0", endpoint.picks, roundRobin.picks)
	}

	// A probe pinned to an endpoint without a ready child must not run
	// against a different endpoint.
	if _, err := p.Pick(balancer.PickInfo{Ctx: PinEndpoint(context.Background(), "10.0.0.9:2379")}); !errors.Is(err, balancer.ErrNoSubConnAvailable) {
		t.Fatalf("unready pinned pick error = %v, want ErrNoSubConnAvailable", err)
	}
	if endpoint.picks != 1 || roundRobin.picks != 0 {
		t.Fatalf("unready pinned probe must not be rerouted: endpoint picks=%d round_robin picks=%d", endpoint.picks, roundRobin.picks)
	}
}

func TestRoutingPickerMutationToLeader(t *testing.T) {
	leader := &stubPicker{}
	roundRobin := &stubPicker{}
	p := &routingPicker{
		roundRobin:        roundRobin,
		leader:            leader,
		leaderHintID:      7,
		leaderHintAddress: "10.0.0.1:2379",
	}

	f := &failureRecord{}
	result, err := p.Pick(balancer.PickInfo{Ctx: markedCtx(f)})
	if err != nil {
		t.Fatalf("leader pick failed: %v", err)
	}
	if leader.picks != 1 || roundRobin.picks != 0 {
		t.Fatalf("mutation: leader picks=%d round_robin picks=%d, want 1 and 0", leader.picks, roundRobin.picks)
	}

	result.Done(balancer.DoneInfo{})
	if f.calls != 0 {
		t.Fatalf("successful RPC reported %d leader failures, want 0", f.calls)
	}

	result.Done(balancer.DoneInfo{Err: status.Error(codes.Unavailable, "conn refused")})
	if f.calls != 1 || f.hintID != 7 || f.address != "10.0.0.1:2379" {
		t.Fatalf("Unavailable completion reported %+v, want one failure with hint 7 at 10.0.0.1:2379", f)
	}

	// One routing state covers all attempts of an RPC: the failure is
	// reported once and later picks bypass the hint.
	result.Done(balancer.DoneInfo{Err: status.Error(codes.Unavailable, "conn refused")})
	if f.calls != 1 {
		t.Fatalf("repeated failure reported %d times, want 1", f.calls)
	}
	if _, err := p.Pick(balancer.PickInfo{Ctx: markedCtx(f)}); err != nil {
		t.Fatalf("bypassed pick failed: %v", err)
	}
	// markedCtx creates a fresh routing state, so this pick uses the leader
	// again; the bypass is per RPC, not per tracker.
	if leader.picks != 2 {
		t.Fatalf("fresh RPC: leader picks=%d, want 2", leader.picks)
	}
}

func TestRoutingPickerMutationSameCtxBypassesAfterFailure(t *testing.T) {
	leader := &stubPicker{}
	roundRobin := &stubPicker{}
	p := &routingPicker{
		roundRobin:        roundRobin,
		leader:            leader,
		leaderHintID:      7,
		leaderHintAddress: "10.0.0.1:2379",
	}

	f := &failureRecord{}
	ctx := markedCtx(f)
	result, err := p.Pick(balancer.PickInfo{Ctx: ctx})
	if err != nil {
		t.Fatalf("leader pick failed: %v", err)
	}
	result.Done(balancer.DoneInfo{Err: status.Error(codes.DeadlineExceeded, "slow")})
	if f.calls != 1 {
		t.Fatalf("DeadlineExceeded completion reported %d failures, want 1", f.calls)
	}

	// A retry sharing the RPC context must not return to the failed hint.
	if _, err := p.Pick(balancer.PickInfo{Ctx: ctx}); err != nil {
		t.Fatalf("retry pick failed: %v", err)
	}
	if leader.picks != 1 || roundRobin.picks != 1 {
		t.Fatalf("retry after leader failure: leader picks=%d round_robin picks=%d, want 1 and 1", leader.picks, roundRobin.picks)
	}
}

func TestRoutingPickerMutationWithoutReadyLeader(t *testing.T) {
	roundRobin := &stubPicker{}
	p := &routingPicker{
		roundRobin:        roundRobin,
		leaderHintID:      3,
		leaderHintAddress: "10.0.0.1:2379",
	}

	f := &failureRecord{}
	if _, err := p.Pick(balancer.PickInfo{Ctx: markedCtx(f)}); err != nil {
		t.Fatalf("pick failed: %v", err)
	}
	if roundRobin.picks != 1 {
		t.Fatalf("mutation without ready leader: round_robin picks=%d, want 1", roundRobin.picks)
	}
	// A hinted leader whose SubConn is not ready invalidates the hint:
	// a transient reconnect costs one probe round, and Status republishes
	// the healthy leader.
	if f.calls != 1 || f.hintID != 3 || f.address != "10.0.0.1:2379" {
		t.Fatalf("unready hint reported %+v, want one failure with hint 3", f)
	}
}

func TestRoutingPickerMutationWithoutHint(t *testing.T) {
	roundRobin := &stubPicker{}
	p := &routingPicker{roundRobin: roundRobin}

	f := &failureRecord{}
	if _, err := p.Pick(balancer.PickInfo{Ctx: markedCtx(f)}); err != nil {
		t.Fatalf("pick failed: %v", err)
	}
	if roundRobin.picks != 1 || f.calls != 0 {
		t.Fatalf("mutation without hint: round_robin picks=%d failures=%d, want 1 and 0", roundRobin.picks, f.calls)
	}
}

func TestRoutingPickerLeaderPickError(t *testing.T) {
	leader := &stubPicker{err: errors.New("pick failed")}
	roundRobin := &stubPicker{}
	p := &routingPicker{
		roundRobin:        roundRobin,
		leader:            leader,
		leaderHintID:      9,
		leaderHintAddress: "10.0.0.1:2379",
	}

	f := &failureRecord{}
	if _, err := p.Pick(balancer.PickInfo{Ctx: markedCtx(f)}); err != nil {
		t.Fatalf("fallback pick failed: %v", err)
	}
	if leader.picks != 1 || roundRobin.picks != 1 {
		t.Fatalf("leader pick error: leader picks=%d round_robin picks=%d, want 1 and 1", leader.picks, roundRobin.picks)
	}
	if f.calls != 1 || f.hintID != 9 {
		t.Fatalf("leader pick error reported %+v, want one failure with hint 9", f)
	}
}

func TestRoutingPickerReadUsesRoundRobin(t *testing.T) {
	leader := &stubPicker{}
	roundRobin := &stubPicker{}
	p := &routingPicker{
		roundRobin:        roundRobin,
		leader:            leader,
		leaderHintID:      7,
		leaderHintAddress: "10.0.0.1:2379",
	}

	if _, err := p.Pick(balancer.PickInfo{Ctx: context.Background()}); err != nil {
		t.Fatalf("read pick failed: %v", err)
	}
	if leader.picks != 0 || roundRobin.picks != 1 {
		t.Fatalf("unmarked RPC: leader picks=%d round_robin picks=%d, want 0 and 1", leader.picks, roundRobin.picks)
	}
}

func TestInvalidatesLeaderHint(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil error", nil, false},
		{"unavailable", status.Error(codes.Unavailable, "down"), true},
		{"deadline exceeded", status.Error(codes.DeadlineExceeded, "slow"), true},
		{"not leader", rpctypes.ErrGRPCNotLeader, true},
		{"canceled", status.Error(codes.Canceled, "caller canceled"), false},
		{"permission denied", status.Error(codes.PermissionDenied, "denied"), false},
		{"plain error", errors.New("boom"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := invalidatesLeaderHint(tt.err); got != tt.want {
				t.Errorf("invalidatesLeaderHint(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

func TestHintAttributesRoundTrip(t *testing.T) {
	// A zero resolver.State has nil Attributes; WithHint and hintFromState
	// must tolerate it because the resolver builds states without attributes.
	var state resolver.State
	if hint := hintFromState(state); hint.address != "" || hint.id != 0 {
		t.Fatalf("hint from zero state = %+v, want empty", hint)
	}

	state = WithHint(state, "10.0.0.1:2379", 42)
	hint := hintFromState(state)
	if hint.address != "10.0.0.1:2379" || hint.id != 42 {
		t.Fatalf("hint round trip = %+v, want 10.0.0.1:2379 with id 42", hint)
	}
}

// noopMetricsRecorder satisfies estats.MetricsRecorder for pickfirst, which
// requires a non-nil recorder from balancer.ClientConn.MetricsRecorder.
type noopMetricsRecorder struct {
	estats.MetricsRecorder
}

func (noopMetricsRecorder) RecordInt64Count(*estats.Int64CountHandle, int64, ...string)       {}
func (noopMetricsRecorder) RecordFloat64Count(*estats.Float64CountHandle, float64, ...string) {}
func (noopMetricsRecorder) RecordInt64Histo(*estats.Int64HistoHandle, int64, ...string)       {}
func (noopMetricsRecorder) RecordFloat64Histo(*estats.Float64HistoHandle, float64, ...string) {}
func (noopMetricsRecorder) RecordInt64Gauge(*estats.Int64GaugeHandle, int64, ...string)       {}
func (noopMetricsRecorder) RecordInt64UpDownCount(*estats.Int64UpDownCountHandle, int64, ...string) {
}
func (noopMetricsRecorder) RegisterAsyncReporter(estats.AsyncMetricReporter, ...estats.AsyncMetric) func() {
	return func() {}
}

// fakeSubConn captures the state listeners that pickfirst registers so the
// test can drive connectivity and health updates.
type fakeSubConn struct {
	balancer.SubConn
	addr           resolver.Address
	stateListener  func(balancer.SubConnState)
	healthListener func(balancer.SubConnState)
}

func (sc *fakeSubConn) Connect() {}

func (sc *fakeSubConn) RegisterHealthListener(fn func(balancer.SubConnState)) {
	sc.healthListener = fn
}

// reportReady drives the pickfirst readiness flow: raw Ready, then a serving
// health update, which round_robin requires because it enables the pickfirst
// health listener.
func (sc *fakeSubConn) reportReady(t *testing.T) {
	t.Helper()
	if sc.stateListener == nil {
		t.Fatalf("subconn %s has no state listener", sc.addr.Addr)
	}
	sc.stateListener(balancer.SubConnState{ConnectivityState: connectivity.Ready})
	if sc.healthListener == nil {
		t.Fatalf("subconn %s has no health listener", sc.addr.Addr)
	}
	sc.healthListener(balancer.SubConnState{ConnectivityState: connectivity.Ready})
}

// fakeBalancerClientConn records SubConns and published balancer states.
type fakeBalancerClientConn struct {
	balancer.ClientConn

	mu       sync.Mutex
	subconns []*fakeSubConn
	states   []balancer.State
}

func (cc *fakeBalancerClientConn) NewSubConn(addrs []resolver.Address, opts balancer.NewSubConnOptions) (balancer.SubConn, error) {
	cc.mu.Lock()
	defer cc.mu.Unlock()
	sc := &fakeSubConn{addr: addrs[0], stateListener: opts.StateListener}
	cc.subconns = append(cc.subconns, sc)
	return sc, nil
}

func (cc *fakeBalancerClientConn) UpdateState(state balancer.State) {
	cc.mu.Lock()
	defer cc.mu.Unlock()
	cc.states = append(cc.states, state)
}

func (cc *fakeBalancerClientConn) MetricsRecorder() estats.MetricsRecorder {
	return noopMetricsRecorder{}
}

func (cc *fakeBalancerClientConn) lastPicker(t *testing.T) *routingPicker {
	t.Helper()
	cc.mu.Lock()
	defer cc.mu.Unlock()
	if len(cc.states) == 0 {
		t.Fatal("balancer published no state")
	}
	picker, ok := cc.states[len(cc.states)-1].Picker.(*routingPicker)
	if !ok {
		t.Fatalf("published picker is %T, want *routingPicker", cc.states[len(cc.states)-1].Picker)
	}
	return picker
}

func (cc *fakeBalancerClientConn) subConnFor(t *testing.T, address string) balancer.SubConn {
	t.Helper()
	cc.mu.Lock()
	defer cc.mu.Unlock()
	for _, sc := range cc.subconns {
		if sc.addr.Addr == address {
			return sc
		}
	}
	t.Fatalf("no SubConn for %s", address)
	return nil
}

// TestLeaderBalancerEndToEnd drives the real round_robin child through a fake
// ClientConn: hint-only resolver updates must produce a new picker that routes
// mutations to the hinted endpoint. This is the grpc-go behavior the whole
// design depends on; the test fails loudly if an upgrade changes it.
func TestLeaderBalancerEndToEnd(t *testing.T) {
	cc := &fakeBalancerClientConn{}
	b := builder{}.Build(cc, balancer.BuildOptions{})

	state := resolver.State{Endpoints: []resolver.Endpoint{
		{Addresses: []resolver.Address{{Addr: "10.0.0.1:2379"}}},
		{Addresses: []resolver.Address{{Addr: "10.0.0.2:2379"}}},
	}}
	state = WithHint(state, "10.0.0.1:2379", 1)
	if err := b.UpdateClientConnState(balancer.ClientConnState{ResolverState: state}); err != nil {
		t.Fatalf("UpdateClientConnState failed: %v", err)
	}

	for _, sc := range cc.subconns {
		sc.reportReady(t)
	}

	picker := cc.lastPicker(t)
	if picker.leader == nil {
		t.Fatal("hinted leader has no ready picker")
	}
	if len(picker.endpoints) != 2 {
		t.Fatalf("picker tracks %d endpoints, want 2", len(picker.endpoints))
	}

	f := &failureRecord{}
	result, err := picker.Pick(balancer.PickInfo{Ctx: markedCtx(f)})
	if err != nil {
		t.Fatalf("mutation pick failed: %v", err)
	}
	if leaderSubConn := cc.subConnFor(t, "10.0.0.1:2379"); result.SubConn != leaderSubConn {
		t.Fatalf("mutation routed to %v, want the hinted leader SubConn", result.SubConn)
	}
	if f.calls != 0 {
		t.Fatalf("successful pick reported %d failures, want 0", f.calls)
	}

	// A pinned probe reaches the specified endpoint.
	probe, err := picker.Pick(balancer.PickInfo{Ctx: PinEndpoint(context.Background(), "10.0.0.2:2379")})
	if err != nil {
		t.Fatalf("pinned pick failed: %v", err)
	}
	if followerSubConn := cc.subConnFor(t, "10.0.0.2:2379"); probe.SubConn != followerSubConn {
		t.Fatalf("probe routed to %v, want the pinned endpoint SubConn", probe.SubConn)
	}

	// A hint-only update (same endpoints, new hint) must publish a new picker
	// that routes mutations to the new leader.
	moved := WithHint(state, "10.0.0.2:2379", 2)
	if moveErr := b.UpdateClientConnState(balancer.ClientConnState{ResolverState: moved}); moveErr != nil {
		t.Fatalf("hint-only UpdateClientConnState failed: %v", moveErr)
	}
	picker = cc.lastPicker(t)
	f2 := &failureRecord{}
	result, err = picker.Pick(balancer.PickInfo{Ctx: markedCtx(f2)})
	if err != nil {
		t.Fatalf("mutation pick after hint move failed: %v", err)
	}
	if newLeaderSubConn := cc.subConnFor(t, "10.0.0.2:2379"); result.SubConn != newLeaderSubConn {
		t.Fatalf("mutation after hint move routed to %v, want the new leader SubConn", result.SubConn)
	}
}
