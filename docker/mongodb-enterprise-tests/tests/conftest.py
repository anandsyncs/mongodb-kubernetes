import os
import subprocess
import tempfile
from typing import Callable, Dict, List, Optional

import kubernetes
from kubernetes import client
from kubernetes.client import ApiextensionsV1Api
from pytest import fixture

from kubetester import (
    get_pod_when_ready,
    create_or_update_configmap,
    is_pod_ready,
    read_secret,
    update_configmap,
)
from kubetester.awss3client import AwsS3Client
from kubetester.certs import Issuer, Certificate, ClusterIssuer
from kubetester.git import clone_and_checkout
from kubetester.helm import helm_install_from_chart
from kubetester.http import get_retriable_https_session
from kubetester.kubetester import KubernetesTester, running_locally
from kubetester.kubetester import fixture as _fixture
from kubetester.mongodb_multi import MultiClusterClient
from kubetester.operator import Operator
from tests.multicluster import prepare_multi_cluster_namespaces

try:
    kubernetes.config.load_kube_config()
except Exception:
    kubernetes.config.load_incluster_config()


KUBECONFIG_FILEPATH = "/etc/config/kubeconfig"
MULTI_CLUSTER_CONFIG_DIR = "/etc/multicluster"
# AppDB monitoring is disabled by default for e2e tests.
# If monitoring is needed use monitored_appdb_operator_installation_config / operator_with_monitored_appdb
MONITOR_APPDB_E2E_DEFAULT = "false"
MULTI_CLUSTER_OPERATOR_NAME = "mongodb-enterprise-operator-multi-cluster"
CLUSTER_HOST_MAPPING = {
    "us-central1-c_central": "https://35.232.85.244",
    "us-east1-b_member-1a": "https://35.243.222.230",
    "us-east1-c_member-2a": "https://34.75.94.207",
    "us-west1-a_member-3a": "https://35.230.121.15",
}


@fixture(scope="module")
def namespace() -> str:
    return os.environ["NAMESPACE"]


@fixture(scope="module")
def version_id() -> str:
    """
    Returns VERSION_ID if it has been defined, or "latest" otherwise.
    """
    return os.environ.get("VERSION_ID", "latest")


@fixture(scope="module")
def operator_installation_config(namespace: str, version_id: str) -> Dict[str, str]:
    """Returns the ConfigMap containing configuration data for the Operator to be created.
    Created in the single_e2e.sh"""
    config = KubernetesTester.read_configmap(namespace, "operator-installation-config")
    config["customEnvVars"] = f"OPS_MANAGER_MONITOR_APPDB={MONITOR_APPDB_E2E_DEFAULT}"

    # if running on evergreen don't use the default image tag
    if version_id != "latest":
        config["database.version"] = version_id
        config["initAppDb.version"] = version_id
        config["initDatabase.version"] = version_id
        config["initOpsManager.version"] = version_id

    return config


@fixture(scope="module")
def monitored_appdb_operator_installation_config(operator_installation_config: Dict[str, str]) -> Dict[str, str]:
    """Returns the ConfigMap containing configuration data for the Operator to be created
    and for the AppDB to be monitored.
    Created in the single_e2e.sh"""
    config = operator_installation_config
    config["customEnvVars"] = "OPS_MANAGER_MONITOR_APPDB=true"
    return config


@fixture(scope="module")
def multi_cluster_operator_installation_config(
    central_cluster_client: kubernetes.client.ApiClient, namespace: str
) -> Dict[str, str]:
    """Returns the ConfigMap containing configuration data for the Operator to be created.
    Created in the single_e2e.sh"""
    config = KubernetesTester.read_configmap(
        namespace, "operator-installation-config", api_client=central_cluster_client
    )
    config["customEnvVars"] = f"OPS_MANAGER_MONITOR_APPDB={MONITOR_APPDB_E2E_DEFAULT}"
    return config


@fixture(scope="module")
def operator_clusterwide(
    namespace: str,
    operator_installation_config: Dict[str, str],
) -> Operator:
    helm_args = operator_installation_config.copy()
    helm_args["operator.watchNamespace"] = "*"
    return Operator(namespace=namespace, helm_args=helm_args).install()


@fixture(scope="module")
def operator_vault_secret_backend(
    namespace: str,
    monitored_appdb_operator_installation_config: Dict[str, str],
) -> Operator:
    helm_args = monitored_appdb_operator_installation_config.copy()
    helm_args["operator.vaultSecretBackend.enabled"] = "true"
    return Operator(namespace=namespace, helm_args=helm_args).install()


