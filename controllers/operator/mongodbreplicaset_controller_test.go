package operator

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/10gen/ops-manager-kubernetes/pkg/util/architectures"

	"github.com/10gen/ops-manager-kubernetes/controllers/om/deployment"

	"github.com/stretchr/testify/require"
	"golang.org/x/xerrors"

	mdbcv1 "github.com/mongodb/mongodb-kubernetes-operator/api/v1"

	"github.com/10gen/ops-manager-kubernetes/controllers/om/backup"
	"github.com/google/uuid"

	"github.com/10gen/ops-manager-kubernetes/controllers/operator/construct"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/pem"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/watch"

	"github.com/10gen/ops-manager-kubernetes/pkg/kube"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/versionutil"

	"github.com/10gen/ops-manager-kubernetes/controllers/operator/authentication"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/controlledfeature"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/mock"

	mdbv1 "github.com/10gen/ops-manager-kubernetes/api/v1/mdb"
	"github.com/10gen/ops-manager-kubernetes/controllers/om"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

type ReplicaSetBuilder struct {
	*mdbv1.MongoDB
}

func TestCreateReplicaSet(t *testing.T) {
	ctx := context.Background()
	rs := DefaultReplicaSetBuilder().Build()

	reconciler, client := defaultReplicaSetReconciler(ctx, rs)

	checkReconcileSuccessful(ctx, t, reconciler, rs, client)

	assert.Len(t, client.GetMapForObject(&corev1.Service{}), 1)
	assert.Len(t, client.GetMapForObject(&appsv1.StatefulSet{}), 1)
	assert.Len(t, client.GetMapForObject(&corev1.Secret{}), 2)

	sts, err := client.GetStatefulSet(ctx, rs.ObjectKey())
	assert.NoError(t, err)
	assert.Equal(t, *sts.Spec.Replicas, int32(3))

	connection := om.CurrMockedConnection
	connection.CheckDeployment(t, deployment.CreateFromReplicaSet(rs), "auth", "ssl")
	connection.CheckNumberOfUpdateRequests(t, 2)
}

func TestReplicaSetRace(t *testing.T) {
	ctx := context.Background()
	rs := DefaultReplicaSetBuilder().Build()
	rs2 := DefaultReplicaSetBuilder().SetName("my-rs2").Build()
	rs3 := DefaultReplicaSetBuilder().SetName("my-rs3").Build()
	reconciler, client := defaultReplicaSetReconcilerWithoutSingleton(ctx, rs)

	testConcurrentReconciles(ctx, t, client, reconciler, rs, rs2, rs3)
}

func TestReplicaSetServiceName(t *testing.T) {
	ctx := context.Background()
	rs := DefaultReplicaSetBuilder().SetService("rs-svc").Build()
	rs.Spec.StatefulSetConfiguration = &mdbcv1.StatefulSetConfiguration{}
	rs.Spec.StatefulSetConfiguration.SpecWrapper.Spec.ServiceName = "foo"

	reconciler, client := defaultReplicaSetReconciler(ctx, rs)

	checkReconcileSuccessful(ctx, t, reconciler, rs, client)
	assert.Equal(t, "foo", rs.ServiceName())
	_, err := client.GetService(ctx, kube.ObjectKey(rs.Namespace, rs.ServiceName()))
	assert.NoError(t, err)
}

func TestHorizonVerificationTLS(t *testing.T) {
	ctx := context.Background()
	replicaSetHorizons := []mdbv1.MongoDBHorizonConfig{
		{"my-horizon": "my-db.com:12345"},
		{"my-horizon": "my-db.com:12346"},
		{"my-horizon": "my-db.com:12347"},
	}
	rs := DefaultReplicaSetBuilder().SetReplicaSetHorizons(replicaSetHorizons).Build()

	reconciler, client := defaultReplicaSetReconciler(ctx, rs)

	msg := "TLS must be enabled in order to use replica set horizons"
	checkReconcileFailed(ctx, t, reconciler, rs, false, msg, client)
}

func TestHorizonVerificationCount(t *testing.T) {
	ctx := context.Background()
	replicaSetHorizons := []mdbv1.MongoDBHorizonConfig{
		{"my-horizon": "my-db.com:12345"},
		{"my-horizon": "my-db.com:12346"},
	}
	rs := DefaultReplicaSetBuilder().
		EnableTLS().
		SetReplicaSetHorizons(replicaSetHorizons).
		Build()

	reconciler, client := defaultReplicaSetReconciler(ctx, rs)

	msg := "Number of horizons must be equal to number of members in replica set"
	checkReconcileFailed(ctx, t, reconciler, rs, false, msg, client)
}

// TestScaleUpReplicaSet verifies scaling up for replica set. Statefulset and OM Deployment must be changed accordingly
func TestScaleUpReplicaSet(t *testing.T) {
	ctx := context.Background()
	rs := DefaultReplicaSetBuilder().SetMembers(3).Build()

	reconciler, client := defaultReplicaSetReconciler(ctx, rs)

	checkReconcileSuccessful(ctx, t, reconciler, rs, client)
	set := &appsv1.StatefulSet{}
	_ = client.Get(ctx, mock.ObjectKeyFromApiObject(rs), set)

	// Now scale up to 5 nodes
	rs = DefaultReplicaSetBuilder().SetMembers(5).Build()
	_ = client.Update(ctx, rs)

	checkReconcileSuccessful(ctx, t, reconciler, rs, client)

	updatedSet := &appsv1.StatefulSet{}
	_ = client.Get(ctx, mock.ObjectKeyFromApiObject(rs), updatedSet)

	// Statefulset is expected to be the same - only number of replicas changed
	set.Spec.Replicas = util.Int32Ref(int32(5))
	assert.Equal(t, set.Spec, updatedSet.Spec)

	connection := om.CurrMockedConnection
	connection.CheckDeployment(t, deployment.CreateFromReplicaSet(rs), "auth", "tls")
	connection.CheckNumberOfUpdateRequests(t, 4)
}

