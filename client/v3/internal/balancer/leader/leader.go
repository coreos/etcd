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

// Package leader implements an opt-in policy layered on round_robin.
//
// It routes marked mutations to a ready endpoint hinted as leader, routes
// internal probes to a specified endpoint, and delegates other RPCs to
// round_robin.
package leader

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"

	"google.golang.org/grpc/balancer"
	"google.golang.org/grpc/balancer/endpointsharding"
	"google.golang.org/grpc/balancer/roundrobin"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/resolver"
	"google.golang.org/grpc/status"

	"go.etcd.io/etcd/api/v3/v3rpc/rpctypes"
)

// Name is namespaced because grpc-go's process-wide registry lets a later
// registration replace an existing policy with the same name.
const Name = "etcd_leader_aware"

func init() {
	balancer.Register(builder{})
}

// Compile-time checks for the gRPC balancer contracts this package
// implements.
var (
	_ balancer.Builder    = (*builder)(nil)
	_ balancer.Balancer   = (*leaderBalancer)(nil)
	_ balancer.ClientConn = (*childClientConn)(nil)
	_ balancer.Picker     = (*routingPicker)(nil)
)

type builder struct{}

func (builder) Name() string { return Name }

func (builder) Build(cc balancer.ClientConn, opts balancer.BuildOptions) balancer.Balancer {
	b := &leaderBalancer{cc: cc}
	childCC := &childClientConn{ClientConn: cc, updateState: b.updateState}
	roundRobin := balancer.Get(roundrobin.Name)
	if roundRobin == nil {
		panic("leader balancer: round_robin is not registered")
	}
	b.child = roundRobin.Build(childCC, opts)
	return b
}

type leaderBalancer struct {
	cc    balancer.ClientConn
	child balancer.Balancer

	mu         sync.RWMutex
	leaderHint leaderHint
}

func (b *leaderBalancer) UpdateClientConnState(state balancer.ClientConnState) error {
	b.mu.Lock()
	b.leaderHint = hintFromState(state.ResolverState)
	b.mu.Unlock()
	return b.child.UpdateClientConnState(state)
}

func (b *leaderBalancer) ResolverError(err error) {
	b.child.ResolverError(err)
}

// UpdateSubConnState is deprecated in the balancer.Balancer contract, which
// still requires it; children that register StateListeners never receive it.
//
//nolint:staticcheck // SA1019: forwarding a required interface method
func (b *leaderBalancer) UpdateSubConnState(sc balancer.SubConn, state balancer.SubConnState) {
	b.child.UpdateSubConnState(sc, state)
}

func (b *leaderBalancer) Close() {
	b.child.Close()
}

func (b *leaderBalancer) ExitIdle() {
	b.child.ExitIdle()
}

func (b *leaderBalancer) updateState(state balancer.State) {
	b.mu.RLock()
	hint := b.leaderHint
	b.mu.RUnlock()
	state.Picker = pickerForLeader(state.Picker, hint)
	b.cc.UpdateState(state)
}

type childClientConn struct {
	balancer.ClientConn
	updateState func(balancer.State)
}

func (cc *childClientConn) UpdateState(state balancer.State) {
	cc.updateState(state)
}

func pickerForLeader(roundRobinPicker balancer.Picker, hint leaderHint) balancer.Picker {
	picker := &routingPicker{
		roundRobin:        roundRobinPicker,
		leaderHintID:      hint.id,
		leaderHintAddress: hint.address,
		endpoints:         make(map[string]balancer.Picker),
	}
	// Reuse round_robin's endpoint child pickers so it retains SubConn ownership
	// and readiness decisions.
	//
	// grpc-go currently exposes an endpointsharding picker here. Recheck this
	// adapter when upgrading grpc-go: that helper is experimental, and an
	// unknown picker leaves application RPCs on round_robin.
	for _, child := range endpointsharding.ChildStatesFromPicker(roundRobinPicker) {
		if child.State.ConnectivityState != connectivity.Ready || child.State.Picker == nil {
			continue
		}
		for _, address := range child.Endpoint.Addresses {
			picker.endpoints[address.Addr] = child.State.Picker
			if address.Addr == hint.address {
				picker.leader = child.State.Picker
			}
		}
	}
	return picker
}

type routingPicker struct {
	roundRobin        balancer.Picker
	leader            balancer.Picker
	leaderHintID      uint64
	leaderHintAddress string
	endpoints         map[string]balancer.Picker
}