@fixture(scope="module")
def operator_vault_secret_backend_tls(
    namespace: str,
    monitored_appdb_operator_installation_config: Dict[str, str],
) -> Operator:
    helm_args = monitored_appdb_operator_installation_config.copy()
    helm_args["operator.vaultSecretBackend.enabled"] = "true"
    helm_args["operator.vaultSecretBackend.tlsSecretRef"] = "vault-tls"
    return Operator(namespace=namespace, helm_args=helm_args).install()


@fixture(scope="module")
def evergreen_task_id() -> str:
    return os.environ.get("TASK_ID", "")


@fixture(scope="module")
def image_type() -> str:
    return os.environ["IMAGE_TYPE"]


@fixture(scope="module")
def managed_security_context() -> str:
    return os.environ["MANAGED_SECURITY_CONTEXT"]


@fixture(scope="module")
def aws_s3_client() -> AwsS3Client:
    return AwsS3Client("us-east-1")


@fixture(scope="session")
def crd_api():
    return ApiextensionsV1Api()


@fixture(scope="module")
def cert_manager(namespace: str) -> str:
    """Installs cert-manager v1.5.4 using Helm."""
    return install_cert_manager(namespace)


@fixture(scope="module")
def multi_cluster_cert_manager(
    namespace: str,
    central_cluster_client: kubernetes.client.ApiClient,
    central_cluster_name: str,
    member_cluster_clients: List[MultiClusterClient],
):
    install_cert_manager(
        namespace,
        cluster_client=central_cluster_client,
        cluster_name=central_cluster_name,
    )

    for client in member_cluster_clients:
        install_cert_manager(
            namespace,
            cluster_client=client.api_client,
            cluster_name=client.cluster_name,
        )


@fixture(scope="module")
def issuer(cert_manager: str, namespace: str) -> str:
    return create_issuer(namespace=namespace)


@fixture(scope="module")
def intermediate_issuer(cert_manager: str, issuer: str, namespace: str) -> str:
    """
    This fixture creates an intermediate "Issuer" in the testing namespace
    """
    # Create the Certificate for the intermediate CA based on the issuer fixture
    intermediate_ca_cert = Certificate(namespace=namespace, name="intermediate-ca-issuer")
    intermediate_ca_cert["spec"] = {
        "isCA": True,
        "commonName": "intermediate-ca-issuer",
        "secretName": "intermediate-ca-secret",
        "issuerRef": {"name": issuer},
        "dnsNames": ["intermediate-ca.example.com"],
    }
    intermediate_ca_cert.create().block_until_ready()

    # Create the intermediate issuer
    issuer = Issuer(name="intermediate-ca-issuer", namespace=namespace)
    issuer["spec"] = {"ca": {"secretName": "intermediate-ca-secret"}}
    issuer.create().block_until_ready()

    return "intermediate-ca-issuer"


@fixture(scope="module")
def multi_cluster_issuer(
    multi_cluster_cert_manager: str,
    namespace: str,
    central_cluster_client: kubernetes.client.ApiClient,
) -> str:
    return create_issuer(namespace, central_cluster_client)


@fixture(scope="module")
def multi_cluster_clusterissuer(
    multi_cluster_cert_manager: str,
    namespace: str,
    central_cluster_client: kubernetes.client.ApiClient,
) -> str:
    return create_issuer(namespace, central_cluster_client, clusterwide=True)


@fixture(scope="module")
def issuer_ca_filepath():
    return _fixture("ca-tls-full-chain.crt")


@fixture(scope="module")
def multi_cluster_issuer_ca_configmap(
    issuer_ca_filepath: str,
    namespace: str,
    central_cluster_client: kubernetes.client.ApiClient,
) -> str:
    """This is the CA file which verifies the certificates signed by it."""
    ca = open(issuer_ca_filepath).read()

    # The operator expects the CA that validates Ops Manager is contained in
    # an entry with a name of "mms-ca.crt"
    data = {"ca-pem": ca, "mms-ca.crt": ca}
    name = "issuer-ca"

    create_or_update_configmap(namespace, name, data, api_client=central_cluster_client)

    return name


