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
# Run all etcd tests
# ./scripts/test.sh
# ./scripts/test.sh -v
#
#
# Run specified test pass
#
# $ PASSES=unit ./scripts/test.sh
# $ PASSES=integration ./scripts/test.sh
#
#
# Run tests for one package
# Each pass has different default timeout, if you just run tests in one package or 1 test case then you can set TIMEOUT
# flag for different expectation
#
# $ PASSES=unit PKG=./wal TIMEOUT=1m ./scripts/test.sh
# $ PASSES=integration PKG=./clientv3 TIMEOUT=1m ./scripts/test.sh
#
# Run specified unit tests in one package
# To run all the tests with prefix of "TestNew", set "TESTCASE=TestNew ";
# to run only "TestNew", set "TESTCASE="\bTestNew\b""
#
# $ PASSES=unit PKG=./wal TESTCASE=TestNew TIMEOUT=1m ./scripts/test.sh
# $ PASSES=unit PKG=./wal TESTCASE="\bTestNew\b" TIMEOUT=1m ./scripts/test.sh
# $ PASSES=integration PKG=./client/integration TESTCASE="\bTestV2NoRetryEOF\b" TIMEOUT=1m ./scripts/test.sh
#
# KEEP_GOING_SUITE must be set to true to keep going with the next suite execution, passed to PASSES variable when there is a failure
# in a particular suite.
# KEEP_GOING_MODULE must be set to true to keep going with execution when there is failure in any module.
#
# Run code coverage
# COVERDIR must either be a absolute path or a relative path to the etcd root
# $ COVERDIR=coverage PASSES="build cov" ./scripts/test.sh
# $ go tool cover -html ./coverage/cover.out
set -e

# Consider command as failed when any component of the pipe fails:
# https://stackoverflow.com/questions/1221833/pipe-output-and-capture-exit-status-in-bash
set -o pipefail
set -o nounset

# The test script is not supposed to make any changes to the files
# e.g. add/update missing dependencies. Such divergences should be 
# detected and trigger a failure that needs explicit developer's action.
export GOFLAGS=-mod=readonly
export ETCD_VERIFY=all

source ./scripts/test_lib.sh
source ./scripts/build_lib.sh

GOPATH_BIN=$(go env GOPATH)/bin
export PATH="${GOPATH_BIN}:${PATH}"

OUTPUT_FILE=${OUTPUT_FILE:-""}

if [ -n "${OUTPUT_FILE}" ]; then
  log_callout "Dumping output to: ${OUTPUT_FILE}"
  exec > >(tee -a "${OUTPUT_FILE}") 2>&1
fi

PASSES=${PASSES:-"bom dep build unit"}
KEEP_GOING_SUITE=${KEEP_GOING_SUITE:-false}
PKG=${PKG:-}
SHELLCHECK_VERSION=${SHELLCHECK_VERSION:-"v0.10.0"}
MARKDOWN_MARKER_VERSION=${MARKDOWN_MARKER_VERSION:="v0.10.0"}

if [ -z "${GOARCH:-}" ]; then
  GOARCH=$(go env GOARCH);
fi

if [ -z "${OS:-}" ]; then
  OS=$(uname -s | tr '[:upper:]' '[:lower:]')
fi

if [ -z "${ARCH:-}" ]; then
  ARCH=$(uname -m)

  if [ "$ARCH" = "arm64" ]; then
    ARCH="aarch64"
  fi
fi

# determine whether target supports race detection
if [ -z "${RACE:-}" ] ; then
  if [ "$GOARCH" == "amd64" ] || [ "$GOARCH" == "arm64" ]; then
    RACE="--race"
  else
    RACE="--race=false"
  fi
else
  RACE="--race=${RACE:-true}"
fi

# This options make sense for cases where SUT (System Under Test) is compiled by test.
COMMON_TEST_FLAGS=("${RACE}")
if [[ -n "${CPU:-}" ]]; then
  COMMON_TEST_FLAGS+=("--cpu=${CPU}")
fi 

log_callout "Running with ${COMMON_TEST_FLAGS[*]}"

