#!/usr/bin/env bash

# Local environment configuration for using the local helm chart with public images
# This avoids the ECR authentication issues with private images

export K8S_CTX="kind-kind"

# Override to use local helm chart instead of remote registry
export OPERATOR_HELM_CHART="../../../helm_chart"

# Use minimal helm values to avoid private ECR images
# Remove imagePullSecrets to avoid authentication issues
declare -a helm_values=(
"registry.imagePullSecrets="
"operator.version=1.2.0"
)

SCRIPT_PATH="${BASH_SOURCE[0]}"
SCRIPT_DIR="$(cd "$(dirname "${SCRIPT_PATH}")" && pwd)"

OPERATOR_ADDITIONAL_HELM_VALUES="$(echo -n "${helm_values[@]}" | tr ' ' ',')"

echo "Using local helm chart configuration with public images"
echo "OPERATOR_HELM_CHART=${OPERATOR_HELM_CHART}"
echo "OPERATOR_ADDITIONAL_HELM_VALUES=${OPERATOR_ADDITIONAL_HELM_VALUES}"
