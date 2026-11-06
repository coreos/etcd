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
	"testing"
	"time"
)

func makeTestStats() Stats {
	lats := []float64{0.001, 0.002, 0.003, 0.004, 0.005, 0.010, 0.015, 0.020, 0.050, 0.100}
	return Stats{
		AvgTotal:  0.21,
		Fastest:   0.001,
		Slowest:   0.100,
		Average:   0.021,
		Stddev:    0.028,
		Total:     2 * time.Second,
		RPS:       5.0,
		Lats:      lats,
		ErrorDist: map[string]int{"context deadline exceeded": 2},
	}
}

func TestFormatJSON_ValidJSON(t *testing.T) {
	out, err := FormatJSON(makeTestStats(), "")
	if err != nil {
		t.Fatalf("FormatJSON returned error: %v", err)
	}
	var jr JSONReport
	if err := json.Unmarshal([]byte(out), &jr); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, out)
	}
}

func TestFormatJSON_ScalarFields(t *testing.T) {
	s := makeTestStats()
	out, err := FormatJSON(s, "")
	if err != nil {
		t.Fatalf("FormatJSON error: %v", err)
	}
	var jr JSONReport
	if err := json.Unmarshal([]byte(out), &jr); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if jr.TotalSecs != round4(s.Total.Seconds()) {
		t.Errorf("TotalSecs: got %v, want %v", jr.TotalSecs, round4(s.Total.Seconds()))
	}
	if jr.FastestSecs != round4(s.Fastest) {
		t.Errorf("FastestSecs: got %v, want %v", jr.FastestSecs, round4(s.Fastest))
	}
	if jr.SlowestSecs != round4(s.Slowest) {
		t.Errorf("SlowestSecs: got %v, want %v", jr.SlowestSecs, round4(s.Slowest))
	}
	if jr.RequestsPerSec != round4(s.RPS) {
		t.Errorf("RequestsPerSec: got %v, want %v", jr.RequestsPerSec, round4(s.RPS))
	}
}

func TestFormatJSON_RoundedTo4Decimals(t *testing.T) {
	s := Stats{
		AvgTotal: 1,
		Fastest:  0.000321508,
		Slowest:  0.003703315,
		Average:  0.0004574702618,
		Stddev:   0.0001419164847919312,
		Total:    time.Duration(4.589500805 * float64(time.Second)),
		RPS:      2178.886206775597,
		Lats:     []float64{0.000321508, 0.003703315},
		ErrorDist: map[string]int{},
	}
	out, err := FormatJSON(s, "put")
	if err != nil {
		t.Fatalf("FormatJSON error: %v", err)
	}
	var jr JSONReport
	if err := json.Unmarshal([]byte(out), &jr); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	want := map[string]float64{
		"total_secs":       4.5895,
		"slowest_secs":     0.0037,
		"fastest_secs":     0.0003,
		"average_secs":     0.0005,
		"stddev_secs":      0.0001,
		"requests_per_sec": 2178.8862,
	}
	got := map[string]float64{
		"total_secs":       jr.TotalSecs,
		"slowest_secs":     jr.SlowestSecs,
		"fastest_secs":     jr.FastestSecs,
		"average_secs":     jr.AverageSecs,
		"stddev_secs":      jr.StddevSecs,
		"requests_per_sec": jr.RequestsPerSec,
	}
	for k, w := range want {
		if got[k] != w {
			t.Errorf("%s: got %v, want %v (4-decimal text alignment)", k, got[k], w)
		}
	}
}

func TestFormatJSON_PercentileKeys(t *testing.T) {
	out, _ := FormatJSON(makeTestStats(), "")
	var jr JSONReport
	json.Unmarshal([]byte(out), &jr) //nolint:errcheck

	for _, key := range []string{"p10", "p25", "p50", "p75", "p90", "p95", "p99", "p99.9"} {
		if _, ok := jr.Percentiles[key]; !ok {
			t.Errorf("missing percentile key %q", key)
		}
	}
}

func TestFormatJSON_ErrorsIncluded(t *testing.T) {
	out, _ := FormatJSON(makeTestStats(), "")
	var jr JSONReport
	json.Unmarshal([]byte(out), &jr) //nolint:errcheck

	if len(jr.Errors) == 0 {
		t.Fatal("expected errors in JSON output, got none")
	}
	if jr.Errors[0].Message != "context deadline exceeded" || jr.Errors[0].Count != 2 {
		t.Errorf("unexpected error entry: %+v", jr.Errors[0])
	}
}

func TestFormatJSON_ErrorsOmittedWhenEmpty(t *testing.T) {
	s := makeTestStats()
	s.ErrorDist = map[string]int{}
	out, _ := FormatJSON(s, "")

	var raw map[string]json.RawMessage
	json.Unmarshal([]byte(out), &raw) //nolint:errcheck
	if _, ok := raw["errors"]; ok {
		t.Error("'errors' key must be omitted when ErrorDist is empty")
	}
}

func TestFormatJSON_Deterministic(t *testing.T) {
	s := makeTestStats()
	s.ErrorDist = map[string]int{"err_a": 1, "err_b": 3, "err_c": 2}
	out1, _ := FormatJSON(s, "")
	out2, _ := FormatJSON(s, "")
	if out1 != out2 {
		t.Error("FormatJSON produced different output on identical input")
	}
}

func TestFormatJSON_EmptyStats(t *testing.T) {
	s := Stats{ErrorDist: map[string]int{}}
	out, err := FormatJSON(s, "")
	if err != nil {
		t.Fatalf("FormatJSON on empty stats: %v", err)
	}
	var jr JSONReport
	if err := json.Unmarshal([]byte(out), &jr); err != nil {
		t.Fatalf("empty stats must produce valid JSON: %v", err)
	}
}
