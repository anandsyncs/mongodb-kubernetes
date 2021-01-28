package construct

import (
	"os"
	"testing"

	mdbv1 "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1/mdb"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/env"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/stringutil"
	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/service"
	"github.com/stretchr/testify/assert"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestBuildStatefulSet_PersistentFlag(t *testing.T) {
	mdb := mdbv1.NewReplicaSetBuilder().SetPersistent(nil).Build()
	set := DatabaseStatefulSet(*mdb, ReplicaSetOptions())
	assert.Len(t, set.Spec.VolumeClaimTemplates, 1)
	assert.Len(t, set.Spec.Template.Spec.Containers[0].VolumeMounts, 4)

	mdb = mdbv1.NewReplicaSetBuilder().SetPersistent(util.BooleanRef(true)).Build()
	set = DatabaseStatefulSet(*mdb, ReplicaSetOptions())
	assert.Len(t, set.Spec.VolumeClaimTemplates, 1)
	assert.Len(t, set.Spec.Template.Spec.Containers[0].VolumeMounts, 4)

	// If no persistence is set then we still mount init scripts
	mdb = mdbv1.NewReplicaSetBuilder().SetPersistent(util.BooleanRef(false)).Build()
	set = DatabaseStatefulSet(*mdb, ReplicaSetOptions())
	assert.Len(t, set.Spec.VolumeClaimTemplates, 0)
	assert.Len(t, set.Spec.Template.Spec.Containers[0].VolumeMounts, 1)
}

// TestBuildStatefulSet_PersistentVolumeClaimSingle checks that one persistent volume claim is created that is mounted by
// 3 points
func TestBuildStatefulSet_PersistentVolumeClaimSingle(t *testing.T) {
	labels := map[string]string{"app": "foo"}
	persistence := mdbv1.NewPersistenceBuilder("40G").SetStorageClass("fast").SetLabelSelector(labels)
	podSpec := mdbv1.NewPodSpecWrapperBuilder().SetSinglePersistence(persistence).Build().MongoDbPodSpec
	rs := mdbv1.NewReplicaSetBuilder().SetPersistent(nil).SetPodSpec(&podSpec).Build()
	set := DatabaseStatefulSet(*rs, ReplicaSetOptions())

	checkPvClaims(t, set, []corev1.PersistentVolumeClaim{pvClaim(util.PvcNameData, "40G", stringutil.Ref("fast"), labels)})

	checkMounts(t, set, []corev1.VolumeMount{
		{Name: util.PvcNameData, MountPath: util.PvcMountPathData, SubPath: util.PvcNameData},
		{Name: util.PvcNameData, MountPath: util.PvcMountPathJournal, SubPath: util.PvcNameJournal},
		{Name: util.PvcNameData, MountPath: util.PvcMountPathLogs, SubPath: util.PvcNameLogs},
		{Name: PvcNameDatabaseScripts, MountPath: PvcMountPathScripts, ReadOnly: true},
	})
}

//TestBuildStatefulSet_PersistentVolumeClaimMultiple checks multiple volumes for multiple mounts. Note, that subpaths
//for mount points are not created (unlike in single mode)
func TestBuildStatefulSet_PersistentVolumeClaimMultiple(t *testing.T) {
	labels1 := map[string]string{"app": "bar"}
	labels2 := map[string]string{"app": "foo"}
	podSpec := mdbv1.NewPodSpecWrapperBuilder().SetMultiplePersistence(
		mdbv1.NewPersistenceBuilder("40G").SetStorageClass("fast"),
		mdbv1.NewPersistenceBuilder("3G").SetStorageClass("slow").SetLabelSelector(labels1),
		mdbv1.NewPersistenceBuilder("500M").SetStorageClass("fast").SetLabelSelector(labels2),
	).Build()

	mdb := mdbv1.NewReplicaSetBuilder().SetPersistent(nil).SetPodSpec(&podSpec.MongoDbPodSpec).Build()
	set := DatabaseStatefulSet(*mdb, ReplicaSetOptions())

	checkPvClaims(t, set, []corev1.PersistentVolumeClaim{
		pvClaim(util.PvcNameData, "40G", stringutil.Ref("fast"), nil),
		pvClaim(util.PvcNameJournal, "3G", stringutil.Ref("slow"), labels1),
		pvClaim(util.PvcNameLogs, "500M", stringutil.Ref("fast"), labels2),
	})

	checkMounts(t, set, []corev1.VolumeMount{
		{Name: util.PvcNameData, MountPath: util.PvcMountPathData},
		{Name: PvcNameDatabaseScripts, MountPath: PvcMountPathScripts, ReadOnly: true},
		{Name: util.PvcNameJournal, MountPath: util.PvcMountPathJournal},
		{Name: util.PvcNameLogs, MountPath: util.PvcMountPathLogs},
	})
}

// TestBuildStatefulSet_PersistentVolumeClaimMultipleDefaults checks the scenario when storage is provided only for one
// mount point. Default values are expected to be used for two others
func TestBuildStatefulSet_PersistentVolumeClaimMultipleDefaults(t *testing.T) {
	podSpec := mdbv1.NewPodSpecWrapperBuilder().SetMultiplePersistence(
		mdbv1.NewPersistenceBuilder("40G").SetStorageClass("fast"),
		nil,
		nil).
		Build()
	mdb := mdbv1.NewReplicaSetBuilder().SetPersistent(nil).SetPodSpec(&podSpec.MongoDbPodSpec).Build()
	set := DatabaseStatefulSet(*mdb, ReplicaSetOptions())

	checkPvClaims(t, set, []corev1.PersistentVolumeClaim{
		pvClaim(util.PvcNameData, "40G", stringutil.Ref("fast"), nil),
		pvClaim(util.PvcNameJournal, util.DefaultJournalStorageSize, nil, nil),
		pvClaim(util.PvcNameLogs, util.DefaultLogsStorageSize, nil, nil),
	})

	checkMounts(t, set, []corev1.VolumeMount{
		{Name: util.PvcNameData, MountPath: util.PvcMountPathData},
		{Name: PvcNameDatabaseScripts, MountPath: PvcMountPathScripts, ReadOnly: true},
		{Name: util.PvcNameJournal, MountPath: util.PvcMountPathJournal},
		{Name: util.PvcNameLogs, MountPath: util.PvcMountPathLogs},
	})
}

func TestBuildAppDbStatefulSetDefault(t *testing.T) {
	appDbSts := AppDbStatefulSet(defaultOpsManagerBuilder().Build())
	podSpecTemplate := appDbSts.Spec.Template.Spec
	assert.Len(t, podSpecTemplate.InitContainers, 1)
	assert.Equal(t, podSpecTemplate.InitContainers[0].Name, "mongodb-enterprise-init-appdb")
	assert.Len(t, podSpecTemplate.Containers, 1, "Should have only the db")
	assert.Equal(t, "mongodb-enterprise-appdb", podSpecTemplate.Containers[0].Name, "Database container should always be first")
}

func TestBasePodSpec_Affinity(t *testing.T) {
	nodeAffinity := defaultNodeAffinity()
	podAffinity := defaultPodAffinity()

	podSpec := mdbv1.NewPodSpecWrapperBuilder().
		SetNodeAffinity(nodeAffinity).
		SetPodAffinity(podAffinity).
		SetPodAntiAffinityTopologyKey("nodeId").
		Build()
	mdb := mdbv1.NewReplicaSetBuilder().
		SetName("s").
		SetPodSpec(&podSpec.MongoDbPodSpec).
		Build()
	sts := DatabaseStatefulSet(
		*mdb, ReplicaSetOptions(),
	)

	spec := sts.Spec.Template.Spec
	assert.Equal(t, nodeAffinity, *spec.Affinity.NodeAffinity)
	assert.Equal(t, podAffinity, *spec.Affinity.PodAffinity)
	assert.Len(t, spec.Affinity.PodAntiAffinity.PreferredDuringSchedulingIgnoredDuringExecution, 1)
	assert.Len(t, spec.Affinity.PodAntiAffinity.RequiredDuringSchedulingIgnoredDuringExecution, 0)
	term := spec.Affinity.PodAntiAffinity.PreferredDuringSchedulingIgnoredDuringExecution[0]
	assert.Equal(t, int32(100), term.Weight)
	assert.Equal(t, map[string]string{PodAntiAffinityLabelKey: "s"}, term.PodAffinityTerm.LabelSelector.MatchLabels)
	assert.Equal(t, "nodeId", term.PodAffinityTerm.TopologyKey)
}

// TestBasePodSpec_AntiAffinityDefaultTopology checks that the default topology key is created if the topology key is
// not specified
func TestBasePodSpec_AntiAffinityDefaultTopology(t *testing.T) {
	sts := DatabaseStatefulSet(*mdbv1.NewStandaloneBuilder().SetName("my-standalone").Build(), StandaloneOptions())

	spec := sts.Spec.Template.Spec
	term := spec.Affinity.PodAntiAffinity.PreferredDuringSchedulingIgnoredDuringExecution[0]
	assert.Equal(t, int32(100), term.Weight)
	assert.Equal(t, map[string]string{PodAntiAffinityLabelKey: "my-standalone"}, term.PodAffinityTerm.LabelSelector.MatchLabels)
	assert.Equal(t, util.DefaultAntiAffinityTopologyKey, term.PodAffinityTerm.TopologyKey)
}

// TestBasePodSpec_ImagePullSecrets verifies that 'spec.ImagePullSecrets' is created only if env variable
// IMAGE_PULL_SECRETS is initialized
func TestBasePodSpec_ImagePullSecrets(t *testing.T) {
	// Cleaning the state (there is no tear down in go test :( )
	defer InitDefaultEnvVariables()

	sts := DatabaseStatefulSet(*mdbv1.NewStandaloneBuilder().Build(), StandaloneOptions())

	template := sts.Spec.Template
	assert.Nil(t, template.Spec.ImagePullSecrets)

	_ = os.Setenv(util.ImagePullSecrets, "foo")

	sts = DatabaseStatefulSet(*mdbv1.NewStandaloneBuilder().Build(), StandaloneOptions())

	template = sts.Spec.Template
	assert.Equal(t, []corev1.LocalObjectReference{{Name: "foo"}}, template.Spec.ImagePullSecrets)

}

// TestBasePodSpec_TerminationGracePeriodSeconds verifies that the TerminationGracePeriodSeconds is set to 600 seconds
func TestBasePodSpec_TerminationGracePeriodSeconds(t *testing.T) {
	sts := DatabaseStatefulSet(*mdbv1.NewReplicaSetBuilder().Build(), ReplicaSetOptions())
	assert.Equal(t, util.Int64Ref(600), sts.Spec.Template.Spec.TerminationGracePeriodSeconds)
}

func checkPvClaims(t *testing.T, set appsv1.StatefulSet, expectedClaims []corev1.PersistentVolumeClaim) {
	assert.Len(t, set.Spec.VolumeClaimTemplates, len(expectedClaims))

	for i, c := range expectedClaims {
		assert.Equal(t, c, set.Spec.VolumeClaimTemplates[i])
	}
}
func checkMounts(t *testing.T, set appsv1.StatefulSet, expectedMounts []corev1.VolumeMount) {
	assert.Len(t, set.Spec.Template.Spec.Containers[0].VolumeMounts, len(expectedMounts))

	for i, c := range expectedMounts {
		assert.Equal(t, c, set.Spec.Template.Spec.Containers[0].VolumeMounts[i])
	}
}

func pvClaim(pvName, size string, storageClass *string, labels map[string]string) corev1.PersistentVolumeClaim {
	quantity, _ := resource.ParseQuantity(size)
	expectedClaim := corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name: pvName,
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceStorage: quantity},
			},
			StorageClassName: storageClass,
		}}
	if len(labels) > 0 {
		expectedClaim.Spec.Selector = &metav1.LabelSelector{MatchLabels: labels}
	}
	return expectedClaim
}

