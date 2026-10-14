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

//go:build !cluster_proxy

package e2e

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"go.etcd.io/etcd/api/v3/version"
	"go.etcd.io/etcd/pkg/v3/expect"
	"go.etcd.io/etcd/tests/v3/framework/e2e"
)

func TestCurlV3ListenCurvePreferencesValid(t *testing.T) {
	testCurlV3ListenCurvePreferences(t, true)
}

func TestCurlV3ListenCurvePreferencesMismatch(t *testing.T) {
	testCurlV3ListenCurvePreferences(t, false)
}

func testCurlV3ListenCurvePreferences(t *testing.T, valid bool) {
	cc := e2e.NewConfigClientTLS()
	cc.ClusterSize = 1
	cc.ServerConfig.ListenCurvePreferences = []string{"CurveP256"}
	testFunc := listenCurvePreferencesTestValid
	if !valid {
		testFunc = listenCurvePreferencesTestMismatch
	}
	testCtl(t, testFunc, withCfg(*cc))
}

func listenCurvePreferencesTestValid(cx ctlCtx) {
	require.NoError(cx.t, e2e.CURLGet(cx.epc, e2e.CURLReq{
		Endpoint: "/metrics",
		Expected: expect.ExpectedResponse{Value: fmt.Sprintf(`etcd_server_version{server_version="%s"} 1`, version.Version)},
		Curves:   "P-256",
	}))
}

func listenCurvePreferencesTestMismatch(cx ctlCtx) {
	err := e2e.CURLGet(cx.epc, e2e.CURLReq{
		Endpoint: "/metrics",
		Expected: expect.ExpectedResponse{Value: "alert handshake failure"},
		Curves:   "X25519",
	})
	// curl exit code 35: "SSL connect error. The SSL handshaking failed."
	// https://curl.se/docs/manpage.html (Exit codes)
	require.ErrorContains(cx.t, err, "curl: (35)")
}
