package construct

import (
	"fmt"
	"os"
	"path"
	"testing"
	"time"

	"github.com/mongodb/mongodb-kubernetes-operator/controllers/construct"

	"github.com/10gen/ops-manager-kubernetes/controllers/operator/mock"

	"github.com/10gen/ops-manager-kubernetes/pkg/util/env"

	mdbv1 "github.com/10gen/ops-manager-kubernetes/api/v1/mdb"

	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"
	corev1 "k8s.io/api/core/v1"
)

func init() {
	logger, _ := zap.NewDevelopment()
	zap.ReplaceGlobals(logger)
	mock.InitDefaultEnvVariables()
}

func Test_buildDatabaseInitContainer(t *testing.T) {
	tag := env.ReadOrDefault(InitDatabaseVersionEnv, "latest")
	modification := buildDatabaseInitContainer()
	container := &corev1.Container{}
	modification(container)
	expectedVolumeMounts := []corev1.VolumeMount{{
		Name:      PvcNameDatabaseScripts,
		MountPath: PvcMountPathScripts,
		ReadOnly:  false,
	}}
	expectedContainer := &corev1.Container{
		Name:         InitDatabaseContainerName,
		Image:        "quay.io/mongodb/mongodb-enterprise-init-database:" + tag,
		VolumeMounts: expectedVolumeMounts,
	}
	assert.Equal(t, expectedContainer, container)

}

func TestStatefulsetCreationPanicsIfEnvVariablesAreNotSet(t *testing.T) {
	t.Run("Empty Agent Image", func(t *testing.T) {
		defer mock.InitDefaultEnvVariables()
		os.Setenv(util.AutomationAgentImage, "")
		rs := mdbv1.NewReplicaSetBuilder().Build()
		assert.Panics(t, func() {
			DatabaseStatefulSet(*rs, ReplicaSetOptions(GetPodEnvOptions()), nil)
		})
	})

	t.Run("Empty Image Pull Policy", func(t *testing.T) {
		defer mock.InitDefaultEnvVariables()
		os.Setenv(util.AutomationAgentImagePullPolicy, "")
		sc := mdbv1.NewClusterBuilder().Build()
		assert.Panics(t, func() {
			DatabaseStatefulSet(*sc, ShardOptions(0), nil)
		})
		assert.Panics(t, func() {
			DatabaseStatefulSet(*sc, ConfigServerOptions(), nil)
		})
		assert.Panics(t, func() {
			DatabaseStatefulSet(*sc, MongosOptions(), nil)
		})
	})
}

func TestStatefulsetCreationSuccessful(t *testing.T) {
	start := time.Now()
	rs := mdbv1.NewReplicaSetBuilder().Build()

	_ = DatabaseStatefulSet(*rs, ReplicaSetOptions(GetPodEnvOptions()), nil)
	assert.True(t, time.Since(start) < time.Second*4) // we waited only a little (considering 2 seconds of wait as well)
}

func TestDatabaseEnvVars(t *testing.T) {
	envVars := defaultPodVars()
	opts := DatabaseStatefulSetOptions{PodVars: envVars}
	podEnv := databaseEnvVars(opts)
	assert.Len(t, podEnv, 5)

	envVars = defaultPodVars()
	envVars.SSLRequireValidMMSServerCertificates = true
	opts = DatabaseStatefulSetOptions{PodVars: envVars}

	podEnv = databaseEnvVars(opts)
	assert.Len(t, podEnv, 6)
	assert.Equal(t, podEnv[5], corev1.EnvVar{
		Name:  util.EnvVarSSLRequireValidMMSCertificates,
		Value: "true",
	})

	envVars = defaultPodVars()
	envVars.SSLMMSCAConfigMap = "custom-ca"
	v := &caVolumeSource{}
	extraEnvs := v.GetEnvs()

	opts = DatabaseStatefulSetOptions{PodVars: envVars, ExtraEnvs: extraEnvs}
	trustedCACertLocation := path.Join(caCertMountPath, util.CaCertMMS)
	podEnv = databaseEnvVars(opts)
	assert.Len(t, podEnv, 6)
	assert.Equal(t, podEnv[5], corev1.EnvVar{
		Name:  util.EnvVarSSLTrustedMMSServerCertificate,
		Value: trustedCACertLocation,
	})

	envVars = defaultPodVars()
	envVars.SSLRequireValidMMSServerCertificates = true
	envVars.SSLMMSCAConfigMap = "custom-ca"
	opts = DatabaseStatefulSetOptions{PodVars: envVars, ExtraEnvs: extraEnvs}
	podEnv = databaseEnvVars(opts)
	assert.Len(t, podEnv, 7)
	assert.Equal(t, podEnv[6], corev1.EnvVar{
		Name:  util.EnvVarSSLTrustedMMSServerCertificate,
		Value: trustedCACertLocation,
	})
	assert.Equal(t, podEnv[5], corev1.EnvVar{
		Name:  util.EnvVarSSLRequireValidMMSCertificates,
		Value: "true",
	})

}

