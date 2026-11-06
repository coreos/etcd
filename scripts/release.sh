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

set -o errexit
set -o nounset
set -o pipefail

source ./scripts/test_lib.sh
source ./scripts/release_mod.sh

DRY_RUN=${DRY_RUN:-true}

# Following preparation steps help with the release process: 

# If you use password-protected gpg key, make sure the password is managed
# by agent: 
#
# % gpg-connect-agent reloadagent /bye
# % gpg -s --default-key [git-email]@google.com -o /dev/null -s /dev/null
#
# Refresh your google credentials: 
#  % gcloud auth login
# or
#  % gcloud auth activate-service-account --key-file=gcp-key-etcd-development.json
#
# Make sure gcloud-docker plugin is configured: 
#  % gcloud auth configure-docker


help() {
  echo "$(basename "$0") [version]"
  echo "Release etcd using the same approach as the etcd-release-runbook (https://goo.gl/Gxwysq)"
  echo ""
  echo "WARNING: This does not perform the 'Add API capabilities', 'Performance testing' "
  echo "         or 'Documentation' steps. These steps must be performed manually BEFORE running this tool."
  echo ""
  echo "WARNING: This script does not send announcement emails. This step must be performed manually AFTER running this tool."
  echo ""
  echo "NOTE: Docker images are built by GitHub Actions (release-docker.yaml) after the release"
  echo "      is published. Use --local-docker to build Docker images locally instead."
  echo ""
  echo "  args:"
  echo "    version: version of etcd to release, e.g. 'v3.2.18'"
  echo "  flags:"
  echo "    --in-place: build binaries using current branch."
  echo "    --local-docker: build and push Docker images locally (not recommended)."
  echo "    --no-gh-release: skip creating the GitHub release using gh."
  echo "    --no-upload: skip gs://etcd binary artifact uploads."
  echo ""
  echo "One can perform a (dry-run) test release from any (uncommitted) branch using:"
  echo "  DRY_RUN=true REPOSITORY=\`pwd\` BRANCH='local-branch-name' ./scripts/release 3.5.0-foobar.2"
}

