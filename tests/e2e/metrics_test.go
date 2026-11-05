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
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"testing"
	"time"

	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
	"github.com/stretchr/testify/require"

	"go.etcd.io/etcd/api/v3/version"
	"go.etcd.io/etcd/pkg/v3/expect"
	"go.etcd.io/etcd/tests/v3/framework/config"
	"go.etcd.io/etcd/tests/v3/framework/e2e"
)

func TestV3MetricsSecure(t *testing.T) {
	cfg := e2e.NewConfigTLS()
	cfg.ClusterSize = 1
	cfg.MetricsURLScheme = "https"
	testCtl(t, metricsTest)
}

func TestV3MetricsInsecure(t *testing.T) {
	cfg := e2e.NewConfigTLS()
	cfg.ClusterSize = 1
	cfg.MetricsURLScheme = "http"
	testCtl(t, metricsTest)
}

func TestV3LearnerMetricRecover(t *testing.T) {
	cfg := e2e.NewConfigTLS()
	cfg.ServerConfig.SnapshotCount = 10
	testCtl(t, learnerMetricRecoverTest, withCfg(*cfg))
}

func TestV3LearnerMetricApplyFromSnapshotTest(t *testing.T) {
	cfg := e2e.NewConfigTLS()
	cfg.ServerConfig.SnapshotCount = 10
	testCtl(t, learnerMetricApplyFromSnapshotTest, withCfg(*cfg))
}

func metricsTest(cx ctlCtx) {
	require.NoError(cx.t, ctlV3Put(cx, "k", "v", ""))

	i := 0
	for _, test := range []struct {
		endpoint, expected string
	}{
		{"/metrics", "etcd_mvcc_put_total 2"},
		{"/metrics", "etcd_debugging_mvcc_keys_total 1"},
		{"/metrics", "etcd_mvcc_delete_total 3"},
		{"/metrics", fmt.Sprintf(`etcd_server_version{server_version="%s"} 1`, version.Version)},
		{"/metrics", fmt.Sprintf(`etcd_cluster_version{cluster_version="%s"} 1`, version.Cluster(version.Version))},
		{"/metrics", `grpc_server_handled_total{grpc_code="Canceled",grpc_method="Watch",grpc_service="etcdserverpb.Watch",grpc_type="bidi_stream"} 6`},
		{"/health", `{"health":"true","reason":""}`},
	} {
		i++
		require.NoError(cx.t, ctlV3Put(cx, fmt.Sprintf("%d", i), "v", ""))
		require.NoError(cx.t, ctlV3Del(cx, []string{fmt.Sprintf("%d", i)}, 1))
		require.NoError(cx.t, ctlV3Watch(cx, []string{"k", "--rev", "1"}, []kvExec{{key: "k", val: "v"}}...))
		require.NoErrorf(cx.t, e2e.CURLGet(cx.epc, e2e.CURLReq{Endpoint: test.endpoint, Expected: expect.ExpectedResponse{Value: test.expected}}), "failed get with curl")
	}
}

func learnerMetricRecoverTest(cx ctlCtx) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	_, err := cx.epc.StartNewProc(ctx, nil, cx.t, true /* addAsLearner */)
	require.NoError(cx.t, err)
	expectLearnerMetrics(cx)

	triggerSnapshot(ctx, cx)

	// Restart cluster
	require.NoError(cx.t, cx.epc.Restart(ctx))
	expectLearnerMetrics(cx)
}

func learnerMetricApplyFromSnapshotTest(cx ctlCtx) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Add learner but do not start it
	_, learnerCfg, err := cx.epc.AddMember(ctx, nil, cx.t, true /* addAsLearner */)
	require.NoError(cx.t, err)

	triggerSnapshot(ctx, cx)

	// Start the learner
	require.NoError(cx.t, cx.epc.StartNewProcFromConfig(ctx, cx.t, learnerCfg))
	expectLearnerMetrics(cx)
}

func triggerSnapshot(ctx context.Context, cx ctlCtx) {
	etcdctl := cx.epc.Procs[0].Etcdctl()
	for i := 0; i < int(cx.epc.Cfg.ServerConfig.SnapshotCount); i++ {
		require.NoError(cx.t, etcdctl.Put(ctx, "k", "v", config.PutOptions{}))
	}
}

func expectLearnerMetrics(cx ctlCtx) {
	expectLearnerMetric(cx, 0, "etcd_server_is_learner 0")
	expectLearnerMetric(cx, 1, "etcd_server_is_learner 1")
}

func expectLearnerMetric(cx ctlCtx, procIdx int, expectMetric string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	args := e2e.CURLPrefixArgsCluster(cx.epc.Cfg, cx.epc.Procs[procIdx], "GET", e2e.CURLReq{Endpoint: "/metrics"})
	require.NoError(cx.t, e2e.SpawnWithExpectsContext(ctx, args, nil, expect.ExpectedResponse{Value: expectMetric}))
}

