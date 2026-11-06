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

set -euo pipefail

source ./scripts/test_utils.sh

ROOT_MODULE="go.etcd.io/etcd"

if [[ "$(go list)" != "${ROOT_MODULE}/v3" ]]; then
  echo "must be run from '${ROOT_MODULE}/v3' module directory"
  exit 255
fi

function set_root_dir {
  ETCD_ROOT_DIR=$(go list -f '{{.Dir}}' "${ROOT_MODULE}/v3")
}

set_root_dir

####   Discovery of files/packages within a go module #####

# pkgs_in_module [optional:package_pattern]
# returns list of all packages in the current (dir) module.
# if the package_pattern is given, its being resolved.
function pkgs_in_module {
  go list -mod=mod "${1:-./...}";
}

# Prints subdirectory (from the repo root) for the current module.
function module_subdir {
  relativePath "${ETCD_ROOT_DIR}" "${PWD}"
}

####    Running actions against multiple modules ####

# run [command...] - runs given command, printing it first and
# again if it failed (in RED). Use to wrap important test commands
# that user might want to re-execute to shorten the feedback loop when fixing
# the test.
function run {
  local rpath
  local command
  rpath=$(module_subdir)
  # Quoting all components as the commands are fully copy-parsable:
  command=("${@}")
  command=("${command[@]@Q}")
  if [[ "${rpath}" != "." && "${rpath}" != "" ]]; then
    repro="(cd ${rpath} && ${command[*]})"
  else 
    repro="${command[*]}"
  fi

  log_cmd "% ${repro}"
  "${@}" 2> >(while read -r line; do echo -e "${COLOR_NONE}stderr: ${COLOR_MAGENTA}${line}${COLOR_NONE}">&2; done)
  local error_code=$?
  if [ ${error_code} -ne 0 ]; then
    log_error -e "FAIL: (code:${error_code}):\\n  % ${repro}"
    return ${error_code}
  fi
}

# run_for_module [module] [cmd]
# executes given command in the given module for given pkgs.
#   module_name - "." (in future: tests, client, server)
#   cmd         - cmd to be executed - that takes package as last argument
function run_for_module {
  local module=${1:-"."}
  shift 1
  (
    cd "${ETCD_ROOT_DIR}/${module}" && "$@"
  )
}

# maybe_run [cmd...] runs given command depending on the DRY_RUN flag.
function maybe_run() {
  if ${DRY_RUN}; then
    log_warning -e "# DRY_RUN:\\n  % ${*}"
  else
    run "${@}"
  fi
}

# Receives a reference to an array variable, and returns the workspace relative modules.
function load_workspace_relative_modules() {
  local -n _relative_modules=$1
  while IFS= read -r line; do _relative_modules+=("$line"); done < <(
    go work edit -json | jq -r '.Use[].DiskPath + "/..."'
  )
}

# Receives a reference to an array variable, and returns the workspace relative modules, not
# including the tools, as they are not considered to be added to the bill for materials.
function load_workspace_relative_modules_for_bom() {
  local -n relative_modules_for_bom=$1
  local modules=()
  load_workspace_relative_modules modules
  for module in "${modules[@]}"; do
    if [[ ! "${module}" =~ ^./tools ]]; then
      relative_modules_for_bom+=("${module}")
    fi
  done
}

#  run_for_all_workspace_modules [cmd]
#  run given command across all workspace modules
#  (unless the set is limited using ${PKG} or / ${USERMOD})
function run_for_all_workspace_modules {
  local pkg="${PKG:-./...}"
  if [ -z "${USERMOD:-}" ]; then
    local _modules=()
    load_workspace_relative_modules _modules
    run "$@" "${_modules[@]}"
  else
    run_for_module "${USERMOD}" "$@" "${pkg}" || return "$?"
  fi
}

# run_for_workspace_modules [cmd]
# run given command in each individual workspace module
# (unless the set is limited using ${PKG} or / ${USERMOD})
function run_for_workspace_modules {
  local keep_going_module=${KEEP_GOING_MODULE:-false}
  local fail_mod=false
  local pkg="${PKG:-./...}"

  if [ -z "${USERMOD:-}" ]; then
    local _modules=()
    load_workspace_relative_modules _modules
    for module in "${_modules[@]}"; do
      if ! run_for_module "${module%...}" "$@"; then
        if [ "$keep_going_module" = false ]; then
          log_error "There was a Failure in module ${module}, aborting..."
          return 1
        fi
        log_error "There was a Failure in module ${module}, keep going..."
        fail_mod=true
      fi
    done
    if [ "$fail_mod" = true ]; then
      return 1
    fi
  else
    run_for_module "${USERMOD}" "$@" "${pkg}" || return "$?"
  fi
}

