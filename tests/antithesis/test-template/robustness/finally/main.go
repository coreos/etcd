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

//go:build cgo && amd64

package main

import (
	"maps"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/antithesishq/antithesis-sdk-go/assert"
	"go.uber.org/zap"

	"go.etcd.io/etcd/tests/v3/antithesis/test-template/robustness/common"
	"go.etcd.io/etcd/tests/v3/robustness/report"
	"go.etcd.io/etcd/tests/v3/robustness/validate"
)

const (
	reportFileName = "history.html"
)

func main() {
	_, reportPath, dirs := common.GetPaths()

	lg, err := zap.NewProduction()
	if err != nil {
		panic(err)
	}
	reports, err := report.LoadClientReports(reportPath)
	assert.Always(err == nil, "Loaded client reports", map[string]any{"error": err})
	tf, err := report.LoadTrafficDetail(reportPath)
	if err != nil && !os.IsNotExist(err) {
		panic(err)
	}

	result := validateReports(lg, dirs, reports, tf)
	if err := result.Linearization.Visualize(lg, filepath.Join(reportPath, reportFileName)); err != nil {
		panic(err)
	}
}

func validateReports(lg *zap.Logger, serversDataPath map[string]string, reports []report.ClientReport, tf report.TrafficDetail) validate.RobustnessResult {
	persistedRequests, err := report.PersistedRequests(lg, slices.Collect(maps.Values(serversDataPath)))
	assertResult(validate.ResultFromError(err), "Loaded persisted requests")

	validateConfig := validate.Config{ExpectRevisionUnique: tf.ExpectUniqueRevision}
	result := validate.ValidateAndReturnVisualize(lg, validateConfig, reports, persistedRequests, 5*time.Minute)
	assertResult(result.Assumptions, "Validation assumptions fulfilled")
	assertResult(result.Linearization.Result, "Linearization validation passes")
	assertResult(result.Watch, "Watch validation passes")
	assertResult(result.Serializable, "Serializable validation passes")
	lg.Info("Completed robustness validation")
	return result
}

func assertResult(result validate.Result, name string) {
	switch result.Status {
	case validate.Success, validate.Failure:
		assert.Always(result.Status == validate.Success, name, map[string]any{"msg": result.Message})
	case validate.Skipped:
		// Validation intentionally skipped (e.g. an earlier stage already
		// failed, or required data wasn't available) — expected, not a bug.
	default:
		assert.Unreachable(name, map[string]any{"error": result.Error().Error()})
	}
}