@fixture(scope="module")
def issuer_ca_configmap(issuer_ca_filepath: str, namespace: str) -> str:
    """This is the CA file which verifies the certificates signed by it."""
    ca = open(issuer_ca_filepath).read()

    # The operator expects the CA that validates Ops Manager is contained in
    # an entry with a name of "mms-ca.crt"
    data = {"ca-pem": ca, "mms-ca.crt": ca}

    name = "issuer-ca"
    create_or_update_configmap(namespace, name, data)
    return name


@fixture(scope="module")
def ops_manager_issuer_ca_configmap(issuer_ca_filepath: str, namespace: str) -> str:
    """
    This is the CA file which verifies the certificates signed by it.
    This CA is used to community with Ops Manager. This is needed by the database pods
    which talk to OM.
    """
    ca = open(issuer_ca_filepath).read()

    # The operator expects the CA that validates Ops Manager is contained in
    # an entry with a name of "mms-ca.crt"
    data = {"mms-ca.crt": ca}

    name = "ops-manager-issuer-ca"
    create_or_update_configmap(namespace, name, data)
    return name


@fixture(scope="module")
def app_db_issuer_ca_configmap(issuer_ca_filepath: str, namespace: str) -> str:
    """
    This is the custom ca used with the AppDB hosts. This can be the same as the one used
    for OM but does not need to be the same.
    """
    ca = open(issuer_ca_filepath).read()

    name = "app-db-issuer-ca"
    create_or_update_configmap(namespace, name, {"ca-pem": ca})
    return name


@fixture(scope="module")
def issuer_ca_plus(issuer_ca_filepath: str, namespace: str) -> str:
    """Returns the name of a ConfigMap which includes a custom CA and the full
    certificate chain for downloads.mongodb.com, fastdl.mongodb.org,
    downloads.mongodb.org. This allows for the use of a custom CA while still
    allowing the agent to download from MongoDB servers.

    """
    ca = open(issuer_ca_filepath).read()
    plus_ca = open(_fixture("downloads.mongodb.com.chained+root.crt")).read()

    # The operator expects the CA that validates Ops Manager is contained in
    # an entry with a name of "mms-ca.crt"
    data = {"ca-pem": ca + plus_ca, "mms-ca.crt": ca + plus_ca}

    name = "issuer-plus-ca"
    create_or_update_configmap(namespace, name, data)
    yield name


@fixture(scope="module")
def ca_path() -> str:
    """Returns a relative path to a file containing the CA.
    This is required to test TLS enabled connections to MongoDB like:

    def test_connect(replica_set: MongoDB, ca_path: str)
        replica_set.assert_connectivity(ca_path=ca_path)
    """
    return _fixture("ca-tls.crt")


@fixture(scope="module")
def custom_mdb_version() -> str:
    """Returns a CUSTOM_MDB_VERSION for Mongodb to be created/upgraded to for testing.
    Defaults to 5.0.14 (simplifies testing locally)"""
    return os.getenv("CUSTOM_MDB_VERSION", "5.0.14")


@fixture(scope="module")
def custom_appdb_version(custom_mdb_version: str) -> str:
    """Returns a CUSTOM_APPDB_VERSION for AppDB to be created/upgraded to for testing,
    defaults to custom_mdb_version() (in most cases we need to use the same version for MongoDB as for AppDB)
    """

    return os.getenv("CUSTOM_APPDB_VERSION", f"{custom_mdb_version}-ent")


@fixture(scope="module")
def custom_version() -> str:
    """Returns a CUSTOM_OM_VERSION for OM.
    Defaults to 5.0+ (for development)"""
    return os.getenv("CUSTOM_OM_VERSION", "5.0.2")


@fixture(scope="module")
def default_operator(
    namespace: str,
    operator_installation_config: Dict[str, str],
) -> Operator:
    """Installs/upgrades a default Operator used by any test not interested in some custom Operator setting.
    TODO we use the helm template | kubectl apply -f process so far as Helm install/upgrade needs more refactoring in
    the shared environment"""
    operator = Operator(
        namespace=namespace,
        helm_args=operator_installation_config,
    ).upgrade()

    # If we're running locally, then immediately after installing the deployment, we scale it to zero.
    # Note: There will be a short moment that an operator pod is running interfering with our application
    # This way operator in POD is not interfering with locally running one.
    if local_operator():
        client.AppsV1Api().patch_namespaced_deployment_scale(
            namespace=namespace,
            name=operator.name,
            body={"spec": {"replicas": 0}},
        )

    return operator


