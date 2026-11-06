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

# http://redsymbol.net/articles/unofficial-bash-strict-mode/
set -euo pipefail
IFS=$'\n\t'

source ./scripts/test_lib.sh
source ./scripts/build_lib.sh

# Can't run darwin binaries in linux containers.
if [[ $(go env GOOS) == "darwin" ]]; then
  log_error "Error: Please use a Linux machine to test release images builds."
  exit 1
fi

# Can't proceed without Docker.
if ! command -v docker >/dev/null; then
  log_error "Error: Cannot find docker. Please follow the installation instructions at: https://docs.docker.com/get-docker/"
  exit 1
fi

# Start a container with the given Docker image.
function start_container {
  local container_name=$1
  local image=$2

    # run docker in the background
    docker run --detach --rm --name "${container_name}" "${image}"

    # wait for etcd daemon to bootstrap
    local attempts=0
    while ! docker exec "${container_name}" /usr/local/bin/etcdctl endpoint health --command-timeout=1s; do
      sleep 1
      attempts=$((attempts + 1))
      if [ "${attempts}" -gt 10 ]; then
        log_error "Error: etcd daemon failed to start."
        exit 1
      fi
    done
  }

# Run a version check for the given Docker image.
function run_version_check {
  local output
  local found_version
  local image=$1
  shift
  local expected_version=$1
  shift

  output=$(docker run --rm "${image}" "${@}")
  found_version=$(echo "${output}" | head -1 | rev | cut -d" " -f 1 | rev)
  if [[ "${found_version}" != "${expected_version}" ]]; then
    log_error "Error: Invalid Version."
    log_error "Got ${found_version}, expected ${expected_version}."
    log_error "Output: ${output}."
    exit 1
  fi
}

# Put a key-value pair in the etcd container and check if it can be retrieved,
# and has the expected value.
function put_get_check {
  local container_name=$1
  local key="foo"
  local value="bar"
  local result

  result=$(docker exec "${container_name}" /usr/local/bin/etcdctl put "${key}" "${value}")
  if [ "${result}" != "OK" ]; then
    log_error "Error: Storing key failed. Result: ${result}."
    exit 1
  fi

  result=$(docker exec "${container_name}" /usr/local/bin/etcdctl get "${key}" --print-value-only)
  if [ "${result}" != "${value}" ]; then
    log_error "Error: Problem with getting key. Got: ${result}, expected: ${value}."
    exit 1
  fi
}

# Verify that the image has a consistent manifest type (multi-platform list/index).
function verify_manifest_type {
  local image=$1
  local media_type

  media_type=$(docker manifest inspect "${image}" 2>/dev/null | grep -o '"mediaType"[[:space:]]*:[[:space:]]*"[^"]*"' | head -1 | sed 's/.*"mediaType"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/')

  case "${media_type}" in
    application/vnd.docker.distribution.manifest.list.v2+json|application/vnd.oci.image.index.v1+json)
      log_success "Manifest type for ${image}: ${media_type}"
      ;;
    *)
      log_error "Error: Invalid manifest type for ${image}: ${media_type}"
      log_error "Expected application/vnd.docker.distribution.manifest.list.v2+json or application/vnd.oci.image.index.v1+json"
      exit 1
      ;;
  esac
}

function main {
  local version="$1"
  local repository=${REPOSITORY:-"gcr.io/etcd-development/etcd"}
  local tag="v${version}"
  local image="${TEST_IMAGE:-"${repository}:${tag}"}"
  local container_name="test_etcd"

  if [[ "$(docker images -q "${image}" 2> /dev/null)" == "" ]]; then
    log_error "Error: ${image} not present locally."
    exit 1
  fi

  log_callout "Verifying manifest type."
  verify_manifest_type "${image}"

  log_callout "Running version check."
  run_version_check "${image}" "${version}" "/usr/local/bin/etcd" "--version"
  run_version_check "${image}" "${version}" "/usr/local/bin/etcdctl" "version"
  run_version_check "${image}" "${version}" "/usr/local/bin/etcdutl" "version"
  log_success "Successfully ran version check."

  log_callout "Running sanity check in Docker image."
  start_container "${container_name}" "${image}"
  # stop container
  trap 'docker stop '"${container_name}" EXIT
  put_get_check "${container_name}"
  log_success "Successfully tested etcd local image ${tag}."
}

if [ -z "$VERSION" ]; then
  log_error "Error: VERSION not supplied."
  exit 1
fi

main "${VERSION}"
