// Copyright 2023 The etcd Authors
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
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc"

	clientv3 "go.etcd.io/etcd/client/v3"
	"go.etcd.io/etcd/client/v3/healthcheck"
	"go.etcd.io/etcd/tests/v3/framework/config"
	"go.etcd.io/etcd/tests/v3/framework/e2e"
)

const (
	// in sync with how kubernetes uses etcd
	// https://github.com/kubernetes/kubernetes/blob/release-1.28/staging/src/k8s.io/apiserver/pkg/storage/storagebackend/factory/etcd3.go#L59-L71
	keepaliveTime    = 30 * time.Second
	keepaliveTimeout = 10 * time.Second
	dialTimeout      = 20 * time.Second

	clientRuntime  = 10 * time.Second
	requestTimeout = 100 * time.Millisecond
)

func TestFailoverOnDefrag(t *testing.T) {
	tcs := []struct {
		name            string
		clusterOptions  []e2e.EPClusterOption
		gRPCDialOptions []grpc.DialOption

		// common assertion
		expectedMinQPS float64
		// happy case assertion
		expectedMaxFailureRate float64
		// negative case assertion
		expectedMinFailureRate float64
	}{
		{
			name: "defrag failover happy case",
			clusterOptions: []e2e.EPClusterOption{
				e2e.WithClusterSize(3),
				e2e.WithServerFeatureGate("StopGRPCServiceOnDefrag", true),
				e2e.WithServerFeatureGate("NonBlockingDefrag", false),
				e2e.WithGoFailEnabled(true),
			},
			gRPCDialOptions:        healthcheck.GRPCClientHealthCheckOptions(),
			expectedMinQPS:         20,
			expectedMaxFailureRate: 0.01,
		},
		{
			name: "defrag blocks one-third of requests with stopGRPCServiceOnDefrag set to false",
			clusterOptions: []e2e.EPClusterOption{
				e2e.WithClusterSize(3),
				e2e.WithServerFeatureGate("StopGRPCServiceOnDefrag", false),
				e2e.WithServerFeatureGate("NonBlockingDefrag", false),
				e2e.WithGoFailEnabled(true),
			},
			gRPCDialOptions:        healthcheck.GRPCClientHealthCheckOptions(),
			expectedMinQPS:         20,
			expectedMinFailureRate: 0.25,
		},
		{
			name: "defrag blocks one-third of requests with stopGRPCServiceOnDefrag set to true and client health check disabled",
			clusterOptions: []e2e.EPClusterOption{
				e2e.WithClusterSize(3),
				e2e.WithServerFeatureGate("StopGRPCServiceOnDefrag", true),
				e2e.WithServerFeatureGate("NonBlockingDefrag", false),
				e2e.WithGoFailEnabled(true),
			},
			expectedMinQPS:         20,
			expectedMinFailureRate: 0.25,
		},
		{
			name: "defrag failover happy case with feature gate",
			clusterOptions: []e2e.EPClusterOption{
				e2e.WithClusterSize(3),
				e2e.WithServerFeatureGate("StopGRPCServiceOnDefrag", true),
				e2e.WithServerFeatureGate("NonBlockingDefrag", false),
				e2e.WithGoFailEnabled(true),
			},
			gRPCDialOptions:        healthcheck.GRPCClientHealthCheckOptions(),
			expectedMinQPS:         20,
			expectedMaxFailureRate: 0.01,
		},
		{
			name: "defrag blocks one-third of requests with StopGRPCServiceOnDefrag feature gate set to false",
			clusterOptions: []e2e.EPClusterOption{
				e2e.WithClusterSize(3),
				e2e.WithServerFeatureGate("StopGRPCServiceOnDefrag", false),
				e2e.WithServerFeatureGate("NonBlockingDefrag", false),
				e2e.WithGoFailEnabled(true),
			},
			gRPCDialOptions:        healthcheck.GRPCClientHealthCheckOptions(),
			expectedMinQPS:         20,
			expectedMinFailureRate: 0.25,
		},
		{
			name: "defrag blocks one-third of requests with StopGRPCServiceOnDefrag feature gate set to true and client health check disabled",
			clusterOptions: []e2e.EPClusterOption{
				e2e.WithClusterSize(3),
				e2e.WithServerFeatureGate("StopGRPCServiceOnDefrag", true),
				e2e.WithServerFeatureGate("NonBlockingDefrag", false),
				e2e.WithGoFailEnabled(true),
			},
			expectedMinQPS:         20,
			expectedMinFailureRate: 0.25,
		},
	}

	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			e2e.BeforeTest(t)
			clus, cerr := e2e.NewEtcdProcessCluster(t.Context(), t, tc.clusterOptions...)
			require.NoError(t, cerr)
			t.Cleanup(func() { clus.Stop() })

			endpoints := clus.EndpointsGRPC()

			requestVolume, successfulRequestCount := 0, 0
			start := time.Now()
			g := new(errgroup.Group)
			g.Go(func() (lastErr error) {
				clusterClient, cerr := clientv3.New(clientv3.Config{
					DialTimeout:          dialTimeout,
					DialKeepAliveTime:    keepaliveTime,
					DialKeepAliveTimeout: keepaliveTimeout,
					Endpoints:            endpoints,
					DialOptions:          tc.gRPCDialOptions,
				})
				if cerr != nil {
					return cerr
				}
				defer clusterClient.Close()

				timeout := time.After(clientRuntime)
				for {
					select {
					case <-timeout:
						return lastErr
					default:
					}
					getContext, cancel := context.WithTimeout(t.Context(), requestTimeout)
					_, err := clusterClient.Get(getContext, "health")
					cancel()
					requestVolume++
					if err != nil {
						lastErr = err
						continue
					}
					successfulRequestCount++
				}
			})
			triggerDefrag(t, clus.Procs[0])

			err := g.Wait()
			if err != nil {
				t.Logf("etcd client failed to fail over, error (%v)", err)
			}

			qps := float64(requestVolume) / float64(time.Since(start)) * float64(time.Second)
			failureRate := 1 - float64(successfulRequestCount)/float64(requestVolume)
			t.Logf("request failure rate is %.2f%%, qps is %.2f requests/second", failureRate*100, qps)

			require.GreaterOrEqual(t, qps, tc.expectedMinQPS)
			if tc.expectedMaxFailureRate != 0.0 {
				require.LessOrEqual(t, failureRate, tc.expectedMaxFailureRate)
			}
			if tc.expectedMinFailureRate != 0.0 {
				require.GreaterOrEqual(t, failureRate, tc.expectedMinFailureRate)
			}
		})
	}
}

func triggerDefrag(t *testing.T, member e2e.EtcdProcess) {
	require.NoError(t, member.Failpoints().SetupHTTP(t.Context(), "defragBeforeCopy", `sleep("10s")`))
	require.NoError(t, member.Etcdctl().Defragment(t.Context(), config.DefragOption{Timeout: time.Minute}))
}