func TestNoMetricsMissing(t *testing.T) {
	var (
		// Note the list doesn't contain all the metrics, because the
		// labelled metrics won't be exposed by prometheus by default.
		// They are only exposed when at least one value with labels
		// is set.
		basicMetrics = []string{
			"etcd_cluster_version",
			"etcd_debugging_auth_revision",
			"etcd_debugging_disk_backend_commit_rebalance_duration_seconds",
			"etcd_debugging_disk_backend_commit_spill_duration_seconds",
			"etcd_debugging_disk_backend_commit_write_duration_seconds",
			"etcd_debugging_lease_granted_total",
			"etcd_debugging_lease_renewed_total",
			"etcd_debugging_lease_revoked_total",
			"etcd_debugging_lease_ttl_total",
			"etcd_debugging_mvcc_compact_revision",
			"etcd_debugging_mvcc_current_revision",
			"etcd_debugging_mvcc_db_compaction_keys_total",
			"etcd_debugging_mvcc_db_compaction_last",
			"etcd_debugging_mvcc_db_compaction_pause_duration_milliseconds",
			"etcd_debugging_mvcc_db_compaction_total_duration_milliseconds",
			"etcd_debugging_mvcc_events_total",
			"etcd_debugging_mvcc_index_compaction_pause_duration_milliseconds",
			"etcd_debugging_mvcc_keys_total",
			"etcd_debugging_mvcc_pending_events_total",
			"etcd_debugging_mvcc_slow_watcher_total",
			"etcd_debugging_mvcc_total_put_size_in_bytes",
			"etcd_debugging_mvcc_watch_stream_total",
			"etcd_debugging_mvcc_watcher_total",
			"etcd_debugging_server_lease_expired_total",
			"etcd_debugging_server_watch_send_loop_watch_stream_duration_seconds",
			"etcd_debugging_server_watch_send_loop_watch_stream_duration_per_event_seconds",
			"etcd_debugging_server_watch_send_loop_control_stream_duration_seconds",
			"etcd_debugging_server_watch_send_loop_progress_duration_seconds",
			"etcd_debugging_snap_save_marshalling_duration_seconds",
			"etcd_debugging_snap_save_total_duration_seconds",
			"etcd_debugging_store_expires_total",
			"etcd_debugging_store_watch_requests_total",
			"etcd_debugging_store_watchers",
			"etcd_debugging_store_writes_total",
			"etcd_disk_backend_commit_duration_seconds",
			"etcd_disk_backend_defrag_duration_seconds",
			"etcd_disk_backend_snapshot_duration_seconds",
			"etcd_disk_defrag_inflight",
			"etcd_disk_wal_fsync_duration_seconds",
			"etcd_disk_wal_write_bytes_total",
			"etcd_disk_wal_write_duration_seconds",
			"etcd_grpc_proxy_cache_hits_total",
			"etcd_grpc_proxy_cache_keys_total",
			"etcd_grpc_proxy_cache_misses_total",
			"etcd_grpc_proxy_events_coalescing_total",
			"etcd_grpc_proxy_watchers_coalescing_total",
			"etcd_mvcc_db_open_read_transactions",
			"etcd_mvcc_db_total_size_in_bytes",
			"etcd_mvcc_db_total_size_in_use_in_bytes",
			"etcd_mvcc_delete_total",
			"etcd_mvcc_hash_duration_seconds",
			"etcd_mvcc_hash_rev_duration_seconds",
			"etcd_mvcc_put_total",
			"etcd_mvcc_range_total",
			"etcd_mvcc_txn_total",
			"etcd_network_client_grpc_received_bytes_total",
			"etcd_network_client_grpc_sent_bytes_total",
			"etcd_network_known_peers",
			"etcd_server_apply_duration_seconds",
			"etcd_server_client_requests_total",
			"etcd_server_go_version",
			"etcd_server_has_leader",
			"etcd_server_health_failures",
			"etcd_server_health_success",
			"etcd_server_heartbeat_send_failures_total",
			"etcd_server_id",
			"etcd_server_is_leader",
			"etcd_server_is_learner",
			"etcd_server_leader_changes_seen_total",
			"etcd_server_learner_promote_successes",
			"etcd_server_proposals_applied_total",
			"etcd_server_proposals_committed_total",
			"etcd_server_proposals_failed_total",
			"etcd_server_proposals_pending",
			"etcd_server_quota_backend_bytes",
			"etcd_server_range_duration_seconds",
			"etcd_server_read_indexes_failed_total",
			"etcd_server_slow_apply_total",
			"etcd_server_slow_read_indexes_total",
			"etcd_server_snapshot_apply_in_progress_total",
			"etcd_server_version",
			"etcd_snap_db_fsync_duration_seconds",
			"etcd_snap_db_save_total_duration_seconds",
			"etcd_snap_fsync_duration_seconds",
			"go_gc_duration_seconds",
			"go_gc_gogc_percent",
			"go_gc_gomemlimit_bytes",
			"go_goroutines",
			"go_info",
			"go_memstats_alloc_bytes",
			"go_memstats_alloc_bytes_total",
			"go_memstats_buck_hash_sys_bytes",
			"go_memstats_frees_total",
			"go_memstats_gc_sys_bytes",
			"go_memstats_heap_alloc_bytes",
			"go_memstats_heap_idle_bytes",
			"go_memstats_heap_inuse_bytes",
			"go_memstats_heap_objects",
			"go_memstats_heap_released_bytes",
			"go_memstats_heap_sys_bytes",
			"go_memstats_last_gc_time_seconds",
			"go_memstats_mallocs_total",
			"go_memstats_mcache_inuse_bytes",
			"go_memstats_mcache_sys_bytes",
			"go_memstats_mspan_inuse_bytes",
			"go_memstats_mspan_sys_bytes",
			"go_memstats_next_gc_bytes",
			"go_memstats_other_sys_bytes",
			"go_memstats_stack_inuse_bytes",
			"go_memstats_stack_sys_bytes",
			"go_memstats_sys_bytes",
			"go_sched_gomaxprocs_threads",
			"go_threads",
			"grpc_server_handled_total",
			"grpc_server_msg_received_total",
			"grpc_server_msg_sent_total",
			"grpc_server_started_total",
			"os_fd_limit",
			"os_fd_used",
			"promhttp_metric_handler_requests_in_flight",
			"promhttp_metric_handler_requests_total",
		}
		extraMultipleMemberClusterMetrics = []string{
			"etcd_network_active_peers",
			"etcd_network_peer_received_bytes_total",
			"etcd_network_peer_sent_bytes_total",
		}
		extraExtensiveMetrics = []string{"grpc_server_handling_seconds"}
	)

	testCases := []struct {
		name            string
		options         []e2e.EPClusterOption
		expectedMetrics []string
	}{
		{
			name: "basic metrics of 1 member cluster",
			options: []e2e.EPClusterOption{
				e2e.WithClusterSize(1),
			},
			expectedMetrics: basicMetrics,
		},
		{
			name: "basic metrics of 3 member cluster",
			options: []e2e.EPClusterOption{
				e2e.WithClusterSize(3),
			},
			expectedMetrics: append(basicMetrics, extraMultipleMemberClusterMetrics...),
		},
		{
			name: "extensive metrics of 1 member cluster",
			options: []e2e.EPClusterOption{
				e2e.WithClusterSize(1),
				e2e.WithExtensiveMetrics(),
			},
			expectedMetrics: append(basicMetrics, extraExtensiveMetrics...),
		},
		{
			name: "extensive metrics of 3 member cluster",
			options: []e2e.EPClusterOption{
				e2e.WithClusterSize(3),
				e2e.WithExtensiveMetrics(),
			},
			expectedMetrics: append(append(basicMetrics, extraExtensiveMetrics...), extraMultipleMemberClusterMetrics...),
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			e2e.BeforeTest(t)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			epc, err := e2e.NewEtcdProcessCluster(ctx, t, tc.options...)
			require.NoError(t, err)
			defer epc.Close()

			c := epc.Procs[0].Etcdctl()
			for i := 0; i < 3; i++ {
				err = c.Put(ctx, fmt.Sprintf("key_%d", i), fmt.Sprintf("value_%d", i), config.PutOptions{})
				require.NoError(t, err)
			}
			_, err = c.Get(ctx, "k", config.GetOptions{})
			require.NoError(t, err)

			metricsURL, err := url.JoinPath(epc.Procs[0].Config().ClientURL, "metrics")
			require.NoError(t, err)

			mfs, err := getMetrics(metricsURL)
			require.NoError(t, err)

			var missingMetrics []string
			for _, metrics := range tc.expectedMetrics {
				if _, ok := mfs[metrics]; !ok {
					missingMetrics = append(missingMetrics, metrics)
				}
			}
			require.Emptyf(t, missingMetrics, "Some metrics are missing: %v", missingMetrics)

			// Please keep the log below to generate the expected metrics.
			// t.Logf("All metrics: %v", formatMetrics(slices.Sorted(maps.Keys(mfs))))
		})
	}
}

func getMetrics(metricsURL string) (map[string]*dto.MetricFamily, error) {
	httpClient := http.Client{Transport: &http.Transport{}}
	resp, err := httpClient.Get(metricsURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var parser expfmt.TextParser
	return parser.TextToMetricFamilies(bytes.NewReader(data))
}

// formatMetrics is only for test purpose
/*func formatMetrics(metrics []string) string {
	quoted := make([]string, len(metrics))
	for i, s := range metrics {
		quoted[i] = fmt.Sprintf(`"%s",`, s)
	}

	return fmt.Sprintf("[]string{\n%s\n}", strings.Join(quoted, "\n"))
}*/