function get_junit_filename_prefix {
  local junit_report_dir="$1"
  if [[ -z "${junit_report_dir}" ]]; then
    echo ""
    return
  fi

  mkdir -p "${junit_report_dir}"
  mktemp --dry-run "${junit_report_dir}/junit_XXXXXXXXXX"
}

function produce_junit_xmlreport {
  local -r junit_filename_prefix=${1:-}
  if [[ -z "${junit_filename_prefix}" ]]; then
    return
  fi

  local junit_xml_filename
  junit_xml_filename="${junit_filename_prefix}.xml"

  # Ensure that gotestsum is run without cross-compiling
  run_go_tool gotest.tools/gotestsum --junitfile "${junit_xml_filename}" --raw-command cat "${junit_filename_prefix}"*.stdout || exit 1
  if [ "${VERBOSE:-}" != "1" ]; then
    rm "${junit_filename_prefix}"*.stdout
  fi

  log_callout "Saved JUnit XML test report to ${junit_xml_filename}"
}


####    Running go test  ########

# run_go_tests_expanding_packages [arguments to pass to go test]
# Expands the packages in the list of arguments, i.e. ./... into a list of
# packages for that given module. Then, it calls run_go_tests with the expanded
# packages. Implements the legacy modes for non-parallel testing.
function run_go_tests_expanding_packages {
  local packages=()
  local args=()
  for arg in "$@"; do
    if [[ "${arg}" =~ ^\./ || "${arg}" =~ ^go\.etcd\.io/etcd ]]; then
      packages+=("${arg}")
    else
      args+=("${arg}")
    fi
  done

  # Expanding patterns (like ./...) into list of packages
  local unpacked_packages=()
  while IFS='' read -r line; do unpacked_packages+=("$line"); done < <(
    go list "${packages[@]}"
  )

  run_go_tests "${unpacked_packages[@]}" "${args[@]}"
}

# run_go_test [arguments to pass to go test]
# The following environment variables affect how the tests run:
#   - JUNIT_REPORT_DIR/ARTIFACTS: Enables collecting JUnit XML reports.
#   - VERBOSE: Sets a verbose output.
#
# Example:
#   KEEP_GOING_TESTS=true run_go_tests "./..." --short
#
# The function returns != 0 code in case of test failure.
function run_go_tests {
  local go_test_flags=()

  # If JUNIT_REPORT_DIR is unset, and ARTIFACTS is set, then have them match.
  local junit_report_dir=${JUNIT_REPORT_DIR:-${ARTIFACTS:-}}

  local go_test_grep_pattern=".*"
  if [[ -n "${junit_report_dir}" ]]; then
    # Show only summary lines by matching lines like "status package/test"
    go_test_grep_pattern="^[^[:space:]]\+[[:space:]]\+[^[:space:]]\+/[^[[:space:]]\+"
  fi

  if [[ -n "${junit_report_dir}" || "${VERBOSE:-}" == "1" ]]; then
    go_test_flags+=("-v" "-json")
  fi

  local cmd=(go test "${go_test_flags[@]}" "$@")

  local junit_filename_prefix
  junit_filename_prefix=$(get_junit_filename_prefix "${junit_report_dir}")

  if ! run env ETCD_VERIFY="${ETCD_VERIFY}" "${cmd[@]}" | tee ${junit_filename_prefix:+"${junit_filename_prefix}.stdout"} | grep --binary-files=text "${go_test_grep_pattern}" ; then
    produce_junit_xmlreport "${junit_filename_prefix}"
    return 2
  fi

  produce_junit_xmlreport "${junit_filename_prefix}"
}

#### Other ####

# tool_exists [tool] [instruction]
# Checks whether given [tool] is installed. In case of failure,
# prints a warning with installation [instruction] and returns !=0 code.
#
# WARNING: This depend on "any" version of the 'binary' that might be tricky
# from hermetic build perspective. For go binaries prefer 'tool_go_run'
function tool_exists {
  local tool="${1}"
  local instruction="${2}"
  if ! command -v "${tool}" >/dev/null; then
    log_warning "Tool: '${tool}' not found on PATH. ${instruction}"
    return 255
  fi
}

