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

tmp_sbom_dir=$(mktemp -d)
trap 'rm -rf "${tmp_sbom_dir}"' EXIT

./scripts/generate-modern-sbom.sh "main" "${tmp_sbom_dir}"

# syft stamps a fresh timestamp/UUID on every run. Only adopt the regenerated
# file when its meaningful content (ignoring those fields) changed, so repeated
# runs stay byte-stable. Filters must match scripts/verify_modern_sbom.sh.
spdx_filter='del(.creationInfo.created)'
cdx_filter='del(.serialNumber, .metadata.timestamp)'

function update_if_changed {
  local committed="$1" generated="$2" filter="$3"
  if [ ! -f "${committed}" ] ||
    ! diff <(jq "${filter}" "${committed}") <(jq "${filter}" "${generated}") >/dev/null; then
    cp "${generated}" "${committed}"
  fi
}

update_if_changed "${ETCD_ROOT_DIR}/sbom.spdx.json" "${tmp_sbom_dir}/sbom.spdx.json" "${spdx_filter}"
update_if_changed "${ETCD_ROOT_DIR}/sbom.cdx.json" "${tmp_sbom_dir}/sbom.cdx.json" "${cdx_filter}"