@fixture(scope="module")
def operator_with_monitored_appdb(
    namespace: str,
    monitored_appdb_operator_installation_config: Dict[str, str],
) -> Operator:
    """Installs/upgrades a default Operator used by any test that needs the AppDB monitoring enabled."""
    return Operator(
        namespace=namespace,
        helm_args=monitored_appdb_operator_installation_config,
    ).upgrade()


@fixture(scope="module")
def central_cluster_name() -> str:
    central_cluster = os.environ.get("CENTRAL_CLUSTER")
    if not central_cluster:
        raise ValueError("No central cluster specified in environment variable CENTRAL_CLUSTER!")
    return central_cluster


@fixture(scope="module")
def central_cluster_client(
    central_cluster_name: str, cluster_clients: Dict[str, kubernetes.client.ApiClient]
) -> kubernetes.client.ApiClient:
    return cluster_clients[central_cluster_name]


@fixture(scope="module")
def member_cluster_names() -> List[str]:
    member_clusters = os.environ.get("MEMBER_CLUSTERS")
    if not member_clusters:
        raise ValueError("No member clusters specified in environment variable MEMBER_CLUSTERS!")
    return sorted(member_clusters.split())


@fixture(scope="module")
def member_cluster_clients(
    cluster_clients: Dict[str, kubernetes.client.ApiClient],
    member_cluster_names: List[str],
) -> List[MultiClusterClient]:
    member_cluster_clients = []
    for i, member_cluster in enumerate(sorted(member_cluster_names)):
        member_cluster_clients.append(MultiClusterClient(cluster_clients[member_cluster], member_cluster, i))
    return member_cluster_clients


@fixture(scope="module")
def multi_cluster_operator(
    namespace: str,
    central_cluster_name: str,
    multi_cluster_operator_installation_config: Dict[str, str],
    central_cluster_client: client.ApiClient,
    member_cluster_clients: List[MultiClusterClient],
    member_cluster_names: List[str],
) -> Operator:
    os.environ["HELM_KUBECONTEXT"] = central_cluster_name

    # when running with the local operator, this is executed by scripts/dev/prepare_local_e2e_run.sh
    if not local_operator():
        run_kube_config_creation_tool(member_cluster_names, namespace, namespace, member_cluster_names)
    return _install_multi_cluster_operator(
        namespace,
        multi_cluster_operator_installation_config,
        central_cluster_client,
        member_cluster_clients,
        {
            "operator.name": MULTI_CLUSTER_OPERATOR_NAME,
            # override the serviceAccountName for the operator deployment
            "operator.createOperatorServiceAccount": "false",
        },
        central_cluster_name,
    )


@fixture(scope="module")
def multi_cluster_operator_manual_remediation(
    namespace: str,
    central_cluster_name: str,
    multi_cluster_operator_installation_config: Dict[str, str],
    central_cluster_client: client.ApiClient,
    member_cluster_clients: List[MultiClusterClient],
    member_cluster_names: List[str],
    cluster_clients,
) -> Operator:
    os.environ["HELM_KUBECONTEXT"] = central_cluster_name
    run_kube_config_creation_tool(member_cluster_names, namespace, namespace, member_cluster_names)
    return _install_multi_cluster_operator(
        namespace,
        multi_cluster_operator_installation_config,
        central_cluster_client,
        member_cluster_clients,
        {
            "operator.name": MULTI_CLUSTER_OPERATOR_NAME,
            # override the serviceAccountName for the operator deployment
            "operator.createOperatorServiceAccount": "false",
            "multiCluster.performFailOver": "false",
        },
        central_cluster_name,
    )


@fixture(scope="module")
def multi_cluster_operator_clustermode(
    namespace: str,
    central_cluster_name: str,
    multi_cluster_operator_installation_config: Dict[str, str],
    central_cluster_client: client.ApiClient,
    member_cluster_clients: List[MultiClusterClient],
    member_cluster_names: List[str],
    cluster_clients: Dict[str, kubernetes.client.ApiClient],
) -> Operator:
    os.environ["HELM_KUBECONTEXT"] = central_cluster_name
    run_kube_config_creation_tool(member_cluster_names, namespace, namespace, member_cluster_names, True)
    return _install_multi_cluster_operator(
        namespace,
        multi_cluster_operator_installation_config,
        central_cluster_client,
        member_cluster_clients,
        {
            "operator.name": MULTI_CLUSTER_OPERATOR_NAME,
            # override the serviceAccountName for the operator deployment
            "operator.createOperatorServiceAccount": "false",
            "operator.watchNamespace": "*",
        },
        central_cluster_name,
    )