func TestExposedExternallyReplicaSet(t *testing.T) {
	ctx := context.Background()
	// given
	rs := DefaultReplicaSetBuilder().SetMembers(3).ExposedExternally(nil, nil, nil).Build()

	reconciler, client := defaultReplicaSetReconciler(ctx, rs)

	// when
	checkReconcileSuccessful(ctx, t, reconciler, rs, client)

	// then
	// We removed support for single external service named <replicaset-name>-svc-external (round-robin to all pods).
	externalService := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{},
	}
	err := client.Get(ctx, types.NamespacedName{Name: rs.Name + "-svc-external", Namespace: rs.Namespace}, externalService)
	assert.Error(t, err)

	for podNum := 0; podNum < 3; podNum++ {
		err := client.Get(ctx, types.NamespacedName{Name: fmt.Sprintf("%s-%d-svc-external", rs.Name, podNum), Namespace: rs.Namespace}, externalService)
		assert.NoError(t, err)

		assert.NoError(t, err)
		assert.Equal(t, corev1.ServiceTypeLoadBalancer, externalService.Spec.Type)
		assert.Len(t, externalService.Spec.Ports, 1)
		assert.Equal(t, "mongodb", externalService.Spec.Ports[0].Name)
		assert.Equal(t, 27017, externalService.Spec.Ports[0].TargetPort.IntValue())
	}

	processes := om.CurrMockedConnection.GetProcesses()
	require.Len(t, processes, 3)
	// check hostnames are pod's headless service FQDNs
	for i, process := range processes {
		assert.Equal(t, fmt.Sprintf("%s-%d.%s-svc.%s.svc.cluster.local", rs.Name, i, rs.Name, rs.Namespace), process.HostName())
	}
}

func TestExposedExternallyReplicaSetExternalDomainInHostnames(t *testing.T) {
	ctx := context.Background()
	externalDomain := "example.com"
	memberCount := 3
	replicaSetName := "rs"
	var expectedHostnames []string
	for i := 0; i < memberCount; i++ {
		expectedHostnames = append(expectedHostnames, fmt.Sprintf("%s-%d.%s", replicaSetName, i, externalDomain))
	}

	testExposedExternallyReplicaSetExternalDomainInHostnames(ctx, t, replicaSetName, memberCount, externalDomain, expectedHostnames)
}

func testExposedExternallyReplicaSetExternalDomainInHostnames(ctx context.Context, t *testing.T, replicaSetName string, memberCount int, externalDomain string, expectedHostnames []string) {
	rs := DefaultReplicaSetBuilder().SetName(replicaSetName).SetMembers(memberCount).ExposedExternally(nil, nil, &externalDomain).Build()
	reconciler, client := defaultReplicaSetReconciler(ctx, rs)

	// We set this to mock processes that agents are registering in OM, otherwise reconcile will hang on agent.WaitForRsAgentsToRegister.
	// hostnames are already mocked in controllers/operator/mock/mockedkubeclient.go::markStatefulSetsReady,
	// but we don't have externalDomain in statefulset there, hence we're setting them here
	om.CurrMockedConnection = om.NewMockedOmConnection(nil)
	om.CurrMockedConnection.Hostnames = expectedHostnames

	checkReconcileSuccessful(ctx, t, reconciler, rs, client)

	processes := om.CurrMockedConnection.GetProcesses()
	require.Len(t, processes, memberCount)
	// check hostnames are external domain
	for i, process := range processes {
		// process.HostName is created when building automation config using resource spec
		assert.Equal(t, expectedHostnames[i], process.HostName())
	}
}

func TestExposedExternallyReplicaSetWithNodePort(t *testing.T) {
	ctx := context.Background()
	// given
	rs := DefaultReplicaSetBuilder().
		SetMembers(3).
		ExposedExternally(
			&corev1.ServiceSpec{
				Type: corev1.ServiceTypeNodePort,
			},
			map[string]string{"test": "test"},
			nil).
		Build()

	reconciler, client := defaultReplicaSetReconciler(ctx, rs)

	// when
	checkReconcileSuccessful(ctx, t, reconciler, rs, client)
	externalService := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{},
	}

	// then
	for podNum := 0; podNum < 3; podNum++ {
		err := client.Get(ctx, types.NamespacedName{Name: fmt.Sprintf("%s-%d-svc-external", rs.Name, podNum), Namespace: rs.Namespace}, externalService)
		assert.NoError(t, err)

		assert.NoError(t, err)
		assert.Equal(t, corev1.ServiceTypeNodePort, externalService.Spec.Type)
		assert.Len(t, externalService.Spec.Ports, 1)
		assert.Equal(t, "mongodb", externalService.Spec.Ports[0].Name)
		assert.Equal(t, 27017, externalService.Spec.Ports[0].TargetPort.IntValue())
	}
}

