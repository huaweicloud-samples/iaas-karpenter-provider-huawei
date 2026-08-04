#!/usr/bin/env bash

set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" &>/dev/null && pwd)"
REPO_ROOT="$(cd -- "${SCRIPT_DIR}/.." &>/dev/null && pwd)"

CONTROLLER_GEN_BIN="${CONTROLLER_GEN:-${REPO_ROOT}/bin/controller-gen}"
HELM_BIN="${HELM:-helm}"
CHART_DIR="${CHART_DIR:-${REPO_ROOT}/charts/karpenter-provider-huawei}"
RELEASE_NAME="${RELEASE_NAME:-karpenter-provider-huawei}"
NAMESPACE="${NAMESPACE:-karpenter-provider-huawei-system}"
PROVIDER_CRD="karpenter.k8s.huawei_ccenodeclasses.yaml"
GENERATED_ROLE="manager-role"
RENDERED_ROLE="manifest-verification-manager-role"

if [[ "${CHART_DIR}" != /* ]]; then
  CHART_DIR="${REPO_ROOT}/${CHART_DIR}"
fi

require_executable() {
  local executable="$1"
  if [[ "${executable}" == */* ]]; then
    [[ -x "${executable}" ]] || { echo "executable not found: ${executable}" >&2; exit 1; }
    return
  fi
  command -v "${executable}" &>/dev/null || { echo "executable not found: ${executable}" >&2; exit 1; }
}

require_executable "${CONTROLLER_GEN_BIN}"
require_executable "${HELM_BIN}"
[[ -d "${CHART_DIR}" ]] || { echo "chart directory not found: ${CHART_DIR}" >&2; exit 1; }

TEMP_DIR="$(mktemp -d "${TMPDIR:-/tmp}/karpenter-provider-manifests.XXXXXX")"
cleanup() {
  [[ -n "${TEMP_DIR:-}" && -d "${TEMP_DIR}" ]] && rm -rf -- "${TEMP_DIR}"
}
trap cleanup EXIT

mkdir -p "${TEMP_DIR}/crds" "${TEMP_DIR}/rbac"

echo "Linting Helm chart: ${CHART_DIR}"
"${HELM_BIN}" lint "${CHART_DIR}"

echo "Rendering complete Helm chart, including CRDs"
"${HELM_BIN}" template "${RELEASE_NAME}" "${CHART_DIR}" \
  --namespace "${NAMESPACE}" \
  --include-crds \
  --set-string namePrefix=manifest-verification- \
  >"${TEMP_DIR}/rendered.yaml"

echo "Checking provider CRD generation drift"
(
  cd "${REPO_ROOT}"
  "${CONTROLLER_GEN_BIN}" crd paths=./pkg/apis/... \
    output:crd:artifacts:config="${TEMP_DIR}/crds"
)

shopt -s nullglob
generated_crds=("${TEMP_DIR}/crds/"*.yaml)
if (( ${#generated_crds[@]} != 1 )) || [[ "$(basename -- "${generated_crds[0]:-}")" != "${PROVIDER_CRD}" ]]; then
  echo "provider CRD generation must produce only ${PROVIDER_CRD}; generated:" >&2
  if (( ${#generated_crds[@]} == 0 )); then
    echo "  (none)" >&2
  else
    printf '  %s\n' "${generated_crds[@]##*/}" >&2
  fi
  exit 1
fi

if ! diff -u \
  --label "checked-in/${PROVIDER_CRD}" \
  --label "generated/${PROVIDER_CRD}" \
  "${CHART_DIR}/crds/${PROVIDER_CRD}" \
  "${TEMP_DIR}/crds/${PROVIDER_CRD}"; then
  echo "provider CRD drift detected; run 'make manifests' and commit the updated chart CRD" >&2
  exit 1
fi

echo "Checking rendered manager RBAC against controller markers"
(
  cd "${REPO_ROOT}"
  "${CONTROLLER_GEN_BIN}" rbac:roleName="${GENERATED_ROLE}" paths=./pkg/controllers/... \
    output:rbac:artifacts:config="${TEMP_DIR}/rbac"
  go run ./hack/rbac-verifier \
    --generated "${TEMP_DIR}/rbac/role.yaml" \
    --generated-role "${GENERATED_ROLE}" \
    --rendered "${TEMP_DIR}/rendered.yaml" \
    --rendered-role "${RENDERED_ROLE}"
)

echo "Helm manifests are verified"