@fixture(scope="module")
def install_multi_cluster_operator_set_members_fn(
    namespace: str,
    central_cluster_name: str,
    multi_cluster_operator_installation_config: Dict[str, str],
    central_cluster_client: client.ApiClient,
    member_cluster_clients: List[MultiClusterClient],
) -> Callable[[List[str]], Operator]:
    def _fn(member_cluster_names: List[str]) -> Operator:
        os.environ["HELM_KUBECONTEXT"] = central_cluster_name
        mcn = ",".join(member_cluster_names)
        return _install_multi_cluster_operator(
            namespace,
            multi_cluster_operator_installation_config,
            central_cluster_client,
            member_cluster_clients,
            {
                "operator.name": MULTI_CLUSTER_OPERATOR_NAME,
                # override the serviceAccountName for the operator deployment
                "operator.createOperatorServiceAccount": "false",
                "multiCluster.clusters": "{" + mcn + "}",
            },
            central_cluster_name,
        )

    return _fn


def _install_multi_cluster_operator(
    namespace: str,
    multi_cluster_operator_installation_config: Dict[str, str],
    central_cluster_client: client.ApiClient,
    member_cluster_clients: List[MultiClusterClient],
    helm_opts: Dict[str, str],
    central_cluster_name: str,
    operator_name: Optional[str] = MULTI_CLUSTER_OPERATOR_NAME,
) -> Operator:
    prepare_multi_cluster_namespaces(
        namespace,
        multi_cluster_operator_installation_config,
        member_cluster_clients,
        central_cluster_name,
    )
    multi_cluster_operator_installation_config.update(helm_opts)

    operator = Operator(
        name=operator_name,
        namespace=namespace,
        helm_args=multi_cluster_operator_installation_config,
        api_client=central_cluster_client,
    ).upgrade(multi_cluster=True)

    # If we're running locally, then immediately after installing the deployment, we scale it to zero.
    # This way operator in POD is not interfering with locally running one.
    if local_operator():
        client.AppsV1Api(api_client=central_cluster_client).patch_namespaced_deployment_scale(
            namespace=namespace,
            name=operator.name,
            body={"spec": {"replicas": 0}},
        )

    return operator


@fixture(scope="module")
def official_operator(
    namespace: str,
    image_type: str,
    managed_security_context: str,
    operator_installation_config: Dict[str, str],
) -> Operator:
    """
    Installs the Operator from the official Helm Chart.

    The version installed is always the latest version published as a Helm Chart.
    """

    helm_options = []

    # When running in Openshift "managedSecurityContext" will be true.
    # When running in kind "managedSecurityContext" will be false, but still use the ubi images.

    helm_args = {
        "registry.imagePullSecrets": operator_installation_config["registry.imagePullSecrets"],
        "managedSecurityContext": managed_security_context,
    }
    name = "mongodb-enterprise-operator"

    # Note, that we don't intend to install the official Operator to standalone clusters (kops/openshift) as we want to
    # avoid damaged CRDs. But we may need to install the "openshift like" environment to Kind instead if the "ubi" images
    # are used for installing the dev Operator
    helm_args["operator.operator_image_name"] = name

    temp_dir = tempfile.mkdtemp()
    # Values files are now located in `helm-charts` repo.
    clone_and_checkout(
        "https://github.com/mongodb/helm-charts",
        temp_dir,
        "main",  # main branch of helm-charts.
    )
    chart_dir = os.path.join(temp_dir, "charts", "enterprise-operator")

    # When testing the UBI image type we need to assume a few things

    # 1. The testing cluster is Openshift
    # 2. The "values.yaml" file is "values-openshift.yaml"
    if image_type == "ubi":
        helm_options = [
            "--values",
            os.path.join(chart_dir, "values-openshift.yaml"),
        ]

    # The "official" Operator will be installed, from the Helm Repo ("mongodb/enterprise-operator")
    return Operator(
        namespace=namespace,
        helm_args=helm_args,
        helm_chart_path="mongodb/enterprise-operator",
        helm_options=helm_options,
        name=name,
    ).install()