RUN_ARG=()
if [ -n "${TESTCASE:-}" ]; then
  RUN_ARG=("-run=${TESTCASE}")
fi

function build_pass {
  log_callout "Building etcd"
  run_for_workspace_modules run go build "${@}" ./... || return 2
  GO_BUILD_FLAGS="-v" etcd_build "${@}"
  GO_BUILD_FLAGS="-v" tools_build "${@}"
}

################# REGULAR TESTS ################################################

function unit_pass {
  run_for_all_workspace_modules \
    run_go_tests -short \
                 -failfast \
                 -timeout="${TIMEOUT:-3m}" \
                 "${COMMON_TEST_FLAGS[@]}" \
                 "${RUN_ARG[@]}" \
                 "$@"
}

function integration_extra {
  if [ -z "${PKG}" ] ; then
    run_go_tests_expanding_packages ./tests/integration/v2store/... \
                                    -timeout="${TIMEOUT:-5m}" \
                                    "${COMMON_TEST_FLAGS[@]}" \
                                    "${RUN_ARG[@]}" \
                                    "$@"
  else
    log_warning "integration_extra ignored when PKG is specified"
  fi
}

function integration_pass {
  run_go_tests ./tests/integration/... \
               -p=2 \
               -failfast \
               -timeout="${TIMEOUT:-15m}" \
               "${COMMON_TEST_FLAGS[@]}" \
               "${RUN_ARG[@]}" \
               "$@" || return 2

  run_go_tests ./tests/common/... \
               -p=2 \
               -failfast \
               -tags=integration \
               -timeout="${TIMEOUT:-15m}" \
               "${COMMON_TEST_FLAGS[@]}" \
               "${RUN_ARG[@]}" \
               "$@" || return 2

  integration_extra "$@"
}

function e2e_pass {
  # e2e tests are running pre-build binary. Settings like --race,-cover,-cpu do not have any impact.
  run_go_tests_expanding_packages ./tests/e2e/... \
                                    -timeout="${TIMEOUT:-30m}" \
                                    "${RUN_ARG[@]}" \
                                    "$@" || return 2

  run_go_tests_expanding_packages ./tests/common/... \
                                    -tags=e2e \
                                    -timeout="${TIMEOUT:-30m}" \
                                    "${RUN_ARG[@]}" \
                                    "$@"
}

function robustness_pass {
  # e2e tests are running pre-build binary. Settings like --race,-cover,-cpu does not have any impact.
  run_go_tests ./tests/robustness \
                 -timeout="${TIMEOUT:-30m}" \
                 "${RUN_ARG[@]}" \
                 "$@"
}

function integration_e2e_pass {
  run_pass "integration" "${@}" || return 2
  run_pass "e2e" "${@}"
}

# generic_checker [cmd...]
# executes given command in the current module, and clearly fails if it
# failed or returned output.
function generic_checker {
  local cmd=("$@")
  if ! output=$("${cmd[@]}"); then
    echo "${output}"
    log_error -e "FAIL: '${cmd[*]}' checking failed (!=0 return code)"
    return 255
  fi
  if [ -n "${output}" ]; then
    echo "${output}"
    log_error -e "FAIL: '${cmd[*]}' checking failed (printed output)"
    return 255
  fi
}

function grpcproxy_pass {
  run_pass "grpcproxy_integration" "${@}" || return 2
  run_pass "grpcproxy_e2e" "${@}"
}

function grpcproxy_integration_pass {
  run_go_tests_expanding_packages ./tests/integration/... \
               -tags=cluster_proxy \
               -timeout="${TIMEOUT:-30m}" \
               "${COMMON_TEST_FLAGS[@]}" \
               "${RUN_ARG[@]}" \
               "$@"
}

function grpcproxy_e2e_pass {
  run_go_tests_expanding_packages ./tests/e2e/... \
               -tags=cluster_proxy \
               -timeout="${TIMEOUT:-30m}" \
               "${COMMON_TEST_FLAGS[@]}" \
               "${RUN_ARG[@]}" \
               "$@"
}

