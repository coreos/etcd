#!/usr/bin/env bash

# Copyright 2026 The etcd Authors
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

set -euo pipefail

ETCD_ROOT_DIR=${ETCD_ROOT_DIR:-$(git rev-parse --show-toplevel)}
source "${ETCD_ROOT_DIR}/scripts/test_lib.sh"

log_callout "Generating bill of materials..."

_bom_modules=()
load_workspace_relative_modules_without_tools _bom_modules

# Internally license-bill-of-materials tends to modify go.sum
run cp go.sum go.sum.tmp || exit 2
run cp go.mod go.mod.tmp || exit 2

# Intentionally run the command once first, so it fetches dependencies. The exit code on the first
# run in a just cloned repository is always dirty.
GOOS=linux run_go_tool github.com/appscodelabs/license-bill-of-materials \
  --override-file ./bill-of-materials.override.json "${_bom_modules[@]}" &>/dev/null || true

# BOM file should be generated for linux. Otherwise running this command on other operating systems such as OSX
# results in certain dependencies being excluded from the BOM file, such as procfs.
# For more info, https://github.com/etcd-io/etcd/issues/19665
output=$(GOOS=linux run_go_tool github.com/appscodelabs/license-bill-of-materials \
  --override-file ./bill-of-materials.override.json \
  "${_bom_modules[@]}")
code="$?"

run cp go.sum.tmp go.sum || exit 2
run cp go.mod.tmp go.mod || exit 2

if [ "${code}" -ne 0 ]; then
  log_error -e "license-bill-of-materials (code: ${code}) failed with:\\n${output}"
  exit 255
fi

echo "${output}" > bill-of-materials.json
log_success "bill-of-materials.json generated"
