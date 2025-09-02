export K8S_CTX="kind-kind"

export MDB_NS="mongodb"

export MDB_VERSION="8.0.10"

export MDB_ADMIN_USER_PASSWORD="admin-user-password-CHANGE-ME"
export MDB_USER_PASSWORD="mdb-user-password-CHANGE-ME"
export MDB_SEARCH_SYNC_USER_PASSWORD="search-sync-user-password-CHANGE-ME"

export OPERATOR_HELM_CHART="mongodb/mongodb-kubernetes"
export OPERATOR_ADDITIONAL_HELM_VALUES=""

export MDB_CONNECTION_STRING="mongodb://mdb-user:${MDB_USER_PASSWORD}@mdbc-rs-0.mdbc-rs-svc.${MDB_NS}.svc.cluster.local:27017/?replicaSet=mdbc-rs"
