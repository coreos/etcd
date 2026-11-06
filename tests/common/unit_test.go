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

//go:build !(e2e || integration)

package common

import (
	"go.etcd.io/etcd/tests/v3/framework"
	"go.etcd.io/etcd/tests/v3/framework/config"
)

func init() {
	testRunner = framework.UnitTestRunner
	clusterTestCases = unitClusterTestCases
}

func unitClusterTestCases() []testCase {
	return nil
}

// WithAuth is when a build tag (e.g. e2e or integration) isn't configured
// in IDE, then IDE may complain "Unresolved reference 'WithAuth'".
// So we need to define a default WithAuth to resolve such case.
func WithAuth(userName, password string) config.ClientOption {
	return func(any) {}
}

func WithAuthToken(token string) config.ClientOption {
	return func(any) {}
}

func WithEndpoints(endpoints []string) config.ClientOption {
	return func(any) {}
}

func WithHTTP2Debug() config.ClusterOption {
	return func(c *config.ClusterConfig) {}
}

func WithTCPClient() config.ClusterOption {
	return func(c *config.ClusterConfig) {}
}

func WithUnixClient() config.ClusterOption {
	return func(c *config.ClusterConfig) {}
}

func WithBasePort(port int) config.ClusterOption {
	return func(c *config.ClusterConfig) {}
}

func configureMirrorDestTLS(mm *config.MakeMirrorOptions, _ config.TLSConfig) {}
