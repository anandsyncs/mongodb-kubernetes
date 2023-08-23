package multicluster

import (
	"fmt"
	"os"
	"testing"

	mdbc "github.com/mongodb/mongodb-kubernetes-operator/api/v1"

	"github.com/10gen/ops-manager-kubernetes/api/v1/mdb"
	"github.com/10gen/ops-manager-kubernetes/api/v1/mdbmulti"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/construct"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/mock"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"github.com/stretchr/testify/assert"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/pointer"
)

func init() {
	mock.InitDefaultEnvVariables()
}

func getMultiClusterMongoDB() mdbmulti.MongoDBMultiCluster {
	spec := mdbmulti.MongoDBMultiSpec{
		DbCommonSpec: mdb.DbCommonSpec{
			Version: "5.0.0",
			ConnectionSpec: mdb.ConnectionSpec{
				SharedConnectionSpec: mdb.SharedConnectionSpec{
					OpsManagerConfig: &mdb.PrivateCloudConfig{
						ConfigMapRef: mdb.ConfigMapRef{
							Name: mock.TestProjectConfigMapName,
						},
					},
				}, Credentials: mock.TestCredentialsSecretName,
			},
			ResourceType: mdb.ReplicaSet,
			Security: &mdb.Security{
				TLSConfig: &mdb.TLSConfig{},
				Authentication: &mdb.Authentication{
					Modes: []mdb.AuthMode{},
				},
				Roles: []mdb.MongoDbRole{},
			},
		},
		ClusterSpecList: []mdb.ClusterSpecItem{
			{
				ClusterName: "foo",
				Members:     3,
			},
		},
	}

	return mdbmulti.MongoDBMultiCluster{Spec: spec, ObjectMeta: metav1.ObjectMeta{Name: "pod-aff", Namespace: mock.TestNamespace}}
}

func TestMultiClusterStatefulSet(t *testing.T) {

	t.Run("No override provided", func(t *testing.T) {
		mdbm := getMultiClusterMongoDB()
		opts := MultiClusterReplicaSetOptions(
			WithClusterNum(0),
			WithMemberCount(3),
			construct.GetPodEnvOptions(),
		)
		sts := MultiClusterStatefulSet(mdbm, opts)

		expectedReplicas := mdbm.Spec.ClusterSpecList[0].Members
		assert.Equal(t, expectedReplicas, int(*sts.Spec.Replicas))

	})

	t.Run("Override provided at clusterSpecList level only", func(t *testing.T) {
		singleClusterOverride := &mdbc.StatefulSetConfiguration{SpecWrapper: mdbc.StatefulSetSpecWrapper{
			Spec: appsv1.StatefulSetSpec{
				Replicas: pointer.Int32(int32(4)),
				Selector: &metav1.LabelSelector{
					MatchLabels: map[string]string{"foo": "bar"},
				},
			},
		}}

		mdbm := getMultiClusterMongoDB()
		mdbm.Spec.ClusterSpecList[0].StatefulSetConfiguration = singleClusterOverride

		opts := MultiClusterReplicaSetOptions(
			WithClusterNum(0),
			WithMemberCount(3),
			construct.GetPodEnvOptions(),
			WithStsOverride(&singleClusterOverride.SpecWrapper.Spec),
		)

		sts := MultiClusterStatefulSet(mdbm, opts)

		expectedMatchLabels := singleClusterOverride.SpecWrapper.Spec.Selector.MatchLabels
		expectedMatchLabels["app"] = ""
		expectedMatchLabels["pod-anti-affinity"] = mdbm.Name
		expectedMatchLabels["controller"] = "mongodb-enterprise-operator"

		assert.Equal(t, singleClusterOverride.SpecWrapper.Spec.Replicas, sts.Spec.Replicas)
		assert.Equal(t, expectedMatchLabels, sts.Spec.Selector.MatchLabels)

	})

	t.Run("Override provided only at Spec level", func(t *testing.T) {
		stsOverride := &mdbc.StatefulSetConfiguration{SpecWrapper: mdbc.StatefulSetSpecWrapper{Spec: appsv1.StatefulSetSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"foo": "bar"},
			},
			ServiceName: "overrideservice",
		},
		},
		}

		mdbm := getMultiClusterMongoDB()
		mdbm.Spec.StatefulSetConfiguration = stsOverride
		opts := MultiClusterReplicaSetOptions(
			WithClusterNum(0),
			WithMemberCount(3),
			construct.GetPodEnvOptions(),
		)

		sts := MultiClusterStatefulSet(mdbm, opts)

		expectedReplicas := mdbm.Spec.ClusterSpecList[0].Members
		assert.Equal(t, expectedReplicas, int(*sts.Spec.Replicas))

		assert.Equal(t, stsOverride.SpecWrapper.Spec.ServiceName, sts.Spec.ServiceName)

	})

	t.Run("Override provided at both Spec and clusterSpecList level", func(t *testing.T) {

		stsOverride := &mdbc.StatefulSetConfiguration{SpecWrapper: mdbc.StatefulSetSpecWrapper{Spec: appsv1.StatefulSetSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"foo": "bar"},
			},
			ServiceName: "overrideservice",
		},
		},
		}

		singleClusterOverride := &mdbc.StatefulSetConfiguration{SpecWrapper: mdbc.StatefulSetSpecWrapper{
			Spec: appsv1.StatefulSetSpec{
				ServiceName: "clusteroverrideservice",
				Replicas:    pointer.Int32(int32(4)),
			},
		},
		}

		mdbm := getMultiClusterMongoDB()
		mdbm.Spec.StatefulSetConfiguration = stsOverride

		opts := MultiClusterReplicaSetOptions(
			WithClusterNum(0),
			WithMemberCount(3),
			construct.GetPodEnvOptions(),
			WithStsOverride(&singleClusterOverride.SpecWrapper.Spec),
		)

		sts := MultiClusterStatefulSet(mdbm, opts)

		assert.Equal(t, singleClusterOverride.SpecWrapper.Spec.ServiceName, sts.Spec.ServiceName)
		assert.Equal(t, singleClusterOverride.SpecWrapper.Spec.Replicas, sts.Spec.Replicas)
	})
}