func TestAgentFlags(t *testing.T) {
	agentStartupParameters := mdbv1.StartupParameters{
		"Key1": "Value1",
		"Key2": "Value2",
	}

	mdb := mdbv1.NewReplicaSetBuilder().SetAgentConfig(mdbv1.AgentConfig{StartupParameters: agentStartupParameters}).Build()
	sts := DatabaseStatefulSet(*mdb, ReplicaSetOptions(GetPodEnvOptions()), nil)
	variablesMap := env.ToMap(sts.Spec.Template.Spec.Containers[0].Env...)
	val, ok := variablesMap["AGENT_FLAGS"]
	assert.True(t, ok)
	assert.Contains(t, val, "-Key1,Value1", "-Key2,Value2")

}

func TestLabelsAndAnotations(t *testing.T) {
	labels := map[string]string{"l1": "val1", "l2": "val2"}
	annotations := map[string]string{"a1": "val1", "a2": "val2"}

	mdb := mdbv1.NewReplicaSetBuilder().SetAnnotations(annotations).SetLabels(labels).Build()
	sts := DatabaseStatefulSet(*mdb, ReplicaSetOptions(GetPodEnvOptions()), nil)

	// add the default label to the map
	labels["app"] = "test-mdb-svc"
	assert.Equal(t, labels, sts.Labels)
}

func TestReplaceImageTagOrDigestToTag(t *testing.T) {
	assert.Equal(t, "quay.io/mongodb/mongodb-agent:9876-54321", replaceImageTagOrDigestToTag("quay.io/mongodb/mongodb-agent:1234-567", "9876-54321"))
	assert.Equal(t, "docker.io/mongodb/mongodb-enterprise-server:9876-54321", replaceImageTagOrDigestToTag("docker.io/mongodb/mongodb-enterprise-server:1234-567", "9876-54321"))
	assert.Equal(t, "quay.io/mongodb/mongodb-agent:9876-54321", replaceImageTagOrDigestToTag("quay.io/mongodb/mongodb-agent@sha256:6a82abae27c1ba1133f3eefaad71ea318f8fa87cc57fe9355d6b5b817ff97f1a", "9876-54321"))
	assert.Equal(t, "quay.io/mongodb/mongodb-enterprise-database:some-tag", replaceImageTagOrDigestToTag("quay.io/mongodb/mongodb-enterprise-database:45678", "some-tag"))
	assert.Equal(t, "quay.io:3000/mongodb/mongodb-enterprise-database:some-tag", replaceImageTagOrDigestToTag("quay.io:3000/mongodb/mongodb-enterprise-database:45678", "some-tag"))
}

