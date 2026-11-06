// Copyright 2018 The etcd Authors
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

//go:build !cluster_proxy

package e2e

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"go.etcd.io/etcd/pkg/v3/expect"
	"go.etcd.io/etcd/tests/v3/framework/e2e"
)

func TestCurlV3CipherSuitesValid(t *testing.T)    { testCurlV3CipherSuites(t, true) }
func TestCurlV3CipherSuitesMismatch(t *testing.T) { testCurlV3CipherSuites(t, false) }
func testCurlV3CipherSuites(t *testing.T, valid bool) {
	cc := e2e.NewConfigClientTLS()
	cc.ClusterSize = 1
	cc.ServerConfig.CipherSuites = []string{
		"TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256",
		"TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384",
		"TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256",
		"TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384",
		"TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305",
		"TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305",
	}
	testFunc := cipherSuiteTestValid
	if !valid {
		testFunc = cipherSuiteTestMismatch
	}
	testCtl(t, testFunc, withCfg(*cc))
}

func cipherSuiteTestValid(cx ctlCtx) {
	if err := e2e.CURLGet(cx.epc, e2e.CURLReq{
		Endpoint: "/metrics",
		Expected: expect.ExpectedResponse{Value: fmt.Sprintf(`etcd_server_version{server_version="%s"} 1`, e2e.ServerVersion())},
		Ciphers:  "ECDHE-RSA-AES128-GCM-SHA256", // TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256
	}); err != nil {
		require.ErrorContains(cx.t, err, fmt.Sprintf(`etcd_server_version{server_version="%s"} 1`, e2e.ServerVersion()))
	}
}

func cipherSuiteTestMismatch(cx ctlCtx) {
	err := e2e.CURLGet(cx.epc, e2e.CURLReq{
		Endpoint: "/metrics",
		Expected: expect.ExpectedResponse{Value: "failed setting cipher list"},
		Ciphers:  "ECDHE-RSA-DES-CBC3-SHA", // TLS_ECDHE_RSA_WITH_3DES_EDE_CBC_SHA
	})
	require.ErrorContains(cx.t, err, "curl: (59) failed setting cipher list")
}