func TestCreateReplicaSet_TLS(t *testing.T) {
	ctx := context.Background()
	rs := DefaultReplicaSetBuilder().SetMembers(3).EnableTLS().SetTLSCA("custom-ca").Build()

	reconciler, client := defaultReplicaSetReconciler(ctx, rs)
	addKubernetesTlsResources(ctx, client, rs)
	client.ApproveAllCSRs(ctx)
	checkReconcileSuccessful(ctx, t, reconciler, rs, client)

	processes := om.CurrMockedConnection.GetProcesses()
	assert.Len(t, processes, 3)
	for _, v := range processes {
		assert.NotNil(t, v.TLSConfig())
		assert.Len(t, v.TLSConfig(), 2)
		assert.Equal(t, fmt.Sprintf("%s/%s", util.TLSCertMountPath, pem.ReadHashFromSecret(ctx, reconciler.SecretClient, rs.Namespace, fmt.Sprintf("%s-cert", rs.Name), "", zap.S())), v.TLSConfig()["certificateKeyFile"])
		assert.Equal(t, "requireTLS", v.TLSConfig()["mode"])
	}

	sslConfig := om.CurrMockedConnection.GetTLS()
	assert.Equal(t, fmt.Sprintf("%s/%s", util.TLSCaMountPath, "ca-pem"), sslConfig["CAFilePath"])
	assert.Equal(t, "OPTIONAL", sslConfig["clientCertificateMode"])
}

// TestUpdateDeploymentTLSConfiguration a combination of tests checking that:
//
// TLS Disabled -> TLS Disabled: should not lock members
// TLS Disabled -> TLS Enabled: should not lock members
// TLS Enabled -> TLS Enabled: should not lock members
// TLS Enabled -> TLS Disabled: *should lock members*
func TestUpdateDeploymentTLSConfiguration(t *testing.T) {
	rsWithTLS := mdbv1.NewReplicaSetBuilder().SetSecurityTLSEnabled().Build()
	rsNoTLS := mdbv1.NewReplicaSetBuilder().Build()
	deploymentWithTLS := deployment.CreateFromReplicaSet(rsWithTLS)
	deploymentNoTLS := deployment.CreateFromReplicaSet(rsNoTLS)
	stsWithTLS := construct.DatabaseStatefulSet(*rsWithTLS, construct.ReplicaSetOptions(construct.GetPodEnvOptions()), nil)
	stsNoTLS := construct.DatabaseStatefulSet(*rsNoTLS, construct.ReplicaSetOptions(construct.GetPodEnvOptions()), nil)

	// TLS Disabled -> TLS Disabled
	shouldLockMembers, err := updateOmDeploymentDisableTLSConfiguration(om.NewMockedOmConnection(deploymentNoTLS), 3, rsNoTLS, stsNoTLS, zap.S(), util.CAFilePathInContainer)
	assert.NoError(t, err)
	assert.False(t, shouldLockMembers)

	// TLS Disabled -> TLS Enabled
	shouldLockMembers, err = updateOmDeploymentDisableTLSConfiguration(om.NewMockedOmConnection(deploymentNoTLS), 3, rsWithTLS, stsWithTLS, zap.S(), util.CAFilePathInContainer)
	assert.NoError(t, err)
	assert.False(t, shouldLockMembers)

	// TLS Enabled -> TLS Enabled
	shouldLockMembers, err = updateOmDeploymentDisableTLSConfiguration(om.NewMockedOmConnection(deploymentWithTLS), 3, rsWithTLS, stsWithTLS, zap.S(), util.CAFilePathInContainer)
	assert.NoError(t, err)
	assert.False(t, shouldLockMembers)

	// TLS Enabled -> TLS Disabled
	shouldLockMembers, err = updateOmDeploymentDisableTLSConfiguration(om.NewMockedOmConnection(deploymentWithTLS), 3, rsNoTLS, stsNoTLS, zap.S(), util.CAFilePathInContainer)
	assert.NoError(t, err)
	assert.True(t, shouldLockMembers)
}

// TestCreateDeleteReplicaSet checks that no state is left in OpsManager on removal of the replicaset
func TestCreateDeleteReplicaSet(t *testing.T) {
	ctx := context.Background()
	// First we need to create a replicaset
	rs := DefaultReplicaSetBuilder().Build()

	reconciler, client := defaultReplicaSetReconciler(ctx, rs)

	checkReconcileSuccessful(ctx, t, reconciler, rs, client)
	omConn := om.CurrMockedConnection
	omConn.CleanHistory()

	// Now delete it
	assert.NoError(t, reconciler.OnDelete(ctx, rs, zap.S()))

	// Operator doesn't mutate K8s state, so we don't check its changes, only OM
	omConn.CheckResourcesDeleted(t)

	omConn.CheckOrderOfOperations(t,
		reflect.ValueOf(omConn.ReadUpdateDeployment), reflect.ValueOf(omConn.ReadAutomationStatus),
		reflect.ValueOf(omConn.GetHosts), reflect.ValueOf(omConn.RemoveHost))
}

func TestX509IsNotEnabledWithOlderVersionsOfOpsManager(t *testing.T) {
	ctx := context.Background()
	rs := DefaultReplicaSetBuilder().EnableAuth().EnableTLS().SetTLSCA("custom-ca").SetAuthModes([]mdbv1.AuthMode{util.X509}).Build()
	reconciler, client := defaultReplicaSetReconciler(ctx, rs)
	reconciler.omConnectionFactory = func(context *om.OMContext) om.Connection {
		conn := om.NewEmptyMockedOmConnection(context)

		// make the mocked connection return an error behaving as an older version of Ops Manager
		conn.(*om.MockedOmConnection).UpdateMonitoringAgentConfigFunc = func(mac *om.MonitoringAgentConfig, log *zap.SugaredLogger) (bytes []byte, e error) {
			return nil, xerrors.Errorf("some error. Detail: %s", util.MethodNotAllowed)
		}
		return conn
	}

	addKubernetesTlsResources(ctx, client, rs)
	checkReconcileFailed(ctx, t, reconciler, rs, true, "unable to configure X509 with this version of Ops Manager", client)
}

