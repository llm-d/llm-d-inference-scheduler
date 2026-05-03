#!/usr/bin/env bash

# Copyright 2025, 2026 The Kubernetes Authors.
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

#echo "Generating CRDs"
#go run ./pkg/generator

SCRIPT_ROOT=$(dirname "${BASH_SOURCE[0]}")/..
CODEGEN_PKG=${1:-bin}
source "${CODEGEN_PKG}/kube_codegen.sh"
THIS_PKG="github.com/llm-d/llm-d-inference-scheduler"


kube::codegen::gen_helpers \
    --boilerplate "/dev/null" \
    "${SCRIPT_ROOT}"

kube::codegen::gen_register \
    --boilerplate "/dev/null" \
    "${SCRIPT_ROOT}"

kube::codegen::gen_client \
    --with-watch \
    --with-applyconfig \
    --output-dir "${SCRIPT_ROOT}/client-go" \
    --output-pkg "${THIS_PKG}/client-go" \
    --boilerplate "/dev/null" \
    "${SCRIPT_ROOT}"
