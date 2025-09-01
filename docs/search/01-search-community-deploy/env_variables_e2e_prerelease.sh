export K8S_CTX="${CLUSTER_NAME}"

export PRERELEASE_VERSION="1.4.0-prerelease-68b1a853973bae0007d5eaa0"

export PRERELEASE_IMAGE_PULLSECRET="${COMMUNITY_PRIVATE_PREVIEW_PULLSECRET_DOCKERCONFIGJSON}"
export OPERATOR_ADDITIONAL_HELM_VALUES="registry.imagePullSecrets=prerelease-image-pullsecret"
export OPERATOR_HELM_CHART="oci://quay.io/mongodb/staging/mongodb-kubernetes:${PRERELEASE_VERSION}"

