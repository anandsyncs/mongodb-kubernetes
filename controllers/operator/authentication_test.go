package operator

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"testing"
	"time"

	"k8s.io/utils/ptr"

	"github.com/10gen/ops-manager-kubernetes/controllers/om/deployment"

	certsv1 "k8s.io/api/certificates/v1beta1"

	kubernetesClient "github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/client"
	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/secret"

	mdbv1 "github.com/10gen/ops-manager-kubernetes/api/v1/mdb"
	"github.com/10gen/ops-manager-kubernetes/api/v1/mdbmulti"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/authentication"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/certs"

	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/10gen/ops-manager-kubernetes/controllers/operator/mock"

	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"

	"github.com/10gen/ops-manager-kubernetes/controllers/om"
	"github.com/10gen/ops-manager-kubernetes/pkg/dns"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func TestX509CanBeEnabled_WhenThereAreOnlyTlsDeployments_ReplicaSet(t *testing.T) {
	ctx := context.Background()
	rs := DefaultReplicaSetBuilder().EnableTLS().EnableX509().SetTLSCA("custom-ca").Build()
	manager := mock.NewManager(ctx, rs)
	createConfigMap(ctx, t, manager.Client)

	addKubernetesTlsResources(ctx, manager.Client, rs)

	reconciler := newReplicaSetReconciler(ctx, manager, om.NewEmptyMockedOmConnection)
	checkReconcileSuccessful(ctx, t, reconciler, rs, manager.Client)
}

func TestX509ClusterAuthentication_CanBeEnabled_IfX509AuthenticationIsEnabled_ReplicaSet(t *testing.T) {
	ctx := context.Background()
	rs := DefaultReplicaSetBuilder().EnableTLS().EnableX509().SetTLSCA("custom-ca").Build()
	manager := mock.NewManager(ctx, rs)
	addKubernetesTlsResources(ctx, manager.Client, rs)
	createConfigMap(ctx, t, manager.Client)

	reconciler := newReplicaSetReconciler(ctx, manager, om.NewEmptyMockedOmConnection)
	checkReconcileSuccessful(ctx, t, reconciler, rs, manager.Client)
}

func TestX509ClusterAuthentication_CanBeEnabled_IfX509AuthenticationIsEnabled_ShardedCluster(t *testing.T) {
	ctx := context.Background()
	scWithTls := DefaultClusterBuilder().EnableTLS().EnableX509().SetName("sc-with-tls").SetTLSCA("custom-ca").Build()
	reconciler, client := defaultClusterReconciler(ctx, scWithTls)
	addKubernetesTlsResources(ctx, client, scWithTls)

	checkReconcileSuccessful(ctx, t, reconciler, scWithTls, client)
}

func TestX509CanBeEnabled_WhenThereAreOnlyTlsDeployments_ShardedCluster(t *testing.T) {
	ctx := context.Background()
	scWithTls := DefaultClusterBuilder().EnableTLS().EnableX509().SetName("sc-with-tls").SetTLSCA("custom-ca").Build()

	reconciler, client := defaultClusterReconciler(ctx, scWithTls)
	addKubernetesTlsResources(ctx, client, scWithTls)

	checkReconcileSuccessful(ctx, t, reconciler, scWithTls, client)
}

func TestUpdateOmAuthentication_NoAuthenticationEnabled(t *testing.T) {
	ctx := context.Background()
	conn := om.NewMockedOmConnection(om.NewDeployment())
	rs := DefaultReplicaSetBuilder().SetName("my-rs").SetMembers(3).Build()
	processNames := []string{"my-rs-0", "my-rs-1", "my-rs-2"}

	r := newReplicaSetReconciler(ctx, mock.NewManager(ctx, rs), om.NewEmptyMockedOmConnection)
	r.updateOmAuthentication(ctx, conn, processNames, rs, "", "", "", false, zap.S())

	ac, _ := conn.ReadAutomationConfig()

	assert.True(t, ac.Auth.Disabled, "authentication was not specified to enabled, so it should remain disabled in Ops Manager")
	assert.Len(t, ac.Auth.Users, 0)
}

func TestUpdateOmAuthentication_EnableX509_TlsNotEnabled(t *testing.T) {
	ctx := context.Background()
	rs := DefaultReplicaSetBuilder().SetName("my-rs").SetMembers(3).Build()
	// deployment with existing non-tls non-x509 replica set
	conn := om.NewMockedOmConnection(deployment.CreateFromReplicaSet(rs))

	// configure X509 authentication & tls
	rs.Spec.Security.Authentication.Modes = []mdbv1.AuthMode{"X509"}
	rs.Spec.Security.Authentication.Enabled = true
	rs.Spec.Security.TLSConfig.Enabled = true

	r := newReplicaSetReconciler(ctx, mock.NewManager(ctx, rs), om.NewEmptyMockedOmConnection)
	status, isMultiStageReconciliation := r.updateOmAuthentication(ctx, conn, []string{"my-rs-0", "my-rs-1", "my-rs-2"}, rs, "", "", "", false, zap.S())

	assert.True(t, status.IsOK(), "configuring both options at once should not result in a failed status")
	assert.True(t, isMultiStageReconciliation, "configuring both tls and x509 at once should result in a multi stage reconciliation")
}

