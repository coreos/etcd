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

package integration

import (
	"fmt"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	"go.etcd.io/etcd/api/v3/version"
	"go.etcd.io/etcd/client/pkg/v3/transport"
	clientv3 "go.etcd.io/etcd/client/v3"
	"go.etcd.io/etcd/server/v3/storage"
	"go.etcd.io/etcd/tests/v3/framework/integration"
)

// TestMetricDbSizeBoot checks that the db size metric is set on boot.
func TestMetricDbSizeBoot(t *testing.T) {
	integration.BeforeTest(t)
	clus := integration.NewCluster(t, &integration.ClusterConfig{Size: 1})
	defer clus.Terminate(t)

	v, err := clus.Members[0].Metric("etcd_debugging_mvcc_db_total_size_in_bytes")
	require.NoError(t, err)

	require.NotEqualf(t, "0", v, "expected non-zero, got %q", v)
}

func TestMetricDbSizeDefrag(t *testing.T) {
	testMetricDbSizeDefrag(t, "etcd")
}

// testMetricDbSizeDefrag checks that the db size metric is set after defrag.
func testMetricDbSizeDefrag(t *testing.T, name string) {
	integration.BeforeTest(t)
	clus := integration.NewCluster(t, &integration.ClusterConfig{Size: 1})
	defer clus.Terminate(t)

	kvc := integration.ToGRPC(clus.Client(0)).KV
	mc := integration.ToGRPC(clus.Client(0)).Maintenance

	// expand the db size
	numPuts := 25 // large enough to write more than 1 page
	putreq := &pb.PutRequest{Key: []byte("k"), Value: make([]byte, 4096)}
	for i := 0; i < numPuts; i++ {
		time.Sleep(10 * time.Millisecond) // to execute multiple backend txn
		_, err := kvc.Put(t.Context(), putreq)
		require.NoError(t, err)
	}

	// wait for backend txn sync
	time.Sleep(500 * time.Millisecond)

	expected := numPuts * len(putreq.Value)
	beforeDefrag, err := clus.Members[0].Metric(name + "_mvcc_db_total_size_in_bytes")
	require.NoError(t, err)
	bv, err := strconv.Atoi(beforeDefrag)
	require.NoError(t, err)
	require.GreaterOrEqualf(t, bv, expected, "expected db size greater than %d, got %d", expected, bv)
	beforeDefragInUse, err := clus.Members[0].Metric("etcd_mvcc_db_total_size_in_use_in_bytes")
	require.NoError(t, err)
	biu, err := strconv.Atoi(beforeDefragInUse)
	require.NoError(t, err)
	require.GreaterOrEqualf(t, biu, expected, "expected db size in use is greater than %d, got %d", expected, biu)

	// clear out historical keys, in use bytes should free pages
	creq := &pb.CompactionRequest{Revision: int64(numPuts), Physical: true}
	_, kerr := kvc.Compact(t.Context(), creq)
	require.NoError(t, kerr)

	validateAfterCompactionInUse := func() error {
		// Put to move PendingPages to FreePages
		_, verr := kvc.Put(t.Context(), putreq)
		require.NoError(t, verr)
		time.Sleep(500 * time.Millisecond)

		afterCompactionInUse, verr := clus.Members[0].Metric("etcd_mvcc_db_total_size_in_use_in_bytes")
		require.NoError(t, verr)
		aciu, verr := strconv.Atoi(afterCompactionInUse)
		require.NoError(t, verr)
		if biu <= aciu {
			return fmt.Errorf("expected less than %d, got %d after compaction", biu, aciu)
		}
		return nil
	}

	// backend rollbacks read transaction asynchronously (PR #10523),
	// which causes the result to be flaky. Retry 3 times.
	maxRetry, retry := 3, 0
	for {
		err = validateAfterCompactionInUse()
		if err == nil {
			break
		}
		retry++
		require.Lessf(t, retry, maxRetry, "%v", err.Error())
	}

	// defrag should give freed space back to fs
	mc.Defragment(t.Context(), &pb.DefragmentRequest{})

	afterDefrag, err := clus.Members[0].Metric(name + "_mvcc_db_total_size_in_bytes")
	require.NoError(t, err)
	av, err := strconv.Atoi(afterDefrag)
	require.NoError(t, err)
	require.Greaterf(t, bv, av, "expected less than %d, got %d after defrag", bv, av)

	afterDefragInUse, err := clus.Members[0].Metric("etcd_mvcc_db_total_size_in_use_in_bytes")
	require.NoError(t, err)
	adiu, err := strconv.Atoi(afterDefragInUse)
	require.NoError(t, err)
	require.LessOrEqualf(t, adiu, av, "db size in use (%d) is expected less than db size (%d) after defrag", adiu, av)
}

