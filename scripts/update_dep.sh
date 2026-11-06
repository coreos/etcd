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
#
# Usage:
#    ./scripts/update_dep.sh module version
# or ./scripts/update_dep.sh module (to update to the latest version)
# e.g.
#   ./scripts/update_dep.sh github.com/golang/groupcache
#   ./scripts/update_dep.sh github.com/soheilhy/cmux v0.1.5
#
# Updates version of given dependency in all the modules that depend on the mod.

set -euo pipefail

source ./scripts/test_lib.sh

if [ "$#" -lt 1 ] || [ "$#" -gt 2 ]; then
    log_error "Illegal number of parameters. Usage: $0 module [version]"
    exit 1
fi

mod="$1"
ver="${2:-}"

function print_current_dep_version {
  log_info "${mod} version in all go.mod files:"
  find . -name go.mod -exec grep -H "^\s*${mod}\s" {} + | sed 's|:|\t|' || true
  printf "\n"
}

function is_fully_indirect {
  local result
  result=$(find . -name go.mod -print0 | xargs -0 -I{} /bin/sh -c "cd \$(dirname {}); go list -f \"{{if eq .Path \\\"${mod}\\\"}}{{.Indirect}}{{end}}\" -m all" | sort | uniq)
  [ "$result" = "true" ]
}

function update_module {
  local subdir
  subdir=$(module_subdir)

  # The `go get` command is most effective on dependencies that are explicitly
  # listed as direct requirements in the go.mod file. When updating a purely
  # indirect dependency, `go get` might not update it as expected.
  #
  # To work around this, we temporarily promote the indirect dependency to a
  # direct one in the go.mod file using `go mod edit`. This ensures that
  # `go get` will see and correctly update the module. Subsequent cleanup
  # commands (like `go mod tidy`) will automatically move it back
  # to an indirect dependency, but at the designated updated version.
  #
  # Note: `go mod edit` requires a specific version (e.g., v1.2.3), so we only
  # use it when a version is explicitly provided. For "latest", we skip this
  # step and let `go get -u` handle it directly.
  if [ -n "${ver}" ]; then
    run go mod edit -require "${mod}@${ver}" || true
  fi

  # Check if the module is a dependency.
  if go list -m all | grep -q -E "^\s*${mod}\s"; then
    log_info "  Updating in ${subdir}..."
    if [ -z "${ver}" ]; then
      run go get -u "${mod}"
    else
      run go get "${mod}@${ver}"
    fi
  fi
}

function check_otel_semconv {
  local cursemconv newsemconv confirm
  cursemconv=$(git grep go.opentelemetry.io/otel/semconv/v -- '**/*.go' | awk -F'[/"]' '{ print $(NF-1) }')
  newsemconv=$(go list -deps | awk -F/ '/^go.opentelemetry.io\/otel\/semconv\/v[0-9.]+$/ { print $4 }' | sort -V | tail -n 1)
  if [[ -n "$cursemconv" ]] && [[ -n "$newsemconv" ]] && [[ "$cursemconv" != "$newsemconv" ]]; then
    read -p "An otel semconv update from $cursemconv to $newsemconv is available. Do you want to update it? [Y/n] " -r confirm
    [[ "$confirm" == [Nn] ]] && return
    git grep -l -z "go.opentelemetry.io/otel/semconv/$cursemconv" | xargs -0 sed -i "sXgo.opentelemetry.io/otel/semconv/${cursemconv}Xgo.opentelemetry.io/otel/semconv/${newsemconv}X"
  fi
}

print_current_dep_version
if is_fully_indirect; then
  read -p "Module ${mod} is a purely indirect dependency. Are you sure you want to update it? [y/N] " -r confirm
  [[ "$confirm" == [Yy] ]] || exit # Default is No
fi

log_info "Updating '${mod}' to ${ver:-latest} across all modules..."
run_for_workspace_modules update_module

make fix-mod-tidy fix-bom update-go-workspace verify-dep

check_otel_semconv

print_current_dep_version
