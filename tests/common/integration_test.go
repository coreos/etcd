// Copyright 2022 The etcd Authors
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

//go:build integration

package common

import (
	"go.etcd.io/etcd/tests/v3/framework"
	"go.etcd.io/etcd/tests/v3/framework/config"
	"go.etcd.io/etcd/tests/v3/framework/integration"
)

func init() {
	testRunner = framework.IntegrationTestRunner
	clusterTestCases = integrationClusterTestCases
}

func integrationClusterTestCases() []testCase {
	return []testCase{
		{
			name:   "NoTLS",
			config: config.ClusterConfig{ClusterSize: 1},
		},
		{
			name:   "PeerTLS and ClientTLS",
			config: config.ClusterConfig{ClusterSize: 3, PeerTLS: config.ManualTLS, ClientTLS: config.ManualTLS},
		},
		{
			name:   "PeerAutoTLS and ClientAutoTLS",
			config: config.ClusterConfig{ClusterSize: 3, PeerTLS: config.AutoTLS, ClientTLS: config.AutoTLS},
		},
	}
}

func WithAuth(userName, password string) config.ClientOption {
	return integration.WithAuth(userName, password)
}

func WithAuthToken(token string) config.ClientOption {
	return integration.WithAuthToken(token)
}

func WithEndpoints(endpoints []string) config.ClientOption {
	return integration.WithEndpoints(endpoints)
}

func WithHTTP2Debug() config.ClusterOption {
	return integration.WithHTTP2Debug()
}

func WithUnixClient() config.ClusterOption {
	return integration.WithUnixClient()
}

func WithTCPClient() config.ClusterOption {
	return integration.WithTCPClient()
}

func WithBasePort(port int) config.ClusterOption {
	return func(c *config.ClusterConfig) {}
}

func ensureIntegrationClusterContext(c *config.ClusterConfig) *integration.ClusterContext {
	ctx, _ := c.ClusterContext.(*integration.ClusterContext)
	if ctx == nil {
		ctx = &integration.ClusterContext{}
	}
	return ctx
}

func configureMirrorDestTLS(mm *config.MakeMirrorOptions, _ config.TLSConfig) {}