func TestDefaultPodSpec_FsGroup(t *testing.T) {
	defer InitDefaultEnvVariables()

	sts := DatabaseStatefulSet(*mdbv1.NewStandaloneBuilder().Build(), StandaloneOptions())

	spec := sts.Spec.Template.Spec
	assert.Len(t, spec.InitContainers, 1)
	assert.NotNil(t, spec.SecurityContext)
	assert.Equal(t, util.Int64Ref(util.FsGroup), spec.SecurityContext.FSGroup)

	_ = os.Setenv(util.ManagedSecurityContextEnv, "true")

	sts = DatabaseStatefulSet(*mdbv1.NewStandaloneBuilder().Build(), StandaloneOptions())
	assert.Nil(t, sts.Spec.Template.Spec.SecurityContext)
	// TODO: assert the container security context
}

func TestPodSpec_Requirements(t *testing.T) {
	podSpec := mdbv1.NewPodSpecWrapperBuilder().
		SetCpuRequests("0.1").
		SetMemoryRequest("512M").
		SetCpu("0.3").
		SetMemory("1012M").
		Build()

	sts := DatabaseStatefulSet(*mdbv1.NewReplicaSetBuilder().SetPodSpec(&podSpec.MongoDbPodSpec).Build(), ReplicaSetOptions())

	podSpecTemplate := sts.Spec.Template
	container := podSpecTemplate.Spec.Containers[0]
	expectedLimits := corev1.ResourceList{corev1.ResourceCPU: ParseQuantityOrZero("0.3"), corev1.ResourceMemory: ParseQuantityOrZero("1012M")}
	expectedRequests := corev1.ResourceList{corev1.ResourceCPU: ParseQuantityOrZero("0.1"), corev1.ResourceMemory: ParseQuantityOrZero("512M")}
	assert.Equal(t, expectedLimits, container.Resources.Limits)
	assert.Equal(t, expectedRequests, container.Resources.Requests)
}