func (p *routingPicker) Pick(info balancer.PickInfo) (balancer.PickResult, error) {
	if address := pinnedEndpoint(info.Ctx); address != "" {
		if endpoint := p.endpoints[address]; endpoint != nil {
			return endpoint.Pick(info)
		}
		// A Status probe must not run against a different endpoint.
		// ErrNoSubConnAvailable parks the probe until a picker change or its
		// timeout instead of silently probing the wrong member.
		return balancer.PickResult{}, balancer.ErrNoSubConnAvailable
	}

	// A consensus write sent to a follower first crosses the peer link to the
	// leader before normal replication.
	//
	// Routing it to the leader removes that proposal copy. In a healthy
	// three-voter cluster, the payload model predicts roughly 25 percent fewer
	// peer-sent bytes. That prediction counts serialized Raft messages; it is
	// not a wire-byte or cross-zone cost measurement.
	mutation := mutationFromContext(info.Ctx)
	if mutation != nil && !mutation.bypassLeader.Load() {
		if p.leader == nil {
			if p.leaderHintID != 0 {
				// A missing ready SubConn does not prove that leadership changed.
				//
				// Invalidate now rather than wait for periodic refresh. A transient
				// reconnect costs at most one probe round because Status republishes
				// the healthy leader.
				mutation.failLeader(true, p.leaderHintID, p.leaderHintAddress)
			}
			return p.roundRobin.Pick(info)
		}
		// A failed leader pick or RPC bypasses the hint for this context, so later
		// attempts use native round_robin.
		result, err := p.leader.Pick(info)
		if err != nil {
			mutation.failLeader(true, p.leaderHintID, p.leaderHintAddress)
			return p.roundRobin.Pick(info)
		}

		childDone := result.Done
		result.Done = func(info balancer.DoneInfo) {
			if info.Err != nil {
				// A late completion may belong to an older picker, so invalidate
				// only the hint that selected this RPC.
				mutation.failLeader(invalidatesLeaderHint(info.Err), p.leaderHintID, p.leaderHintAddress)
			}
			if childDone != nil {
				childDone(info)
			}
		}
		return result, nil
	}
	return p.roundRobin.Pick(info)
}

func invalidatesLeaderHint(err error) bool {
	code := status.Code(err)
	// Unavailable means the hinted endpoint did not answer, and ErrNotLeader
	// means it answered but is no longer leader.
	//
	// DeadlineExceeded also invalidates the hint because a paused or black-holed
	// leader can keep its transport open and produce no other signal. A tight
	// deadline against a healthy but slow leader costs one coalesced probe round;
	// Status usually republishes the same leader.
	return code == codes.Unavailable || code == codes.DeadlineExceeded || errors.Is(rpctypes.Error(err), rpctypes.ErrNotLeader)
}

type endpointKey struct{}

// PinEndpoint marks an internal probe for routing to the specified endpoint.
func PinEndpoint(ctx context.Context, address string) context.Context {
	return context.WithValue(ctx, endpointKey{}, address)
}

func pinnedEndpoint(ctx context.Context) string {
	address, _ := ctx.Value(endpointKey{}).(string)
	return address
}

type mutationKey struct{}

type mutationRouting struct {
	bypassLeader    atomic.Bool
	onLeaderFailure func(hintID uint64, hintAddress string)
}

// MarkMutation marks an RPC for leader-aware routing.
//
// onLeaderFailure runs at most once with the identity and address of the hint
// used by a failed leader attempt.
func MarkMutation(ctx context.Context, onLeaderFailure func(hintID uint64, hintAddress string)) context.Context {
	return context.WithValue(ctx, mutationKey{}, &mutationRouting{onLeaderFailure: onLeaderFailure})
}

func mutationFromContext(ctx context.Context) *mutationRouting {
	mutation, _ := ctx.Value(mutationKey{}).(*mutationRouting)
	return mutation
}

func (m *mutationRouting) failLeader(invalidate bool, hintID uint64, hintAddress string) {
	if m.bypassLeader.Swap(true) || !invalidate || m.onLeaderFailure == nil {
		return
	}
	m.onLeaderFailure(hintID, hintAddress)
}

type hintKey struct{}

type leaderHint struct {
	address string
	id      uint64
}

// WithHint stores a leader-address hint and its identity in state.
func WithHint(state resolver.State, address string, id uint64) resolver.State {
	state.Attributes = state.Attributes.WithValue(hintKey{}, leaderHint{address: address, id: id})
	return state
}

func hintFromState(state resolver.State) leaderHint {
	hint, _ := state.Attributes.Value(hintKey{}).(leaderHint)
	return hint
}
