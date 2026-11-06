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

// Package healthcheck provides the dial options that let a clientv3
// client drop an etcd member from its rotation when that member's
// server-side health self-check reports NOT_SERVING.
//
// Pass the result to clientv3.Config.DialOptions:
//
//	clientv3.New(clientv3.Config{
//		Endpoints:   []string{"member-0", "member-1", "member-2"},
//		DialOptions: healthcheck.GRPCClientHealthCheckOptions(),
//	})
//
// This mirrors upstream etcd PR #21674 so the package can be dropped
// in favour of the upstream one once that PR merges.
package healthcheck

import (
	"google.golang.org/grpc"
	// Register the gRPC client's health check implementation via its init().
	// It sets internal.HealthCheckFunc = clientHealthCheck, which is required
	// for the client-side healthCheckConfig (see GRPCClientHealthCheckOptions)
	// to perform health checks against the server.
	_ "google.golang.org/grpc/health"
)

// GRPCClientHealthCheckOptions returns the dial options that enable
// grpc-go's client-side health checking against the aggregate
// grpc.health.v1 entry (service name "").
//
// WithDisableServiceConfig is load-bearing, not cosmetic: clientv3's
// EtcdManualResolver always publishes a non-nil ServiceConfig, and
// grpc-go ignores WithDefaultServiceConfig unless the resolver's
// ServiceConfig is nil or service config lookup is disabled. So the
// round_robin policy the resolver would have supplied is written back
// here, keeping the default endpoint selection behaviour unchanged.
//
// A member is dropped only while its aggregate entry reports
// NOT_SERVING. Server-side, that entry is driven by the conditions
// where moving the client to another member is an improvement; see
// aggregateEligible in server/etcdserver/api/v3rpc/health.go.
func GRPCClientHealthCheckOptions() []grpc.DialOption {
	return []grpc.DialOption{
		grpc.WithDisableServiceConfig(),
		grpc.WithDefaultServiceConfig(`{"loadBalancingPolicy": "round_robin", "healthCheckConfig": {"serviceName": ""}}`),
	}
}