func Test_MultiClusterStatefulSetWithRelatedImages(t *testing.T) {
	databaseRelatedImageEnv := fmt.Sprintf("RELATED_IMAGE_%s_1_0_0", util.AutomationAgentImage)
	initDatabaseRelatedImageEnv := fmt.Sprintf("RELATED_IMAGE_%s_2_0_0", util.InitDatabaseImageUrlEnv)

	t.Setenv(util.AutomationAgentImage, "quay.io/mongodb/mongodb-enterprise-database")
	t.Setenv(construct.DatabaseVersionEnv, "1.0.0")
	t.Setenv(util.InitDatabaseImageUrlEnv, "quay.io/mongodb/mongodb-enterprise-init-database")
	t.Setenv(construct.InitDatabaseVersionEnv, "2.0.0")
	t.Setenv(databaseRelatedImageEnv, "quay.io/mongodb/mongodb-enterprise-database:@sha256:MONGODB_DATABASE")
	t.Setenv(initDatabaseRelatedImageEnv, "quay.io/mongodb/mongodb-enterprise-init-database:@sha256:MONGODB_INIT_DATABASE")

	mdbm := getMultiClusterMongoDB()
	opts := MultiClusterReplicaSetOptions(
		WithClusterNum(0),
		WithMemberCount(3),
		construct.GetPodEnvOptions(),
	)

	sts := MultiClusterStatefulSet(mdbm, opts)

	assert.Equal(t, "quay.io/mongodb/mongodb-enterprise-init-database:@sha256:MONGODB_INIT_DATABASE", sts.Spec.Template.Spec.InitContainers[0].Image)
	assert.Equal(t, "quay.io/mongodb/mongodb-enterprise-database:@sha256:MONGODB_DATABASE", sts.Spec.Template.Spec.Containers[0].Image)
}

func TestPVCOverride(t *testing.T) {

	tests := []struct {
		inp appsv1.StatefulSetSpec
		out struct {
			Storage    int64
			AccessMode []corev1.PersistentVolumeAccessMode
		}
	}{
		{
			inp: appsv1.StatefulSetSpec{
				VolumeClaimTemplates: []corev1.PersistentVolumeClaim{
					{
						ObjectMeta: metav1.ObjectMeta{
							Name: "data",
						},
						Spec: corev1.PersistentVolumeClaimSpec{
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceStorage: construct.ParseQuantityOrZero("20"),
								},
							},
							AccessModes: []corev1.PersistentVolumeAccessMode{},
						},
					},
				},
			},
			out: struct {
				Storage    int64
				AccessMode []corev1.PersistentVolumeAccessMode
			}{
				Storage:    20,
				AccessMode: []corev1.PersistentVolumeAccessMode{"ReadWriteOnce"},
			},
		},
		{
			inp: appsv1.StatefulSetSpec{
				VolumeClaimTemplates: []corev1.PersistentVolumeClaim{
					{
						ObjectMeta: metav1.ObjectMeta{
							Name: "data",
						},
						Spec: corev1.PersistentVolumeClaimSpec{
							AccessModes: []corev1.PersistentVolumeAccessMode{"ReadWriteMany"},
						},
					},
				},
			},
			out: struct {
				Storage    int64
				AccessMode []corev1.PersistentVolumeAccessMode
			}{
				Storage:    16000000000,
				AccessMode: []corev1.PersistentVolumeAccessMode{"ReadWriteOnce", "ReadWriteMany"},
			},
		},
	}

	os.Setenv(util.AutomationAgentImage, "some-registry")
	os.Setenv(util.InitDatabaseImageUrlEnv, "some-registry")

	for _, tt := range tests {
		mdbm := getMultiClusterMongoDB()

		stsOverrideConfiguration := &mdbc.StatefulSetConfiguration{SpecWrapper: mdbc.StatefulSetSpecWrapper{Spec: tt.inp}}
		opts := MultiClusterReplicaSetOptions(
			WithClusterNum(0),
			WithMemberCount(3),
			construct.GetPodEnvOptions(),
			WithStsOverride(&stsOverrideConfiguration.SpecWrapper.Spec),
		)
		sts := MultiClusterStatefulSet(mdbm, opts)
		assert.Equal(t, tt.out.AccessMode, sts.Spec.VolumeClaimTemplates[0].Spec.AccessModes)
		storage, _ := sts.Spec.VolumeClaimTemplates[0].Spec.Resources.Requests.Storage().AsInt64()
		assert.Equal(t, tt.out.Storage, storage)
	}
}
