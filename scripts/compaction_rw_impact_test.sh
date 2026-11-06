#!/usr/bin/env bash
# Copyright 2026 The etcd Authors
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
# http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# Runs the compaction R/W impact e2e benchmark (tests/e2e/
# compaction_rw_impact_test.go, build tag `compaction_bench`) and
# invokes the chart renderer on the resulting JSON report.
#
# Output (under /tmp/etcd-compaction-rw-impact/):
#   - JSON report: compaction_rw_impact_<unix>.json
#   - PNG chart:   compaction_rw_impact_<unix>.json.chart.png
#
# Usage:
#   ./scripts/compaction_rw_impact_test.sh
#
# Environment overrides (optional):
#   COMPRESSION_TEST_TIMEOUT  test timeout (default: 5m)
#   CHART_RENDERER            path to rw-heatmaps binary that has the
#                             compaction-chart subcommand built in
#                             (default: ./bin/rw-heatmaps; auto-built
#                             from ./tools/rw-heatmaps on first run
#                             if missing)

set -euo pipefail

source ./scripts/test_lib.sh

TIMEOUT="${COMPRESSION_TEST_TIMEOUT:-5m}"
CHART_RENDERER="${CHART_RENDERER:-./bin/rw-heatmaps}"

# Auto-build the chart renderer on first run if it is missing. The
# renderer lives in its own Go module (tools/rw-heatmaps/go.mod), so
# `go build` from the repo root does not pick it up; it must be
# built explicitly.
if [ ! -x "${CHART_RENDERER}" ] && [ "${CHART_RENDERER}" = "./bin/rw-heatmaps" ]; then
    log_callout "Building chart renderer (./bin/rw-heatmaps)"
    if ! go build -o bin/rw-heatmaps ./tools/rw-heatmaps; then
        log_callout "Failed to build chart renderer; continuing without it"
    fi
fi

log_callout "Running compaction R/W impact benchmark (timeout=${TIMEOUT})"
set +e
OUTPUT=$(go test -count=1 \
    -tags compaction_bench \
    -run '^TestCompactionReadWriteImpact$' \
    ./tests/e2e/ \
    -v \
    -timeout "${TIMEOUT}" 2>&1)
status=$?
set -e
echo "${OUTPUT}"

if [ "${status}" -ne 0 ]; then
    log_callout "TEST FAILED (exit ${status}) — see log above for errors"
    exit "${status}"
fi

# Find the most recent JSON report path in the log output.
REPORT_PATH=$(echo "${OUTPUT}" | grep -oE '_artifacts/compaction_rw_impact_[0-9]+\.json' | tail -1 || true)
if [ -z "${REPORT_PATH}" ]; then
    log_callout "No report found in test output"
    exit 1
fi

# The test chdir's to a temp dir before writing the file. The path
# printed in the log is relative to that temp dir, which is gone by
# now. We rely on the test mirroring the report to a stable absolute
# path so this script can find it.
MIRROR_DIR="/tmp/etcd-compaction-rw-impact"
REPORT_PATH=$(find "${MIRROR_DIR}" -maxdepth 1 -name 'compaction_rw_impact_*.json' -print0 2>/dev/null | xargs -0 ls -t 2>/dev/null | head -1 || true)
if [ -z "${REPORT_PATH}" ]; then
    log_callout "No mirrored report found in ${MIRROR_DIR}"
    exit 1
fi
log_callout "Report: ${REPORT_PATH}"

if [ -x "${CHART_RENDERER}" ]; then
    log_callout "Rendering chart via ${CHART_RENDERER}"
    if "${CHART_RENDERER}" compaction-chart "${REPORT_PATH}"; then
        log_callout "Chart: ${REPORT_PATH}.chart.png"
    else
        log_callout "Chart rendering failed; report still available"
    fi
fi
