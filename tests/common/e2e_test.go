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

//go:build e2e

package common

import (
	"fmt"
	"os"
	"strconv"
	"time"

	"go.etcd.io/etcd/client/pkg/v3/fileutil"
	"go.etcd.io/etcd/tests/v3/framework"
	"go.etcd.io/etcd/tests/v3/framework/config"
	"go.etcd.io/etcd/tests/v3/framework/e2e"
)

func init() {
	testRunner = framework.E2eTestRunner
	clusterTestCases = e2eClusterTestCases
}

const (
	// minimalE2eEnabledEnvVarName is for reducing e2e test matrix, leading to faster CI runtimes in some cases(e.g., presubmits).
	// See https://github.com/etcd-io/etcd/issues/18983 for background.
	minimalE2eEnabledEnvVarName = "E2E_TEST_MINIMAL"
)

func minimalE2eEnabled() bool {
	v, ok := os.LookupEnv(minimalE2eEnabledEnvVarName)
	if !ok {
		return false
	}
	parsed, err := strconv.ParseBool(v)
	if err != nil {
		fmt.Printf("Invalid %s value %q: %v\n", minimalE2eEnabledEnvVarName, v, err)
		return false
	}
	return parsed
}

func e2eClusterTestCases() []testCase {
	minimalTestCases := []testCase{
		{
			name:   "NoTLS",
			config: config.ClusterConfig{ClusterSize: 1},
		},
		{
			name:   "PeerTLS and ClientTLS",
			config: config.ClusterConfig{ClusterSize: 3, PeerTLS: config.ManualTLS, ClientTLS: config.ManualTLS},
		},
	}

	if minimalE2eEnabled() {
		return minimalTestCases
	}

	tcs := append(minimalTestCases,
		testCase{
			name:   "PeerAutoTLS and ClientAutoTLS",
			config: config.ClusterConfig{ClusterSize: 3, PeerTLS: config.AutoTLS, ClientTLS: config.AutoTLS},
		},
	)

	if fileutil.Exist(e2e.BinPath.EtcdLastRelease) {
		tcs = append(tcs,
			testCase{
				name: "MinorityLastVersion",
				config: config.ClusterConfig{
					ClusterSize: 3,
					ClusterContext: &e2e.ClusterContext{
						Version: e2e.MinorityLastVersion,
					},
				},
			},
			testCase{
				name: "QuorumLastVersion",
				config: config.ClusterConfig{
					ClusterSize: 3,
					ClusterContext: &e2e.ClusterContext{
						Version: e2e.QuorumLastVersion,
					},
				},
			})
	}

	return tcs
}

func WithAuth(userName, password string) config.ClientOption {
	return e2e.WithAuth(userName, password)
}

func WithAuthToken(token string) config.ClientOption {
	return e2e.WithAuthToken(token)
}

func WithEndpoints(endpoints []string) config.ClientOption {
	return e2e.WithEndpoints(endpoints)
}

func WithDialTimeout(tio time.Duration) config.ClientOption {
	return e2e.WithDialTimeout(tio)
}

func WithHTTP2Debug() config.ClusterOption {
	return e2e.WithHTTP2Debug()
}

func WithUnixClient() config.ClusterOption {
	return e2e.WithUnixClient()
}

func WithTCPClient() config.ClusterOption {
	return e2e.WithTCPClient()
}

func WithBasePort(port int) config.ClusterOption {
	return e2e.WithClusterOptionBasePort(port)
}

func configureMirrorDestTLS(mm *config.MakeMirrorOptions, tls config.TLSConfig) {
	switch tls {
	case config.ManualTLS:
		mm.DestCert = e2e.CertPath
		mm.DestKey = e2e.PrivateKeyPath
		mm.DestCACert = e2e.CaPath
		mm.DestInsecureTransport = false
	default:
		mm.DestInsecureTransport = true
	}
}
