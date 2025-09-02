kubectl apply --context "${K8S_CTX}" -n "${MDB_NS}" -f - <<EOF
apiVersion: mongodb.com/v1
kind: MongoDBSearch
metadata:
  name: mdbs
spec:
  source:
    external:
      hostAndPorts:
        - mdbc-rs-0.mdbc-rs-svc.${MDB_NS}.svc.cluster.local:27017
        - mdbc-rs-1.mdbc-rs-svc.${MDB_NS}.svc.cluster.local:27017
        - mdbc-rs-2.mdbc-rs-svc.${MDB_NS}.svc.cluster.local:27017
      keyFileSecretRef:
        name: mdbc-rs-keyfile
        key: keyfile
      tls:
        enabled: false
    username: search-sync-source
    passwordSecretRef:
      name: mdbc-rs-search-sync-source-password
      key: password
  resourceRequirements:
    limits:
      cpu: "3"
      memory: 5Gi
    requests:
      cpu: "2"
      memory: 3Gi
EOF