def get_headers() -> Dict[str, str]:
    """
    Returns an authentication header that can be used when accessing
    the Github API. This is to avoid rate limiting when accessing the
    API from the Evergreen hosts.
    """

    if github_token := os.getenv("GITHUB_TOKEN_READ"):
        return {"Authorization": "token {}".format(github_token)}

    return dict()


def fetch_latest_released_operator_version() -> str:
    """
    Fetches the currently released operator version from the Github API.
    """

    response = get_retriable_https_session(tls_verify=True).get(
        "https://api.github.com/repos/mongodb/mongodb-enterprise-kubernetes/releases/latest",
        headers=get_headers(),
    )
    response.raise_for_status()

    return response.json()["tag_name"]


def _read_multi_cluster_config_value(value: str) -> str:
    multi_cluster_config_dir = os.environ.get("MULTI_CLUSTER_CONFIG_DIR", MULTI_CLUSTER_CONFIG_DIR)
    filepath = f"{multi_cluster_config_dir}/{value}".rstrip()
    if not os.path.isfile(filepath):
        raise ValueError(f"{filepath} does not exist!")
    with open(filepath, "r") as f:
        return f.read().strip()


def _get_client_for_cluster(
    cluster_name: str,
) -> kubernetes.client.api_client.ApiClient:
    token = _read_multi_cluster_config_value(cluster_name)

    if not token:
        raise ValueError(f"No token found for cluster {cluster_name}")

    configuration = kubernetes.client.Configuration()
    kubernetes.config.load_kube_config(
        context=cluster_name,
        config_file=os.environ.get("KUBECONFIG", KUBECONFIG_FILEPATH),
        client_configuration=configuration,
    )
    configuration.host = CLUSTER_HOST_MAPPING.get(cluster_name, configuration.host)

    configuration.verify_ssl = False
    configuration.api_key = {"authorization": f"Bearer {token}"}
    return kubernetes.client.api_client.ApiClient(configuration=configuration)


def install_cert_manager(
    namespace: str,
    cluster_client: Optional[client.ApiClient] = None,
    cluster_name: Optional[str] = None,
    name="cert-manager",
    version="v1.5.4",
) -> str:
    if cluster_name is not None:
        # ensure we cert-manager in the member clusters.
        os.environ["HELM_KUBECONTEXT"] = cluster_name

    install_required = True

    if running_locally():
        webhook_ready = is_pod_ready(
            name,
            f"app.kubernetes.io/instance={name},app.kubernetes.io/component=webhook",
            api_client=cluster_client,
        )
        controller_ready = is_pod_ready(
            name,
            f"app.kubernetes.io/instance={name},app.kubernetes.io/component=controller",
            api_client=cluster_client,
        )
        if webhook_ready is not None and controller_ready is not None:
            print("Cert manager already installed, skipping helm install")
            install_required = False

    if install_required:
        helm_install_from_chart(
            name,  # cert-manager is installed on a specific namespace
            name,
            f"jetstack/{name}",
            version=version,
            custom_repo=("jetstack", "https://charts.jetstack.io"),
            helm_args={"installCRDs": "true"},
        )

    # waits until the cert-manager webhook and controller are Ready, otherwise creating
    # Certificate Custom Resources will fail.
    get_pod_when_ready(
        name,
        f"app.kubernetes.io/instance={name},app.kubernetes.io/component=webhook",
        api_client=cluster_client,
    )
    get_pod_when_ready(
        name,
        f"app.kubernetes.io/instance={name},app.kubernetes.io/component=controller",
        api_client=cluster_client,
    )
    return name


@fixture(scope="module")
def cluster_clients(
    namespace: str, member_cluster_names: List[str]
) -> Dict[str, kubernetes.client.api_client.ApiClient]:
    member_clusters = [
        _read_multi_cluster_config_value("member_cluster_1"),
        _read_multi_cluster_config_value("member_cluster_2"),
    ]

    if len(member_cluster_names) == 3:
        member_clusters.append(_read_multi_cluster_config_value("member_cluster_3"))
    return get_clients_for_clusters(member_clusters)


def get_clients_for_clusters(
    member_cluster_names: List[str],
) -> Dict[str, kubernetes.client.ApiClient]:
    central_cluster = _read_multi_cluster_config_value("central_cluster")

    return {c: _get_client_for_cluster(c) for c in ([central_cluster] + member_cluster_names)}


