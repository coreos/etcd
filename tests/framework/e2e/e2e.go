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

package e2e

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.etcd.io/etcd/client/pkg/v3/testutil"
	"go.etcd.io/etcd/pkg/v3/expect"
	"go.etcd.io/etcd/tests/v3/framework/config"
	intf "go.etcd.io/etcd/tests/v3/framework/interfaces"
)

type e2eRunner struct{}

func NewE2eRunner() intf.TestRunner {
	return &e2eRunner{}
}

func (e e2eRunner) TestMain(m *testing.M) {
	InitFlags()
	v := m.Run()
	if v == 0 && testutil.CheckLeakedGoroutine() {
		os.Exit(1)
	}
	os.Exit(v)
}

func (e e2eRunner) BeforeTest(tb testing.TB) {
	BeforeTest(tb)
}

func (e e2eRunner) NewCluster(ctx context.Context, tb testing.TB, opts ...config.ClusterOption) intf.Cluster {
	cfg := config.NewClusterConfig(opts...)
	e2eConfig := NewConfig(
		WithClusterSize(cfg.ClusterSize),
		WithQuotaBackendBytes(cfg.QuotaBackendBytes),
		WithStrictReconfigCheck(cfg.StrictReconfigCheck),
		WithAuthTokenOpts(cfg.AuthToken),
		WithSnapshotCount(cfg.SnapshotCount),
	)

	if ctx, ok := cfg.ClusterContext.(*ClusterContext); ok && ctx != nil {
		e2eConfig.Version = ctx.Version
		e2eConfig.EnvVars = ctx.EnvVars
		if ctx.UseUnix {
			e2eConfig.BaseClientScheme = "unix"
		}
		if ctx.BasePort != 0 {
			e2eConfig.BasePort = ctx.BasePort
		}
	}

	switch cfg.ClientTLS {
	case config.NoTLS:
		e2eConfig.Client.ConnectionType = ClientNonTLS
	case config.AutoTLS:
		e2eConfig.Client.AutoTLS = true
		e2eConfig.Client.ConnectionType = ClientTLS
	case config.ManualTLS:
		e2eConfig.Client.AutoTLS = false
		e2eConfig.Client.ConnectionType = ClientTLS
	default:
		tb.Fatalf("ClientTLS config %q not supported", cfg.ClientTLS)
	}
	switch cfg.PeerTLS {
	case config.NoTLS:
		e2eConfig.IsPeerTLS = false
		e2eConfig.IsPeerAutoTLS = false
	case config.AutoTLS:
		e2eConfig.IsPeerTLS = true
		e2eConfig.IsPeerAutoTLS = true
	case config.ManualTLS:
		e2eConfig.IsPeerTLS = true
		e2eConfig.IsPeerAutoTLS = false
	default:
		tb.Fatalf("PeerTLS config %q not supported", cfg.PeerTLS)
	}
	epc, err := NewEtcdProcessCluster(ctx, tb, WithConfig(e2eConfig))
	if err != nil {
		tb.Fatalf("could not start etcd integrationCluster: %s", err)
	}
	return &e2eCluster{tb, *epc}
}

type e2eCluster struct {
	t testing.TB
	EtcdProcessCluster
}

func (c *e2eCluster) Client(opts ...config.ClientOption) (intf.Client, error) {
	etcdctl, err := NewEtcdctl(c.Cfg.Client, c.EndpointsGRPC(), opts...)
	return e2eClient{etcdctl}, err
}

func (c *e2eCluster) Endpoints() []string {
	return c.EndpointsGRPC()
}

func (c *e2eCluster) Members() (ms []intf.Member) {
	for _, proc := range c.EtcdProcessCluster.Procs {
		ms = append(ms, e2eMember{EtcdProcess: proc, Cfg: c.Cfg})
	}
	return ms
}

func (c *e2eCluster) TemplateEndpoints(tb testing.TB, pattern string) []string {
	tb.Helper()
	var endpoints []string
	for i := 0; i < c.Cfg.ClusterSize; i++ {
		ent := pattern
		ent = strings.ReplaceAll(ent, "${MEMBER_PORT}", fmt.Sprintf("%d", EtcdProcessBasePort+i*5))
		endpoints = append(endpoints, ent)
	}
	return endpoints
}

func (c *e2eCluster) AssertAuthority(tb testing.TB, expectAuthorityPattern string) {
	for i := range c.Procs {
		line, _ := c.Procs[i].Logs().ExpectWithContext(tb.Context(), expect.ExpectedResponse{
			Value: `http2: decoded hpack field header field ":authority"`,
		})
		line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")

		u, err := url.Parse(c.Procs[i].EndpointsGRPC()[0])
		require.NoError(tb, err)
		expectAuthority := strings.ReplaceAll(expectAuthorityPattern, "${MEMBER_PORT}", u.Port())
		expectLine := fmt.Sprintf(`http2: decoded hpack field header field ":authority" = %q`, expectAuthority)
		assert.Truef(tb, strings.HasSuffix(line, expectLine), "Got %q expected suffix %q", line, expectLine)
	}
}

type e2eClient struct {
	*EtcdctlV3
}

type e2eMember struct {
	EtcdProcess
	Cfg *EtcdProcessClusterConfig
}

func (m e2eMember) Client() intf.Client {
	etcdctl, err := NewEtcdctl(m.Cfg.Client, m.EndpointsGRPC())
	if err != nil {
		panic(err)
	}
	return e2eClient{etcdctl}
}

func (m e2eMember) Start(ctx context.Context) error {
	return m.EtcdProcess.Start(ctx)
}

func (m e2eMember) Stop() {
	m.EtcdProcess.Stop()
}