func TestContainerImage(t *testing.T) {
	initDatabaseRelatedImageEnv1 := fmt.Sprintf("RELATED_IMAGE_%s_1_0_0", InitDatabaseVersionEnv)
	initDatabaseRelatedImageEnv2 := fmt.Sprintf("RELATED_IMAGE_%s_12_0_4_7554_1", InitDatabaseVersionEnv)
	initDatabaseRelatedImageEnv3 := fmt.Sprintf("RELATED_IMAGE_%s_2_0_0_b20220912000000", InitDatabaseVersionEnv)

	t.Setenv(InitDatabaseVersionEnv, "quay.io/mongodb/mongodb-enterprise-init-database")
	t.Setenv(initDatabaseRelatedImageEnv1, "quay.io/mongodb/mongodb-enterprise-init-database@sha256:608daf56296c10c9bd02cc85bb542a849e9a66aff0697d6359b449540696b1fd")
	t.Setenv(initDatabaseRelatedImageEnv2, "quay.io/mongodb/mongodb-enterprise-init-database@sha256:b631ee886bb49ba8d7b90bb003fe66051dadecbc2ac126ac7351221f4a7c377c")
	t.Setenv(initDatabaseRelatedImageEnv3, "quay.io/mongodb/mongodb-enterprise-init-database@sha256:f1a7f49cd6533d8ca9425f25cdc290d46bb883997f07fac83b66cc799313adad")

	// there is no related image for 0.0.1
	assert.Equal(t, "quay.io/mongodb/mongodb-enterprise-init-database:0.0.1", ContainerImage(InitDatabaseVersionEnv, "0.0.1", nil))
	// for 10.2.25.6008-1 there is no RELATED_IMAGE variable set, so we use input instead of digest
	assert.Equal(t, "quay.io/mongodb/mongodb-enterprise-init-database:10.2.25.6008-1", ContainerImage(InitDatabaseVersionEnv, "10.2.25.6008-1", nil))
	// for following versions we set RELATED_IMAGE_MONGODB_IMAGE_* env variables to sha256 digest
	assert.Equal(t, "quay.io/mongodb/mongodb-enterprise-init-database@sha256:608daf56296c10c9bd02cc85bb542a849e9a66aff0697d6359b449540696b1fd", ContainerImage(InitDatabaseVersionEnv, "1.0.0", nil))
	assert.Equal(t, "quay.io/mongodb/mongodb-enterprise-init-database@sha256:b631ee886bb49ba8d7b90bb003fe66051dadecbc2ac126ac7351221f4a7c377c", ContainerImage(InitDatabaseVersionEnv, "12.0.4.7554-1", nil))
	assert.Equal(t, "quay.io/mongodb/mongodb-enterprise-init-database@sha256:f1a7f49cd6533d8ca9425f25cdc290d46bb883997f07fac83b66cc799313adad", ContainerImage(InitDatabaseVersionEnv, "2.0.0-b20220912000000", nil))

	// env var has input already, so it is replaced
	t.Setenv(util.InitAppdbImageUrlEnv, "quay.io/mongodb/mongodb-enterprise-init-appdb:12.0.4.7554-1")
	assert.Equal(t, "quay.io/mongodb/mongodb-enterprise-init-appdb:10.2.25.6008-1", ContainerImage(util.InitAppdbImageUrlEnv, "10.2.25.6008-1", nil))

	// env var has input already, but there is related image with this input
	t.Setenv(fmt.Sprintf("RELATED_IMAGE_%s_12_0_4_7554_1", util.InitAppdbImageUrlEnv), "quay.io/mongodb/mongodb-enterprise-init-appdb@sha256:a48829ce36bf479dc25a4de79234c5621b67beee62ca98a099d0a56fdb04791c")
	assert.Equal(t, "quay.io/mongodb/mongodb-enterprise-init-appdb@sha256:a48829ce36bf479dc25a4de79234c5621b67beee62ca98a099d0a56fdb04791c", ContainerImage(util.InitAppdbImageUrlEnv, "12.0.4.7554-1", nil))

	t.Setenv(util.InitAppdbImageUrlEnv, "quay.io/mongodb/mongodb-enterprise-init-appdb@sha256:608daf56296c10c9bd02cc85bb542a849e9a66aff0697d6359b449540696b1fd")
	// env var has input already as digest, but there is related image with this input
	assert.Equal(t, "quay.io/mongodb/mongodb-enterprise-init-appdb@sha256:a48829ce36bf479dc25a4de79234c5621b67beee62ca98a099d0a56fdb04791c", ContainerImage(util.InitAppdbImageUrlEnv, "12.0.4.7554-1", nil))
	// env var has input already as digest, there is no related image with this input, so we use input instead of digest
	assert.Equal(t, "quay.io/mongodb/mongodb-enterprise-init-appdb:1.2.3", ContainerImage(util.InitAppdbImageUrlEnv, "1.2.3", nil))

	t.Setenv(util.OpsManagerImageUrl, "quay.io:3000/mongodb/ops-manager-kubernetes")
	assert.Equal(t, "quay.io:3000/mongodb/ops-manager-kubernetes:1.2.3", ContainerImage(util.OpsManagerImageUrl, "1.2.3", nil))

	t.Setenv(util.OpsManagerImageUrl, "localhost/mongodb/ops-manager-kubernetes")
	assert.Equal(t, "localhost/mongodb/ops-manager-kubernetes:1.2.3", ContainerImage(util.OpsManagerImageUrl, "1.2.3", nil))

	t.Setenv(util.OpsManagerImageUrl, "mongodb")
	assert.Equal(t, "mongodb:1.2.3", ContainerImage(util.OpsManagerImageUrl, "1.2.3", nil))
}

