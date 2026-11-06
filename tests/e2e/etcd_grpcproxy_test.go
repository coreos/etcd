// Copyright 2017 The etcd Authors
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
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.etcd.io/etcd/api/v3/v3rpc/rpctypes"
	clientv3 "go.etcd.io/etcd/client/v3"
	"go.etcd.io/etcd/pkg/v3/expect"
	v3electionpb "go.etcd.io/etcd/server/v3/etcdserver/api/v3election/v3electionpb"
	"go.etcd.io/etcd/tests/v3/framework/config"
	"go.etcd.io/etcd/tests/v3/framework/e2e"
	"go.etcd.io/etcd/tests/v3/framework/testutils"
)

func TestGrpcProxyAutoSync(t *testing.T) {
	e2e.SkipInShortMode(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	epc, err := e2e.NewEtcdProcessCluster(ctx, t, e2e.WithClusterSize(1))
	require.NoError(t, err)
	defer func() {
		assert.NoError(t, epc.Close())
	}()

	var (
		node1ClientURL = epc.Procs[0].Config().ClientURL
		proxyClientURL = "127.0.0.1:32379"
	)

	// Run independent grpc-proxy instance
	proxyProc, err := e2e.SpawnCmd([]string{
		e2e.BinPath.Etcd, "grpc-proxy", "start",
		"--advertise-client-url", proxyClientURL, "--listen-addr", proxyClientURL,
		"--endpoints", node1ClientURL,
		"--endpoints-auto-sync-interval", "1s",
	}, nil)
	require.NoError(t, err)
	defer func() {
		assert.NoError(t, proxyProc.Stop())
	}()

	proxyCtl, err := e2e.NewEtcdctl(e2e.ClientConfig{}, []string{proxyClientURL})
	require.NoError(t, err)
	_, err = proxyCtl.Put(ctx, "k1", "v1", config.PutOptions{})
	require.NoError(t, err)

	// Add and start second member
	_, err = epc.StartNewProc(ctx, nil, t, false /* addAsLearner */)
	require.NoError(t, err)

	// Wait for auto sync of endpoints
	err = waitForEndpointInLog(ctx, proxyProc, epc.Procs[1].Config().ClientURL)
	require.NoError(t, err)

	err = epc.CloseProc(ctx, func(proc e2e.EtcdProcess) bool {
		return proc.Config().ClientURL == node1ClientURL
	})
	require.NoError(t, err)

	var resp *clientv3.GetResponse
	for i := 0; i < 10; i++ {
		resp, err = proxyCtl.Get(ctx, "k1", config.GetOptions{})
		if err != nil && strings.Contains(err.Error(), rpctypes.ErrGRPCLeaderChanged.Error()) {
			time.Sleep(500 * time.Millisecond)
			continue
		}
	}
	require.NoError(t, err)

	kvs := testutils.KeyValuesFromGetResponse(resp)
	assert.Equal(t, []testutils.KV{{Key: "k1", Val: "v1"}}, kvs)
}

func TestGrpcProxyTLSVersions(t *testing.T) {
	e2e.SkipInShortMode(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	epc, err := e2e.NewEtcdProcessCluster(ctx, t, e2e.WithClusterSize(1))
	require.NoError(t, err)
	defer func() {
		assert.NoError(t, epc.Close())
	}()

	var (
		node1ClientURL = epc.Procs[0].Config().ClientURL
		proxyClientURL = "127.0.0.1:42379"
	)

	// Run independent grpc-proxy instance
	proxyProc, err := e2e.SpawnCmd([]string{
		e2e.BinPath.Etcd, "grpc-proxy", "start",
		"--advertise-client-url", proxyClientURL,
		"--listen-addr", proxyClientURL,
		"--endpoints", node1ClientURL,
		"--endpoints-auto-sync-interval", "1s",
		"--cert-file", e2e.CertPath2,
		"--key-file", e2e.PrivateKeyPath2,
		"--trusted-ca-file", e2e.CaPath,
		"--tls-min-version", "TLS1.2",
		"--tls-max-version", "TLS1.3",
	}, nil)
	require.NoError(t, err)
	defer func() {
		assert.NoError(t, proxyProc.Stop())
	}()

	_, err = proxyProc.ExpectFunc(ctx, func(s string) bool {
		return strings.Contains(s, "started gRPC proxy")
	})
	require.NoError(t, err)

	ctx, cancel = context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	healthErr := e2e.SpawnWithExpectsContext(ctx, []string{
		"curl",
		"--http1.1",
		"--fail",
		"--verbose",
		"--cacert", e2e.CaPath,
		"--cert", e2e.CertPath2,
		"--key", e2e.PrivateKeyPath2,
		"https://" + proxyClientURL + "/proxy/health",
	}, nil, expect.ExpectedResponse{Value: `"health":"true"`})
	require.NoError(t, healthErr)
}

func waitForEndpointInLog(ctx context.Context, proxyProc *expect.ExpectProcess, endpoint string) error {
	endpoint = strings.Replace(endpoint, "http://", "", 1)

	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	_, err := proxyProc.ExpectFunc(ctx, func(s string) bool {
		return strings.Contains(s, endpoint)
	})

	return err
}

func TestGRPCProxyWatchersAfterTokenExpiry(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	cluster, err := e2e.NewEtcdProcessCluster(ctx, t,
		e2e.WithClusterSize(1),
		e2e.WithAuthTokenOpts("simple"),
		e2e.WithAuthTokenTTL(1),
	)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, cluster.Stop()) })

	cli := cluster.Etcdctl()

	createUsers(ctx, t, cli)

	require.NoError(t, cli.AuthEnable(ctx))

	var (
		node1ClientURL = cluster.Procs[0].Config().ClientURL
		proxyClientURL = "127.0.0.1:42379"
	)

	proxyProc, err := e2e.SpawnCmd([]string{
		e2e.BinPath.Etcd, "grpc-proxy", "start",
		"--advertise-client-url", proxyClientURL,
		"--listen-addr", proxyClientURL,
		"--endpoints", node1ClientURL,
	}, nil)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, proxyProc.Stop()) })

	var totalEventsCount int64

	handler := func(events clientv3.WatchChan) {
		for {
			select {
			case ev, open := <-events:
				if !open {
					return
				}
				if ev.Err() != nil {
					t.Logf("watch response error: %s", ev.Err())
					continue
				}
				atomic.AddInt64(&totalEventsCount, 1)
			case <-ctx.Done():
				return
			}
		}
	}

	withAuth := e2e.WithAuth("root", "rootPassword")
	withEndpoint := e2e.WithEndpoints([]string{proxyClientURL})

	events := cluster.Etcdctl(withAuth, withEndpoint).Watch(ctx, "/test", config.WatchOptions{Prefix: true, Revision: 1})

	wg := sync.WaitGroup{}

	wg.Add(1)
	go func() {
		defer wg.Done()
		handler(events)
	}()

	clusterCli := cluster.Etcdctl(withAuth)
	_, err = clusterCli.Put(ctx, "/test/1", "test", config.PutOptions{})
	require.NoError(t, err)
	require.NoError(t, err)

	time.Sleep(time.Second * 2)

	events2 := cluster.Etcdctl(withAuth, withEndpoint).Watch(ctx, "/test", config.WatchOptions{Prefix: true, Revision: 1})

	wg.Add(1)
	go func() {
		defer wg.Done()
		handler(events2)
	}()

	events3 := cluster.Etcdctl(withAuth, withEndpoint).Watch(ctx, "/test", config.WatchOptions{Prefix: true, Revision: 1})

	wg.Add(1)
	go func() {
		defer wg.Done()
		handler(events3)
	}()

	time.Sleep(time.Second)

	cancel()
	wg.Wait()

	assert.Equal(t, int64(3), atomic.LoadInt64(&totalEventsCount))
}