################# COVERAGE #####################################################

# split_dir [dir] [num]
function split_dir {
  local d="${1}"
  local num="${2}"
  local i=0
  for f in "${d}/"*; do
    local g=$(( i % num ))
    mkdir -p "${d}_${g}"
    mv "${f}" "${d}_${g}/"
    (( i++ ))
  done
}

function split_dir_pass {
  split_dir ./covdir/integration 4
}


# merge_cov_files [coverdir] [outfile]
# merges all coverprofile files into a single file in the given directory.
function merge_cov_files {
  local coverdir="${1}"
  local cover_out_file="${2}"
  log_callout "Merging coverage results in: ${coverdir}"
  # gocovmerge requires not-empty test to start with:
  echo "mode: set" > "${cover_out_file}"

  local i=0
  local count
  count=$(find "${coverdir}"/*.coverprofile | wc -l)
  for f in "${coverdir}"/*.coverprofile; do
    # print once per 20 files
    if ! (( "${i}" % 20 )); then
      log_callout "${i} of ${count}: Merging file: ${f}"
    fi
    run_go_tool "github.com/alexfalkowski/gocovmerge" "${f}" "${cover_out_file}"  > "${coverdir}/cover.tmp" 2>/dev/null
    if [ -s "${coverdir}"/cover.tmp ]; then
      mv "${coverdir}/cover.tmp" "${cover_out_file}"
    fi
    (( i++ ))
  done
}

# https://docs.codecov.com/docs/unexpected-coverage-changes#reasons-for-indirect-changes
function cov_pass {
  # shellcheck disable=SC2153
  if [ -z "${COVERDIR:-}" ]; then
    log_error "COVERDIR undeclared"
    return 255
  fi

  local coverdir
  coverdir=$(readlink -f "${COVERDIR}")
  mkdir -p "${coverdir}"
  find "${coverdir}" -name '*.coverprofile' -delete

  local modules=()
  load_workspace_relative_modules modules
  local covpkgs=()
  for module in "${modules[@]}"; do
    if [[ ! "${module}" =~ ^./tests && ! "${module}" =~ ^./...$ ]]; then
      covpkgs+=("${module}")
    fi
  done

  local coverpkg_comma
  coverpkg_comma=$(echo "${covpkgs[@]}" | xargs | tr ' ' ',')
  local gocov_build_flags=("-covermode=set" "-coverpkg=$coverpkg_comma")

  local failed=()

  log_callout "[$(date)] Collecting coverage from unit tests ..."
  run_go_tests "${modules[@]}" -short -timeout=30m "${gocov_build_flags[@]}" -coverprofile="${coverdir}/unit.coverprofile" "$@" || failed+=("unit")

  log_callout "[$(date)] Collecting coverage from integration tests ..."
  run_go_tests "./tests/integration/..." -timeout=30m "${gocov_build_flags[@]}" -coverprofile="${coverdir}/integration.coverprofile" "$@" || failed+=("integration")
  # integration-store-v2
  run_go_tests "./tests/integration/v2store/..." -timeout=5m "${gocov_build_flags[@]}" -coverprofile="${coverdir}/integration_v2.coverprofile" "$@" || failed+=("integration_v2")
  # integration_cluster_proxy
  run_go_tests "./tests/integration/..." -tags cluster_proxy -timeout=30m "${gocov_build_flags[@]}" -coverprofile="${coverdir}/integration_cluster_proxy.coverprofile" "$@" || failed+=("integration_cluster_proxy")
  run_go_tests "./tests/common/" -tags integration -timeout=30m "${gocov_build_flags[@]}" -coverprofile="${coverdir}/common.coverprofile" "$@" || failed+=("common")

  local cover_out_file="${coverdir}/all.coverprofile"
  merge_cov_files "${coverdir}" "${cover_out_file}"

  # strip out generated files (using GNU-style sed)
  sed --in-place -E "/[.]pb[.](gw[.])?go/d" "${cover_out_file}" || true

  sed --in-place -E "s|go.etcd.io/etcd/(.*)/v3|\1|g" "${cover_out_file}" || true

  # held failures to generate the full coverage file, now fail
  if [ "${#failed[@]}" -gt 0 ]; then
    for f in "${failed[@]}"; do
      log_error "--- FAIL:" "$f"
    done
    log_warning "Despite failures, you can see partial report:"
    log_warning "  go tool cover -html ${cover_out_file}"
    return 255
  fi

  log_success "done :) [see report: go tool cover -html ${cover_out_file}]"
}

######### Code formatting checkers #############################################

function shellcheck_pass {
  SHELLCHECK=shellcheck
  if ! tool_exists "shellcheck" "https://github.com/koalaman/shellcheck#installing"; then
    log_callout "Installing shellcheck $SHELLCHECK_VERSION"
    wget -qO- "https://github.com/koalaman/shellcheck/releases/download/${SHELLCHECK_VERSION}/shellcheck-${SHELLCHECK_VERSION}.${OS}.${ARCH}.tar.xz" | tar -xJv -C /tmp/ --strip-components=1
    mkdir -p ./bin
    mv /tmp/shellcheck ./bin/
    SHELLCHECK=./bin/shellcheck
  fi
  generic_checker run ${SHELLCHECK} -fgcc scripts/*.sh
}

function shellws_pass {
  log_callout "Ensuring no tab-based indentation in shell scripts"
  local files
  if files=$(find . -name '*.sh' -print0 | xargs -0 grep -E -n $'^\s*\t'); then
    log_error "FAIL: found tab-based indentation in the following bash scripts. Use '  ' (double space):"
    log_error "${files}"
    log_warning "Suggestion: run \"make fix\" to address the issue."
    return 255
  fi
  log_success "SUCCESS: no tabulators found."
}

function markdown_marker_pass {
  local marker="marker"
  # TODO: check other markdown files when marker handles headers with '[]'
  if ! tool_exists "$marker" "https://crates.io/crates/marker"; then
    log_callout "Installing markdown marker $MARKDOWN_MARKER_VERSION"
    MARKER_OS=$OS
    if [ "$OS" = "darwin" ]; then
      MARKER_OS="apple-darwin"
    elif [ "$OS" = "linux" ]; then
      MARKER_OS="unknown-linux-musl"
    fi

    wget -qO- "https://github.com/crawford/marker/releases/download/${MARKDOWN_MARKER_VERSION}/marker-${MARKDOWN_MARKER_VERSION}-${ARCH}-${MARKER_OS}.tar.gz" | tar -xzv -C /tmp/ --strip-components=1 >/dev/null
    mkdir -p ./bin
    mv /tmp/marker ./bin/
    marker=./bin/marker
  fi

  generic_checker run "${marker}" --skip-http --allow-absolute-paths --root "${ETCD_ROOT_DIR}" -e ./CHANGELOG -e ./etcdctl -e etcdutl -e ./tools 2>&1
}

function govuln_pass {
  local tool_bin
  tool_bin=$(tool_get_bin golang.org/x/vuln/cmd/govulncheck)
  run_for_all_workspace_modules run govulncheck -show verbose
}

function lint_pass {
  run_for_all_workspace_modules golangci-lint run --config "${ETCD_ROOT_DIR}/tools/.golangci.yaml"
}

function lint_fix_pass {
  run_for_all_workspace_modules golangci-lint run --config "${ETCD_ROOT_DIR}/tools/.golangci.yaml" --fix
}

function bom_pass {
  log_callout "Checking bill of materials..."
  local _bom_modules=()
  load_workspace_relative_modules_for_bom _bom_modules

  # Internally license-bill-of-materials tends to modify go.sum
  run cp go.sum go.sum.tmp || return 2
  run cp go.mod go.mod.tmp || return 2

  # Intentionally run the command once first, so it fetches dependencies. The exit code on the first
  # run in a just cloned repository is always dirty.
  GOOS=linux run_go_tool github.com/appscodelabs/license-bill-of-materials \
    --override-file ./bill-of-materials.override.json "${_bom_modules[@]}" &>/dev/null

  # BOM file should be generated for linux. Otherwise running this command on other operating systems such as OSX
  # results in certain dependencies being excluded from the BOM file, such as procfs.
  # For more info, https://github.com/etcd-io/etcd/issues/19665
  output=$(GOOS=linux run_go_tool github.com/appscodelabs/license-bill-of-materials \
    --override-file ./bill-of-materials.override.json \
    "${_bom_modules[@]}")
  local code="$?"

  run cp go.sum.tmp go.sum || return 2
  run cp go.mod.tmp go.mod || return 2

  if [ "${code}" -ne 0 ] ; then
    log_error -e "license-bill-of-materials (code: ${code}) failed with:\\n${output}"
    return 255
  else
    echo "${output}" > "bom-now.json.tmp"
  fi
  if ! diff ./bill-of-materials.json bom-now.json.tmp; then
    log_error "modularized licenses do not match given bill of materials"
    return 255
  fi
  rm bom-now.json.tmp
}

function module_gomodguard {
  if [ ! -f .gomodguard.yaml ]; then
    # Nothing to validate, return.
    return
  fi

  local tool_bin="$1"
  run "${tool_bin}"
}

function gomodguard_pass {
  local tool_bin
  tool_bin=$(tool_get_bin github.com/ryancurrah/gomodguard/cmd/gomodguard)
  run_for_workspace_modules module_gomodguard "${tool_bin}"
}

######## VARIOUS CHECKERS ######################################################

function dump_module_deps() {
  local json_mod
  json_mod=$(run go mod edit -json)

  local module
  if ! module=$(echo "${json_mod}" | jq -r .Module.Path); then
    return 255
  fi

  local require
  require=$(echo "${json_mod}" | jq -r '.Require')
  if [ "$require" == "null" ]; then
    return 0
  fi

  echo "$require" | jq -r '.[] | .Path+","+.Version+","+if .Indirect then " (indirect)" else "" end+",'"${module}"'"'
}

# Checks whether dependencies are consistent across modules
function dep_pass {
  local all_dependencies
  all_dependencies=$(run_for_workspace_modules dump_module_deps | sort) || return 2

  local duplicates
  duplicates=$(echo "${all_dependencies}" | cut -d ',' -f 1,2 | sort | uniq | cut -d ',' -f 1 | sort | uniq -d) || return 2

  if [[ -n "${duplicates}" ]]; then
    for dup in ${duplicates}; do
      log_error "FAIL: inconsistent versions for dependency: ${dup}"
      echo "${all_dependencies}" | grep "${dup}," | sed 's|\([^,]*\),\([^,]*\),\([^,]*\),\([^,]*\)|  - \1@\2\3 from: \4|g'
    done

    log_error "FAIL: inconsistent dependencies"
    return 2
  fi

  log_success "SUCCESS: dependencies are consistent across modules"
}

function release_pass {
  rm -f ./bin/etcd-last-release

  # Work out the previous release based on the version reported by etcd binary
  binary_version=$(./bin/etcd --version | grep --only-matching --perl-regexp '(?<=etcd Version: )v?\d+\.\d+\.\d+' | sed 's/^v//')
  binary_major=$(echo "${binary_version}" | cut -d '.' -f 1)
  binary_minor=$(echo "${binary_version}" | cut -d '.' -f 2)
  previous_minor=$((binary_minor - 1))

  # Handle the edge case where we go to a new major version
  # When this happens we obtain latest minor release of previous major
  if [ "${binary_minor}" -eq 0 ]; then
    binary_major=$((binary_major - 1))
    previous_minor=$(git ls-remote --tags https://github.com/etcd-io/etcd.git \
    | grep --only-matching --perl-regexp "(?<=v)${binary_major}.\d.[\d]+?(?=[\^])" \
    | sort --numeric-sort --key 1.3 | tail -1 | cut -d '.' -f 2)
  fi
  
  # This gets a list of all remote tags for the release branch in regex
  # Sort key is used to sort numerically by patch version
  # Latest version is then stored for use below
  UPGRADE_VER=$(git ls-remote --tags https://github.com/etcd-io/etcd.git \
    | grep --only-matching --perl-regexp "(?<=v)${binary_major}.${previous_minor}.[\d]+(?:-[\w.]+)?(?=[\^])" \
    | sort --numeric-sort --key 1.5 | tail -1 | sed 's/^/v/')
  log_callout "Found previous minor version (v${binary_major}.${previous_minor}) latest release: ${UPGRADE_VER}."

  if [ -n "${MANUAL_VER:-}" ]; then
    # in case, we need to test against different version
    UPGRADE_VER=$MANUAL_VER
  fi
  if [[ -z ${UPGRADE_VER} ]]; then
    UPGRADE_VER="v3.5.0"
    log_warning "fallback to" ${UPGRADE_VER}
  fi

  local file
  if [[ "$(uname -s)" == 'Darwin' ]]; then
    file="etcd-$UPGRADE_VER-darwin-$GOARCH.zip"
  else
    file="etcd-$UPGRADE_VER-linux-$GOARCH.tar.gz"
  fi

  log_callout "Downloading $file"

  set +e
  curl --fail -L "https://github.com/etcd-io/etcd/releases/download/$UPGRADE_VER/$file" -o "/tmp/$file"
  local result=$?
  set -e
  case $result in
    0)  ;;
    *)  log_error "--- FAIL:" ${result}
      return $result
      ;;
  esac

  tar xzvf "/tmp/$file" -C /tmp/ --strip-components=1 --no-same-owner
  mkdir -p ./bin
  mv /tmp/etcd ./bin/etcd-last-release
}

function release_tests_pass {
  if [ -z "${VERSION:-}" ]; then
    VERSION=$(go list -m go.etcd.io/etcd/api/v3 2>/dev/null | \
     awk '{split(substr($2,2), a, "."); print a[1]"."a[2]".99"}')
  fi

  if [ -n "${CI:-}" ]; then
    git config user.email "prow@etcd.io"
    git config user.name "Prow"

    gpg --batch --gen-key <<EOF
%no-protection
Key-Type: 1
Key-Length: 2048
Subkey-Type: 1
Subkey-Length: 2048
Name-Real: Prow
Name-Email: prow@etcd.io
Expire-Date: 0
EOF

    git remote add origin https://github.com/etcd-io/etcd.git
  fi

  DRY_RUN=true run "${ETCD_ROOT_DIR}/scripts/release.sh" --no-upload --no-docker-push --no-gh-release --in-place "${VERSION}"
}

function mod_tidy_pass {
  run_for_workspace_modules run go mod tidy -diff
}

function proto_annotations_pass {
  "${ETCD_ROOT_DIR}/scripts/verify_proto_annotations.sh"
}

function genproto_pass {
  "${ETCD_ROOT_DIR}/scripts/verify_genproto.sh"
}

function go_workspace_pass {
  log_callout "Ensuring go workspace is in sync."

  run go mod download
  if [ -n "$(git status --porcelain go.work.sum)" ]; then
    log_error "Go workspace not in sync."
    log_warning "Suggestion: run \"make fix\" to address the issue."
    return 255
  fi
}

########### MAIN ###############################################################

function run_pass {
  local pass="${1}"
  shift 1
  log_callout -e "\\n'${pass}' started at $(date)"
  if "${pass}_pass" "$@" ; then
    log_success "'${pass}' PASSED and completed at $(date)"
    return 0
  else
    log_error "FAIL: '${pass}' FAILED at $(date)"
    if [ "$KEEP_GOING_SUITE" = true ]; then
      return 2
    else
      exit 255
    fi
  fi
}

log_callout "Starting at: $(date)"
fail_flag=false
for pass in $PASSES; do
  if run_pass "${pass}" "$@"; then
    continue
  else
    fail_flag=true
  fi
done
if [ "$fail_flag" = true ]; then
  log_error "There was FAILURE in the test suites ran. Look above log detail"
  exit 255
fi

log_success "SUCCESS"