func Test_DatabaseStatefulSetWithRelatedImages(t *testing.T) {
	databaseRelatedImageEnv := fmt.Sprintf("RELATED_IMAGE_%s_1_0_0", util.AutomationAgentImage)
	initDatabaseRelatedImageEnv := fmt.Sprintf("RELATED_IMAGE_%s_2_0_0", util.InitDatabaseImageUrlEnv)

	t.Setenv(util.AutomationAgentImage, "quay.io/mongodb/mongodb-enterprise-database")
	t.Setenv(DatabaseVersionEnv, "1.0.0")
	t.Setenv(util.InitDatabaseImageUrlEnv, "quay.io/mongodb/mongodb-enterprise-init-database")
	t.Setenv(InitDatabaseVersionEnv, "2.0.0")
	t.Setenv(databaseRelatedImageEnv, "quay.io/mongodb/mongodb-enterprise-database:@sha256:MONGODB_DATABASE")
	t.Setenv(initDatabaseRelatedImageEnv, "quay.io/mongodb/mongodb-enterprise-init-database:@sha256:MONGODB_INIT_DATABASE")

	rs := mdbv1.NewReplicaSetBuilder().Build()
	sts := DatabaseStatefulSet(*rs, ReplicaSetOptions(GetPodEnvOptions()), nil)

	assert.Equal(t, "quay.io/mongodb/mongodb-enterprise-init-database:@sha256:MONGODB_INIT_DATABASE", sts.Spec.Template.Spec.InitContainers[0].Image)
	assert.Equal(t, "quay.io/mongodb/mongodb-enterprise-database:@sha256:MONGODB_DATABASE", sts.Spec.Template.Spec.Containers[0].Image)
}