func TestReplicaSetScramUpgradeDowngrade(t *testing.T) {
	ctx := context.Background()
	rs := DefaultReplicaSetBuilder().SetVersion("4.0.0").EnableAuth().SetAuthModes([]mdbv1.AuthMode{"SCRAM"}).Build()

	reconciler, client := defaultReplicaSetReconciler(ctx, rs)

	checkReconcileSuccessful(ctx, t, reconciler, rs, client)

	ac, _ := om.CurrMockedConnection.ReadAutomationConfig()
	assert.Contains(t, ac.Auth.AutoAuthMechanisms, string(authentication.ScramSha256))

	// downgrade to version that will not use SCRAM-SHA-256
	rs.Spec.Version = "3.6.9"

	_ = client.Update(ctx, rs)

	checkReconcileFailed(ctx, t, reconciler, rs, false, "Unable to downgrade to SCRAM-SHA-1 when SCRAM-SHA-256 has been enabled", client)
}

func TestReplicaSetCustomPodSpecTemplate(t *testing.T) {
	ctx := context.Background()
	podSpec := corev1.PodSpec{
		NodeName: "some-node-name",
		Hostname: "some-host-name",
		Containers: []corev1.Container{{
			Name:  "my-custom-container",
			Image: "my-custom-image",
			VolumeMounts: []corev1.VolumeMount{{
				Name: "my-volume-mount",
			}},
		}},
		RestartPolicy: corev1.RestartPolicyAlways,
	}

	rs := DefaultReplicaSetBuilder().EnableTLS().SetTLSCA("custom-ca").SetPodSpecTemplate(corev1.PodTemplateSpec{
		Spec: podSpec,
	}).Build()

	reconciler, client := defaultReplicaSetReconciler(ctx, rs)

	addKubernetesTlsResources(ctx, client, rs)

	checkReconcileSuccessful(ctx, t, reconciler, rs, client)

	// read the stateful set that was created by the operator
	statefulSet, err := client.GetStatefulSet(ctx, mock.ObjectKeyFromApiObject(rs))
	assert.NoError(t, err)

	assertPodSpecSts(t, &statefulSet, podSpec.NodeName, podSpec.Hostname, podSpec.RestartPolicy)

	podSpecTemplate := statefulSet.Spec.Template.Spec
	assert.Len(t, podSpecTemplate.Containers, 2, "Should have 2 containers now")
	assert.Equal(t, util.DatabaseContainerName, podSpecTemplate.Containers[0].Name, "Database container should always be first")
	assert.Equal(t, "my-custom-container", podSpecTemplate.Containers[1].Name, "Custom container should be second")
}

func TestReplicaSetCustomPodSpecTemplateStatic(t *testing.T) {
	ctx := context.Background()
	t.Setenv(architectures.DefaultEnvArchitecture, string(architectures.Static))

	podSpec := corev1.PodSpec{
		NodeName: "some-node-name",
		Hostname: "some-host-name",
		Containers: []corev1.Container{{
			Name:  "my-custom-container",
			Image: "my-custom-image",
			VolumeMounts: []corev1.VolumeMount{{
				Name: "my-volume-mount",
			}},
		}},
		RestartPolicy: corev1.RestartPolicyAlways,
	}

	rs := DefaultReplicaSetBuilder().EnableTLS().SetTLSCA("custom-ca").SetPodSpecTemplate(corev1.PodTemplateSpec{
		Spec: podSpec,
	}).Build()

	reconciler, client := defaultReplicaSetReconciler(ctx, rs)

	addKubernetesTlsResources(ctx, client, rs)

	checkReconcileSuccessful(ctx, t, reconciler, rs, client)

	// read the stateful set that was created by the operator
	statefulSet, err := client.GetStatefulSet(ctx, mock.ObjectKeyFromApiObject(rs))
	assert.NoError(t, err)

	assertPodSpecSts(t, &statefulSet, podSpec.NodeName, podSpec.Hostname, podSpec.RestartPolicy)

	podSpecTemplate := statefulSet.Spec.Template.Spec
	assert.Len(t, podSpecTemplate.Containers, 3, "Should have 3 containers now")
	assert.Equal(t, util.AgentContainerName, podSpecTemplate.Containers[0].Name, "Database container should always be first")
	assert.Equal(t, "my-custom-container", podSpecTemplate.Containers[2].Name, "Custom container should be second")
}

func TestFeatureControlPolicyAndTagAddedWithNewerOpsManager(t *testing.T) {
	ctx := context.Background()
	rs := DefaultReplicaSetBuilder().Build()

	reconciler, client := defaultReplicaSetReconciler(ctx, rs)
	reconciler.omConnectionFactory = func(context *om.OMContext) om.Connection {
		context.Version = versionutil.OpsManagerVersion{
			VersionString: "5.0.0",
		}
		conn := om.NewEmptyMockedOmConnection(context)
		return conn
	}

	checkReconcileSuccessful(ctx, t, reconciler, rs, client)

	mockedConn := om.CurrMockedConnection
	cf, _ := mockedConn.GetControlledFeature()

	assert.Len(t, cf.Policies, 3)
	assert.Equal(t, cf.ManagementSystem.Version, util.OperatorVersion)
	assert.Equal(t, cf.ManagementSystem.Name, util.OperatorName)

	project := mockedConn.FindGroup("my-project")
	assert.Contains(t, project.Tags, util.OmGroupExternallyManagedTag)
}

