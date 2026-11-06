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
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"

	"go.etcd.io/etcd/pkg/v3/report"
)

const defaultMetricsSampleInterval = time.Second

// metricSample contains one scrape result from the Prometheus /metrics endpoint.
type metricSample struct {
	Timestamp time.Time          `json:"timestamp"`
	Values    map[string]float64 `json:"values"`
}

type metricSampler struct {
	url      string
	metrics  []string
	interval time.Duration
	client   *http.Client

	mu                sync.Mutex
	valuesByMetric    map[string][]float64
	timeSeriesSamples []metricSample

	cancel context.CancelFunc
	donec  chan struct{}
}

func newMetricSampler(url string, metrics []string) *metricSampler {
	return &metricSampler{
		url:      url,
		metrics:  metrics,
		interval: defaultMetricsSampleInterval,
		client: &http.Client{
			Timeout: 5 * time.Second,
		},
		valuesByMetric: make(map[string][]float64, len(metrics)),
		donec:          make(chan struct{}),
	}
}

func (s *metricSampler) start() {
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	go func() {
		defer close(s.donec)
		s.collect(ctx)
		ticker := time.NewTicker(s.interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				s.collect(ctx)
			case <-ctx.Done():
				return
			}
		}
	}()
}

func (s *metricSampler) stop() ([]report.MetricSummary, []metricSample) {
	if s.cancel != nil {
		s.cancel()
		<-s.donec
	}
	s.collect(context.Background())
	return s.summaries(), s.timeSeries()
}

func (s *metricSampler) collect(ctx context.Context) {
	values, err := fetchMetricValues(ctx, s.client, s.url, s.metrics)
	if err != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.timeSeriesSamples = append(s.timeSeriesSamples, metricSample{
		Timestamp: time.Now().UTC(),
		Values:    values,
	})
	for name, value := range values {
		s.valuesByMetric[name] = append(s.valuesByMetric[name], value)
	}
}

func (s *metricSampler) summaries() []report.MetricSummary {
	s.mu.Lock()
	defer s.mu.Unlock()

	summaries := make([]report.MetricSummary, 0, len(s.metrics))
	for _, name := range s.metrics {
		values := s.valuesByMetric[name]
		if len(values) == 0 {
			continue
		}
		summary := report.MetricSummary{
			Name: name,
			Max:  math.Inf(-1),
		}
		for _, value := range values {
			summary.Max = math.Max(summary.Max, value)
		}
		summaries = append(summaries, summary)
	}
	return summaries
}

func (s *metricSampler) timeSeries() []metricSample {
	s.mu.Lock()
	defer s.mu.Unlock()

	series := make([]metricSample, len(s.timeSeriesSamples))
	copy(series, s.timeSeriesSamples)
	return series
}

func fetchMetricValues(ctx context.Context, client *http.Client, url string, metrics []string) (map[string]float64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code %d", resp.StatusCode)
	}

	parser := expfmt.NewTextParser(model.LegacyValidation)
	families, err := parser.TextToMetricFamilies(resp.Body)
	if err != nil {
		return nil, err
	}
	return extractMetricValues(families, metrics), nil
}

func extractMetricValues(families map[string]*dto.MetricFamily, metrics []string) map[string]float64 {
	values := make(map[string]float64, len(metrics))
	for _, name := range metrics {
		family := families[name]
		if family == nil {
			continue
		}
		metrics := family.GetMetric()
		if len(metrics) == 0 {
			continue
		}
		value, ok := metricValue(metrics[0])
		if ok {
			values[name] = value
		}
	}
	return values
}

func metricValue(metric *dto.Metric) (float64, bool) {
	switch {
	case metric.GetGauge() != nil:
		return metric.GetGauge().GetValue(), true
	case metric.GetCounter() != nil:
		return metric.GetCounter().GetValue(), true
	case metric.GetUntyped() != nil:
		return metric.GetUntyped().GetValue(), true
	default:
		return 0, false
	}
}

type benchmarkReport struct {
	report.Report
	sampler *metricSampler
}

func newBenchmarkReport(base report.Report, sampler *metricSampler) report.Report {
	return &benchmarkReport{
		Report:  base,
		sampler: sampler,
	}
}

func (r *benchmarkReport) Run() <-chan string {
	r.sampler.start()
	return r.Report.Run()
}

type timeSeriesReport struct {
	Version   string         `json:"version"`
	Operation string         `json:"operation"`
	Metric    string         `json:"metric"`
	Samples   []metricSample `json:"samples"`
}

func writeTimeSeriesReport(benchmarkOp, metric string, samples []metricSample) {
	report := timeSeriesReport{
		Version:   "v1",
		Operation: strings.ToUpper(benchmarkOp),
		Metric:    metric,
		Samples:   samples,
	}
	reportB, _ := json.MarshalIndent(report, "", "  ")

	artifactsDir := os.Getenv("ARTIFACTS")
	if artifactsDir == "" {
		artifactsDir = "./_artifacts"
	}

	fileName := fmt.Sprintf("EtcdResourceMetrics_benchmark_%s_%s.json", benchmarkOp, time.Now().UTC().Format(time.RFC3339))
	err := os.MkdirAll(artifactsDir, 0o755)
	if err != nil {
		fmt.Println("Error creating artifacts directory:", err)
	}
	destPath := filepath.Join(artifactsDir, fileName)
	err = os.WriteFile(destPath, reportB, 0o644)
	if err != nil {
		fmt.Println("Error writing to file:", err)
	}
	fmt.Println("Successfully created a JSON resource metrics report at", destPath)
}
