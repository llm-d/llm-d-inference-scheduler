#!/usr/bin/env bash

# Copyright 2026 The Kubernetes Authors.
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

set -euo pipefail

SCRIPT_ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
HELM="${HELM:-${SCRIPT_ROOT}/bin/helm}"
YQ="${YQ:-${SCRIPT_ROOT}/bin/yq}"
TEMP_DIR=$(mktemp -d)
trap 'rm -rf "${TEMP_DIR}"' EXIT

assert_yaml() {
  if ! "${YQ}" -e "$2" "$1" > /dev/null; then
    echo "Assertion failed: $2 in $1" >&2
    exit 1
  fi
}

render() {
  "${HELM}" template sync-test "${chart_path}" "${args[@]}" "$@" > "${output}" || return $?
  "${YQ}" 'select(.kind == "Deployment" and .metadata.name == "sync-test-epp")' "${output}" > "${deployment}"
}

assert_rejected() {
  local message=$1
  shift
  if render "$@" 2> "${TEMP_DIR}/error.log"; then
    echo "Accepted invalid configuration: $*" >&2
    exit 1
  fi
  grep -Fq -- "${message}" "${TEMP_DIR}/error.log"
}

for chart in llm-d-router-gateway llm-d-router-standalone; do
  echo "Verifying cross-replica configuration for ${chart}..."
  chart_path="${SCRIPT_ROOT}/config/charts/${chart}"
  args=(--set router.modelServers.matchLabels.app=llm-instance-gateway)
  output="${TEMP_DIR}/render.yaml"
  deployment="${TEMP_DIR}/deployment.yaml"
  config="${TEMP_DIR}/config.yaml"
  "${HELM}" dependency build "${chart_path}"

  render --set router.epp.replicas=3
  cp "${output}" "${TEMP_DIR}/baseline.yaml"
  render --set router.epp.replicas=3 --set router.epp.crossReplica.enabled=false
  diff -u "${TEMP_DIR}/baseline.yaml" "${output}"

  sync=(--set router.epp.crossReplica.enabled=true
        --set router.epp.crossReplica.redis.address=redis.example:6379
        --set router.epp.replicas=3)
  render "${sync[@]}"
  assert_yaml "${deployment}" '.spec.replicas == 3 and .spec.strategy.type == "RollingUpdate" and .spec.strategy.rollingUpdate.maxUnavailable == 0 and .spec.strategy.rollingUpdate.maxSurge == 1'
  assert_yaml "${deployment}" '.spec.template.spec.containers[] | select(.name == "epp") | .args | contains(["--ha-enable-leader-election=false", "--allow-experimental-plugins=true"])'
  assert_yaml "${output}" 'select(.kind == "ConfigMap" and .metadata.name == "sync-test-epp") | .data."default-plugins.yaml" | from_yaml | .plugins[] | select(.type == "redis-syncer") | (.parameters | sort_keys(..) | to_json) == ({"address": "redis.example:6379", "coordinationTTL": "180s", "db": 0, "stateTTL": "2s"} | sort_keys(..) | to_json)'
  "${YQ}" -N 'select(.metadata.name == "sync-test-epp-leader-election" or .metadata.name == "sync-test-epp-leader-election-binding" or .kind == "StatefulSet" or .kind == "Secret")' "${output}" > "${TEMP_DIR}/unexpected.yaml"
  test ! -s "${TEMP_DIR}/unexpected.yaml"

  render "${sync[@]}" --set router.epp.autoscaling.enabled=true --set router.epp.autoscaling.minReplicas=2 --set router.epp.autoscaling.maxReplicas=7
  assert_yaml "${deployment}" 'has("spec") and (.spec | has("replicas") | not)'
  assert_yaml "${output}" 'select(.kind == "HorizontalPodAutoscaler") | .spec.minReplicas == 2 and .spec.maxReplicas == 7 and .spec.scaleTargetRef.name == "sync-test-epp"'
  render "${sync[@]}" --set router.epp.deploymentStrategy.type=Recreate
  assert_yaml "${deployment}" '(.spec.strategy | sort_keys(..) | to_json) == ({"type": "Recreate"} | sort_keys(..) | to_json)'

  # Only the selected configuration gains a syncer; its routing profile is preserved.
  for source in builtin structured raw; do
    for filename in default-plugins.yaml payload-agnostic.yaml custom.yaml; do
      if [[ ${source} == builtin && ${filename} == custom.yaml ]]; then continue; fi
      values=(--set "router.epp.pluginsConfigFile=${filename}")
      if [[ ${source} != builtin ]]; then
        "${YQ}" '.router.epp.pluginsConfig.plugins += [{"type": "metrics-data-source"}, {"type": "core-metrics-extractor"}] | .router.epp.pluginsConfig.dataLayer = {"injectDefaults": false, "sources": [{"pluginRef": "metrics-data-source", "extractors": [{"pluginRef": "core-metrics-extractor"}]}]} | del(.router.epp.pluginsCustomConfig)' \
          "${SCRIPT_ROOT}/hack/testdata/plugins-config-base.yaml" > "${TEMP_DIR}/values.yaml"
        if [[ ${source} == raw ]]; then
          CONFIG_FILE="${filename}" "${YQ}" -i '.router.epp.pluginsCustomConfig[strenv(CONFIG_FILE)] = (.router.epp.pluginsConfig | to_yaml) | del(.router.epp.pluginsConfig)' "${TEMP_DIR}/values.yaml"
        fi
        values=(-f "${TEMP_DIR}/values.yaml" "${values[@]}")
      fi
      render "${values[@]}"
      CONFIG_FILE="${filename}" "${YQ}" -o=json 'select(.kind == "ConfigMap" and .metadata.name == "sync-test-epp") | .data | del(.[strenv(CONFIG_FILE)]) | sort_keys(..)' "${output}" > "${TEMP_DIR}/other-before.json"
      CONFIG_FILE="${filename}" "${YQ}" 'select(.kind == "ConfigMap" and .metadata.name == "sync-test-epp") | .data[strenv(CONFIG_FILE)] | from_yaml' "${output}" > "${TEMP_DIR}/before.yaml"
      render "${values[@]}" "${sync[@]}" \
        --set router.epp.crossReplica.redis.db=4 \
        --set router.epp.crossReplica.redis.stateTTL=5s \
        --set router.epp.crossReplica.redis.coordinationTTL=60s \
        --set router.epp.crossReplica.syncInterval=100ms \
        --set router.epp.crossReplica.publishTimeout=500ms \
        --set router.epp.crossReplica.redis.passwordSecretRef.name=redis-auth \
        --set router.epp.crossReplica.redis.passwordSecretRef.key=password
      CONFIG_FILE="${filename}" "${YQ}" 'select(.kind == "ConfigMap" and .metadata.name == "sync-test-epp") | .data[strenv(CONFIG_FILE)] | from_yaml' "${output}" > "${config}"
      CONFIG_FILE="${filename}" "${YQ}" -o=json 'select(.kind == "ConfigMap" and .metadata.name == "sync-test-epp") | .data | del(.[strenv(CONFIG_FILE)]) | sort_keys(..)' "${output}" > "${TEMP_DIR}/other-after.json"
      diff -u "${TEMP_DIR}/other-before.json" "${TEMP_DIR}/other-after.json"
      assert_yaml "${config}" '(.dataLayer.crossReplica | sort_keys(..) | to_json) == ({"publishTimeout": "500ms", "syncInterval": "100ms", "syncerPluginRef": "redis-syncer"} | sort_keys(..) | to_json)'
      assert_yaml "${config}" '[.plugins[] | select(.type == "redis-syncer")] | length == 1'
      assert_yaml "${config}" '.plugins[] | select(.type == "redis-syncer") | (.parameters | sort_keys(..) | to_json) == ({"address": "redis.example:6379", "coordinationTTL": "60s", "db": 4, "passwordEnv": "EPP_REDIS_PASSWORD", "stateTTL": "5s"} | sort_keys(..) | to_json)'
      assert_yaml "${deployment}" '.spec.template.spec.containers[] | select(.name == "epp") | .env[] | select(.name == "EPP_REDIS_PASSWORD") | (.valueFrom.secretKeyRef | sort_keys(..) | to_json) == ({"key": "password", "name": "redis-auth"} | sort_keys(..) | to_json)'
      # Compare all original fields, allowing the generated dataLayer container.
      "${YQ}" -o=json 'del(.dataLayer.crossReplica) | .plugins |= map(select(.type != "redis-syncer")) | sort_keys(..)' "${config}" > "${TEMP_DIR}/after.yaml"
      "${YQ}" -o=json '.dataLayer = (.dataLayer // {}) | sort_keys(..)' "${TEMP_DIR}/before.yaml" > "${TEMP_DIR}/expected.yaml"
      diff -u "${TEMP_DIR}/expected.yaml" "${TEMP_DIR}/after.yaml"
    done
  done

  render --set router.epp.haMode=all-active --set router.epp.replicas=3
  assert_yaml "${deployment}" '.spec.template.spec.containers[] | select(.name == "epp") | .args | contains(["--ha-enable-leader-election=false"])'
  render --set router.epp.haMode=leader-elected
  assert_yaml "${output}" 'select(.metadata.name == "sync-test-epp-leader-election") | .kind == "Role"'

  assert_rejected 'router.epp.haMode must be' --set router.epp.haMode=invalid
  assert_rejected 'requires all-active' "${sync[@]}" --set router.epp.haMode=leader-elected
  assert_rejected 'conflicts with router.epp.haMode' "${sync[@]}" --set router.epp.flags.ha-enable-leader-election=true
  assert_rejected 'requires active-active' --set router.epp.haMode=leader-elected --set router.epp.autoscaling.enabled=true
  assert_rejected 'requires allow-experimental-plugins' "${sync[@]}" --set router.epp.flags.allow-experimental-plugins=false
  assert_rejected 'redis.address is required' --set router.epp.crossReplica.enabled=true
  assert_rejected 'passwordSecretRef requires name and key' "${sync[@]}" --set router.epp.crossReplica.redis.passwordSecretRef.name=redis-auth
  assert_rejected 'plaintext password' "${sync[@]}" --set router.epp.crossReplica.redis.password=never-inline
  assert_rejected 'reserved' "${sync[@]}" --set router.epp.crossReplica.redis.passwordSecretRef.name=redis-auth --set router.epp.crossReplica.redis.passwordSecretRef.key=password --set-json 'router.epp.env=[{"name":"EPP_REDIS_PASSWORD","value":"collision"}]'
  for flag in config-file config-text; do
    assert_rejected 'cannot override' "${sync[@]}" --set "router.epp.flags.${flag}=external"
  done
  assert_rejected 'already configures crossReplica' "${sync[@]}" -f "${SCRIPT_ROOT}/hack/testdata/plugins-config-base.yaml" --set router.epp.pluginsConfig.dataLayer.crossReplica.syncerPluginRef=manual
  assert_rejected 'already configures redis-syncer' "${sync[@]}" -f "${SCRIPT_ROOT}/hack/testdata/plugins-config-base.yaml" --set-json 'router.epp.pluginsConfig.plugins=[{"type":"redis-syncer"}]'
  assert_rejected 'requires an llm-d.ai/v1 EndpointPickerConfig' "${sync[@]}" -f "${SCRIPT_ROOT}/hack/testdata/plugins-config-base.yaml" --set router.epp.pluginsConfig.apiVersion=llm-d.ai/v1alpha1
  assert_rejected 'selected pluginsConfigFile' "${sync[@]}" --set router.epp.pluginsConfigFile=missing.yaml
  if [[ ${chart} == llm-d-router-gateway ]]; then
    assert_rejected 'incompatible with priority routing and GKE preferredBackends' "${sync[@]}" --set provider.name=gke --set provider.gke.preferredBackends.enabled=true
  else
    assert_rejected 'incompatible with priority routing and GKE preferredBackends' "${sync[@]}" --set router.proxy.mode=service --set router.proxy.priorityRouting.enabled=true
  fi
  echo "Cross-replica configuration checks passed for ${chart}."
done
