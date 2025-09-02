echo; echo "MongoDBCommunity resource"
kubectl --context "${K8S_CTX}" -n "${MDB_NS}" get mdbc/mdbc-rs
echo; echo "MongoDBSearch resource"
kubectl --context "${K8S_CTX}" -n "${MDB_NS}" get mdbs/mdbs
echo; echo "Pods running in cluster ${K8S_CTX}"
kubectl --context "${K8S_CTX}" -n "${MDB_NS}" get pods
