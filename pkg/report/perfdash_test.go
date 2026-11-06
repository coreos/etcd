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

package report

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestWritePerfDashReportIncludesMetricSummaries(t *testing.T) {
	artifactsDir := t.TempDir()
	t.Setenv("ARTIFACTS", artifactsDir)

	r := newReport("%4.4f", "put", Options{})
	r.stats.Lats = []float64{0.001, 0.002, 0.003}
	r.writePerfDashReport("put", []MetricSummary{
		{
			Name: "process_resident_memory_bytes",
			Max:  300,
		},
		{
			Name: "go_memstats_heap_alloc_bytes",
			Max:  200,
		},
		{
			Name: "go_memstats_heap_inuse_bytes",
			Max:  250,
		},
	})

	matches, err := filepath.Glob(filepath.Join(artifactsDir, "EtcdAPI_benchmark_put_*.json"))
	require.NoError(t, err)
	require.Len(t, matches, 1)

	data, err := os.ReadFile(matches[0])
	require.NoError(t, err)

	var report perfdashFormattedReport
	require.NoError(t, json.Unmarshal(data, &report))
	require.Equal(t, "v1", report.Version)
	require.Len(t, report.DataItems, 4)

	require.Equal(t, "ms", report.DataItems[0].Unit)
	require.Equal(t, "PUT", report.DataItems[0].Labels["Operation"])
	require.Equal(t, "latency", report.DataItems[0].Labels["Metric"])
	require.Contains(t, report.DataItems[0].Data, "Perc50")
	require.Contains(t, report.DataItems[0].Data, "Perc90")
	require.Contains(t, report.DataItems[0].Data, "Perc99")

	maxByMetric := make(map[string]float64)
	for _, item := range report.DataItems[1:] {
		require.Equal(t, "bytes", item.Unit)
		require.Equal(t, "PUT", item.Labels["Operation"])
		require.Len(t, item.Data, 1)
		maxByMetric[item.Labels["Metric"]] = item.Data["Max"]
	}
	require.Equal(t, map[string]float64{
		"process_resident_memory_bytes": 300,
		"go_memstats_heap_alloc_bytes":  200,
		"go_memstats_heap_inuse_bytes":  250,
	}, maxByMetric)

	_, err = time.Parse(time.RFC3339, filepath.Base(matches[0])[len("EtcdAPI_benchmark_put_"):len(filepath.Base(matches[0]))-len(".json")])
	require.NoError(t, err)
}