func TestGRPCProxyStreamAuthTokenForwarding(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	cluster, err := e2e.NewEtcdProcessCluster(ctx, t,
		e2e.WithClusterSize(1),
		e2e.WithAuthTokenOpts("simple"),
	)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, cluster.Stop()) })

	cli := cluster.Etcdctl()
	createUsers(ctx, t, cli)
	require.NoError(t, cli.AuthEnable(ctx))

	var (
		proxyClientURL = "127.0.0.1:52379"
	)

	proxyProc, err := e2e.SpawnCmd([]string{
		e2e.BinPath.Etcd, "grpc-proxy", "start",
		"--advertise-client-url", proxyClientURL,
		"--listen-addr", proxyClientURL,
		"--endpoints", cluster.Procs[0].Config().ClientURL,
	}, nil)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, proxyProc.Stop()) })

	_, err = proxyProc.ExpectFunc(ctx, func(s string) bool {
		return strings.Contains(s, "started gRPC proxy")
	})
	require.NoError(t, err)

	proxyClient, err := clientv3.New(clientv3.Config{
		Endpoints:   []string{proxyClientURL},
		DialTimeout: 5 * time.Second,
		Username:    "root",
		Password:    "rootPassword",
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, proxyClient.Close()) })

	electionClient := v3electionpb.NewElectionClient(proxyClient.ActiveConnection())

	cctx, ccancel := context.WithTimeout(ctx, 10*time.Second)
	defer ccancel()

	const electionName = "test-election"
	const proposal = "test-proposal"

	_, err = electionClient.Campaign(cctx, &v3electionpb.CampaignRequest{
		Name:  []byte(electionName),
		Value: []byte(proposal),
	})
	require.NoError(t, err)

	obs, err := electionClient.Observe(cctx, &v3electionpb.LeaderRequest{Name: []byte(electionName)})
	require.NoError(t, err)

	leader, err := obs.Recv()
	require.NoError(t, err)
	require.NotNil(t, leader)
	require.NotNil(t, leader.Kv)
	assert.Equal(t, proposal, string(leader.Kv.Value))
}