func TestFeatureControlPolicyNoAuthNewerOpsManager(t *testing.T) {
	ctx := context.Background()
	rsBuilder := DefaultReplicaSetBuilder()
	rsBuilder.Spec.Security = nil

	rs := rsBuilder.Build()

	reconciler, client := defaultReplicaSetReconciler(ctx, rs)
	reconciler.omConnectionFactory = func(context *om.OMContext) om.Connection {
		context.Version = versionutil.OpsManagerVersion{
			VersionString: "5.0.0",
		}
		conn := om.NewEmptyMockedOmConnection(context)
		return conn
	}

	checkReconcileSuccessful(ctx, t, reconciler, rs, client)

	mockedConn := om.CurrMockedConnection
	cf, _ := mockedConn.GetControlledFeature()

	assert.Len(t, cf.Policies, 2)
	assert.Equal(t, cf.ManagementSystem.Version, util.OperatorVersion)
	assert.Equal(t, cf.ManagementSystem.Name, util.OperatorName)
	assert.Equal(t, cf.Policies[0].PolicyType, controlledfeature.ExternallyManaged)
	assert.Equal(t, cf.Policies[1].PolicyType, controlledfeature.DisableMongodVersion)
	assert.Len(t, cf.Policies[0].DisabledParams, 0)
}

func TestScalingScalesOneMemberAtATime_WhenScalingDown(t *testing.T) {
	ctx := context.Background()
	rs := DefaultReplicaSetBuilder().SetMembers(5).Build()
	reconciler, client := defaultReplicaSetReconciler(ctx, rs)
	// perform initial reconciliation so we are not creating a new resource
	checkReconcileSuccessful(ctx, t, reconciler, rs, client)

	// scale down from 5 to 3 members
	rs.Spec.Members = 3

	err := client.Update(ctx, rs)
	assert.NoError(t, err)

	res, err := reconciler.Reconcile(ctx, requestFromObject(rs))

	assert.NoError(t, err)
	assert.Equal(t, time.Duration(10000000000), res.RequeueAfter, "Scaling from 5 -> 4 should enqueue another reconciliation")

	assertCorrectNumberOfMembersAndProcesses(ctx, t, 4, rs, client, "We should have updated the status with the intermediate value of 4")

	res, err = reconciler.Reconcile(ctx, requestFromObject(rs))
	assert.NoError(t, err)
	assert.Equal(t, util.TWENTY_FOUR_HOURS, res.RequeueAfter, "Once we reach the target value, we should not scale anymore")

	assertCorrectNumberOfMembersAndProcesses(ctx, t, 3, rs, client, "The members should now be set to the final desired value")
}

func TestScalingScalesOneMemberAtATime_WhenScalingUp(t *testing.T) {
	ctx := context.Background()
	rs := DefaultReplicaSetBuilder().SetMembers(1).Build()
	reconciler, client := defaultReplicaSetReconciler(ctx, rs)
	// perform initial reconciliation so we are not creating a new resource
	checkReconcileSuccessful(ctx, t, reconciler, rs, client)

	// scale up from 1 to 3 members
	rs.Spec.Members = 3

	err := client.Update(ctx, rs)
	assert.NoError(t, err)

	res, err := reconciler.Reconcile(ctx, requestFromObject(rs))
	assert.NoError(t, err)

	assert.Equal(t, time.Duration(10000000000), res.RequeueAfter, "Scaling from 1 -> 3 should enqueue another reconciliation")

	assertCorrectNumberOfMembersAndProcesses(ctx, t, 2, rs, client, "We should have updated the status with the intermediate value of 2")

	res, err = reconciler.Reconcile(ctx, requestFromObject(rs))
	assert.NoError(t, err)

	assertCorrectNumberOfMembersAndProcesses(ctx, t, 3, rs, client, "Once we reach the target value, we should not scale anymore")
}

func TestReplicaSetPortIsConfigurable_WithAdditionalMongoConfig(t *testing.T) {
	ctx := context.Background()
	config := mdbv1.NewAdditionalMongodConfig("net.port", 30000)
	rs := mdbv1.NewReplicaSetBuilder().
		SetNamespace(mock.TestNamespace).
		SetAdditionalConfig(config).
		SetConnectionSpec(testConnectionSpec()).
		Build()

	reconciler, client := defaultReplicaSetReconciler(ctx, rs)

	checkReconcileSuccessful(ctx, t, reconciler, rs, client)

	svc, err := client.GetService(ctx, kube.ObjectKey(rs.Namespace, rs.ServiceName()))
	assert.NoError(t, err)
	assert.Equal(t, int32(30000), svc.Spec.Ports[0].Port)
}

// TestReplicaSet_ConfigMapAndSecretWatched verifies that config map and secret are added to the internal
// map that allows to watch them for changes
func TestReplicaSet_ConfigMapAndSecretWatched(t *testing.T) {
	ctx := context.Background()
	rs := DefaultReplicaSetBuilder().Build()

	reconciler, client := defaultReplicaSetReconciler(ctx, rs)

	checkReconcileSuccessful(ctx, t, reconciler, rs, client)

	expected := map[watch.Object][]types.NamespacedName{
		{ResourceType: watch.ConfigMap, Resource: kube.ObjectKey(mock.TestNamespace, mock.TestProjectConfigMapName)}: {kube.ObjectKey(mock.TestNamespace, rs.Name)},
		{ResourceType: watch.Secret, Resource: kube.ObjectKey(mock.TestNamespace, rs.Spec.Credentials)}:              {kube.ObjectKey(mock.TestNamespace, rs.Name)},
	}

	assert.Equal(t, reconciler.resourceWatcher.GetWatchedResources(), expected)
}