func TestMetricQuotaBackendBytes(t *testing.T) {
	integration.BeforeTest(t)
	clus := integration.NewCluster(t, &integration.ClusterConfig{Size: 1})
	defer clus.Terminate(t)

	qs, err := clus.Members[0].Metric("etcd_server_quota_backend_bytes")
	require.NoError(t, err)
	qv, err := strconv.ParseFloat(qs, 64)
	require.NoError(t, err)
	require.Equalf(t, storage.DefaultQuotaBytes, int64(qv), "expected %d, got %f", storage.DefaultQuotaBytes, qv)
}

func TestMetricsHealth(t *testing.T) {
	integration.BeforeTest(t)
	clus := integration.NewCluster(t, &integration.ClusterConfig{Size: 1})
	defer clus.Terminate(t)

	tr, err := transport.NewTransport(transport.TLSInfo{}, 5*time.Second)
	require.NoError(t, err)
	u := clus.Members[0].ClientURLs[0]
	u.Path = "/health"
	resp, err := tr.RoundTrip(&http.Request{
		Header: make(http.Header),
		Method: http.MethodGet,
		URL:    &u,
	})
	resp.Body.Close()
	require.NoError(t, err)

	hv, err := clus.Members[0].Metric("etcd_server_health_failures")
	require.NoError(t, err)
	require.Equalf(t, "0", hv, "expected '0' from etcd_server_health_failures, got %q", hv)
}

// TestMetricClusterVersionRecover verifies that the etcd_cluster_version metric
// is set after a member restart, which goes through the RaftCluster.Recover() path.
func TestMetricClusterVersionRecover(t *testing.T) {
	integration.BeforeTest(t)
	clus := integration.NewCluster(t, &integration.ClusterConfig{Size: 1})
	defer clus.Terminate(t)

	clusterVersionLabel := fmt.Sprintf(`cluster_version="%s"`, version.Cluster(version.Version))

	// The metric is set asynchronously by monitorClusterVersions on the
	// leader, so poll until it appears with the expected version label.
	waitMetric := func() string {
		var (
			v   string
			err error
		)
		require.Eventuallyf(t, func() bool {
			v, err = clus.Members[0].Metric("etcd_cluster_version", clusterVersionLabel)
			return err == nil && v == "1"
		}, 10*time.Second, 100*time.Millisecond,
			"expected etcd_cluster_version{%s} = 1, last value %q, err %v",
			clusterVersionLabel, v, err)
		return v
	}

	waitMetric()

	// Restart the member: this exercises RaftCluster.Recover()
	clus.Members[0].Stop(t)
	require.NoError(t, clus.Members[0].Restart(t))
	require.Equal(t, 0, clus.WaitMembersForLeader(t, clus.Members))
	clus.Members[0].WaitOK(t)

	// After RaftCluster.Recover(), the metric must still report 1 for the current cluster version.
	v := waitMetric()
	require.Equalf(t, "1", v,
		"expected etcd_cluster_version{%s} = 1 after restart, got %q",
		clusterVersionLabel, v)
}

func TestMetricsRangeDurationSeconds(t *testing.T) {
	integration.BeforeTest(t)
	clus := integration.NewCluster(t, &integration.ClusterConfig{Size: 1})
	defer clus.Terminate(t)

	client := clus.RandClient()

	keys := []string{
		"my-namespace/foobar", "my-namespace/foobar1", "namespace/foobar1",
	}
	for _, key := range keys {
		_, err := client.Put(t.Context(), key, "data")
		require.NoError(t, err)
	}

	_, err := client.Get(t.Context(), "", clientv3.WithFromKey())
	require.NoError(t, err)

	rangeDurationSeconds, err := clus.Members[0].Metric("etcd_server_range_duration_seconds")
	require.NoError(t, err)

	require.NotEmptyf(t, rangeDurationSeconds, "expected a number from etcd_server_range_duration_seconds")

	rangeDuration, err := strconv.ParseFloat(rangeDurationSeconds, 64)
	require.NoErrorf(t, err, "failed to parse duration: %s", rangeDurationSeconds)

	maxRangeDuration := 600.0
	require.GreaterOrEqualf(t, rangeDuration, 0.0, "expected etcd_server_range_duration_seconds to be between 0 and %f, got %f", maxRangeDuration, rangeDuration)
	require.LessOrEqualf(t, rangeDuration, maxRangeDuration, "expected etcd_server_range_duration_seconds to be between 0 and %f, got %f", maxRangeDuration, rangeDuration)
}
