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

set -o errexit
set -o nounset
set -o pipefail

cd "${ETCD_REPO}"

export ETCD_VERIFY=all
source ./scripts/test_lib.sh

run_for_module "tests" run_go_tests "./robustness/coverage/..." -timeout=60s -run="^TestInterfaceUse/"
