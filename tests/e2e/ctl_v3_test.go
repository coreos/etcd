// Copyright 2016 The etcd Authors
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

package e2e

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.etcd.io/etcd/client/pkg/v3/testutil"
	"go.etcd.io/etcd/pkg/v3/expect"
	"go.etcd.io/etcd/pkg/v3/featuregate"
	"go.etcd.io/etcd/pkg/v3/flags"
	"go.etcd.io/etcd/tests/v3/framework/e2e"
)

func TestCtlV3Version(t *testing.T) { testCtl(t, versionTest) }

func TestClusterVersion(t *testing.T) {
	e2e.BeforeTest(t)

	tests := []struct {
		name         string
		rollingStart bool
	}{
		{
			name:         "When start servers at the same time",
			rollingStart: false,
		},
		{
			name:         "When start servers one by one",
			rollingStart: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e2e.BeforeTest(t)
			cfg := e2e.NewConfig(
				e2e.WithSnapshotCount(3),
				e2e.WithBasePeerScheme("unix"), // to avoid port conflict)
				e2e.WithRollingStart(tt.rollingStart),
			)

			epc, err := e2e.NewEtcdProcessCluster(t.Context(), t, e2e.WithConfig(cfg))
			if err != nil {
				t.Fatalf("could not start etcd process cluster (%v)", err)
			}
			defer func() {
				if errC := epc.Close(); errC != nil {
					t.Fatalf("error closing etcd processes (%v)", errC)
				}
			}()

			ctx := ctlCtx{
				t:   t,
				cfg: *cfg,
				epc: epc,
			}
			cv := e2e.ServerClusterVersion()
			clusterVersionTest(ctx, `"etcdcluster":"`+cv)
		})
	}
}

func versionTest(cx ctlCtx) {
	if err := ctlV3Version(cx); err != nil {
		cx.t.Fatalf("versionTest ctlV3Version error (%v)", err)
	}
}

func clusterVersionTest(cx ctlCtx, expected string) {
	var err error
	for i := 0; i < 35; i++ {
		if err = e2e.CURLGet(cx.epc, e2e.CURLReq{Endpoint: "/version", Expected: expect.ExpectedResponse{Value: expected}}); err != nil {
			cx.t.Logf("#%d: v3 is not ready yet (%v)", i, err)
			time.Sleep(200 * time.Millisecond)
			continue
		}
		break
	}
	if err != nil {
		cx.t.Fatalf("failed cluster version test expected %v got (%v)", expected, err)
	}
}

func ctlV3Version(cx ctlCtx) error {
	cmdArgs := append(cx.PrefixArgs(), "version")
	return e2e.SpawnWithExpectWithEnv(cmdArgs, cx.envMap, expect.ExpectedResponse{Value: e2e.ServerVersion()})
}

// TestCtlV3DialWithHTTPScheme ensures that client handles Endpoints with HTTPS scheme.
func TestCtlV3DialWithHTTPScheme(t *testing.T) {
	testCtl(t, dialWithSchemeTest, withCfg(*e2e.NewConfigClientTLS()))
}

func dialWithSchemeTest(cx ctlCtx) {
	cmdArgs := append(cx.prefixArgs(cx.epc.EndpointsGRPC()), "put", "foo", "bar")
	require.NoError(cx.t, e2e.SpawnWithExpectWithEnv(cmdArgs, cx.envMap, expect.ExpectedResponse{Value: "OK"}))
}

type ctlCtx struct {
	t   *testing.T
	cfg e2e.EtcdProcessClusterConfig

	corruptFunc                func(string) error
	disableStrictReconfigCheck bool

	epc *e2e.EtcdProcessCluster

	envMap map[string]string

	dialTimeout time.Duration
	testTimeout time.Duration

	quorum      bool // if true, set up 3-node cluster and linearizable read
	interactive bool

	user string
	pass string

	initialCorruptCheck bool

	// dir that was used during the test
	dataDir string
}

type ctlOption func(*ctlCtx)

func (cx *ctlCtx) applyOpts(opts []ctlOption) {
	for _, opt := range opts {
		opt(cx)
	}

	cx.initialCorruptCheck = true
}

func withCfg(cfg e2e.EtcdProcessClusterConfig) ctlOption {
	return func(cx *ctlCtx) { cx.cfg = cfg }
}

func withDefaultDialTimeout() ctlOption {
	return withDialTimeout(0)
}

func withDialTimeout(timeout time.Duration) ctlOption {
	return func(cx *ctlCtx) { cx.dialTimeout = timeout }
}

func withTestTimeout(timeout time.Duration) ctlOption {
	return func(cx *ctlCtx) { cx.testTimeout = timeout }
}

func withQuorum() ctlOption {
	return func(cx *ctlCtx) { cx.quorum = true }
}

func withInteractive() ctlOption {
	return func(cx *ctlCtx) { cx.interactive = true }
}

func withInitialCorruptCheck() ctlOption {
	return func(cx *ctlCtx) { cx.initialCorruptCheck = true }
}

func withCorruptFunc(f func(string) error) ctlOption {
	return func(cx *ctlCtx) { cx.corruptFunc = f }
}

func withFlagByEnv() ctlOption {
	return func(cx *ctlCtx) { cx.envMap = make(map[string]string) }
}