func TestGetAppDBImage(t *testing.T) {
	// Note: if no construct.DefaultImageType is given, we will default to ubi8
	tests := []struct {
		name      string
		input     string
		want      string
		setupEnvs func(t *testing.T)
	}{
		{
			name:  "Getting official image",
			input: "4.2.11-ubi8",
			want:  "quay.io/mongodb/mongodb-enterprise-server:4.2.11-ubi8",
			setupEnvs: func(t *testing.T) {
				t.Setenv(construct.MongodbRepoUrl, "quay.io/mongodb")
				t.Setenv(construct.MongodbImageEnv, util.OfficialServerImageAppdbUrl)
			},
		},
		{
			name:  "Getting official image without suffix",
			input: "4.2.11",
			want:  "quay.io/mongodb/mongodb-enterprise-server:4.2.11-ubi8",
			setupEnvs: func(t *testing.T) {
				t.Setenv(construct.MongodbRepoUrl, "quay.io/mongodb")
				t.Setenv(construct.MongodbImageEnv, util.OfficialServerImageAppdbUrl)
			},
		},
		{
			name:  "Getting official image keep suffix",
			input: "4.2.11-something",
			want:  "quay.io/mongodb/mongodb-enterprise-server:4.2.11-something",
			setupEnvs: func(t *testing.T) {
				t.Setenv(construct.MongodbRepoUrl, "quay.io/mongodb")
				t.Setenv(construct.MongodbImageEnv, util.OfficialServerImageAppdbUrl)
			},
		},
		{
			name:  "Getting official image with legacy suffix",
			input: "4.2.11-ent",
			want:  "quay.io/mongodb/mongodb-enterprise-server:4.2.11-ubi8",
			setupEnvs: func(t *testing.T) {
				t.Setenv(construct.MongodbRepoUrl, "quay.io/mongodb")
				t.Setenv(construct.MongodbImageEnv, util.OfficialServerImageAppdbUrl)
			},
		},
		{
			name:  "Getting official image with legacy image",
			input: "4.2.11-ent",
			want:  "quay.io/mongodb/mongodb-enterprise-appdb-database-ubi:4.2.11-ent",
			setupEnvs: func(t *testing.T) {
				t.Setenv(construct.MongodbRepoUrl, "quay.io/mongodb")
				t.Setenv(construct.MongodbImageEnv, util.DeprecatedImageAppdbUbiUrl)
			},
		},
		{
			name:  "Getting official image with related image from deprecated URL",
			input: "4.2.11-ubi8",
			want:  "quay.io/mongodb/mongodb-enterprise-server:4.2.11-ubi8",
			setupEnvs: func(t *testing.T) {
				t.Setenv("RELATED_IMAGE_MONGODB_IMAGE_4_2_11_ubi8", "quay.io/mongodb/mongodb-enterprise-server:4.2.11-ubi8")
				t.Setenv(construct.MongoDBImageType, "ubi8")
				t.Setenv(construct.MongodbImageEnv, util.DeprecatedImageAppdbUbiUrl)
				t.Setenv(construct.MongodbRepoUrl, construct.OfficialMongodbRepoUrls[1])
			},
		},
		{
			name:  "Getting official image with related image with ent suffix even if old related image exists",
			input: "4.2.11-ent",
			want:  "quay.io/mongodb/mongodb-enterprise-server:4.2.11-ubi8",
			setupEnvs: func(t *testing.T) {
				t.Setenv("RELATED_IMAGE_MONGODB_IMAGE_4_2_11_ubi8", "quay.io/mongodb/mongodb-enterprise-server:4.2.11-ubi8")
				t.Setenv("RELATED_IMAGE_MONGODB_IMAGE_4_2_11_ent", "quay.io/mongodb/mongodb-enterprise-server:4.2.11-ent")
				t.Setenv(construct.MongoDBImageType, "ubi8")
				t.Setenv(construct.MongodbImageEnv, util.OfficialServerImageAppdbUrl)
				t.Setenv(construct.MongodbRepoUrl, construct.OfficialMongodbRepoUrls[1])
			},
		},
		{
			name:  "Getting deprecated image with related image from official URL. We do not replace -ent because the url is not a deprecated one we want to replace",
			input: "4.2.11-ent",
			want:  "quay.io/mongodb/mongodb-enterprise-appdb-database-ubi:4.2.11-ent",
			setupEnvs: func(t *testing.T) {
				t.Setenv("RELATED_IMAGE_MONGODB_IMAGE_4_2_11_ubi8", "quay.io/mongodb/mongodb-enterprise-server:4.2.11-ubi8")
				t.Setenv(construct.MongodbImageEnv, util.DeprecatedImageAppdbUbiUrl)
				t.Setenv(construct.MongodbRepoUrl, construct.OfficialMongodbRepoUrls[1])
			},
		},
		{
			name:  "Getting official image with legacy suffix but stopping migration",
			input: "4.2.11-ent",
			want:  "quay.io/mongodb/mongodb-enterprise-server:4.2.11-ent",
			setupEnvs: func(t *testing.T) {
				t.Setenv(construct.MongodbRepoUrl, "quay.io/mongodb")
				t.Setenv(construct.MongodbImageEnv, util.OfficialServerImageAppdbUrl)
				t.Setenv(util.MdbAppdbAssumeOldFormat, "true")
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.setupEnvs(t)
			assert.Equalf(t, tt.want, getAppDBImage(tt.input), "getAppDBImage(%v)", tt.input)
		})
	}
}
