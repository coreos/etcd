// Copyright 2025 The etcd Authors
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
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type DataItem struct {
	Data   map[string]float64 `json:"data"`
	Labels map[string]string  `json:"labels"`
	Unit   string             `json:"unit"`
}

type perfdashFormattedReport struct {
	Version   string     `json:"version"`
	DataItems []DataItem `json:"dataItems"`
}

func (r *report) writePerfDashReport(benchmarkOp string, metricSummaries []MetricSummary) {
	pcls, data := Percentiles(r.stats.Lats)
	pclsData := make(map[float64]float64)
	for i := 0; i < len(pcls); i++ {
		pclsData[pcls[i]] = data[i] * 1000 // Since the reported data is in seconds, convert to ms.
	}
	dataItems := []DataItem{
		{
			Data: map[string]float64{
				"Perc50": math.Round(pclsData[50]*10000) / 10000,
				"Perc90": math.Round(pclsData[90]*10000) / 10000,
				"Perc99": math.Round(pclsData[99]*10000) / 10000,
			},
			Unit: "ms",
			Labels: map[string]string{
				"Operation": strings.ToUpper(benchmarkOp),
				"Metric":    "latency",
			},
		},
	}
	for _, summary := range metricSummaries {
		dataItems = append(dataItems, DataItem{
			Data: map[string]float64{
				"Max": math.Round(summary.Max*10000) / 10000,
			},
			Unit: metricUnit(summary.Name),
			Labels: map[string]string{
				"Operation": strings.ToUpper(benchmarkOp),
				"Metric":    summary.Name,
			},
		})
	}
	report := perfdashFormattedReport{
		Version:   "v1",
		DataItems: dataItems,
	}
	reportB, _ := json.MarshalIndent(report, "", "  ")

	artifactsDir := os.Getenv("ARTIFACTS")
	if artifactsDir == "" {
		artifactsDir = "./_artifacts"
	}

	fileName := fmt.Sprintf("EtcdAPI_benchmark_%s_%s.json", benchmarkOp, time.Now().UTC().Format(time.RFC3339))
	err := os.MkdirAll(artifactsDir, 0o755)
	if err != nil {
		fmt.Println("Error creating artifacts directory:", err)
	}
	destPath := filepath.Join(artifactsDir, fileName)
	err = os.WriteFile(destPath, reportB, 0o644)
	if err != nil {
		fmt.Println("Error writing to file:", err)
	}
	fmt.Println("Successfully created a JSON perf report at", destPath)
}

func metricUnit(name string) string {
	if strings.HasSuffix(name, "_bytes") || strings.HasSuffix(name, "_in_bytes") {
		return "bytes"
	}
	return ""
}
