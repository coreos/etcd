#!/usr/bin/env bash

set -euo pipefail

# Default image registry to use.
REGISTRY=${REGISTRY:-gcr.io/etcd-development/etcd}

# Default severity levels to report.
SEVERITY=${SEVERITY:-HIGH,CRITICAL}

source ./scripts/test_lib.sh

if [ "$#" -lt 1 ]; then
  log_error "Error: missing version to check."
  log_error "Usage: ${0} <VERSION TO CHECK>"
  log_error "Example: ${0} 3.7"
  exit 1
fi

if ! command -v "${ETCD_ROOT_DIR}/bin/trivy" >/dev/null; then
  log_error "Error: Cannot find trivy. Please run make install-trivy."
  exit 1
fi

function main {
  local version=${1#v}

  if [ "$(tr -dc '.' <<<"$version" | wc -c)" -eq 1 ]; then
    # Resolve the latest version for the minor
    version=$(git ls-remote --tags https://github.com/etcd-io/etcd.git |
      grep --only-matching --perl-regexp "(?<=v)${version}.[\d]+(?:-[\w.]+)?(?=[\^])" |
      sort --numeric-sort --key 1.5 | tail -1)
  fi

  trivy image --severity "${SEVERITY}" "${REGISTRY}:v${version}"
}

main "$1"
