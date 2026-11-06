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

if [ "$#" -lt 1 ]; then
  echo "Usage: $0 VERSION [NO_DOCKER_PUSH]" >&2
  exit 1
fi

VERSION=${1}
if [ -z "$VERSION" ]; then
  echo "Usage: ${0} VERSION [NO_DOCKER_PUSH]" >&2
  exit 1
fi
NO_DOCKER_PUSH=${2:-}

PLATFORMS=${PLATFORMS:-"linux/amd64,linux/arm64,linux/ppc64le,linux/s390x"}

BUILDDIR=${BUILDDIR:-release}
mkdir -p "${BUILDDIR}"

for platform in $(echo "${PLATFORMS}" | tr ',' ' '); do
  RELEASE="etcd-${VERSION}-linux-${platform#linux/}"
  if [ ! -d "${BUILDDIR}/${RELEASE}" ]; then
    TARFILE="${RELEASE}.tar.gz"
    TARURL="https://github.com/etcd-io/etcd/releases/download/${VERSION}/${TARFILE}"
    if ! curl -f -L -o "${BUILDDIR}/${TARFILE}" "${TARURL}" ; then
      echo "Failed to download ${TARURL}."
      exit 1
    fi
    tar -C "${BUILDDIR}" -zvxf "${BUILDDIR}/${TARFILE}"
  fi
done

tag_args=()
if [ -z "${OCI_REGISTRY:-}" ]; then
  tag_args+=("-t" "gcr.io/etcd-development/etcd:${VERSION}")
  tag_args+=("-t" "quay.io/coreos/etcd:${VERSION}")
else
  tag_args+=("-t" "${OCI_REGISTRY}/${OCI_PATH:-etcd}:${VERSION}")
fi

if [ "${NO_DOCKER_PUSH}" == 1 ]; then
  # Build a single architecture to be tested later.
  docker build --build-arg="VERSION=${VERSION}" \
    --build-arg="BUILDDIR=${BUILDDIR}" \
    --build-arg="TARGETARCH=$(go env GOARCH)" \
    "${tag_args[@]}" \
    .
else
  docker run --privileged --rm tonistiigi/binfmt --install all
  docker buildx rm --force multiarch-multiplatform-builder || true
  docker buildx create \
    --name multiarch-multiplatform-builder \
    --driver docker-container \
    --bootstrap --use
  docker buildx build --build-arg="VERSION=${VERSION}" \
    --build-arg="BUILDDIR=${BUILDDIR}" \
    --platform="${PLATFORMS}" \
    --sbom=false --provenance=false \
    --push \
    "${tag_args[@]}" \
    .
  docker buildx stop multiarch-multiplatform-builder
fi