def get_api_servers_from_pod_kubeconfig(kubeconfig: str, cluster_clients: Dict[str, kubernetes.client.ApiClient]):
    api_servers = dict()
    fd, kubeconfig_tmp_path = tempfile.mkstemp()
    with os.fdopen(fd, "w") as fp:
        fp.write(kubeconfig)

        for cluster_name, cluster_client in cluster_clients.items():
            configuration = kubernetes.client.Configuration()
            kubernetes.config.load_kube_config(
                context=cluster_name,
                config_file=kubeconfig_tmp_path,
                client_configuration=configuration,
            )
            api_servers[cluster_name] = configuration.host

    return api_servers


def run_kube_config_creation_tool(
    member_clusters: List[str],
    central_namespace: str,
    member_namespace: str,
    member_cluster_names: List[str],
    cluster_scoped: Optional[bool] = False,
    service_account_name: Optional[str] = "mongodb-enterprise-operator-multi-cluster",
):
    central_cluster = _read_multi_cluster_config_value("central_cluster")
    member_clusters_str = ",".join(member_clusters)
    args = [
        os.getenv(
            "MULTI_CLUSTER_KUBE_CONFIG_CREATOR_PATH",
            "multi-cluster-kube-config-creator",
        ),
        "multicluster",
        "setup",
        "--member-clusters",
        member_clusters_str,
        "--central-cluster",
        central_cluster,
        "--member-cluster-namespace",
        member_namespace,
        "--central-cluster-namespace",
        central_namespace,
        "--service-account",
        service_account_name,
    ]

    if os.getenv("MULTI_CLUSTER_CREATE_SERVICE_ACCOUNT_TOKEN_SECRETS") == "true":
        args.append("--create-service-account-secrets")

    if not local_operator():
        api_servers = get_api_servers_from_test_pod_kubeconfig(member_namespace, member_cluster_names)

        if len(api_servers) > 0:
            args.append("--member-clusters-api-servers")
            args.append(",".join([api_servers[member_cluster] for member_cluster in member_clusters]))

    if cluster_scoped:
        args.append("--cluster-scoped")

    try:
        print(f"Running multi-cluster cli setup tool: {' '.join(args)}")
        subprocess.check_output(args, stderr=subprocess.PIPE)
        print("Finished running multi-cluster cli setup tool")
    except subprocess.CalledProcessError as exc:
        print("Status: FAIL", exc.returncode, exc.output)
        return exc.returncode

    return 0


def get_api_servers_from_kubeconfig_secret(
    namespace: str,
    secret_name: str,
    secret_cluster_client: kubernetes.client.ApiClient,
    cluster_clients: Dict[str, kubernetes.client.ApiClient],
):
    kubeconfig_secret = read_secret(namespace, secret_name, api_client=secret_cluster_client)
    return get_api_servers_from_pod_kubeconfig(kubeconfig_secret["kubeconfig"], cluster_clients)


def get_api_servers_from_test_pod_kubeconfig(namespace: str, member_cluster_names: List[str]) -> Dict[str, str]:
    test_pod_cluster = os.environ["TEST_POD_CLUSTER"]
    cluster_clients = get_clients_for_clusters(member_cluster_names)

    return get_api_servers_from_kubeconfig_secret(
        namespace,
        "test-pod-kubeconfig",
        cluster_clients[test_pod_cluster],
        cluster_clients,
    )


def run_multi_cluster_recovery_tool(
    member_clusters: List[str],
    central_namespace: str,
    member_namespace: str,
    cluster_scoped: Optional[bool] = False,
) -> int:
    central_cluster = _read_multi_cluster_config_value("central_cluster")
    member_clusters_str = ",".join(member_clusters)
    args = [
        os.getenv(
            "MULTI_CLUSTER_KUBE_CONFIG_CREATOR_PATH",
            "multi-cluster-kube-config-creator",
        ),
        "multicluster",
        "recover",
        "--member-clusters",
        member_clusters_str,
        "--central-cluster",
        central_cluster,
        "--member-cluster-namespace",
        member_namespace,
        "--central-cluster-namespace",
        central_namespace,
        "--operator-name",
        MULTI_CLUSTER_OPERATOR_NAME,
        "--source-cluster",
        member_clusters[0],
    ]
    if os.getenv("MULTI_CLUSTER_CREATE_SERVICE_ACCOUNT_TOKEN_SECRETS") == "true":
        args.append("--create-service-account-secrets")

    if cluster_scoped:
        args.extend(["--cluster-scoped", "true"])

    try:
        print(f"Running multi-cluster cli recovery tool: {' '.join(args)}")
        subprocess.check_output(args, stderr=subprocess.PIPE)
        print("Finished running multi-cluster cli recovery tool")
    except subprocess.CalledProcessError as exc:
        print("Status: FAIL", exc.returncode, exc.output)
        return exc.returncode
    return 0


