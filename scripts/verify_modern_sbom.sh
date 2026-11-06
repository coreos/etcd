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
cd "${ETCD_ROOT_DIR}"
source "${ETCD_ROOT_DIR}/scripts/test_lib.sh"

log_callout "Checking modern SBOM..."

temp_sbom_dir=$(mktemp -d)
trap 'rm -rf "${temp_sbom_dir}"' EXIT

if ! "${ETCD_ROOT_DIR}/scripts/generate-modern-sbom.sh" "main" "${temp_sbom_dir}"; then
  log_error "modern SBOM generation failed"
  exit 1
fi

# Ignore fields that change on every regen (timestamps, UUIDs); any
# remaining diff is a real dependency change.
spdx_filter='del(.creationInfo.created)'
cdx_filter='del(.serialNumber, .metadata.timestamp)'

fail=0
spdx_diff=$(diff <(jq "${spdx_filter}" "${ETCD_ROOT_DIR}/sbom.spdx.json") \
                 <(jq "${spdx_filter}" "${temp_sbom_dir}/sbom.spdx.json") || true)
if [ -n "${spdx_diff}" ]; then
  log_error "sbom.spdx.json drift detected. Run 'make fix-modern-sbom' to update."
  head -60 <<<"${spdx_diff}"
  fail=1
fi
cdx_diff=$(diff <(jq "${cdx_filter}" "${ETCD_ROOT_DIR}/sbom.cdx.json") \
                <(jq "${cdx_filter}" "${temp_sbom_dir}/sbom.cdx.json") || true)
if [ -n "${cdx_diff}" ]; then
  log_error "sbom.cdx.json drift detected. Run 'make fix-modern-sbom' to update."
  head -60 <<<"${cdx_diff}"
  fail=1
fi

if [ "${fail}" -ne 0 ]; then
  exit 1
fi

log_success "modern SBOM is up to date"
