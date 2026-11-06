#!/usr/bin/env bash
# Copyright 2025 The etcd Authors
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

# This script runs a benchmark on a locally started etcd server

set -euo pipefail

source ./scripts/test_lib.sh

COMMON_BENCHMARK_FLAGS="--report-perfdash --metrics=process_resident_memory_bytes,go_memstats_heap_alloc_bytes,go_memstats_heap_inuse_bytes"

if [[ $# -lt 1 ]]; then
  echo "Usage: $0 <benchmark-name> [tester args...]"
  exit 1
fi

BENCHMARK_NAME="$1"
ARGS="${*:2}"

echo "Starting the etcd server..."

# Create a directory for etcd data under /tmp/etcd
mkdir -p /tmp/etcd
DATA_DIR=$(mktemp -d /tmp/etcd/data-XXXXXX)
./bin/etcd --data-dir="$DATA_DIR" > /tmp/etcd.log 2>&1 &
etcd_pid=$!

trap 'log_warning -e "Stopping etcd server - PID $etcd_pid";
      kill $etcd_pid 2>/dev/null || true;
      rm -rf $DATA_DIR;
      log_success "Deleted the contents from $DATA_DIR related to benchmark test"' EXIT

# Wait until etcd becomes healthy
for retry in {1..10}; do
  if ./bin/etcdctl endpoint health --cluster> /dev/null 2>&1; then
    log_success -e "\\netcd is healthy"
    break
  fi
  log_warning -e "\\nWaiting for etcd to be healthy..."
  sleep 1
  if [[ $retry -eq 10 ]]; then
    log_error -e "\\nFailed to confirm etcd health after $retry attempts. Check /tmp/etcd.log for more information"
    exit 1
  fi
done

log_success -e "etcd process is running with PID $etcd_pid"

log_callout -e "\\nPerforming benchmark $BENCHMARK_NAME with arguments: $ARGS"
read -r -a TESTER_OPTIONS <<< "$ARGS"
log_callout "Running: benchmark $BENCHMARK_NAME ${TESTER_OPTIONS[*]} $COMMON_BENCHMARK_FLAGS"
benchmark "$BENCHMARK_NAME" "${TESTER_OPTIONS[@]}" $COMMON_BENCHMARK_FLAGS
log_callout "Completed: benchmark $BENCHMARK_NAME ${TESTER_OPTIONS[*]} $COMMON_BENCHMARK_FLAGS"
