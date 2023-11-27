import re
import pytest
from kubetester.omtester import get_sc_cert_names
from kubetester.kubetester import KubernetesTester, skip_if_local
from kubetester.mongotester import ShardedClusterTester
from kubetester.mongodb import MongoDB, Phase
from kubetester.kubetester import fixture as load_fixture
from kubetester.certs import (
    ISSUER_CA_NAME,
    create_mongodb_tls_certs,
    create_agent_tls_certs,
    create_sharded_cluster_certs,
)
import time
import jsonpatch

MDB_RESOURCE_NAME = "test-tls-sc-additional-domains"


@pytest.fixture(scope="module")
def server_certs(issuer: str, namespace: str):
    create_sharded_cluster_certs(
        namespace,
        MDB_RESOURCE_NAME,
        shards=1,
        mongos_per_shard=1,
        config_servers=1,
        mongos=2,
        additional_domains=["additional-cert-test.com"],
    )


@pytest.fixture(scope="module")
def sc(namespace: str, server_certs: str, issuer_ca_configmap: str) -> MongoDB:
    res = MongoDB.from_yaml(
        load_fixture("test-tls-sc-additional-domains.yaml"), namespace=namespace
    )
    res["spec"]["security"]["tls"]["ca"] = issuer_ca_configmap
    return res.create()


@pytest.mark.e2e_tls_sc_additional_certs
class TestShardedClustertWithAdditionalCertDomains(KubernetesTester):
    def test_sharded_cluster_running(self, sc: MongoDB):
        sc.assert_reaches_phase(Phase.Running, timeout=1200)

    @skip_if_local
    def test_has_right_certs(self):
        """Check that mongos processes serving the right certificates."""
        for i in range(2):
            host = f"{MDB_RESOURCE_NAME}-mongos-{i}.{MDB_RESOURCE_NAME}-svc.{self.namespace}.svc"
            assert any(
                re.match(
                    rf"{MDB_RESOURCE_NAME}-mongos-{i}\.additional-cert-test\.com", san
                )
                for san in self.get_mongo_server_sans(host)
            )

    @skip_if_local
    def test_can_still_connect(self, ca_path: str):
        tester = ShardedClusterTester(
            MDB_RESOURCE_NAME, ssl=True, ca_path=ca_path, mongos_count=2
        )
        tester.assert_connectivity()


@pytest.mark.e2e_tls_sc_additional_certs
class TestShardedClustertRemoveAdditionalCertDomains(KubernetesTester):
    """
    update:
      file: test-tls-sc-additional-domains.yaml
      wait_until: in_running_state
      patch: '[{"op":"remove","path":"/spec/security/tls/additionalCertificateDomains"}]'
      timeout: 240
    """

    def test_continues_to_work(self):
        pass

    @skip_if_local
    def test_can_still_connect(self, sc: MongoDB, ca_path: str):
        tester = sc.tester(use_ssl=True, ca_path=ca_path)
        tester.assert_connectivity()