// This function must be called after the `withCfg`, otherwise its value
// may be overwritten by `withCfg`.
func withMaxConcurrentStreams(streams uint32) ctlOption {
	return func(cx *ctlCtx) {
		cx.cfg.ServerConfig.MaxConcurrentStreams = streams
	}
}

func withLogLevel(logLevel string) ctlOption {
	return func(cx *ctlCtx) {
		cx.cfg.ServerConfig.LogLevel = logLevel
	}
}

func testCtl(t *testing.T, testFunc func(ctlCtx), opts ...ctlOption) {
	testCtlWithOffline(t, testFunc, nil, opts...)
}

func getDefaultCtlCtx(t *testing.T) ctlCtx {
	return ctlCtx{
		t:           t,
		cfg:         *e2e.NewConfigAutoTLS(),
		dialTimeout: 7 * time.Second,
	}
}

func testCtlWithOffline(t *testing.T, testFunc func(ctlCtx), testOfflineFunc func(ctlCtx), opts ...ctlOption) {
	e2e.BeforeTest(t)

	ret := getDefaultCtlCtx(t)
	ret.applyOpts(opts)

	if !ret.quorum {
		ret.cfg = *e2e.ConfigStandalone(ret.cfg)
	}
	ret.cfg.ServerConfig.StrictReconfigCheck = !ret.disableStrictReconfigCheck
	if ret.initialCorruptCheck {
		ret.cfg.ServerConfig.ServerFeatureGate.(featuregate.MutableFeatureGate).Set(fmt.Sprintf("InitialCorruptCheck=%t", ret.initialCorruptCheck))
	}
	if testOfflineFunc != nil {
		ret.cfg.KeepDataDir = true
	}

	epc, err := e2e.NewEtcdProcessCluster(t.Context(), t, e2e.WithConfig(&ret.cfg))
	if err != nil {
		t.Fatalf("could not start etcd process cluster (%v)", err)
	}
	ret.epc = epc
	ret.dataDir = epc.Procs[0].Config().DataDirPath

	runCtlTest(t, testFunc, testOfflineFunc, ret)
}

func runCtlTest(t *testing.T, testFunc func(ctlCtx), testOfflineFunc func(ctlCtx), cx ctlCtx) {
	defer func() {
		if cx.envMap != nil {
			for k := range cx.envMap {
				os.Unsetenv(k)
			}
			cx.envMap = make(map[string]string)
		}
		if cx.epc != nil {
			cx.epc.Stop()
			cx.epc.Close()
		}
	}()

	donec := make(chan struct{})
	go func() {
		defer close(donec)
		testFunc(cx)
		t.Log("---testFunc logic DONE")
	}()

	timeout := cx.getTestTimeout()

	select {
	case <-time.After(timeout):
		testutil.FatalStack(t, fmt.Sprintf("test timed out after %v", timeout))
	case <-donec:
	}

	t.Log("closing test cluster...")
	assert.NoError(t, cx.epc.Stop())
	assert.NoError(t, cx.epc.Close())
	cx.epc = nil
	t.Log("closed test cluster...")

	if testOfflineFunc != nil {
		testOfflineFunc(cx)
	}
}

func (cx *ctlCtx) getTestTimeout() time.Duration {
	timeout := cx.testTimeout
	if timeout == 0 {
		timeout = 2*cx.dialTimeout + time.Second
		if cx.dialTimeout == 0 {
			timeout = 30 * time.Second
		}
	}
	return timeout
}

func (cx *ctlCtx) prefixArgs(eps []string) []string {
	fmap := make(map[string]string)
	fmap["endpoints"] = strings.Join(eps, ",")
	fmap["dial-timeout"] = cx.dialTimeout.String()
	if cx.epc.Cfg.Client.ConnectionType == e2e.ClientTLS {
		if cx.epc.Cfg.Client.AutoTLS {
			fmap["insecure-transport"] = "false"
			fmap["insecure-skip-tls-verify"] = "true"
		} else if cx.epc.Cfg.Client.RevokeCerts {
			fmap["cacert"] = e2e.CaPath
			fmap["cert"] = e2e.RevokedCertPath
			fmap["key"] = e2e.RevokedPrivateKeyPath
		} else {
			fmap["cacert"] = e2e.CaPath
			fmap["cert"] = e2e.CertPath
			fmap["key"] = e2e.PrivateKeyPath
		}
	}
	if cx.user != "" {
		fmap["user"] = cx.user + ":" + cx.pass
	}

	useEnv := cx.envMap != nil

	cmdArgs := []string{e2e.BinPath.Etcdctl}
	for k, v := range fmap {
		if useEnv {
			ek := flags.FlagToEnv("ETCDCTL", k)
			cx.envMap[ek] = v
		} else {
			cmdArgs = append(cmdArgs, fmt.Sprintf("--%s=%s", k, v))
		}
	}
	return cmdArgs
}

// PrefixArgs prefixes etcdctl command.
// Make sure to unset environment variables after tests.
func (cx *ctlCtx) PrefixArgs() []string {
	return cx.prefixArgs(cx.epc.EndpointsGRPC())
}

// PrefixArgsUtl returns prefix of the command that is etcdutl
// Please not thet 'utl' compatible commands does not consume --endpoints flag.
func (cx *ctlCtx) PrefixArgsUtl() []string {
	return []string{e2e.BinPath.Etcdutl}
}

func isGRPCTimedout(err error) bool {
	return strings.Contains(err.Error(), "grpc: timed out trying to connect")
}
