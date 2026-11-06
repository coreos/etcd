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

package resolver

import (
	"sync"
	"testing"

	"google.golang.org/grpc/resolver"
	"google.golang.org/grpc/serviceconfig"

	leaderbalancer "go.etcd.io/etcd/client/v3/internal/balancer/leader"
)

// fakeResolverClientConn records every published resolver state.
type fakeResolverClientConn struct {
	mu     sync.Mutex
	states []resolver.State
}

func (cc *fakeResolverClientConn) UpdateState(state resolver.State) error {
	cc.mu.Lock()
	defer cc.mu.Unlock()
	cc.states = append(cc.states, state)
	return nil
}

func (cc *fakeResolverClientConn) ReportError(error) {}

func (cc *fakeResolverClientConn) NewAddress([]resolver.Address) {}

func (cc *fakeResolverClientConn) ParseServiceConfig(string) *serviceconfig.ParseResult {
	return &serviceconfig.ParseResult{}
}

func (cc *fakeResolverClientConn) stateCount() int {
	cc.mu.Lock()
	defer cc.mu.Unlock()
	return len(cc.states)
}

func (cc *fakeResolverClientConn) lastState(t *testing.T) resolver.State {
	t.Helper()
	cc.mu.Lock()
	defer cc.mu.Unlock()
	if len(cc.states) == 0 {
		t.Fatal("resolver published no state")
	}
	return cc.states[len(cc.states)-1]
}

func build(t *testing.T, r *ManualResolver) *fakeResolverClientConn {
	t.Helper()
	cc := &fakeResolverClientConn{}
	if _, err := r.Build(resolver.Target{}, cc, resolver.BuildOptions{}); err != nil {
		t.Fatalf("Build failed: %v", err)
	}
	return cc
}

func TestBalancerServiceConfig(t *testing.T) {
	if got := BalancerServiceConfig(""); got != "{\"loadBalancingPolicy\": \"round_robin\"}" {
		t.Errorf("BalancerServiceConfig(empty) = %s", got)
	}
	want := "{\"loadBalancingConfig\":[{\"etcd_leader_aware\":{}}]}"
	if got := BalancerServiceConfig(leaderbalancer.Name); got != want {
		t.Errorf("BalancerServiceConfig(%q) = %s, want %s", leaderbalancer.Name, got, want)
	}
}

func TestNewCopiesEndpoints(t *testing.T) {
	eps := []string{"http://a:2379"}
	r := New(eps...)
	eps[0] = "http://mutated:2379"
	if r.endpoints[0] != "http://a:2379" {
		t.Fatalf("resolver shares the caller's backing array: %v", r.endpoints)
	}
}

func TestBuildPublishesEndpoints(t *testing.T) {
	r := NewWithBalancer(leaderbalancer.Name, "http://a:2379", "http://b:2379")
	cc := build(t, r)
	state := cc.lastState(t)
	if len(state.Endpoints) != 2 {
		t.Fatalf("published %d endpoints, want 2", len(state.Endpoints))
	}
	if got := state.Endpoints[0].Addresses[0].Addr; got != "a:2379" {
		t.Fatalf("first endpoint address = %q, want a:2379", got)
	}
}

func TestBuildConcurrentWithSetLeader(t *testing.T) {
	r := NewWithBalancer(leaderbalancer.Name, "http://a:2379")
	cc := build(t, r)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for range 1000 {
			if _, err := r.Build(resolver.Target{}, cc, resolver.BuildOptions{}); err != nil {
				t.Error(err)
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		for i := range 1000 {
			r.SetLeader("http://a:2379", 0, uint64(i+1))
		}
	}()
	wg.Wait()
	if !cc.lastState(t).Attributes.Equal(r.state().Attributes) {
		t.Fatal("rebuild overwrote the latest leader hint")
	}
}

func TestSetLeaderFencing(t *testing.T) {
	r := NewWithBalancer(leaderbalancer.Name, "http://a:2379")
	cc := build(t, r)
	if r.endpointGeneration != 0 {
		t.Fatalf("initial generation = %d, want 0", r.endpointGeneration)
	}
	published := cc.stateCount()

	if !r.SetLeader("http://a:2379", 0, 1) {
		t.Fatal("SetLeader with the current generation was rejected")
	}
	if r.leader != "a:2379" || r.leaderHintID != 1 {
		t.Fatalf("leader = %q hint %d, want a:2379 with hint 1", r.leader, r.leaderHintID)
	}
	published++
	if got := cc.stateCount(); got != published {
		t.Fatalf("published %d states after SetLeader, want %d", got, published)
	}

	// Republishing the same leader and hint is a no-op.
	if !r.SetLeader("http://a:2379", 0, 1) {
		t.Fatal("SetLeader with an unchanged hint was rejected")
	}
	if got := cc.stateCount(); got != published {
		t.Fatalf("unchanged hint published state %d, want %d", got, published)
	}

	// An observation from another endpoint generation is rejected: only the
	// current generation may publish.
	if r.SetLeader("http://b:2379", 1, 2) {
		t.Fatal("SetLeader accepted a future generation")
	}
	if r.leader != "a:2379" {
		t.Fatalf("rejected SetLeader changed the leader to %q", r.leader)
	}
	if got := cc.stateCount(); got != published {
		t.Fatalf("rejected SetLeader published state %d, want %d", got, published)
	}

	// An endpoint change clears the hint synchronously.
	r.SetEndpoints([]string{"http://a:2379", "http://b:2379"}, 1)
	if r.leader != "" || r.leaderHintID != 0 {
		t.Fatalf("endpoint change left leader %q hint %d, want cleared", r.leader, r.leaderHintID)
	}
	published++
	if got := cc.stateCount(); got != published {
		t.Fatalf("SetEndpoints published %d states, want %d", got, published)
	}
	if got := len(cc.lastState(t).Endpoints); got != 2 {
		t.Fatalf("SetEndpoints published %d endpoints, want 2", got)
	}

	// A stale observation is rejected even when its address repeats the
	// pre-change leader: A -> B -> A endpoint churn must not revive a hint
	// whose probes predate the change.
	if r.SetLeader("http://a:2379", 0, 9) {
		t.Fatal("SetLeader accepted a stale generation after endpoint churn")
	}
	if got := cc.stateCount(); got != published {
		t.Fatalf("stale SetLeader published state %d, want %d", got, published)
	}

	if !r.SetLeader("http://b:2379", 1, 5) {
		t.Fatal("SetLeader with the new generation was rejected")
	}
	if r.leader != "b:2379" || r.leaderHintID != 5 {
		t.Fatalf("leader = %q hint %d, want b:2379 with hint 5", r.leader, r.leaderHintID)
	}

	if !r.SetLeader("", 1, 0) {
		t.Fatal("clearing the leader was rejected")
	}
	if r.leader != "" || r.leaderHintID != 0 {
		t.Fatalf("cleared leader = %q hint %d, want empty", r.leader, r.leaderHintID)
	}
}