main() {
  # Allow to receive the version with the "v" prefix, i.e. v3.6.0.
  VERSION=${1#v}
  if [[ ! "${VERSION}" =~ ^[0-9]+.[0-9]+.[0-9]+ ]]; then
    log_error "Expected 'version' param of the form '<major-version>.<minor-version>.<patch-version>' but got '${VERSION}'"
    exit 1
  fi
  RELEASE_VERSION="v${VERSION}"
  MINOR_VERSION=$(echo "${VERSION}" | cut -d. -f 1-2)

  if [ "${IN_PLACE}" == 1 ]; then
      # Trigger release in current branch
      REPOSITORY=$(pwd)
      BRANCH=$(git rev-parse --abbrev-ref HEAD)
  else
      REPOSITORY=${REPOSITORY:-"git@github.com:etcd-io/etcd.git"}
      BRANCH=${BRANCH:-"release-${MINOR_VERSION}"}
  fi

  log_warning "DRY_RUN=${DRY_RUN}"
  log_callout "RELEASE_VERSION=${RELEASE_VERSION}"
  log_callout "MINOR_VERSION=${MINOR_VERSION}"
  log_callout "BRANCH=${BRANCH}"
  log_callout "REPOSITORY=${REPOSITORY}"
  log_callout ""
  
  # Required to enable 'docker manifest ...'
  export DOCKER_CLI_EXPERIMENTAL=enabled

  if ! command -v docker >/dev/null; then
    log_error "cannot find docker"
    exit 1
  fi

  # Expected umask for etcd release artifacts
  umask 022

  # Set up release directory.
  local reldir="/tmp/etcd-release-${VERSION}"
  log_callout "Preparing temporary directory: ${reldir}"
  if [ "${IN_PLACE}" == 0 ]; then
    if [ ! -d "${reldir}/etcd" ]; then
      mkdir -p "${reldir}"
      cd "${reldir}"
      run git clone "${REPOSITORY}" --branch "${BRANCH}" --depth 1
    fi
    run cd "${reldir}/etcd" || exit 2
    run git checkout "${BRANCH}" || exit 2
    run git pull origin

    git_assert_branch_in_sync || exit 2
  fi

  # mark local directory as root for test_lib scripts executions
  set_root_dir

  # If a release version tag already exists, use it.
  local remote_tag_exists
  remote_tag_exists=$(run git ls-remote origin "refs/tags/${RELEASE_VERSION}" | grep -c "${RELEASE_VERSION}" || true)

  if [ "${remote_tag_exists}" -gt 0 ]; then
    log_callout "Release version tag exists on remote. Checking out refs/tags/${RELEASE_VERSION}"
    git checkout -q "tags/${RELEASE_VERSION}"
  fi

  # Check go version.
  log_callout "Check go version"
  local go_version current_go_version
  go_version="go$(cat .go-version)"
  current_go_version=$(go version | awk '{ print $3 }')
  if [[ "${current_go_version}" != "${go_version}" ]]; then
    log_error "Current go version is ${current_go_version}, but etcd ${RELEASE_VERSION} requires ${go_version} (see .go-version)."
    exit 1
  fi

  if [ "${NO_GH_RELEASE}" == 1 ]; then
    log_callout "Skipping gh verification, --no-gh-release is set"
  else
    # Check that gh is installed and logged in.
    log_callout "Check gh installation"
    if ! command -v gh >/dev/null; then
      log_error "Cannot find gh. Please follow the installation instructions at https://github.com/cli/cli#installation"
      exit 1
    fi
    if ! gh auth status &>/dev/null; then
      log_error "GitHub authentication failed for gh. Please run gh auth login."
      exit 1
    fi
  fi

  # If the release tag does not already exist remotely, create it.
  log_callout "Create tag if not present"
  if [ "${remote_tag_exists}" -eq 0 ]; then
    # Bump version/version.go to release version.
    local source_version
    source_version=$(grep -E "\s+Version\s*=" api/version/version.go | sed -e "s/.*\"\(.*\)\".*/\1/g")
    if [[ "${source_version}" != "${VERSION}" ]]; then
      source_minor_version=$(echo "${source_version}" | cut -d. -f 1-2 | sed 's/^v//')
      if [[ "${source_minor_version}" != "${MINOR_VERSION}" ]]; then
        log_error "Wrong etcd minor version in api/version/version.go. Expected ${MINOR_VERSION} but got ${source_minor_version}. Aborting."
        exit 1
      fi
      log_callout "Updating modules definitions"
      TARGET_VERSION="v${VERSION}" update_versions_cmd

      log_callout "Updating version from ${source_version} to ${VERSION} in api/version/version.go"
      sed -i "s/${source_version}/${VERSION}/g" api/version/version.go
    fi


    log_callout "Building etcd and checking --version output"
    run ./scripts/build.sh
    local etcd_version
    etcd_version=$(bin/etcd --version | grep "etcd Version" | awk '{ print $3 }')
    if [[ "${etcd_version}" != "${VERSION}" ]]; then
      log_error "Wrong etcd version in version/version.go. Expected ${etcd_version} but got ${VERSION}. Aborting."
      exit 1
    fi

    if [[ -n $(git status -s) ]]; then
      log_callout "Committing mods & api/version/version.go update."
      run git add api/version/version.go
      # shellcheck disable=SC2038,SC2046
      run git add $(find . -name go.mod ! -path './release/*'| xargs)
      run git diff --staged | cat
      run git commit --signoff --message "version: bump up to ${VERSION}"
      run git diff --staged | cat
    fi

    # Push the version change if it's not already been pushed.
    if [ "${DRY_RUN}" != "true" ] && [ "$(git rev-list --count "origin/${BRANCH}..${BRANCH}")" -gt 0 ]; then
      read -p "Push version bump up to ${VERSION} to '$(git remote get-url origin)' [y/N]? " -r confirm
      [[ "${confirm,,}" == "y" ]] || exit 1
      maybe_run git push
    fi

    # Tag release.
    if git tag --list | grep --quiet "^${RELEASE_VERSION}$"; then
      log_callout "Skipping tag step. git tag ${RELEASE_VERSION} already exists."
    else
      log_callout "Tagging release..."
      REMOTE_REPO="origin" push_mod_tags_cmd
    fi

    if [ "${IN_PLACE}" == 0 ]; then
      # Tried with `local branch=$(git branch -a --contains tags/"${RELEASE_VERSION}")`
      # so as to work with both current branch and main/release-3.X.
      # But got error below on current branch mode,
      # Error: Git tag v3.6.99 should be on branch '* (HEAD detached at pull/14860/merge)' but is on '* (HEAD detached from pull/14860/merge)'
      #
      # Verify the version tag is on the right branch
      # shellcheck disable=SC2155
      local branch=$(git for-each-ref --contains "${RELEASE_VERSION}" --format="%(refname)" 'refs/heads' | cut -d '/' -f 3)
      if [ "${branch}" != "${BRANCH}" ]; then
        log_error "Error: Git tag ${RELEASE_VERSION} should be on branch '${BRANCH}' but is on '${branch}'"
        exit 1
      fi
    fi
  fi

  log_callout "Verify the latest commit has the version tag"
  # Verify the latest commit has the version tag
  # shellcheck disable=SC2155
  local tag="$(git describe --exact-match HEAD)"
  if [ "${tag}" != "${RELEASE_VERSION}" ]; then
    log_error "Error: Expected HEAD to be tagged with ${RELEASE_VERSION}, but 'git describe --exact-match HEAD' reported: ${tag}"
    exit 1
  fi

  log_callout "Verify the work space is clean"
  # Verify the clean working tree
  # shellcheck disable=SC2155
  local diff="$(git diff HEAD --stat)"
  if [[ "${diff}" != '' ]]; then
    log_error "Error: Expected clean working tree, but 'git diff --stat' reported: ${diff}"
    exit 1
  fi

  # Build release.
  # TODO: check the release directory for all required build artifacts.
  if [ -d release ]; then
    log_warning "Skipping release build step. /release directory already exists."
  else
    log_callout "Building release..."

    if [ "${DRY_RUN}" == "true" ] || [ "${NO_DOCKER_PUSH}" == 1 ]; then
      log_callout "Skipping docker build. Docker images will be built by GitHub Actions after release."
      # Explicitly set NO_DOCKER_PUSH to 1, if DRY_RUN is true.
      NO_DOCKER_PUSH=1
    else
      read -p "Publish etcd ${RELEASE_VERSION} docker images to registries [y/N]? " -r confirm
      if [[ "${confirm,,}" == "y" ]]; then
        local registries=("gcr.io" "quay.io")
        if [ -n "${OCI_REGISTRY:-}" ]; then
          registries=("${OCI_REGISTRY}")
        fi
        for _ in {1..5}; do
          local login_succeeded=true
          for registry in "${registries[@]}"; do
            if ! docker login "${registry}"; then
              login_succeeded=false
              log_warning "login failed, retrying"
            fi
          done
          if [[ "${login_succeeded}" == "true" ]]; then
            break
          fi
        done
      else
        NO_DOCKER_PUSH=1
      fi
    fi

    OCI_REGISTRY="${OCI_REGISTRY:-}" OCI_PATH="${OCI_PATH:-}" REPOSITORY=$(pwd) ./scripts/build-release.sh "${RELEASE_VERSION}" "${NO_DOCKER_PUSH}"
  fi

  # Sanity checks.
  "./release/etcd-${RELEASE_VERSION}-$(go env GOOS)-amd64/etcd" --version | grep -q "etcd Version: ${VERSION}" || true
  "./release/etcd-${RELEASE_VERSION}-$(go env GOOS)-amd64/etcdctl" version | grep -q "etcdctl version: ${VERSION}" || true
  "./release/etcd-${RELEASE_VERSION}-$(go env GOOS)-amd64/etcdutl" version | grep -q "etcdutl version: ${VERSION}" || true

  # Generate SHA256SUMS
  log_callout "Generating sha256sums of release artifacts."
  pushd ./release
  # shellcheck disable=SC2010
  ls . | grep -E '\.tar.gz$|\.zip$' | xargs shasum -a 256 > ./SHA256SUMS
  popd
  if [ -s ./release/SHA256SUMS ]; then
    cat ./release/SHA256SUMS
  else
    log_error "sha256sums is not valid. Aborting."
    exit 1
  fi

  # Upload artifacts.
  if [ "${DRY_RUN}" == "true" ] || [ "${NO_UPLOAD}" == 1 ]; then
    log_callout "Skipping artifact upload to gs://etcd. --no-upload flag is set."
  else
    read -p "Upload etcd ${RELEASE_VERSION} release artifacts to gs://etcd [y/N]? " -r confirm
    [[ "${confirm,,}" == "y" ]] || exit 1
    maybe_run gsutil -m cp ./release/SHA256SUMS "gs://etcd/${RELEASE_VERSION}/"
    maybe_run gsutil -m cp ./release/*.zip "gs://etcd/${RELEASE_VERSION}/"
    maybe_run gsutil -m cp ./release/*.tar.gz "gs://etcd/${RELEASE_VERSION}/"
    maybe_run gsutil -m acl ch -u allUsers:R -r "gs://etcd/${RELEASE_VERSION}/"
  fi

  ### Release validation
  mkdir -p downloads

  # Check image versions (skip if Docker images are built by CI)
  if [ "${NO_DOCKER_PUSH}" == 0 ]; then
    local images=("quay.io/coreos/etcd:${RELEASE_VERSION}" "gcr.io/etcd-development/etcd:${RELEASE_VERSION}")
    if [ -n "${OCI_REGISTRY:-}" ]; then
      images=("${OCI_REGISTRY}/${OCI_PATH:-etcd}:${RELEASE_VERSION}")
    fi

    for IMAGE in "${images[@]}"; do
      # shellcheck disable=SC2155
      local image_version=$(docker run --rm "${IMAGE}" etcd --version | grep "etcd Version" | awk -F: '{print $2}' | tr -d '[:space:]')
      if [ "${image_version}" != "${VERSION}" ]; then
        log_error "Check failed: etcd --version output for ${IMAGE} is incorrect: ${image_version}"
        exit 1
      fi
    done
  else
    log_callout "Skipping Docker image validation. Images will be validated by GitHub Actions."
  fi

  # Check gsutil binary versions
  # shellcheck disable=SC2155
  local BINARY_TGZ="etcd-${RELEASE_VERSION}-$(go env GOOS)-amd64.tar.gz"
  if [ "${DRY_RUN}" == "true" ] || [ "${NO_UPLOAD}" == 1 ]; then
    cp "./release/${BINARY_TGZ}" downloads
  else
    gsutil cp "gs://etcd/${RELEASE_VERSION}/${BINARY_TGZ}" downloads
  fi
  tar -zx -C downloads -f "downloads/${BINARY_TGZ}"
  # shellcheck disable=SC2155
  local binary_version=$("./downloads/etcd-${RELEASE_VERSION}-$(go env GOOS)-amd64/etcd" --version | grep "etcd Version" | awk -F: '{print $2}' | tr -d '[:space:]')
  if [ "${binary_version}" != "${VERSION}" ]; then
    log_error "Check failed: etcd --version output for ${BINARY_TGZ} from gs://etcd/${RELEASE_VERSION} is incorrect: ${binary_version}"
    exit 1
  fi

  if [ "${DRY_RUN}" == "true" ] || [ "${NO_GH_RELEASE}" == 1 ]; then
    log_warning ""
    log_warning "WARNING: Skipping creating GitHub release, --no-gh-release is set."
    log_warning "WARNING: If not running on DRY_MODE, please do the GitHub release manually."
    log_warning ""
  else
    local gh_repo
    local release_notes_temp_file
    local release_url
    local gh_release_args=()

    # For the main branch (v3.6), we should mark the release as a prerelease.
    # The release-3.5 (v3.5) branch, should be marked as latest. And release-3.4 (v3.4)
    # should be left without any additional mark (therefore, it doesn't need a special argument).
    if [ "${BRANCH}" = "main" ]; then
      gh_release_args=(--prerelease)
    elif [ "${BRANCH}" = "release-3.5" ]; then
      gh_release_args=(--latest)
    fi

    if [ "${REPOSITORY}" = "$(pwd)" ]; then
      gh_repo=$(git remote get-url origin)
    else
      gh_repo="${REPOSITORY}"
    fi

    gh_repo=$(echo "${gh_repo}" | sed 's/^[^@]\+@//' | sed 's/https\?:\/\///' | sed 's/\.git$//' | tr ':' '/')
    log_callout "Creating GitHub release for ${RELEASE_VERSION} on ${gh_repo}"

    release_notes_temp_file=$(mktemp)

    local release_version=${RELEASE_VERSION#v} # Remove the v prefix from the release version (i.e., v3.6.1 -> 3.6.1)
    local release_version_major_minor
    release_version_major_minor=$(echo "${release_version}" | cut -d. -f1-2) # Remove the patch from the version (i.e., 3.6)
    local release_version_major=${release_version_major_minor%.*} # Extract the major (i.e., 3)
    local release_version_minor=${release_version_major_minor/*./} # Extract the minor (i.e., 6)

    # Disable sellcheck SC2016, the single quoted syntax for sed is intentional.
    # shellcheck disable=SC2016
    sed 's/${RELEASE_VERSION}/'"${RELEASE_VERSION}"'/g' ./scripts/release_notes.tpl.txt |
      sed 's/${RELEASE_VERSION_MAJOR_MINOR}/'"${release_version_major_minor}"'/g' |
      sed 's/${RELEASE_VERSION_MAJOR}/'"${release_version_major}"'/g' |
      sed 's/${RELEASE_VERSION_MINOR}/'"${release_version_minor}"'/g' > "${release_notes_temp_file}"

    if ! gh --repo "${gh_repo}" release view "${RELEASE_VERSION}" &>/dev/null; then
      maybe_run gh release create "${RELEASE_VERSION}" \
          --repo "${gh_repo}" \
          --draft \
          --title "${RELEASE_VERSION}" \
          --notes-file "${release_notes_temp_file}" \
          "${gh_release_args[@]}"
    fi

    # Upload files one by one, as gh doesn't support passing globs as input.
    maybe_run find ./release '(' -name '*.tar.gz' -o -name '*.zip' ')' -exec \
      gh --repo "${gh_repo}" release upload "${RELEASE_VERSION}" {} --clobber \;
    maybe_run gh --repo "${gh_repo}" release upload "${RELEASE_VERSION}" ./release/SHA256SUMS --clobber

    release_url=$(gh --repo "${gh_repo}" release view "${RELEASE_VERSION}" --json url --jq '.url')

    log_warning ""
    log_warning "WARNING: The GitHub release for ${RELEASE_VERSION} has been created as a draft, please go to ${release_url} and release it."
    log_warning ""
  fi

  log_success "Success."
  exit 0
}

POSITIONAL=()
NO_UPLOAD=0
NO_DOCKER_PUSH=1
IN_PLACE=0
NO_GH_RELEASE=0

while test $# -gt 0; do
        case "$1" in
          -h|--help)
            shift
            help
            exit 0
            ;;
          --in-place)
            IN_PLACE=1
            shift
            ;;
          --no-upload)
            NO_UPLOAD=1
            shift
            ;;
          --local-docker)
            NO_DOCKER_PUSH=0
            shift
            ;;
          --no-docker-push)
            NO_DOCKER_PUSH=1
            shift
            ;;
          --no-gh-release)
            NO_GH_RELEASE=1
            shift
            ;;
          *)
            POSITIONAL+=("$1") # save it in an array for later
            shift # past argument
            ;;
        esac
done
set -- "${POSITIONAL[@]}" # restore positional parameters

if [[ ! $# -eq 1 ]]; then
  help
  exit 1
fi

# Note that we shouldn't upload artifacts in --in-place mode, so it
# must be called with DRY_RUN=true
if [ "${DRY_RUN}" != "true" ] && [ "${IN_PLACE}" == 1 ]; then
   log_error "--in-place should only be called with DRY_RUN=true"
   exit 1
fi

main "$1"