// TestTLSResourcesAreWatchedAndUnwatched verifies that TLS config map and secret are added to the internal
// map that allows to watch them for changes
func TestTLSResourcesAreWatchedAndUnwatched(t *testing.T) {
	ctx := context.Background()
	rs := DefaultReplicaSetBuilder().EnableTLS().SetTLSCA("custom-ca").Build()

	reconciler, client := defaultReplicaSetReconciler(ctx, rs)

	addKubernetesTlsResources(ctx, client, rs)
	checkReconcileSuccessful(ctx, t, reconciler, rs, client)

	expected := map[watch.Object][]types.NamespacedName{
		{ResourceType: watch.ConfigMap, Resource: kube.ObjectKey(mock.TestNamespace, mock.TestProjectConfigMapName)}: {kube.ObjectKey(mock.TestNamespace, rs.Name)},
		{ResourceType: watch.Secret, Resource: kube.ObjectKey(mock.TestNamespace, rs.Spec.Credentials)}:              {kube.ObjectKey(mock.TestNamespace, rs.Name)},
		{ResourceType: watch.ConfigMap, Resource: kube.ObjectKey(mock.TestNamespace, "custom-ca")}:                   {kube.ObjectKey(mock.TestNamespace, rs.Name)},
		{ResourceType: watch.Secret, Resource: kube.ObjectKey(mock.TestNamespace, rs.GetName()+"-cert")}:             {kube.ObjectKey(mock.TestNamespace, rs.Name)},
	}

	assert.Equal(t, reconciler.resourceWatcher.GetWatchedResources(), expected)

	rs.Spec.Security.TLSConfig.Enabled = false
	checkReconcileSuccessful(ctx, t, reconciler, rs, client)

	expected = map[watch.Object][]types.NamespacedName{
		{ResourceType: watch.ConfigMap, Resource: kube.ObjectKey(mock.TestNamespace, mock.TestProjectConfigMapName)}: {kube.ObjectKey(mock.TestNamespace, rs.Name)},
		{ResourceType: watch.Secret, Resource: kube.ObjectKey(mock.TestNamespace, rs.Spec.Credentials)}:              {kube.ObjectKey(mock.TestNamespace, rs.Name)},
	}

	assert.Equal(t, reconciler.resourceWatcher.GetWatchedResources(), expected)
}

func TestBackupConfiguration_ReplicaSet(t *testing.T) {
	ctx := context.Background()
	rs := mdbv1.NewReplicaSetBuilder().
		SetNamespace(mock.TestNamespace).
		SetConnectionSpec(testConnectionSpec()).
		SetBackup(mdbv1.Backup{
			Mode: "enabled",
		}).
		Build()

	reconciler, client := defaultReplicaSetReconciler(ctx, rs)

	uuidStr := uuid.New().String()
	// configure backup for this project in Ops Manager in the mocked connection
	om.CurrMockedConnection = om.NewMockedOmConnection(om.NewDeployment())
	om.CurrMockedConnection.UpdateBackupConfig(&backup.Config{
		ClusterId: uuidStr,
		Status:    backup.Inactive,
	})

	// add corresponding host cluster.
	om.CurrMockedConnection.BackupHostClusters[uuidStr] = &backup.HostCluster{
		ReplicaSetName: rs.Name,
		ClusterName:    rs.Name,
		TypeName:       "REPLICA_SET",
	}

	t.Run("Backup can be started", func(t *testing.T) {
		checkReconcileSuccessful(ctx, t, reconciler, rs, client)

		configResponse, _ := om.CurrMockedConnection.ReadBackupConfigs()
		assert.Len(t, configResponse.Configs, 1)

		config := configResponse.Configs[0]

		assert.Equal(t, backup.Started, config.Status)
		assert.Equal(t, uuidStr, config.ClusterId)
		assert.Equal(t, "PRIMARY", config.SyncSource)
	})

	t.Run("Backup snapshot schedule tests", backupSnapshotScheduleTests(rs, client, reconciler, uuidStr))

	t.Run("Backup can be stopped", func(t *testing.T) {
		rs.Spec.Backup.Mode = "disabled"
		err := client.Update(ctx, rs)
		assert.NoError(t, err)

		checkReconcileSuccessful(ctx, t, reconciler, rs, client)

		configResponse, _ := om.CurrMockedConnection.ReadBackupConfigs()
		assert.Len(t, configResponse.Configs, 1)

		config := configResponse.Configs[0]

		assert.Equal(t, backup.Stopped, config.Status)
		assert.Equal(t, uuidStr, config.ClusterId)
		assert.Equal(t, "PRIMARY", config.SyncSource)
	})

	t.Run("Backup can be terminated", func(t *testing.T) {
		rs.Spec.Backup.Mode = "terminated"
		err := client.Update(ctx, rs)
		assert.NoError(t, err)

		checkReconcileSuccessful(ctx, t, reconciler, rs, client)

		configResponse, _ := om.CurrMockedConnection.ReadBackupConfigs()
		assert.Len(t, configResponse.Configs, 1)

		config := configResponse.Configs[0]

		assert.Equal(t, backup.Terminating, config.Status)
		assert.Equal(t, uuidStr, config.ClusterId)
		assert.Equal(t, "PRIMARY", config.SyncSource)
	})
}

