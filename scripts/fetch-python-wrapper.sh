#!/usr/bin/env bash
# fetch-python-wrapper.sh
# Fetches the Python wrapper file (render_jinja_template_wrapper.py) from llm-d-kv-cache-manager
# for use in Docker builds and local development.
# Version can be provided as CLI arg or via KVCACHE_MANAGER_VERSION env var (default v0.3.2).
#
# This script replicates the original Dockerfile logic:
# 1. Creates a temporary directory
# 2. Clones the repo into that directory
# 3. Creates the output directory structure
# 4. Copies the wrapper file to the output location
# 5. Cleans up the temporary directory

set -euo pipefail

VERSION="${1:-${KVCACHE_MANAGER_VERSION:-v0.3.2}}"
OUTPUT_DIR="${2:-llm-d-kv-cache-manager/pkg/preprocessing/chat_completions}"

REPO_URL="https://github.com/llm-d/llm-d-kv-cache-manager.git"
WRAPPER_FILE="pkg/preprocessing/chat_completions/render_jinja_template_wrapper.py"

# Create temporary directory (equivalent to: mkdir -p /tmp/kv-cache-manager)
# TEMP_DIR will be an absolute path like /tmp/tmp.XXXXXX
TEMP_DIR=$(mktemp -d)
trap "rm -rf ${TEMP_DIR}" EXIT

echo "Fetching Python wrapper from llm-d-kv-cache-manager@${VERSION}..."

# Equivalent to: cd /tmp/kv-cache-manager && git clone ... .
# (clones repo contents directly into TEMP_DIR - using absolute path, no need to cd)
git clone --depth 1 --branch "${VERSION}" "${REPO_URL}" "${TEMP_DIR}"

# Create output directory if it doesn't exist
# (equivalent to: mkdir -p /workspace/llm-d-kv-cache-manager/pkg/preprocessing/chat_completions)
# OUTPUT_DIR is relative to current working directory (relative path, same as original)
mkdir -p "${OUTPUT_DIR}"

# Copy wrapper file
# Source: absolute path ${TEMP_DIR}/${WRAPPER_FILE} (e.g., /tmp/tmp.XXXXXX/pkg/.../wrapper.py)
# Destination: relative path ${OUTPUT_DIR}/ (e.g., llm-d-kv-cache-manager/pkg/.../)
# (equivalent to original: cp pkg/.../wrapper.py /workspace/... from within temp dir)
cp "${TEMP_DIR}/${WRAPPER_FILE}" "${OUTPUT_DIR}/"

# Cleanup happens automatically via trap (equivalent to: rm -rf /tmp/kv-cache-manager)

echo "Successfully fetched render_jinja_template_wrapper.py to ${OUTPUT_DIR}/"