# tool_get_bin [tool] - returns absolute path to a tool binary (or returns error).
# This function is only used to run commands that are managed by tools/mod.
function tool_get_bin {
  local tool="$1"
  local pkg_part="$1"
  if [[ "$tool" == *"@"* ]]; then
    pkg_part=$(echo "${tool}" | cut -d'@' -f1)
    # shellcheck disable=SC2086
    run go install ${GOBINARGS:-} "${tool}" || return 2
  else
    # shellcheck disable=SC2086
    run_for_module ./tools/mod run go install ${GOBINARGS:-} "${tool}" || return 2
  fi

  # remove the version suffix, such as removing "/v3" from "go.etcd.io/etcd/v3".
  local cmd_base_name
  cmd_base_name=$(basename "${pkg_part}")
  if [[ ${cmd_base_name} =~ ^v[0-9]*$ ]]; then
    pkg_part=$(dirname "${pkg_part}")
  fi

  run_for_module ./tools/mod go list -f '{{.Target}}' "${pkg_part}"
}

# tool_pkg_dir [pkg] - returns absolute path to a directory that stores given pkg.
# The pkg versions must be defined in ./tools/mod directory.
function tool_pkg_dir {
  run_for_module ./tools/mod run go list -f '{{.Dir}}' "${1}"
}

# tool_get_bin [tool]
function run_go_tool {
  local cmdbin
  if ! cmdbin=$(GOARCH="" GOOS="" tool_get_bin "${1}"); then
    log_warning "Failed to install tool '${1}'"
    return 2
  fi
  shift 1
  GOARCH="" run "${cmdbin}" "$@" || return 2
}

# assert_no_git_modifications fails if there are any uncommitted changes.
function assert_no_git_modifications {
  log_callout "Making sure everything is committed."
  if ! git diff --cached --exit-code; then
    log_error "Found staged by uncommitted changes. Do commit/stash your changes first."
    return 2
  fi
  if ! git diff  --exit-code; then
    log_error "Found unstaged and uncommitted changes. Do commit/stash your changes first."
    return 2
  fi
}

# makes sure that the current branch is in sync with the origin branch:
#  - no uncommitted nor unstaged changes
#  - no differencing commits in relation to the origin/$branch
function git_assert_branch_in_sync {
  local branch
  # TODO: When git 2.22 popular, change to:
  # branch=$(git branch --show-current)
  branch=$(run git rev-parse --abbrev-ref HEAD)
  log_callout "Verify the current branch '${branch}' is clean"
  if [[ $(run git status --porcelain --untracked-files=no) ]]; then
    log_error "The workspace in '$(pwd)' for branch: ${branch} has uncommitted changes"
    log_error "Consider cleaning up / renaming this directory or (cd $(pwd) && git reset --hard)"
    return 2
  fi
  log_callout "Verify the current branch '${branch}' is in sync with the 'origin/${branch}'"
  if [ -n "${branch}" ]; then
    ref_local=$(run git rev-parse "${branch}")
    ref_origin=$(run git rev-parse "origin/${branch}")
    if [ "${ref_local}" != "${ref_origin}" ]; then
      log_error "In workspace '$(pwd)' the branch: ${branch} diverges from the origin."
      log_error "Consider cleaning up / renaming this directory or (cd $(pwd) && git reset --hard origin/${branch})"
      return 2
    fi
  else
    log_warning "Cannot verify consistency with the origin, as git is on detached branch."
  fi
}

# The version present in the .go-verion is the default version that test and build scripts will use.
# However, it is possible to control the version that should be used with the help of env vars:
# - FORCE_HOST_GO: if set to a non-empty value, use the version of go installed in system's $PATH.
# - GO_VERSION: desired version of go to be used, might differ from what is present in .go-version.
#               If empty, the value defaults to the version in .go-version.
function determine_go_version {
  # Borrowing from how Kubernetes does this:
  #  https://github.com/kubernetes/kubernetes/blob/17854f0e0a153b06f9d0db096e2cd8ab2fa89c11/hack/lib/golang.sh#L510-L520
  #
  # default GO_VERSION to content of .go-version
  GO_VERSION="${GO_VERSION:-"$(cat "${ETCD_ROOT_DIR}/.go-version")"}"
  if [ "${GOTOOLCHAIN:-auto}" != 'auto' ]; then
    # no-op, just respect GOTOOLCHAIN
    :
  elif [ -n "${FORCE_HOST_GO:-}" ]; then
    export GOTOOLCHAIN='local'
  else
    GOTOOLCHAIN="go${GO_VERSION}"
    export GOTOOLCHAIN
  fi
}

determine_go_version