func TestUpdateOmAuthentication_EnableX509_WithTlsAlreadyEnabled(t *testing.T) {
	ctx := context.Background()
	rs := DefaultReplicaSetBuilder().SetName("my-rs").SetMembers(3).EnableTLS().Build()
	conn := om.NewMockedOmConnection(deployment.CreateFromReplicaSet(rs))
	r := newReplicaSetReconciler(ctx, mock.NewManager(ctx, rs), om.NewEmptyMockedOmConnection)
	status, isMultiStageReconciliation := r.updateOmAuthentication(ctx, conn, []string{"my-rs-0", "my-rs-1", "my-rs-2"}, rs, "", "", "", false, zap.S())

	assert.True(t, status.IsOK(), "configuring x509 when tls has already been enabled should not result in a failed status")
	assert.False(t, isMultiStageReconciliation, "if tls is already enabled, we should be able to configure x509 is a single reconciliation")
}

func TestUpdateOmAuthentication_AuthenticationIsNotConfigured_IfAuthIsNotSet(t *testing.T) {
	ctx := context.Background()
	rs := DefaultReplicaSetBuilder().SetName("my-rs").SetMembers(3).EnableTLS().SetAuthentication(nil).Build()

	rs.Spec.Security.Authentication = nil

	conn := om.NewMockedOmConnection(deployment.CreateFromReplicaSet(rs))
	r := newReplicaSetReconciler(ctx, mock.NewManager(ctx, rs), func(context *om.OMContext) om.Connection {
		return conn
	})

	status, _ := r.updateOmAuthentication(ctx, conn, []string{"my-rs-0", "my-rs-1", "my-rs-2"}, rs, "", "", "", false, zap.S())
	assert.True(t, status.IsOK(), "no authentication should have been configured")

	ac, _ := conn.ReadAutomationConfig()

	// authentication has not been touched
	assert.True(t, ac.Auth.Disabled)
	assert.Len(t, ac.Auth.Users, 0)
	assert.Equal(t, "MONGODB-CR", ac.Auth.AutoAuthMechanism)
}

func TestUpdateOmAuthentication_DoesNotDisableAuth_IfAuthIsNotSet(t *testing.T) {
	ctx := context.Background()
	rs := DefaultReplicaSetBuilder().
		EnableTLS().
		EnableAuth().
		EnableX509().
		SetTLSCA("custom-ca").
		Build()

	manager := mock.NewManager(ctx, rs)
	manager.Client.AddDefaultMdbConfigResources(ctx)
	reconciler, client := newReplicaSetReconciler(ctx, manager, om.NewEmptyMockedOmConnection), manager.Client

	addKubernetesTlsResources(ctx, client, rs)

	checkReconcileSuccessful(ctx, t, reconciler, rs, client)

	ac, _ := om.CurrMockedConnection.ReadAutomationConfig()
	// x509 auth has been enabled
	assert.True(t, ac.Auth.IsEnabled())
	assert.Contains(t, ac.Auth.AutoAuthMechanism, authentication.MongoDBX509)

	rs.Spec.Security.Authentication = nil

	manager = mock.NewManagerSpecificClient(client)
	reconciler = newReplicaSetReconciler(ctx, manager, func(context *om.OMContext) om.Connection {
		return om.CurrMockedConnection
	})

	checkReconcileSuccessful(ctx, t, reconciler, rs, client)

	ac, _ = om.CurrMockedConnection.ReadAutomationConfig()
	assert.True(t, ac.Auth.IsEnabled())
	assert.Contains(t, ac.Auth.AutoAuthMechanism, authentication.MongoDBX509)
}

func TestCanConfigureAuthenticationDisabled_WithNoModes(t *testing.T) {
	ctx := context.Background()
	rs := DefaultReplicaSetBuilder().
		EnableTLS().
		SetTLSCA("custom-ca").
		SetAuthentication(
			&mdbv1.Authentication{
				Enabled: false,
				Modes:   nil,
			}).
		Build()

	manager := mock.NewManager(ctx, rs)
	manager.Client.AddDefaultMdbConfigResources(ctx)
	reconciler, client := newReplicaSetReconciler(ctx, manager, om.NewEmptyMockedOmConnection), manager.Client

	addKubernetesTlsResources(ctx, client, rs)

	checkReconcileSuccessful(ctx, t, reconciler, rs, client)
}