func TestReplicaSetAgentVersionMapping(t *testing.T) {
	ctx := context.Background()
	defaultResource := DefaultReplicaSetBuilder().Build()
	// Go couldn't infer correctly that *ReconcileMongoDbReplicaset implemented *reconciler.Reconciler interface
	// without this anonymous function
	reconcilerFactory := func(rs *mdbv1.MongoDB) (reconcile.Reconciler, *mock.MockedClient) {
		// Call the original defaultReplicaSetReconciler, which returns a *ReconcileMongoDbReplicaSet that implements reconcile.Reconciler
		reconciler, mockClient := defaultReplicaSetReconciler(ctx, rs)
		// Return the reconciler as is, because it implements the reconcile.Reconciler interface
		return reconciler, mockClient
	}
	defaultResources := testReconciliationResources{
		Resource:          defaultResource,
		ReconcilerFactory: reconcilerFactory,
	}

	containers := []corev1.Container{{Name: util.AgentContainerName, Image: "foo"}}
	podTemplate := corev1.PodTemplateSpec{
		Spec: corev1.PodSpec{
			Containers: containers,
		},
	}

	overridenResource := DefaultReplicaSetBuilder().SetPodSpecTemplate(podTemplate).Build()
	overridenResources := testReconciliationResources{
		Resource:          overridenResource,
		ReconcilerFactory: reconcilerFactory,
	}

	agentVersionMappingTest(ctx, t, defaultResources, overridenResources)
}

// assertCorrectNumberOfMembersAndProcesses ensures that both the mongodb resource and the Ops Manager deployment
// have the correct number of processes/replicas at each stage of the scaling operation
func assertCorrectNumberOfMembersAndProcesses(ctx context.Context, t *testing.T, expected int, mdb *mdbv1.MongoDB, client *mock.MockedClient, msg string) {
	err := client.Get(ctx, mdb.ObjectKey(), mdb)
	assert.NoError(t, err)
	assert.Equal(t, expected, mdb.Status.Members, msg)
	dep, err := om.CurrMockedConnection.ReadDeployment()
	assert.NoError(t, err)
	assert.Len(t, dep.ProcessesCopy(), expected)
}

// defaultReplicaSetReconciler is the replica set reconciler used in unit test. It "adds" necessary
// additional K8s objects (rs, connection config map and secrets) necessary for reconciliation
// so it's possible to call 'reconcileAppDB()' on it right away
func defaultReplicaSetReconciler(ctx context.Context, rs *mdbv1.MongoDB) (*ReconcileMongoDbReplicaSet, *mock.MockedClient) {
	return replicaSetReconcilerWithConnection(ctx, rs, om.NewEmptyMockedOmConnection)
}

func defaultReplicaSetReconcilerWithoutSingleton(ctx context.Context, rs *mdbv1.MongoDB) (*ReconcileMongoDbReplicaSet, *mock.MockedClient) {
	return replicaSetReconcilerWithConnection(ctx, rs, om.NewEmptyMockedOmConnectionNoSingleton)
}

func replicaSetReconcilerWithConnection(ctx context.Context, rs *mdbv1.MongoDB, connectionFunc func(ctx *om.OMContext) om.Connection) (*ReconcileMongoDbReplicaSet, *mock.MockedClient) {
	manager := mock.NewManager(ctx, rs)
	manager.Client.AddDefaultMdbConfigResources(ctx)

	return newReplicaSetReconciler(ctx, manager, connectionFunc), manager.Client
}

// newDefaultPodSpec creates pod spec with default values,sets only the topology key and persistence sizes,
// seems we shouldn't set CPU and Memory if they are not provided by user
func newDefaultPodSpec() mdbv1.MongoDbPodSpec {
	podSpecWrapper := mdbv1.NewEmptyPodSpecWrapperBuilder().
		SetSinglePersistence(mdbv1.NewPersistenceBuilder(util.DefaultMongodStorageSize)).
		SetMultiplePersistence(mdbv1.NewPersistenceBuilder(util.DefaultMongodStorageSize),
			mdbv1.NewPersistenceBuilder(util.DefaultJournalStorageSize),
			mdbv1.NewPersistenceBuilder(util.DefaultLogsStorageSize)).
		Build()

	return podSpecWrapper.MongoDbPodSpec
}

// TODO remove in favor of '/api/mongodbbuilder.go'
func DefaultReplicaSetBuilder() *ReplicaSetBuilder {
	podSpec := newDefaultPodSpec()
	spec := mdbv1.MongoDbSpec{
		DbCommonSpec: mdbv1.DbCommonSpec{
			Version:    "4.0.0",
			Persistent: util.BooleanRef(false),
			ConnectionSpec: mdbv1.ConnectionSpec{
				SharedConnectionSpec: mdbv1.SharedConnectionSpec{
					OpsManagerConfig: &mdbv1.PrivateCloudConfig{
						ConfigMapRef: mdbv1.ConfigMapRef{
							Name: mock.TestProjectConfigMapName,
						},
					},
				},
				Credentials: mock.TestCredentialsSecretName,
			},
			ResourceType: mdbv1.ReplicaSet,
			Security: &mdbv1.Security{
				TLSConfig:      &mdbv1.TLSConfig{},
				Authentication: &mdbv1.Authentication{},
				Roles:          []mdbv1.MongoDbRole{},
			},
		},
		Members: 3,
		PodSpec: &podSpec,
	}
	rs := &mdbv1.MongoDB{Spec: spec, ObjectMeta: metav1.ObjectMeta{Name: "temple", Namespace: mock.TestNamespace}}
	return &ReplicaSetBuilder{rs}
}

func (b *ReplicaSetBuilder) SetName(name string) *ReplicaSetBuilder {
	b.Name = name
	return b
}

func (b *ReplicaSetBuilder) SetVersion(version string) *ReplicaSetBuilder {
	b.Spec.Version = version
	return b
}

func (b *ReplicaSetBuilder) SetPersistent(p *bool) *ReplicaSetBuilder {
	b.Spec.Persistent = p
	return b
}

func (b *ReplicaSetBuilder) SetPodSpec(podSpec *mdbv1.MongoDbPodSpec) *ReplicaSetBuilder {
	b.Spec.PodSpec = podSpec
	return b
}

