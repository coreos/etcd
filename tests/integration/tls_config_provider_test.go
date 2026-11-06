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

package integration

import (
	"crypto/tls"
	"testing"

	"github.com/stretchr/testify/require"

	"go.etcd.io/etcd/client/pkg/v3/transport"
	"go.etcd.io/etcd/tests/v3/framework/integration"
)

type clusterTLSConfigProvider struct {
	server, client *tls.Config
}

func (p clusterTLSConfigProvider) ServerConfig() (*tls.Config, error) { return p.server, nil }
func (p clusterTLSConfigProvider) ClientConfig() (*tls.Config, error) { return p.client, nil }

func TestTLSConfigProviderClusterOf3(t *testing.T) {
	integration.BeforeTest(t)
	server, err := integration.TestTLSInfo.ServerConfig()
	require.NoError(t, err)
	client, err := integration.TestTLSInfo.ClientConfig()
	require.NoError(t, err)
	// Only the provider is passed through the cluster's peer TLS settings.
	// Reuse fixture callbacks to avoid introducing another certificate fixture.
	info := transport.TLSInfo{ConfigProvider: clusterTLSConfigProvider{server: server, client: client}}
	c := integration.NewCluster(t, &integration.ClusterConfig{Size: 3, PeerTLS: &info})
	defer c.Terminate(t)
	clusterMustProgress(t, c.Members)
}