func TestUpdateOmAuthentication_EnableX509_FromEmptyDeployment(t *testing.T) {
	ctx := context.Background()
	conn := om.NewMockedOmConnection(om.NewDeployment())

	rs := DefaultReplicaSetBuilder().SetName("my-rs").SetMembers(3).EnableTLS().EnableAuth().EnableX509().Build()
	r := newReplicaSetReconciler(ctx, mock.NewManager(ctx, rs), om.NewEmptyMockedOmConnection)
	createAgentCSRs(ctx, 1, r.client, certsv1.CertificateApproved)

	status, isMultiStageReconciliation := r.updateOmAuthentication(ctx, conn, []string{"my-rs-0", "my-rs-1", "my-rs-2"}, rs, "", "", "", false, zap.S())
	assert.True(t, status.IsOK(), "configuring x509 and tls when there are no processes should not result in a failed status")
	assert.False(t, isMultiStageReconciliation, "if we are enabling tls and x509 at once, this should be done in a single reconciliation")
}

func TestX509AgentUserIsCorrectlyConfigured(t *testing.T) {
	ctx := context.Background()
	rs := DefaultReplicaSetBuilder().SetName("my-rs").SetMembers(3).EnableTLS().SetTLSCA("custom-ca").EnableAuth().EnableX509().Build()
	x509User := DefaultMongoDBUserBuilder().SetDatabase(authentication.ExternalDB).SetMongoDBResourceName("my-rs").Build()

	manager := mock.NewManager(ctx, rs)
	manager.Client.AddDefaultMdbConfigResources(ctx)
	memberClusterMap := getFakeMultiClusterMap()
	err := manager.Client.Create(ctx, x509User)
	assert.NoError(t, err)

	// configure x509/tls resources
	addKubernetesTlsResources(ctx, manager.Client, rs)
	reconciler := newReplicaSetReconciler(ctx, manager, om.NewEmptyMockedOmConnection)

	checkReconcileSuccessful(ctx, t, reconciler, rs, manager.Client)

	userReconciler := newMongoDBUserReconciler(ctx, manager, func(context *om.OMContext) om.Connection {
		return om.CurrMockedConnection // use the same connection
	}, memberClusterMap)

	actual, err := userReconciler.Reconcile(ctx, requestFromObject(x509User))
	expected := reconcile.Result{RequeueAfter: util.TWENTY_FOUR_HOURS}

	assert.NoError(t, err)
	assert.Equal(t, expected, actual)

	ac, _ := om.CurrMockedConnection.ReadAutomationConfig()
	assert.Equal(t, ac.Auth.AutoUser, "CN=mms-automation-agent,OU=cloud,O=MongoDB,L=New York,ST=New York,C=US")
}

func TestScramAgentUserIsCorrectlyConfigured(t *testing.T) {
	ctx := context.Background()
	rs := DefaultReplicaSetBuilder().SetName("my-rs").SetMembers(3).EnableAuth().EnableSCRAM().Build()
	scramUser := DefaultMongoDBUserBuilder().SetMongoDBResourceName("my-rs").Build()

	manager := mock.NewManager(ctx, rs)
	manager.Client.AddDefaultMdbConfigResources(ctx)
	memberClusterMap := getFakeMultiClusterMap()
	err := manager.Client.Create(ctx, scramUser)
	assert.NoError(t, err)

	userPassword := secret.Builder().
		SetNamespace(scramUser.Namespace).
		SetName(scramUser.Spec.PasswordSecretKeyRef.Name).
		SetField(scramUser.Spec.PasswordSecretKeyRef.Key, "password").
		Build()

	err = manager.Client.Create(ctx, &userPassword)

	assert.NoError(t, err)

	reconciler := newReplicaSetReconciler(ctx, manager, om.NewEmptyMockedOmConnection)

	checkReconcileSuccessful(ctx, t, reconciler, rs, manager.Client)

	userReconciler := newMongoDBUserReconciler(ctx, manager, func(context *om.OMContext) om.Connection {
		return om.CurrMockedConnection // use the same connection
	}, memberClusterMap)

	actual, err := userReconciler.Reconcile(ctx, requestFromObject(scramUser))
	expected := reconcile.Result{RequeueAfter: util.TWENTY_FOUR_HOURS}

	assert.NoError(t, err)
	assert.Equal(t, expected, actual)

	ac, _ := om.CurrMockedConnection.ReadAutomationConfig()
	assert.Equal(t, ac.Auth.AutoUser, util.AutomationAgentName)
}

func TestScramAgentUser_IsNotOverridden(t *testing.T) {
	ctx := context.Background()
	rs := DefaultReplicaSetBuilder().SetName("my-rs").SetMembers(3).EnableAuth().EnableSCRAM().Build()
	manager := mock.NewManager(ctx, rs)
	manager.Client.AddDefaultMdbConfigResources(ctx)
	reconciler := newReplicaSetReconciler(ctx, manager, om.NewEmptyMockedOmConnection)
	reconciler.omConnectionFactory = func(ctx *om.OMContext) om.Connection {
		connection := om.NewEmptyMockedOmConnectionWithAutomationConfigChanges(ctx, func(ac *om.AutomationConfig) {
			ac.Auth.AutoUser = "my-custom-agent-name"
		})
		return connection
	}

	checkReconcileSuccessful(ctx, t, reconciler, rs, manager.Client)

	ac, _ := om.CurrMockedConnection.ReadAutomationConfig()

	assert.Equal(t, "my-custom-agent-name", ac.Auth.AutoUser)
}