func (b *ReplicaSetBuilder) SetMembers(m int) *ReplicaSetBuilder {
	b.Spec.Members = m
	return b
}

func (b *ReplicaSetBuilder) SetSecurity(security mdbv1.Security) *ReplicaSetBuilder {
	b.Spec.Security = &security
	return b
}

func (b *ReplicaSetBuilder) SetService(name string) *ReplicaSetBuilder {
	b.Spec.Service = name
	return b
}

func (b *ReplicaSetBuilder) SetAuthentication(auth *mdbv1.Authentication) *ReplicaSetBuilder {
	if b.Spec.Security == nil {
		b.Spec.Security = &mdbv1.Security{}
	}
	b.Spec.Security.Authentication = auth
	return b
}

func (b *ReplicaSetBuilder) SetRoles(roles []mdbv1.MongoDbRole) *ReplicaSetBuilder {
	if b.Spec.Security == nil {
		b.Spec.Security = &mdbv1.Security{}
	}
	b.Spec.Security.Roles = roles
	return b
}

func (b *ReplicaSetBuilder) EnableAuth() *ReplicaSetBuilder {
	b.Spec.Security.Authentication.Enabled = true
	return b
}

func (b *ReplicaSetBuilder) AgentAuthMode(agentMode string) *ReplicaSetBuilder {
	if b.Spec.Security == nil {
		b.Spec.Security = &mdbv1.Security{}
	}

	if b.Spec.Security.Authentication == nil {
		b.Spec.Security.Authentication = &mdbv1.Authentication{}
	}
	b.Spec.Security.Authentication.Agents = mdbv1.AgentAuthentication{Mode: agentMode}
	return b
}

func (b *ReplicaSetBuilder) LDAP(ldap mdbv1.Ldap) *ReplicaSetBuilder {
	b.Spec.Security.Authentication.Ldap = &ldap
	return b
}

func (b *ReplicaSetBuilder) SetAuthModes(modes []mdbv1.AuthMode) *ReplicaSetBuilder {
	b.Spec.Security.Authentication.Modes = modes
	return b
}

func (b *ReplicaSetBuilder) EnableX509InternalClusterAuth() *ReplicaSetBuilder {
	b.Spec.Security.Authentication.InternalCluster = util.X509
	return b
}

func (b *ReplicaSetBuilder) SetReplicaSetHorizons(replicaSetHorizons []mdbv1.MongoDBHorizonConfig) *ReplicaSetBuilder {
	if b.Spec.Connectivity == nil {
		b.Spec.Connectivity = &mdbv1.MongoDBConnectivity{}
	}
	b.Spec.Connectivity.ReplicaSetHorizons = replicaSetHorizons
	return b
}

func (b *ReplicaSetBuilder) EnableTLS() *ReplicaSetBuilder {
	if b.Spec.Security == nil || b.Spec.Security.TLSConfig == nil {
		b.SetSecurity(mdbv1.Security{TLSConfig: &mdbv1.TLSConfig{}})
	}
	b.Spec.Security.TLSConfig.Enabled = true
	return b
}

func (b *ReplicaSetBuilder) SetTLSCA(ca string) *ReplicaSetBuilder {
	if b.Spec.Security == nil || b.Spec.Security.TLSConfig == nil {
		b.SetSecurity(mdbv1.Security{TLSConfig: &mdbv1.TLSConfig{}})
	}
	b.Spec.Security.TLSConfig.CA = ca
	return b
}

func (b *ReplicaSetBuilder) EnableX509() *ReplicaSetBuilder {
	b.Spec.Security.Authentication.Enabled = true
	b.Spec.Security.Authentication.Modes = append(b.Spec.Security.Authentication.Modes, util.X509)
	return b
}

func (b *ReplicaSetBuilder) EnableSCRAM() *ReplicaSetBuilder {
	b.Spec.Security.Authentication.Enabled = true
	b.Spec.Security.Authentication.Modes = append(b.Spec.Security.Authentication.Modes, util.SCRAM)
	return b
}

func (b *ReplicaSetBuilder) EnableLDAP() *ReplicaSetBuilder {
	b.Spec.Security.Authentication.Enabled = true
	b.Spec.Security.Authentication.Modes = append(b.Spec.Security.Authentication.Modes, util.LDAP)
	return b
}

func (b *ReplicaSetBuilder) SetPodSpecTemplate(spec corev1.PodTemplateSpec) *ReplicaSetBuilder {
	if b.Spec.PodSpec == nil {
		b.Spec.PodSpec = &mdbv1.MongoDbPodSpec{}
	}
	b.Spec.PodSpec.PodTemplateWrapper.PodTemplate = &spec
	return b
}

func (b *ReplicaSetBuilder) Build() *mdbv1.MongoDB {
	b.InitDefaults()
	return b.MongoDB.DeepCopy()
}

func (b *ReplicaSetBuilder) ExposedExternally(specOverride *corev1.ServiceSpec, annotationsOverride map[string]string, externalDomain *string) *ReplicaSetBuilder {
	b.Spec.ExternalAccessConfiguration = &mdbv1.ExternalAccessConfiguration{}
	b.Spec.ExternalAccessConfiguration.ExternalDomain = externalDomain
	if specOverride != nil {
		b.Spec.ExternalAccessConfiguration.ExternalService.SpecWrapper = &mdbv1.ServiceSpecWrapper{Spec: *specOverride}
	}
	if len(annotationsOverride) > 0 {
		b.Spec.ExternalAccessConfiguration.ExternalService.Annotations = annotationsOverride
	}
	return b
}
