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

package cmd

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"
	"github.com/stretchr/testify/require"

	"go.etcd.io/etcd/client/pkg/v3/transport"
	"go.etcd.io/etcd/pkg/v3/report"
)

func TestExtractMetricValues(t *testing.T) {
	parser := expfmt.NewTextParser(model.LegacyValidation)
	families, err := parser.TextToMetricFamilies(strings.NewReader(`
# HELP process_resident_memory_bytes Resident memory size in bytes.
# TYPE process_resident_memory_bytes gauge
process_resident_memory_bytes 123
# HELP go_memstats_heap_alloc_bytes Number of heap bytes allocated and still in use.
# TYPE go_memstats_heap_alloc_bytes gauge
go_memstats_heap_alloc_bytes 456
# HELP go_memstats_heap_inuse_bytes Number of heap bytes that are in use.
# TYPE go_memstats_heap_inuse_bytes gauge
go_memstats_heap_inuse_bytes 789
# HELP ignored_metric Ignored.
# TYPE ignored_metric gauge
ignored_metric 999
`))
	require.NoError(t, err)

	values := extractMetricValues(families, []string{
		"process_resident_memory_bytes",
		"go_memstats_heap_alloc_bytes",
		"go_memstats_heap_inuse_bytes",
		"missing_metric",
	})

	require.Equal(t, map[string]float64{
		"process_resident_memory_bytes": 123,
		"go_memstats_heap_alloc_bytes":  456,
		"go_memstats_heap_inuse_bytes":  789,
	}, values)
}

func TestMetricSamplerSummaries(t *testing.T) {
	metricNames := []string{
		"process_resident_memory_bytes",
		"go_memstats_heap_alloc_bytes",
		"go_memstats_heap_inuse_bytes",
	}
	sampler := newMetricSampler("http://127.0.0.1:2379/metrics", metricNames)
	sampler.valuesByMetric["process_resident_memory_bytes"] = []float64{10, 20, 15}
	sampler.valuesByMetric["go_memstats_heap_alloc_bytes"] = []float64{30, 40, 35}
	sampler.valuesByMetric["go_memstats_heap_inuse_bytes"] = []float64{50, 45, 60}

	summaries := sampler.summaries()
	require.Len(t, summaries, 3)

	maxByMetric := make(map[string]float64, len(summaries))
	for _, summary := range summaries {
		maxByMetric[summary.Name] = summary.Max
	}
	require.Equal(t, map[string]float64{
		"process_resident_memory_bytes": 20,
		"go_memstats_heap_alloc_bytes":  40,
		"go_memstats_heap_inuse_bytes":  60,
	}, maxByMetric)
}