func TestX509InternalClusterAuthentication_CanBeEnabledWithScram_ReplicaSet(t *testing.T) {
	ctx := context.Background()
	rs := DefaultReplicaSetBuilder().SetName("my-rs").
		SetMembers(3).
		EnableAuth().
		EnableSCRAM().
		EnableX509InternalClusterAuth().
		Build()

	manager := mock.NewManager(ctx, rs)
	r := newReplicaSetReconciler(ctx, manager, om.NewEmptyMockedOmConnection)
	createConfigMap(ctx, t, r.client)
	addKubernetesTlsResources(ctx, r.client, rs)

	checkReconcileSuccessful(ctx, t, r, rs, manager.Client)

	currConn := om.CurrMockedConnection
	dep, _ := currConn.ReadDeployment()
	for _, p := range dep.ProcessesCopy() {
		assert.Equal(t, p.ClusterAuthMode(), "x509")
	}
}

func TestX509InternalClusterAuthentication_CanBeEnabledWithScram_ShardedCluster(t *testing.T) {
	ctx := context.Background()
	sc := DefaultClusterBuilder().SetName("my-sc").
		EnableAuth().
		EnableSCRAM().
		EnableX509InternalClusterAuth().
		Build()

	r, manager := newShardedClusterReconcilerFromResource(ctx, *sc, om.NewEmptyMockedOmConnection)
	addKubernetesTlsResources(ctx, r.client, sc)
	createConfigMap(ctx, t, manager.Client)
	checkReconcileSuccessful(ctx, t, r, sc, manager.Client)

	currConn := om.CurrMockedConnection
	dep, _ := currConn.ReadDeployment()
	for _, p := range dep.ProcessesCopy() {
		assert.Equal(t, p.ClusterAuthMode(), "x509")
	}
}

func TestConfigureLdapDeploymentAuthentication_WithScramAgentAuthentication(t *testing.T) {
	ctx := context.Background()
	rs := DefaultReplicaSetBuilder().
		SetName("my-rs").
		SetMembers(3).
		SetVersion("4.0.0-ent").
		EnableAuth().
		AgentAuthMode("SCRAM").
		EnableSCRAM().
		EnableLDAP().
		LDAP(
			mdbv1.Ldap{
				BindQueryUser: "bindQueryUser",
				Servers:       []string{"server0:1234", "server1:9876"},
				BindQuerySecretRef: mdbv1.SecretRef{
					Name: "bind-query-password",
				},
				TimeoutMS:                     10000,
				UserCacheInvalidationInterval: 60,
			},
		).
		Build()

	manager := mock.NewManager(ctx, rs)
	manager.Client.AddDefaultMdbConfigResources(ctx)
	r := newReplicaSetReconciler(ctx, manager, om.NewEmptyMockedOmConnection)
	data := map[string]string{
		"password": "LITZTOd6YiCV8j",
	}
	err := secret.CreateOrUpdate(ctx, r.client, secret.Builder().
		SetName("bind-query-password").
		SetNamespace(mock.TestNamespace).
		SetStringMapToData(data).
		Build(),
	)
	assert.NoError(t, err)
	checkReconcileSuccessful(ctx, t, r, rs, manager.Client)

	ac, err := om.CurrMockedConnection.ReadAutomationConfig()
	assert.NoError(t, err)
	assert.Equal(t, "LITZTOd6YiCV8j", ac.Ldap.BindQueryPassword)
	assert.Equal(t, "bindQueryUser", ac.Ldap.BindQueryUser)
	assert.Equal(t, "server0:1234,server1:9876", ac.Ldap.Servers)
	assert.Equal(t, 10000, ac.Ldap.TimeoutMS)
	assert.Equal(t, 60, ac.Ldap.UserCacheInvalidationInterval)
	assert.Contains(t, ac.Auth.DeploymentAuthMechanisms, "PLAIN")
	assert.Contains(t, ac.Auth.DeploymentAuthMechanisms, "SCRAM-SHA-256")
}

