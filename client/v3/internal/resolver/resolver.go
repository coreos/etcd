// Copyright 2021 The etcd Authors
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
	"fmt"
	"sync"

	"google.golang.org/grpc/resolver"
	"google.golang.org/grpc/resolver/manual"
	"google.golang.org/grpc/serviceconfig"

	leaderbalancer "go.etcd.io/etcd/client/v3/internal/balancer/leader"
	endpointpkg "go.etcd.io/etcd/client/v3/internal/endpoint"
)

const (
	Schema = "etcd-endpoints"
)

// Compile-time checks for the gRPC resolver contracts this type implements.
var (
	_ resolver.Builder  = (*ManualResolver)(nil)
	_ resolver.Resolver = (*ManualResolver)(nil)
)

// ManualResolver is a Resolver (and resolver.Builder) that can be updated
// using SetEndpoints.
type ManualResolver struct {
	*manual.Resolver
	mu sync.RWMutex
	// publishMu serializes publications so an older resolver state cannot publish
	// last.
	publishMu          sync.Mutex
	endpoints          []string
	endpointGeneration uint64
	balancerName       string
	leader             string
	leaderHintID       uint64
	serviceConfig      *serviceconfig.ParseResult
}

func New(endpoints ...string) *ManualResolver {
	r := manual.NewBuilderWithScheme(Schema)
	return &ManualResolver{Resolver: r, endpoints: append([]string(nil), endpoints...)}
}

// NewWithBalancer creates an etcd resolver that selects a registered gRPC
// balancer by name.
func NewWithBalancer(name string, endpoints ...string) *ManualResolver {
	r := manual.NewBuilderWithScheme(Schema)
	return &ManualResolver{Resolver: r, endpoints: append([]string(nil), endpoints...), balancerName: name}
}

// BalancerServiceConfig returns a gRPC service config for name.
func BalancerServiceConfig(name string) string {
	if name == "" {
		return `{"loadBalancingPolicy": "round_robin"}`
	}
	return fmt.Sprintf(`{"loadBalancingConfig":[{%q:{}}]}`, name)
}

// Build returns itself for Resolver, because it's both a builder and a resolver.
func (r *ManualResolver) Build(target resolver.Target, cc resolver.ClientConn, opts resolver.BuildOptions) (resolver.Resolver, error) {
	// Rebuilds after channel idleness must not race with hint or endpoint
	// publication: manual.Resolver.InitialState does not lock its state.
	r.publishMu.Lock()
	defer r.publishMu.Unlock()
	r.mu.RLock()
	balancerName := r.balancerName
	r.mu.RUnlock()
	serviceConfig := cc.ParseServiceConfig(BalancerServiceConfig(balancerName))
	if serviceConfig.Err != nil {
		return nil, serviceConfig.Err
	}
	r.mu.Lock()
	r.serviceConfig = serviceConfig
	r.mu.Unlock()
	r.Resolver.InitialState(r.state())
	return r.Resolver.Build(target, cc, opts)
}

func (r *ManualResolver) SetEndpoints(endpoints []string, generation uint64) {
	r.mu.Lock()
	if r.endpointGeneration != generation {
		r.leader = ""
		r.leaderHintID = 0
	}
	r.endpoints = append([]string(nil), endpoints...)
	r.endpointGeneration = generation
	r.mu.Unlock()
	r.updateState()
}

// SetLeader updates the leader hint and reports whether the endpoint generation
// accepted it.
//
// It rejects observations from older endpoint generations, including A -> B ->
// A changes whose values compare equal again.
func (r *ManualResolver) SetLeader(endpoint string, generation, hintID uint64) bool {
	if endpoint != "" {
		endpoint, _ = endpointpkg.Interpret(endpoint)
	}
	r.mu.Lock()
	if r.endpointGeneration != generation {
		r.mu.Unlock()
		return false
	}
	if r.leader == endpoint && r.leaderHintID == hintID {
		r.mu.Unlock()
		return true
	}
	r.leader = endpoint
	r.leaderHintID = hintID
	r.mu.Unlock()
	r.updateState()
	return true
}

func (r *ManualResolver) updateState() {
	r.publishMu.Lock()
	defer r.publishMu.Unlock()
	if getCC(r) != nil {
		r.UpdateState(r.state())
	}
}

// state builds the resolver state (endpoints + ServiceConfig + leader hint)
// from the state currently stored in r.
func (r *ManualResolver) state() resolver.State {
	r.mu.RLock()
	endpoints := append([]string(nil), r.endpoints...)
	balancerName := r.balancerName
	leaderAddress := r.leader
	leaderHintID := r.leaderHintID
	serviceConfig := r.serviceConfig
	r.mu.RUnlock()
	eps := make([]resolver.Endpoint, len(endpoints))
	for i, ep := range endpoints {
		addr, serverName := endpointpkg.Interpret(ep)
		eps[i] = resolver.Endpoint{Addresses: []resolver.Address{
			{Addr: addr, ServerName: serverName},
		}}
	}
	state := resolver.State{
		Endpoints:     eps,
		ServiceConfig: serviceConfig,
	}
	if balancerName == leaderbalancer.Name {
		state = leaderbalancer.WithHint(state, leaderAddress, leaderHintID)
	}
	return state
}

func getCC(r *ManualResolver) (cc resolver.ClientConn) {
	defer func() {
		if rec := recover(); rec != nil {
			cc = nil
		}
	}()

	return r.CC()
}