func TestBenchmarkReportWritesTimeSeriesWithoutPerfDash(t *testing.T) {
	artifactsDir := t.TempDir()
	t.Setenv("ARTIFACTS", artifactsDir)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`# HELP process_resident_memory_bytes Resident memory size in bytes.
# TYPE process_resident_memory_bytes gauge
process_resident_memory_bytes 123
# HELP go_memstats_heap_alloc_bytes Number of heap bytes allocated and still in use.
# TYPE go_memstats_heap_alloc_bytes gauge
go_memstats_heap_alloc_bytes 456
# HELP go_memstats_heap_inuse_bytes Number of heap bytes that are in use.
# TYPE go_memstats_heap_inuse_bytes gauge
go_memstats_heap_inuse_bytes 789
`))
	}))
	defer server.Close()

	metricNames := []string{
		"process_resident_memory_bytes",
		"go_memstats_heap_alloc_bytes",
		"go_memstats_heap_inuse_bytes",
	}
	sampler := newMetricSampler(server.URL, metricNames)
	base := report.NewReportWithOptions("%4.4f", "put", report.Options{
		MetricSummaries: func() []report.MetricSummary {
			summaries, timeSeries := sampler.stop()
			if len(timeSeries) > 0 {
				writeTimeSeriesReport("put", "resource", timeSeries)
			}
			return summaries
		},
	})
	r := newBenchmarkReport(base, sampler)
	go func() {
		start := time.Now()
		r.Results() <- report.Result{Start: start, End: start.Add(time.Millisecond)}
		close(r.Results())
	}()

	output := <-r.Run()
	require.Contains(t, output, "Resource metrics:")
	require.Contains(t, output, "process_resident_memory_bytes max:")
	require.Contains(t, output, "go_memstats_heap_alloc_bytes max:")
	require.Contains(t, output, "go_memstats_heap_inuse_bytes max:")

	resourceReports, err := filepath.Glob(filepath.Join(artifactsDir, "EtcdResourceMetrics_benchmark_put_*.json"))
	require.NoError(t, err)
	require.Len(t, resourceReports, 1)

	data, err := os.ReadFile(resourceReports[0])
	require.NoError(t, err)
	var resourceReport timeSeriesReport
	require.NoError(t, json.Unmarshal(data, &resourceReport))
	require.Equal(t, "PUT", resourceReport.Operation)
	require.Equal(t, "resource", resourceReport.Metric)
	require.NotEmpty(t, resourceReport.Samples)
	require.Equal(t, map[string]float64{
		"process_resident_memory_bytes": 123,
		"go_memstats_heap_alloc_bytes":  456,
		"go_memstats_heap_inuse_bytes":  789,
	}, resourceReport.Samples[0].Values)

	perfReports, err := filepath.Glob(filepath.Join(artifactsDir, "EtcdAPI_benchmark_put_*.json"))
	require.NoError(t, err)
	require.Empty(t, perfReports)
}

func TestWriteMetricTimeSeriesReport(t *testing.T) {
	artifactsDir := t.TempDir()
	t.Setenv("ARTIFACTS", artifactsDir)

	writeTimeSeriesReport("put", "resource", []metricSample{
		{
			Timestamp: time.Date(2026, 5, 15, 1, 2, 3, 0, time.UTC),
			Values: map[string]float64{
				"process_resident_memory_bytes": 123,
			},
		},
	})

	matches, err := filepath.Glob(filepath.Join(artifactsDir, "EtcdResourceMetrics_benchmark_put_*.json"))
	require.NoError(t, err)
	require.Len(t, matches, 1)

	data, err := os.ReadFile(matches[0])
	require.NoError(t, err)

	var resourceReport timeSeriesReport
	require.NoError(t, json.Unmarshal(data, &resourceReport))
	require.Equal(t, "v1", resourceReport.Version)
	require.Equal(t, "PUT", resourceReport.Operation)
	require.Equal(t, "resource", resourceReport.Metric)
	require.Len(t, resourceReport.Samples, 1)
	require.Equal(t, float64(123), resourceReport.Samples[0].Values["process_resident_memory_bytes"])
}

func TestMetricsEndpointURL(t *testing.T) {
	originalEndpoints, originalTLS := endpoints, tls
	t.Cleanup(func() {
		endpoints, tls = originalEndpoints, originalTLS
	})

	testCases := []struct {
		name      string
		endpoints []string
		tls       transport.TLSInfo
		want      string
	}{
		{name: "host", endpoints: []string{"127.0.0.1:2379"}, want: "http://127.0.0.1:2379/metrics"},
		{name: "URL", endpoints: []string{"http://127.0.0.1:2379/v3?foo=bar"}, want: "http://127.0.0.1:2379/metrics"},
		{name: "TLS", endpoints: []string{"127.0.0.1:2379"}, tls: transport.TLSInfo{CertFile: "client.crt"}, want: "https://127.0.0.1:2379/metrics"},
		{name: "empty", endpoints: nil, want: ""},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			endpoints, tls = tc.endpoints, tc.tls
			require.Equal(t, tc.want, metricsEndpointURL())
		})
	}
}