func TestConfigureLdapDeploymentAuthentication_WithCustomRole(t *testing.T) {
	ctx := context.Background()

	customRoles := []mdbv1.MongoDbRole{
		{
			Db:         "admin",
			Role:       "customRole",
			Roles:      []mdbv1.InheritedRole{{Db: "Admin", Role: "inheritedrole"}},
			Privileges: []mdbv1.Privilege{},
		},
	}

	rs := DefaultReplicaSetBuilder().
		SetName("my-rs").
		SetMembers(3).
		SetVersion("4.0.0-ent").
		EnableAuth().
		AgentAuthMode("SCRAM").
		EnableSCRAM().
		EnableLDAP().
		LDAP(
			mdbv1.Ldap{
				BindQueryUser: "bindQueryUser",
				Servers:       []string{"server0:1234"},
				BindQuerySecretRef: mdbv1.SecretRef{
					Name: "bind-query-password",
				},
			},
		).
		SetRoles(customRoles).
		Build()

	manager := mock.NewManager(ctx, rs)
	manager.Client.AddDefaultMdbConfigResources(ctx)
	r := newReplicaSetReconciler(ctx, manager, om.NewEmptyMockedOmConnection)
	data := map[string]string{
		"password": "LITZTOd6YiCV8j",
	}
	err := secret.CreateOrUpdate(ctx, r.client, secret.Builder().
		SetName("bind-query-password").
		SetNamespace(mock.TestNamespace).
		SetStringMapToData(data).
		Build(),
	)
	assert.NoError(t, err)
	checkReconcileSuccessful(ctx, t, r, rs, manager.Client)

	ac, err := om.CurrMockedConnection.ReadAutomationConfig()
	assert.NoError(t, err)
	assert.Equal(t, "server0:1234", ac.Ldap.Servers)

	roles := ac.Deployment["roles"].([]mdbv1.MongoDbRole)
	assert.Len(t, roles, 1)
	assert.Equal(t, customRoles, roles)
}

func TestConfigureLdapDeploymentAuthentication_WithAuthzQueryTemplate_AndUserToDnMapping(t *testing.T) {
	ctx := context.Background()

	userMapping := `[
                     {
 	               match: "(.+)",
                       substitution: "uid={0},dc=example,dc=org"
                     }
                   ]`
	authzTemplate := "{USER}?memberOf?base"
	rs := DefaultReplicaSetBuilder().
		SetName("my-rs").
		SetMembers(3).
		SetVersion("4.0.0-ent").
		EnableAuth().
		AgentAuthMode("SCRAM").
		EnableSCRAM().
		EnableLDAP().
		LDAP(
			mdbv1.Ldap{
				BindQueryUser: "bindQueryUser",
				Servers:       []string{"server0:0000,server1:1111,server2:2222"},
				BindQuerySecretRef: mdbv1.SecretRef{
					Name: "bind-query-password",
				},
				AuthzQueryTemplate: authzTemplate,
				UserToDNMapping:    userMapping,
			},
		).
		Build()

	manager := mock.NewManager(ctx, rs)
	manager.Client.AddDefaultMdbConfigResources(ctx)
	r := newReplicaSetReconciler(ctx, manager, om.NewEmptyMockedOmConnection)
	data := map[string]string{
		"password": "LITZTOd6YiCV8j",
	}
	err := secret.CreateOrUpdate(ctx, r.client, secret.Builder().
		SetName("bind-query-password").
		SetNamespace(mock.TestNamespace).
		SetStringMapToData(data).
		Build(),
	)
	assert.NoError(t, err)
	checkReconcileSuccessful(ctx, t, r, rs, manager.Client)

	ac, err := om.CurrMockedConnection.ReadAutomationConfig()
	assert.NoError(t, err)
	assert.Equal(t, "server0:0000,server1:1111,server2:2222", ac.Ldap.Servers)

	assert.Equal(t, authzTemplate, ac.Ldap.AuthzQueryTemplate)
	assert.Equal(t, userMapping, ac.Ldap.UserToDnMapping)
}

// addKubernetesTlsResources ensures all the required TLS secrets exist for the given MongoDB resource
func addKubernetesTlsResources(ctx context.Context, client kubernetesClient.Client, mdb *mdbv1.MongoDB) {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: mock.TestCredentialsSecretName, Namespace: mock.TestNamespace},
		Data: map[string][]byte{
			"publicApiKey": []byte("someapi"),
			"user":         []byte("someuser"),
		},
		Type: corev1.SecretTypeTLS,
	}

	_ = client.Create(ctx, secret)
	switch mdb.Spec.ResourceType {
	case mdbv1.ReplicaSet:
		createReplicaSetTLSData(ctx, client, mdb)
	case mdbv1.ShardedCluster:
		createShardedClusterTLSData(ctx, client, mdb)
	}
}

// createMockCertAndKeyBytesMulti generates a random key and certificate and returns
// them as bytes with the MongoDBMultiCluster service FQDN in the dns names of the certificate.
func createMockCertAndKeyBytesMulti(mdbm mdbmulti.MongoDBMultiCluster, clusterNum, podNum int) []byte {
	return createMockCertAndKeyBytesWithDNSName(dns.GetMultiServiceFQDN(mdbm.Name, mock.TestNamespace, clusterNum, podNum, mdbm.Spec.GetClusterDomain()))
}

