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

// Package healthcheck provides helpers to configure gRPC client health checking
// for etcd client connections.
package healthcheck

import "google.golang.org/grpc"

// ServiceConfigJSON is the default gRPC service configuration used when client health
// checking is enabled. It configures the round_robin load balancing policy and
// requests aggregate server health status using the empty service name ("").
const ServiceConfigJSON = `{"loadBalancingPolicy": "round_robin", "healthCheckConfig": {"serviceName": ""}}`

// GRPCClientHealthCheckOptions returns the gRPC DialOptions required to enable
// client-side health checking with etcd's manual resolver.
//
// Because etcd's manual resolver supplies its own default service config specifying
// round_robin without healthCheckConfig, grpc.WithDisableServiceConfig() is included
// to prevent the resolver from overriding the health checking configuration.
func GRPCClientHealthCheckOptions() []grpc.DialOption {
	return []grpc.DialOption{
		grpc.WithDisableServiceConfig(),
		grpc.WithDefaultServiceConfig(ServiceConfigJSON),
	}
}