def create_issuer(
    namespace: str,
    api_client: Optional[client.ApiClient] = None,
    clusterwide: bool = False,
):
    """
    This fixture creates an "Issuer" in the testing namespace. This requires cert-manager to be installed in the cluster.
    The ca-tls.key and ca-tls.crt are the private key and certificates used to generate
    certificates. This is based on a Cert-Manager CA Issuer.
    More info here: https://cert-manager.io/docs/configuration/ca/

    Please note, this cert will expire on Dec 8 07:53:14 2023 GMT.
    """
    issuer_data = {
        "tls.key": open(_fixture("ca-tls.key")).read(),
        "tls.crt": open(_fixture("ca-tls.crt")).read(),
    }
    secret = client.V1Secret(
        metadata=client.V1ObjectMeta(name="ca-key-pair"),
        string_data=issuer_data,
    )

    try:
        if clusterwide:
            client.CoreV1Api(api_client=api_client).create_namespaced_secret("cert-manager", secret)
        else:
            client.CoreV1Api(api_client=api_client).create_namespaced_secret(namespace, secret)
    except client.rest.ApiException as e:
        if e.status == 409:
            print("ca-key-pair already exists")
        else:
            raise e

    # And then creates the Issuer
    if clusterwide:
        issuer = ClusterIssuer(name="ca-issuer", namespace="")
    else:
        issuer = Issuer(name="ca-issuer", namespace=namespace)

    issuer["spec"] = {"ca": {"secretName": "ca-key-pair"}}
    issuer.api = kubernetes.client.CustomObjectsApi(api_client=api_client)

    try:
        issuer.create().block_until_ready()
    except client.rest.ApiException as e:
        if e.status == 409:
            print("issuer already exists")
        else:
            raise e

    return "ca-issuer"


def local_operator():
    """Checks if the current test run should assume that the operator is running locally, i.e. not in a pod."""
    return os.getenv("LOCAL_OPERATOR", "") == "true"


def pod_names(replica_set_name: str, replica_set_members: int) -> list[str]:
    """List of pod names for given replica set name."""
    return [f"{replica_set_name}-{i}" for i in range(0, replica_set_members)]


def default_external_domain() -> str:
    """Default external domain used for testing LoadBalancers on Kind."""
    return "mongodb.interconnected"


def external_domain_fqdns(
    replica_set_name: str,
    replica_set_members: int,
    external_domain: str = default_external_domain(),
) -> list[str]:
    """Builds list of hostnames for given replica set when connecting to it using external domain."""
    return [f"{pod_name}.{external_domain}" for pod_name in pod_names(replica_set_name, replica_set_members)]


def update_coredns_hosts(
    host_mappings: list[tuple[str, str]],
    cluster_name: Optional[str] = None,
    api_client: Optional[kubernetes.client.ApiClient] = None,
):
    """Updates kube-system/coredns config map with given host_mappings."""

    indent = " " * 7
    mapping_string = "\n".join([f"{indent}{host_mapping[0]} {host_mapping[1]}" for host_mapping in host_mappings])
    config_data = {"Corefile": coredns_config("interconnected", mapping_string)}

    if cluster_name is None:
        cluster_name = "default cluster"

    print(f"Updating coredns for cluster: {cluster_name}")
    update_configmap("kube-system", "coredns", config_data, api_client=api_client)


def coredns_config(tld: str, mappings: str):
    """Returns coredns config map data with mappings inserted."""
    return f"""
.:53 {{
    errors
    health {{
       lameduck 5s
    }}
    ready
    kubernetes cluster.local in-addr.arpa ip6.arpa {{
       pods insecure
       fallthrough in-addr.arpa ip6.arpa
       ttl 30
    }}
    prometheus :9153
    forward . /etc/resolv.conf {{
       max_concurrent 1000
    }}
    cache 30
    loop
    reload
    loadbalance
    debug
    hosts /etc/coredns/customdomains.db   {tld} {{
{mappings}
       fallthrough
    }}
}}
"""