func createMockCertAndKeyBytesWithDNSName(dnsName string) []byte {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		panic(err)
	}

	privBytes, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		panic(err)
	}

	serialNumberLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	serialNumber, err := rand.Int(rand.Reader, serialNumberLimit)
	if err != nil {
		panic(err)
	}

	template := x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			Organization: []string{"MongoDB"},
		},
		NotBefore:             time.Now(),
		NotAfter:              time.Now().AddDate(10, 0, 0), // cert expires in 10 years
		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		DNSNames:              []string{dnsName},
	}
	certBytes, err := x509.CreateCertificate(rand.Reader, &template, &template, &priv.PublicKey, priv)
	if err != nil {
		panic(err)
	}

	certPemBytes := &bytes.Buffer{}
	if err := pem.Encode(certPemBytes, &pem.Block{Type: "CERTIFICATE", Bytes: certBytes}); err != nil {
		panic(err)
	}

	privPemBytes := &bytes.Buffer{}
	if err := pem.Encode(privPemBytes, &pem.Block{Type: "PRIVATE KEY", Bytes: privBytes}); err != nil {
		panic(err)
	}

	return append(certPemBytes.Bytes(), privPemBytes.Bytes()...)
}

// createMockCertAndKeyBytes generates a random key and certificate and returns
// them as bytes
func createMockCertAndKeyBytes(certOpts ...func(cert *x509.Certificate)) (cert, key []byte) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		panic(err)
	}

	privBytes, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		panic(err)
	}

	serialNumberLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	serialNumber, err := rand.Int(rand.Reader, serialNumberLimit)
	if err != nil {
		panic(err)
	}

	template := x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			Organization: []string{"MongoDB"},
		},
		NotBefore:             time.Now(),
		NotAfter:              time.Now().AddDate(10, 0, 0), // cert expires in 10 years
		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		DNSNames:              []string{"somehost.com"},
	}

	for _, opt := range certOpts {
		opt(&template)
	}
	certBytes, err := x509.CreateCertificate(rand.Reader, &template, &template, &priv.PublicKey, priv)
	if err != nil {
		panic(err)
	}

	certPemBytes := &bytes.Buffer{}
	if err := pem.Encode(certPemBytes, &pem.Block{Type: "CERTIFICATE", Bytes: certBytes}); err != nil {
		panic(err)
	}

	privPemBytes := &bytes.Buffer{}
	if err := pem.Encode(privPemBytes, &pem.Block{Type: "PRIVATE KEY", Bytes: privBytes}); err != nil {
		panic(err)
	}

	return certPemBytes.Bytes(), privPemBytes.Bytes()
}

// createReplicaSetTLSData creates and populates secrets required for a TLS enabled ReplicaSet
func createReplicaSetTLSData(ctx context.Context, client kubernetesClient.Client, mdb *mdbv1.MongoDB) {
	// Lets create a secret with Certificates and private keys!
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-cert", mdb.Name),
			Namespace: mock.TestNamespace,
		},
		Type: corev1.SecretTypeTLS,
	}

	certs := map[string][]byte{}
	clientCerts := map[string][]byte{}

	certs["tls.crt"], certs["tls.key"] = createMockCertAndKeyBytes()
	clientCerts["tls.crt"], clientCerts["tls.key"] = createMockCertAndKeyBytes()
	secret.Data = certs

	_ = client.Create(ctx, secret)

	_ = client.Create(ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("%s-clusterfile", mdb.Name), Namespace: mock.TestNamespace},
		Data:       clientCerts,
		Type:       corev1.SecretTypeTLS,
	})

	agentCerts := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "agent-certs",
			Namespace: mock.TestNamespace,
		},
		Type: corev1.SecretTypeTLS,
	}

	subjectModifier := func(cert *x509.Certificate) {
		cert.Subject.OrganizationalUnit = []string{"cloud"}
		cert.Subject.Locality = []string{"New York"}
		cert.Subject.Province = []string{"New York"}
		cert.Subject.Country = []string{"US"}
	}

	agentCerts.Data = make(map[string][]byte)
	agentCerts.Data["tls.crt"], agentCerts.Data["tls.key"] = createMockCertAndKeyBytes(subjectModifier, func(cert *x509.Certificate) { cert.Subject.CommonName = "mms-automation-agent" })
	_ = client.Create(ctx, agentCerts)
}