func TestService_merge0(t *testing.T) {
	dst := corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "my-service", Namespace: "my-namespace"}}
	src := corev1.Service{}
	dst = service.Merge(dst, src)
	assert.Equal(t, "my-service", dst.ObjectMeta.Name)
	assert.Equal(t, "my-namespace", dst.ObjectMeta.Namespace)

	// Name and Namespace will not be copied over.
	src = corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "new-service", Namespace: "new-namespace"}}
	dst = service.Merge(dst, src)
	assert.Equal(t, "my-service", dst.ObjectMeta.Name)
	assert.Equal(t, "my-namespace", dst.ObjectMeta.Namespace)
}

func TestService_NodePortIsNotOverwritten(t *testing.T) {
	dst := corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "my-service", Namespace: "my-namespace"},
		Spec:       corev1.ServiceSpec{Ports: []corev1.ServicePort{{NodePort: 30030}}},
	}
	src := corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "my-service", Namespace: "my-namespace"},
		Spec:       corev1.ServiceSpec{},
	}

	dst = service.Merge(dst, src)
	assert.Equal(t, int32(30030), dst.Spec.Ports[0].NodePort)
}

func TestService_NodePortIsNotOverwrittenIfNoNodePortIsSpecified(t *testing.T) {
	dst := corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "my-service", Namespace: "my-namespace"},
		Spec:       corev1.ServiceSpec{Ports: []corev1.ServicePort{{NodePort: 30030}}},
	}
	src := corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "my-service", Namespace: "my-namespace"},
		Spec:       corev1.ServiceSpec{Ports: []corev1.ServicePort{{}}},
	}

	dst = service.Merge(dst, src)
	assert.Equal(t, int32(30030), dst.Spec.Ports[0].NodePort)
}

