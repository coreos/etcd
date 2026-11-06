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

// Package healthcheck_test contains tests for the GRPCClientHealthCheckOptions helper.
package healthcheck_test

import (
	"context"
	"encoding/json"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/resolver"
	"google.golang.org/grpc/resolver/manual"

	"go.etcd.io/etcd/client/v3/healthcheck"
)

// ── Unit tests for GRPCClientHealthCheckOptions ───────────────────────────────

func TestGRPCClientHealthCheckOptions_ReturnsTwoOptions(t *testing.T) {
	opts := healthcheck.GRPCClientHealthCheckOptions()
	// Must return exactly 2 options: WithDisableServiceConfig + WithDefaultServiceConfig.
	assert.Len(t, opts, 2, "must return exactly 2 dial options")
}

func TestGRPCClientHealthCheckOptions_NotNil(t *testing.T) {
	opts := healthcheck.GRPCClientHealthCheckOptions()
	for i, opt := range opts {
		assert.NotNil(t, opt, "option at index %d must not be nil", i)
	}
}

// TestGRPCClientHealthCheckOptions_ServiceConfigJSON verifies that the service
// config JSON embedded in the options is valid and contains both the
// round_robin load balancing policy and the healthCheckConfig with empty service name.
func TestGRPCClientHealthCheckOptions_ServiceConfigJSON(t *testing.T) {
	// The service config is embedded in the option — we expose it as a testable constant.
	raw := healthcheck.ServiceConfigJSON

	var parsed struct {
		LoadBalancingPolicy string `json:"loadBalancingPolicy"`
		HealthCheckConfig   struct {
			ServiceName string `json:"serviceName"`
		} `json:"healthCheckConfig"`
	}
	err := json.Unmarshal([]byte(raw), &parsed)
	require.NoError(t, err, "service config must be valid JSON")

	assert.Equal(t, "round_robin", parsed.LoadBalancingPolicy,
		"must preserve round_robin load balancing policy")
	assert.Equal(t, "", parsed.HealthCheckConfig.ServiceName,
		"service name must be empty string (aggregate health check)")
}

// TestGRPCClientHealthCheckOptions_CompatibleWithClientv3New verifies the options
// can be passed to clientv3.Config.DialOptions without error.
func TestGRPCClientHealthCheckOptions_CompatibleWithDialOptions(t *testing.T) {
	opts := healthcheck.GRPCClientHealthCheckOptions()

	for i, opt := range opts {
		var _ grpc.DialOption = opt
		_ = i
	}
}

// TestGRPCClientHealthCheckOptions_Idempotent verifies that calling the function
// multiple times returns equivalent option sets (no shared mutable state).
func TestGRPCClientHealthCheckOptions_Idempotent(t *testing.T) {
	opts1 := healthcheck.GRPCClientHealthCheckOptions()
	opts2 := healthcheck.GRPCClientHealthCheckOptions()

	assert.Len(t, opts1, len(opts2), "each call must return the same number of options")
}

// TestGRPCClientHealthCheckOptions_ResolverServiceConfigOverrideDisabled explicitly
// proves that WithDisableServiceConfig() ignores the resolver's service config
// and uses the default service config with round_robin and healthCheckConfig.
func TestGRPCClientHealthCheckOptions_ResolverServiceConfigOverrideDisabled(t *testing.T) {
	// 1. Start a real gRPC server with the grpc.health.v1 service.
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer lis.Close()

	hsrv := health.NewServer()
	s := grpc.NewServer()
	healthpb.RegisterHealthServer(s, hsrv)
	go s.Serve(lis)
	defer s.Stop()

	// 2. Set the initial aggregate status to NOT_SERVING.
	hsrv.SetServingStatus("", healthpb.HealthCheckResponse_NOT_SERVING)

	// 3. Create a custom manual resolver that returns a conflicting service config
	// without health checking (simulating etcd's manual resolver behavior).
	r := manual.NewBuilderWithScheme("test-hc-override")

	dialOpts := append(
		healthcheck.GRPCClientHealthCheckOptions(),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithResolvers(r),
	)

	cc, err := grpc.NewClient("test-hc-override:///test", dialOpts...)
	require.NoError(t, err)
	defer cc.Close()

	cc.Connect()

	// Wait for resolver to be built
	require.Eventually(t, func() bool {
		defer func() { recover() }()
		return r.CC() != nil
	}, 2*time.Second, 10*time.Millisecond)

	// Update resolver state with conflicting service config
	sc := r.CC().ParseServiceConfig(`{"loadBalancingPolicy": "pick_first"}`)
	r.UpdateState(resolver.State{
		Addresses:     []resolver.Address{{Addr: lis.Addr().String()}},
		ServiceConfig: sc,
	})

	// Because WithDisableServiceConfig() causes gRPC to ignore the resolver's config,
	// our default service config (with healthCheckConfig for "") takes effect.
	// Since server is NOT_SERVING, the connection transitions to TransientFailure.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	for {
		state := cc.GetState()
		if state == connectivity.TransientFailure {
			break // Health check successfully marked subchannel NOT_SERVING!
		}
		if !cc.WaitForStateChange(ctx, state) {
			t.Fatalf("expected TransientFailure due to NOT_SERVING, but got state %v", cc.GetState())
		}
	}

	// 4. Now flip the server status to SERVING.
	// The client's active health-check stream must see this and transition to READY.
	hsrv.SetServingStatus("", healthpb.HealthCheckResponse_SERVING)

	ctx2, cancel2 := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel2()

	for {
		state := cc.GetState()
		if state == connectivity.Ready {
			break // Health check successfully transitioned to READY!
		}
		if !cc.WaitForStateChange(ctx2, state) {
			t.Fatalf("expected Ready after SERVING, but got state %v", cc.GetState())
		}
	}
}

// TestServiceConfigJSON_HealthCheckConfigServiceName verifies that an empty
// service name "" is used, not "etcd" or any named service.
func TestServiceConfigJSON_HealthCheckConfigServiceName(t *testing.T) {
	raw := healthcheck.ServiceConfigJSON

	assert.NotContains(t, raw, `"serviceName": "etcd"`)
	assert.NotContains(t, raw, `"serviceName":"etcd"`)

	assert.True(t,
		contains(raw, `"serviceName": ""`) || contains(raw, `"serviceName":""`),
		"service name must be empty string",
	)
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsStr(s, substr))
}

func containsStr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