// createShardedClusterTLSData creates and populates all the  secrets needed for a TLS enabled Sharded
// Cluster with internal cluster authentication. Mongos, config server and all shards.
func createShardedClusterTLSData(ctx context.Context, client kubernetesClient.Client, mdb *mdbv1.MongoDB) {
	// create the secrets for all the shards
	for i := 0; i < mdb.Spec.ShardCount; i++ {
		secretName := fmt.Sprintf("%s-%d-cert", mdb.Name, i)
		shardData := make(map[string][]byte)
		shardData["tls.crt"], shardData["tls.key"] = createMockCertAndKeyBytes()

		_ = client.Create(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: secretName, Namespace: mock.TestNamespace},
			Data:       shardData,
			Type:       corev1.SecretTypeTLS,
		})
		_ = client.Create(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("%s-%d-clusterfile", mdb.Name, i), Namespace: mock.TestNamespace},
			Data:       shardData,
			Type:       corev1.SecretTypeTLS,
		})
	}

	// populate with the expected cert and key fields
	mongosData := make(map[string][]byte)
	mongosData["tls.crt"], mongosData["tls.key"] = createMockCertAndKeyBytes()

	// create the mongos secret
	mongosSecretName := fmt.Sprintf("%s-mongos-cert", mdb.Name)
	_ = client.Create(ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: mongosSecretName, Namespace: mock.TestNamespace},
		Data:       mongosData,
		Type:       corev1.SecretTypeTLS,
	})

	_ = client.Create(ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("%s-mongos-clusterfile", mdb.Name), Namespace: mock.TestNamespace},
		Data:       mongosData,
		Type:       corev1.SecretTypeTLS,
	})

	// create secret for config server
	configData := make(map[string][]byte)
	configData["tls.crt"], configData["tls.key"] = createMockCertAndKeyBytes()

	_ = client.Create(ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("%s-config-cert", mdb.Name), Namespace: mock.TestNamespace},
		Data:       configData,
		Type:       corev1.SecretTypeTLS,
	})

	_ = client.Create(ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("%s-config-clusterfile", mdb.Name), Namespace: mock.TestNamespace},
		Data:       configData,
		Type:       corev1.SecretTypeTLS,
	})
	agentCerts := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "agent-certs",
			Namespace: mock.TestNamespace,
		},
		Type: corev1.SecretTypeTLS,
	}

	subjectModifier := func(cert *x509.Certificate) {
		cert.Subject.OrganizationalUnit = []string{"cloud"}
		cert.Subject.Locality = []string{"New York"}
		cert.Subject.Province = []string{"New York"}
		cert.Subject.Country = []string{"US"}
	}

	agentCerts.Data = make(map[string][]byte)
	agentCerts.Data["tls.crt"], agentCerts.Data["tls.key"] = createMockCertAndKeyBytes(subjectModifier, func(cert *x509.Certificate) { cert.Subject.CommonName = "mms-automation-agent" })
	_ = client.Create(ctx, agentCerts)
}

// createMultiClusterReplicaSetTLSData creates and populates secrets required for a TLS enabled MongoDBMultiCluster ReplicaSet.
func createMultiClusterReplicaSetTLSData(ctx context.Context, client *mock.MockedClient, mdbm *mdbmulti.MongoDBMultiCluster, caName string) {
	// Create CA configmap
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      caName,
			Namespace: mock.TestNamespace,
		},
	}
	cm.Data = map[string]string{
		"ca-pem":     "capublickey",
		"mms-ca.crt": "capublickey",
	}
	client.Create(ctx, cm)
	// Lets create a secret with Certificates and private keys!
	secretName := fmt.Sprintf("%s-cert", mdbm.Name)
	if mdbm.Spec.Security.CertificatesSecretsPrefix != "" {
		secretName = fmt.Sprintf("%s-%s", mdbm.Spec.Security.CertificatesSecretsPrefix, secretName)
	}

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretName,
			Namespace: mock.TestNamespace,
		},
	}

	certs := map[string][]byte{}
	clientCerts := map[string][]byte{}

	clusterSpecs, err := mdbm.GetClusterSpecItems()
	if err != nil {
		panic(err)
	}

	for _, item := range clusterSpecs {
		for podNum := 0; podNum < item.Members; podNum++ {
			pemFile := createMockCertAndKeyBytesMulti(*mdbm, mdbm.ClusterNum(item.ClusterName), podNum)
			certs[fmt.Sprintf("%s-%d-%d-pem", mdbm.Name, mdbm.ClusterNum(item.ClusterName), podNum)] = pemFile
			clientCerts[fmt.Sprintf("%s-%d-%d-pem", mdbm.Name, mdbm.ClusterNum(item.ClusterName), podNum)] = pemFile
		}
	}

	secret.Data = certs
	// create cert in the central cluster, the operator would create the concatenated
	// pem cert in the member clusters.
	client.Create(ctx, secret)
}

func createConfigMap(ctx context.Context, t *testing.T, client kubernetesClient.Client) {
	err := client.CreateConfigMap(ctx, configMap())
	assert.NoError(t, err)
}

func TestInvalidPEM_SecretDoesNotContainKey(t *testing.T) {
	ctx := context.Background()
	rs := DefaultReplicaSetBuilder().
		EnableTLS().
		EnableAuth().
		EnableX509().
		Build()

	manager := mock.NewManager(ctx, rs)

	reconciler := newReplicaSetReconciler(ctx, manager, om.NewEmptyMockedOmConnection)
	client := manager.Client

	addKubernetesTlsResources(ctx, client, rs)

	// Replace the secret with an empty one
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-cert", rs.Name),
			Namespace: mock.TestNamespace,
		},
		Type: corev1.SecretTypeTLS,
	}

	_ = client.Update(ctx, secret)

	err := certs.VerifyAndEnsureCertificatesForStatefulSet(ctx, reconciler.SecretClient, reconciler.SecretClient, fmt.Sprintf("%s-cert", rs.Name), certs.ReplicaSetConfig(*rs), nil)
	assert.Equal(t, err.Error(), "the certificate is not complete\n")
}