func TestService_NodePortIsKeptWhenChangingServiceType(t *testing.T) {
	dst := corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "my-service", Namespace: "my-namespace"},
		Spec: corev1.ServiceSpec{
			Ports: []corev1.ServicePort{{NodePort: 30030}},
			Type:  corev1.ServiceTypeLoadBalancer,
		},
	}
	src := corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "my-service", Namespace: "my-namespace"},
		Spec: corev1.ServiceSpec{
			Ports: []corev1.ServicePort{{NodePort: 30099}},
			Type:  corev1.ServiceTypeNodePort,
		},
	}
	dst = service.Merge(dst, src)
	assert.Equal(t, int32(30099), dst.Spec.Ports[0].NodePort)

	src = corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "my-service", Namespace: "my-namespace"},
		Spec: corev1.ServiceSpec{
			Ports: []corev1.ServicePort{{NodePort: 30011}},
			Type:  corev1.ServiceTypeLoadBalancer,
		},
	}

	dst = service.Merge(dst, src)
	assert.Equal(t, int32(30011), dst.Spec.Ports[0].NodePort)
}

func TestService_mergeAnnotations(t *testing.T) {
	// Annotations will be added
	annotationsDest := make(map[string]string)
	annotationsDest["annotation0"] = "value0"
	annotationsDest["annotation1"] = "value1"
	annotationsSrc := make(map[string]string)
	annotationsSrc["annotation0"] = "valueXXXX"
	annotationsSrc["annotation2"] = "value2"

	src := corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: annotationsSrc,
		},
	}
	dst := corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name: "my-service", Namespace: "my-namespace",
			Annotations: annotationsDest,
		},
	}
	dst = service.Merge(dst, src)
	assert.Len(t, dst.ObjectMeta.Annotations, 3)
	assert.Equal(t, dst.ObjectMeta.Annotations["annotation0"], "valueXXXX")
}

func defaultPodVars() *env.PodEnvVars {
	return &env.PodEnvVars{BaseURL: "http://localhost:8080", ProjectID: "myProject", User: "user@some.com"}
}
