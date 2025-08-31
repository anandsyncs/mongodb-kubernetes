test -z "${PRERELEASE_IMAGE_PULLSECRET}" && return 0;

#echo "Verifying mongodb-kubernetes-database-pods service account contains proper pull secret"
#if ! kubectl get --context "${K8S_CTX}" -n "${MDB_NAMESPACE}" -o json \
#  sa mongodb-kubernetes-database-pods -o=jsonpath='{.imagePullSecrets[*]}' | \
#    grep prerelease-image-pullsecret; then
#  echo "ERROR: mongodb-kubernetes-database-pods service account doesn't contain necessary pullsecret"
#  kubectl get --context "${K8S_CTX}" -n "${MDB_NAMESPACE}" -o json \
#    sa mongodb-kubernetes-database-pods -o=yaml
#  return 1
#fi
#echo "SUCCESS: mongodb-kubernetes-database-pods service account contains proper pull secret"