func Test_NoAdditionalDomainsPresent(t *testing.T) {
	ctx := context.Background()
	rs := DefaultReplicaSetBuilder().
		EnableTLS().
		EnableAuth().
		EnableX509().
		Build()

	// The default secret we create does not contain additional domains so it will not be valid for this RS
	rs.Spec.Security.TLSConfig.AdditionalCertificateDomains = []string{"foo"}

	manager := mock.NewManager(ctx, rs)
	reconciler := newReplicaSetReconciler(ctx, manager, om.NewEmptyMockedOmConnection)
	client := manager.Client

	addKubernetesTlsResources(ctx, client, rs)

	secret := &corev1.Secret{}

	_ = client.Get(ctx, types.NamespacedName{Name: fmt.Sprintf("%s-cert", rs.Name), Namespace: rs.Namespace}, secret)

	err := certs.VerifyAndEnsureCertificatesForStatefulSet(ctx, reconciler.SecretClient, reconciler.SecretClient, fmt.Sprintf("%s-cert", rs.Name), certs.ReplicaSetConfig(*rs), nil)
	for i := 0; i < rs.Spec.Members; i++ {
		expectedErrorMessage := fmt.Sprintf("domain %s-%d.foo is not contained in the list of DNSNames", rs.Name, i)
		assert.Contains(t, err.Error(), expectedErrorMessage)
	}
}

func Test_NoExternalDomainPresent(t *testing.T) {
	ctx := context.Background()
	rs := DefaultReplicaSetBuilder().
		EnableTLS().
		EnableAuth().
		EnableX509().
		Build()

	rs.Spec.ExternalAccessConfiguration = &mdbv1.ExternalAccessConfiguration{ExternalDomain: ptr.To("foo")}

	manager := mock.NewManager(ctx, rs)
	reconciler := newReplicaSetReconciler(ctx, manager, om.NewEmptyMockedOmConnection)
	client := manager.Client

	addKubernetesTlsResources(ctx, client, rs)

	secret := &corev1.Secret{}

	_ = client.Get(ctx, types.NamespacedName{Name: fmt.Sprintf("%s-cert", rs.Name), Namespace: rs.Namespace}, secret)

	err := certs.VerifyAndEnsureCertificatesForStatefulSet(ctx, reconciler.SecretClient, reconciler.SecretClient, fmt.Sprintf("%s-cert", rs.Name), certs.ReplicaSetConfig(*rs), nil)
	assert.Error(t, err)
}

// createAgentCSRs creates all the agent CSRs needed for x509 at the specified condition type
func createAgentCSRs(ctx context.Context, numAgents int, client kubernetesClient.Client, conditionType certsv1.RequestConditionType) {
	if numAgents != 1 && numAgents != 3 {
		return
	}
	// create the secret the agent certs will exist in
	certAuto, _ := os.ReadFile("testdata/certificates/cert_auto")

	builder := secret.Builder().
		SetNamespace(mock.TestNamespace).
		SetName(util.AgentSecretName).
		SetField(util.AutomationAgentPemSecretKey, string(certAuto))

	client.CreateSecret(ctx, builder.Build())

	addCsrs(ctx, client, createCSR("mms-automation-agent", mock.TestNamespace, conditionType))
}

func addCsrs(ctx context.Context, client kubernetesClient.Client, csrs ...certsv1.CertificateSigningRequest) {
	for _, csr := range csrs {
		_ = client.Create(ctx, &csr)
	}
}

// createCSR creates a CSR object which can be set to either CertificateApproved or CertificateDenied
func createCSR(name, ns string, conditionType certsv1.RequestConditionType) certsv1.CertificateSigningRequest {
	return certsv1.CertificateSigningRequest{
		ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("%s.%s", name, ns)},
		Spec: certsv1.CertificateSigningRequestSpec{
			Request: createMockCSRBytes(),
		},
		Status: certsv1.CertificateSigningRequestStatus{
			Conditions: []certsv1.CertificateSigningRequestCondition{
				{Type: conditionType},
			},
		},
	}
}

// createMockCSRBytes creates a new Certificate Signing Request, signed with a
// fresh private key
func createMockCSRBytes() []byte {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		panic(err)
	}

	template := x509.CertificateRequest{
		Subject: pkix.Name{
			Organization: []string{"MongoDB"},
		},
		DNSNames: []string{"somehost.com"},
	}
	certRequestBytes, err := x509.CreateCertificateRequest(rand.Reader, &template, priv)
	if err != nil {
		panic(err)
	}

	certRequestPemBytes := &bytes.Buffer{}
	pemBlock := pem.Block{Type: "CERTIFICATE REQUEST", Bytes: certRequestBytes}
	if err := pem.Encode(certRequestPemBytes, &pemBlock); err != nil {
		panic(err)
	}

	return certRequestPemBytes.Bytes()
}
