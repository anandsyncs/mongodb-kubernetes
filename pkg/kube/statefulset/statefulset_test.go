package statefulset

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	TestNamespace = "test-ns"
	TestName      = "test-name"
)

func int64Ref(i int64) *int64 {
	return &i
}

func TestGetContainerIndexByName(t *testing.T) {
	containers := []corev1.Container{
		{
			Name: "container-0",
		},
		{
			Name: "container-1",
		},
		{
			Name: "container-2",
		},
	}

	stsBuilder := defaultStatefulSetBuilder().SetPodTemplateSpec(podTemplateWithContainers(containers))
	idx, err := stsBuilder.getContainerIndexByName("container-0")

	assert.NoError(t, err)
	assert.NotEqual(t, -1, idx)
	assert.Equal(t, 0, idx)

	idx, err = stsBuilder.getContainerIndexByName("container-1")

	assert.NoError(t, err)
	assert.NotEqual(t, -1, idx)
	assert.Equal(t, 1, idx)

	idx, err = stsBuilder.getContainerIndexByName("container-2")

	assert.NoError(t, err)
	assert.NotEqual(t, -1, idx)
	assert.Equal(t, 2, idx)

	idx, err = stsBuilder.getContainerIndexByName("doesnt-exist")

	assert.Error(t, err)
	assert.Equal(t, -1, idx)
}

func TestAddVolumeAndMount(t *testing.T) {
	var stsBuilder *Builder
	var sts appsv1.StatefulSet
	var err error
	vmd := VolumeMountData{
		MountPath: "mount-path",
		Name:      "mount-name",
		ReadOnly:  true,
		Volume:    CreateVolumeFromConfigMap("mount-name", "config-map"),
	}

	stsBuilder = defaultStatefulSetBuilder().SetPodTemplateSpec(podTemplateWithContainers([]corev1.Container{{Name: "container-name"}})).AddVolumeAndMount(vmd, "container-name")
	sts, err = stsBuilder.Build()

	// assert container was correctly updated with the volumes
	assert.NoError(t, err, "volume should successfully mount when the container exists")
	assert.Len(t, sts.Spec.Template.Spec.Containers[0].VolumeMounts, 1, "volume mount should have been added to the container in the stateful set")
	assert.Equal(t, sts.Spec.Template.Spec.Containers[0].VolumeMounts[0].Name, "mount-name")
	assert.Equal(t, sts.Spec.Template.Spec.Containers[0].VolumeMounts[0].MountPath, "mount-path")

	// assert the volumes were added to the podspec template
	assert.Len(t, sts.Spec.Template.Spec.Volumes, 1)
	assert.Equal(t, sts.Spec.Template.Spec.Volumes[0].Name, "mount-name")
	assert.NotNil(t, sts.Spec.Template.Spec.Volumes[0].VolumeSource.ConfigMap, "volume should have been configured from a config map source")
	assert.Nil(t, sts.Spec.Template.Spec.Volumes[0].VolumeSource.Secret, "volume should not have been configured from a secret source")

	stsBuilder = defaultStatefulSetBuilder().SetPodTemplateSpec(podTemplateWithContainers([]corev1.Container{{Name: "container-0"}, {Name: "container-1"}})).AddVolumeAndMount(vmd, "container-0")
	sts, err = stsBuilder.Build()

	assert.NoError(t, err, "volume should successfully mount when the container exists")

	secretVmd := VolumeMountData{
		MountPath: "mount-path-secret",
		Name:      "mount-name-secret",
		ReadOnly:  true,
		Volume:    CreateVolumeFromSecret("mount-name-secret", "secret"),
	}

	// add a 2nd container to previously defined stsBuilder
	sts, err = stsBuilder.AddVolumeAndMount(secretVmd, "container-1").Build()

	assert.NoError(t, err, "volume should successfully mount when the container exists")
	assert.Len(t, sts.Spec.Template.Spec.Containers[1].VolumeMounts, 1, "volume mount should have been added to the container in the stateful set")
	assert.Equal(t, sts.Spec.Template.Spec.Containers[1].VolumeMounts[0].Name, "mount-name-secret")
	assert.Equal(t, sts.Spec.Template.Spec.Containers[1].VolumeMounts[0].MountPath, "mount-path-secret")

	assert.Len(t, sts.Spec.Template.Spec.Volumes, 2)
	assert.Equal(t, sts.Spec.Template.Spec.Volumes[1].Name, "mount-name-secret")
	assert.Nil(t, sts.Spec.Template.Spec.Volumes[1].VolumeSource.ConfigMap, "volume should not have been configured from a config map source")
	assert.NotNil(t, sts.Spec.Template.Spec.Volumes[1].VolumeSource.Secret, "volume should have been configured from a secret source")

}

func TestAddVolumeClaimTemplates(t *testing.T) {
	claim := corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name: "claim-0",
		},
	}
	mount := corev1.VolumeMount{
		Name: "mount-0",
	}
	sts, err := defaultStatefulSetBuilder().SetPodTemplateSpec(podTemplateWithContainers([]corev1.Container{{Name: "container-name"}})).AddVolumeClaimTemplates([]corev1.PersistentVolumeClaim{claim}).AddVolumeMounts("container-name", []corev1.VolumeMount{mount}).Build()

	assert.NoError(t, err)
	assert.Len(t, sts.Spec.VolumeClaimTemplates, 1)
	assert.Equal(t, sts.Spec.VolumeClaimTemplates[0].Name, "claim-0")
	assert.Len(t, sts.Spec.Template.Spec.Containers[0].VolumeMounts, 1)
	assert.Equal(t, sts.Spec.Template.Spec.Containers[0].VolumeMounts[0].Name, "mount-0")
}

func TestBuildStructImmutable(t *testing.T) {
	labels := map[string]string{"label_1": "a", "label_2": "b"}
	replicas := new(int32)
	*replicas = 2
	stsBuilder := defaultStatefulSetBuilder().SetLabels(labels).SetReplicas(replicas)
	var sts appsv1.StatefulSet
	var err error
	sts, err = stsBuilder.Build()
	assert.NoError(t, err)
	assert.Len(t, sts.ObjectMeta.Labels, 2)
	assert.Equal(t, *sts.Spec.Replicas, int32(2))

	delete(labels, "label_2")
	*replicas = 1
	// checks that modifying the underlying object did not change the built statefulset
	assert.Len(t, sts.ObjectMeta.Labels, 2)
	assert.Equal(t, *sts.Spec.Replicas, int32(2))
	sts, err = stsBuilder.Build()
	assert.NoError(t, err)
	assert.Len(t, sts.ObjectMeta.Labels, 1)
	assert.Equal(t, *sts.Spec.Replicas, int32(1))
}

func defaultStatefulSetBuilder() *Builder {
	return NewBuilder().
		SetName(TestName).
		SetNamespace(TestNamespace).
		SetServiceName(fmt.Sprintf("%s-svc", TestName)).
		SetLabels(map[string]string{})
}

func podTemplateWithContainers(containers []corev1.Container) corev1.PodTemplateSpec {
	return corev1.PodTemplateSpec{
		Spec: corev1.PodSpec{
			Containers: containers,
		},
	}
}

func TestBuildStatefulSet_SortedEnvVariables(t *testing.T) {
	podTemplateSpec := podTemplateWithContainers([]corev1.Container{{Name: "container-name"}})
	podTemplateSpec.Spec.Containers[0].Env = []corev1.EnvVar{
		{Name: "one", Value: "X"},
		{Name: "two", Value: "Y"},
		{Name: "three", Value: "Z"},
	}
	sts, err := defaultStatefulSetBuilder().SetPodTemplateSpec(podTemplateSpec).Build()
	assert.NoError(t, err)
	expectedVars := []corev1.EnvVar{
		{Name: "one", Value: "X"},
		{Name: "three", Value: "Z"},
		{Name: "two", Value: "Y"},
	}
	assert.Equal(t, expectedVars, sts.Spec.Template.Spec.Containers[0].Env)
}

func getDefaultContainer() corev1.Container {
	return corev1.Container{
		Name:  "container-0",
		Image: "image-0",
		ReadinessProbe: &corev1.Probe{
			Handler: corev1.Handler{HTTPGet: &corev1.HTTPGetAction{
				Path: "/foo",
			}},
			PeriodSeconds: 10,
		},
		VolumeMounts: []corev1.VolumeMount{
			{
				Name: "container-0.volume-mount-0",
			},
		},
	}
}

func getCustomContainer() corev1.Container {
	return corev1.Container{
		Name:  "container-1",
		Image: "image-1",
	}
}

func TestCreateContainerMap(t *testing.T) {
	defaultContainer := getDefaultContainer()
	customContainer := getCustomContainer()
	result := createContainerMap([]corev1.Container{defaultContainer, customContainer})
	assert.Len(t, result, 2)
	assert.Equal(t, defaultContainer, result["container-0"])
	assert.Equal(t, customContainer, result["container-1"])
}

func TestMergeVolumeMounts(t *testing.T) {
	vol0 := corev1.VolumeMount{Name: "container-0.volume-mount-0"}
	vol1 := corev1.VolumeMount{Name: "another-mount"}
	volumeMounts := []corev1.VolumeMount{vol0, vol1}
	var mergedVolumeMounts []corev1.VolumeMount
	var err error

	mergedVolumeMounts, err = mergeVolumeMounts(nil, volumeMounts)
	assert.NoError(t, err)
	assert.Equal(t, []corev1.VolumeMount{vol0, vol1}, mergedVolumeMounts)

	vol2 := vol1
	vol2.MountPath = "/somewhere"
	mergedVolumeMounts, err = mergeVolumeMounts([]corev1.VolumeMount{vol2}, []corev1.VolumeMount{vol0, vol1})
	assert.NoError(t, err)
	assert.Equal(t, []corev1.VolumeMount{vol0, vol2}, mergedVolumeMounts)
}

func TestMergeContainer(t *testing.T) {
	vol0 := corev1.VolumeMount{Name: "container-0.volume-mount-0"}
	sideCarVol := corev1.VolumeMount{Name: "container-1.volume-mount-0"}

	anotherVol := corev1.VolumeMount{Name: "another-mount"}

	overrideDefaultContainer := corev1.Container{Name: "container-0"}
	overrideDefaultContainer.Image = "overridden"
	overrideDefaultContainer.ReadinessProbe = &corev1.Probe{PeriodSeconds: 20}

	otherDefaultContainer := getDefaultContainer()
	otherDefaultContainer.Name = "default-side-car"
	otherDefaultContainer.VolumeMounts = []corev1.VolumeMount{sideCarVol}

	overrideOtherDefaultContainer := otherDefaultContainer
	overrideOtherDefaultContainer.Env = []corev1.EnvVar{{Name: "env_var", Value: "xxx"}}
	overrideOtherDefaultContainer.VolumeMounts = []corev1.VolumeMount{anotherVol}

	mergedContainers, err := mergeContainers(
		[]corev1.Container{getCustomContainer(), overrideDefaultContainer, overrideOtherDefaultContainer},
		[]corev1.Container{getDefaultContainer(), otherDefaultContainer},
	)
	assert.NoError(t, err)
	assert.Len(t, mergedContainers, 3)

	assert.Equal(t, getCustomContainer(), mergedContainers[2])

	mergedDefaultContainer := mergedContainers[0]
	assert.Equal(t, "container-0", mergedDefaultContainer.Name)
	assert.Equal(t, []corev1.VolumeMount{vol0}, mergedDefaultContainer.VolumeMounts)
	assert.Equal(t, "overridden", mergedDefaultContainer.Image)
	// only "periodSeconds" was overwritten - other fields stayed untouched
	assert.Equal(t, corev1.Handler{HTTPGet: &corev1.HTTPGetAction{Path: "/foo"}}, mergedDefaultContainer.ReadinessProbe.Handler)
	assert.Equal(t, int32(20), mergedDefaultContainer.ReadinessProbe.PeriodSeconds)

	mergedOtherContainer := mergedContainers[1]
	assert.Equal(t, "default-side-car", mergedOtherContainer.Name)
	assert.Equal(t, []corev1.VolumeMount{sideCarVol, anotherVol}, mergedOtherContainer.VolumeMounts)
	assert.Len(t, mergedOtherContainer.Env, 1)
	assert.Equal(t, "env_var", mergedOtherContainer.Env[0].Name)
	assert.Equal(t, "xxx", mergedOtherContainer.Env[0].Value)
}

func getDefaultPodSpec() corev1.PodTemplateSpec {
	initContainer := getDefaultContainer()
	initContainer.Name = "init-container-default"
	return corev1.PodTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-default-name",
			Namespace: "my-default-namespace",
		},
		Spec: corev1.PodSpec{
			NodeSelector: map[string]string{
				"node-0": "node-0",
			},
			ServiceAccountName:            "my-default-service-account",
			TerminationGracePeriodSeconds: int64Ref(12),
			ActiveDeadlineSeconds:         int64Ref(10),
			Containers:                    []corev1.Container{getDefaultContainer()},
			InitContainers:                []corev1.Container{initContainer},
		},
	}
}

func getCustomPodSpec() corev1.PodTemplateSpec {
	initContainer := getCustomContainer()
	initContainer.Name = "init-container-custom"
	return corev1.PodTemplateSpec{
		Spec: corev1.PodSpec{
			NodeSelector: map[string]string{
				"node-1": "node-1",
			},
			ServiceAccountName:            "my-service-account-override",
			TerminationGracePeriodSeconds: int64Ref(11),
			NodeName:                      "my-node-name",
			RestartPolicy:                 corev1.RestartPolicy("Always"),
			Containers:                    []corev1.Container{getCustomContainer()},
			InitContainers:                []corev1.Container{initContainer},
		},
	}
}

func TestMergePodSpecsEmptyCustom(t *testing.T) {

	defaultPodSpec := getDefaultPodSpec()
	customPodSpecTemplate := corev1.PodTemplateSpec{}

	mergedPodTemplateSpec, err := mergePodSpecs(customPodSpecTemplate, defaultPodSpec)

	assert.NoError(t, err)
	assert.Equal(t, "my-default-service-account", mergedPodTemplateSpec.Spec.ServiceAccountName)
	assert.Equal(t, int64Ref(12), mergedPodTemplateSpec.Spec.TerminationGracePeriodSeconds)

	assert.Equal(t, "my-default-name", mergedPodTemplateSpec.ObjectMeta.Name)
	assert.Equal(t, "my-default-namespace", mergedPodTemplateSpec.ObjectMeta.Namespace)
	assert.Equal(t, int64Ref(10), mergedPodTemplateSpec.Spec.ActiveDeadlineSeconds)

	// ensure collections have been merged
	assert.Contains(t, mergedPodTemplateSpec.Spec.NodeSelector, "node-0")
	assert.Len(t, mergedPodTemplateSpec.Spec.Containers, 1)
	assert.Equal(t, "container-0", mergedPodTemplateSpec.Spec.Containers[0].Name)
	assert.Equal(t, "image-0", mergedPodTemplateSpec.Spec.Containers[0].Image)
	assert.Equal(t, "container-0.volume-mount-0", mergedPodTemplateSpec.Spec.Containers[0].VolumeMounts[0].Name)
	assert.Len(t, mergedPodTemplateSpec.Spec.InitContainers, 1)
	assert.Equal(t, "init-container-default", mergedPodTemplateSpec.Spec.InitContainers[0].Name)
}

func TestMergePodSpecsEmptyDefault(t *testing.T) {

	defaultPodSpec := corev1.PodTemplateSpec{}
	customPodSpecTemplate := getCustomPodSpec()

	mergedPodTemplateSpec, err := mergePodSpecs(customPodSpecTemplate, defaultPodSpec)

	assert.NoError(t, err)
	assert.Equal(t, "my-service-account-override", mergedPodTemplateSpec.Spec.ServiceAccountName)
	assert.Equal(t, int64Ref(11), mergedPodTemplateSpec.Spec.TerminationGracePeriodSeconds)
	assert.Equal(t, "my-node-name", mergedPodTemplateSpec.Spec.NodeName)
	assert.Equal(t, corev1.RestartPolicy("Always"), mergedPodTemplateSpec.Spec.RestartPolicy)

	assert.Len(t, mergedPodTemplateSpec.Spec.Containers, 1)
	assert.Equal(t, "container-1", mergedPodTemplateSpec.Spec.Containers[0].Name)
	assert.Equal(t, "image-1", mergedPodTemplateSpec.Spec.Containers[0].Image)
	assert.Len(t, mergedPodTemplateSpec.Spec.InitContainers, 1)
	assert.Equal(t, "init-container-custom", mergedPodTemplateSpec.Spec.InitContainers[0].Name)

}

func TestMergePodSpecsBoth(t *testing.T) {

	defaultPodSpec := getDefaultPodSpec()
	customPodSpecTemplate := getCustomPodSpec()

	var mergedPodTemplateSpec corev1.PodTemplateSpec
	var err error

	// multiple merges must give the same result
	for i := 0; i < 3; i++ {
		mergedPodTemplateSpec, err = mergePodSpecs(customPodSpecTemplate, defaultPodSpec)

		assert.NoError(t, err)
		// ensure values that were specified in the custom pod spec template remain unchanged
		assert.Equal(t, "my-service-account-override", mergedPodTemplateSpec.Spec.ServiceAccountName)
		assert.Equal(t, int64Ref(11), mergedPodTemplateSpec.Spec.TerminationGracePeriodSeconds)
		assert.Equal(t, "my-node-name", mergedPodTemplateSpec.Spec.NodeName)
		assert.Equal(t, corev1.RestartPolicy("Always"), mergedPodTemplateSpec.Spec.RestartPolicy)

		// ensure values from the default pod spec template have been merged in
		assert.Equal(t, "my-default-name", mergedPodTemplateSpec.ObjectMeta.Name)
		assert.Equal(t, "my-default-namespace", mergedPodTemplateSpec.ObjectMeta.Namespace)
		assert.Equal(t, int64Ref(10), mergedPodTemplateSpec.Spec.ActiveDeadlineSeconds)

		// ensure collections have been merged
		assert.Contains(t, mergedPodTemplateSpec.Spec.NodeSelector, "node-0")
		assert.Contains(t, mergedPodTemplateSpec.Spec.NodeSelector, "node-1")
		assert.Len(t, mergedPodTemplateSpec.Spec.Containers, 2)
		assert.Equal(t, "container-0", mergedPodTemplateSpec.Spec.Containers[0].Name)
		assert.Equal(t, "image-0", mergedPodTemplateSpec.Spec.Containers[0].Image)
		assert.Equal(t, "container-0.volume-mount-0", mergedPodTemplateSpec.Spec.Containers[0].VolumeMounts[0].Name)
		assert.Equal(t, "container-1", mergedPodTemplateSpec.Spec.Containers[1].Name)
		assert.Equal(t, "image-1", mergedPodTemplateSpec.Spec.Containers[1].Image)
		assert.Len(t, mergedPodTemplateSpec.Spec.InitContainers, 2)
		assert.Equal(t, "init-container-default", mergedPodTemplateSpec.Spec.InitContainers[0].Name)
		assert.Equal(t, "init-container-custom", mergedPodTemplateSpec.Spec.InitContainers[1].Name)
	}
}

func TestMergeSpec(t *testing.T) {
	t.Run("Add Container to PodSpecTemplate", func(t *testing.T) {
		sts, err := defaultStatefulSetBuilder().Build()
		assert.NoError(t, err)
		customSts, err := defaultStatefulSetBuilder().SetPodTemplateSpec(podTemplateWithContainers([]corev1.Container{{Name: "container-0"}})).Build()
		assert.NoError(t, err)

		mergedSts, err := MergeSpec(sts, &customSts.Spec)
		assert.NoError(t, err)
		assert.Contains(t, mergedSts.Spec.Template.Spec.Containers, corev1.Container{Name: "container-0"})
	})
	t.Run("Change terminationGracePeriodSeconds", func(t *testing.T) {
		sts, err := defaultStatefulSetBuilder().Build()
		assert.NoError(t, err)
		sts.Spec.Template.Spec.TerminationGracePeriodSeconds = int64Ref(30)
		customSts, err := defaultStatefulSetBuilder().SetPodTemplateSpec(podTemplateWithContainers([]corev1.Container{{Name: "container-0"}})).Build()
		sts.Spec.Template.Spec.TerminationGracePeriodSeconds = int64Ref(600)
		assert.NoError(t, err)

		mergedSts, err := MergeSpec(sts, &customSts.Spec)
		assert.NoError(t, err)
		assert.Contains(t, mergedSts.Spec.Template.Spec.Containers, corev1.Container{Name: "container-0"})
		assert.Equal(t, mergedSts.Spec.Template.Spec.TerminationGracePeriodSeconds, int64Ref(600))
	})
	t.Run("Containers are added to existing list", func(t *testing.T) {
		sts, err := defaultStatefulSetBuilder().SetPodTemplateSpec(podTemplateWithContainers([]corev1.Container{{Name: "container-0"}})).Build()
		assert.NoError(t, err)
		customSts, err := defaultStatefulSetBuilder().SetPodTemplateSpec(podTemplateWithContainers([]corev1.Container{{Name: "container-1"}})).Build()
		assert.NoError(t, err)

		mergedSts, err := MergeSpec(sts, &customSts.Spec)
		assert.NoError(t, err)
		assert.Len(t, mergedSts.Spec.Template.Spec.Containers, 2)
		assert.Contains(t, mergedSts.Spec.Template.Spec.Containers, corev1.Container{Name: "container-0"})
		assert.Contains(t, mergedSts.Spec.Template.Spec.Containers, corev1.Container{Name: "container-1"})
	})
	t.Run("Cannot change fields in the StatefulSet outside of the spec", func(t *testing.T) {
		sts, err := defaultStatefulSetBuilder().Build()
		assert.NoError(t, err)
		customSts, err := defaultStatefulSetBuilder().Build()
		assert.NoError(t, err)
		customSts.Annotations = map[string]string{
			"some-annotation": "some-value",
		}
		mergedSts, err := MergeSpec(sts, &customSts.Spec)
		assert.NoError(t, err)
		assert.NotContains(t, mergedSts.Annotations, "some-annotation")
	})
	t.Run("change fields in the StatefulSet the Operator doesn't touch", func(t *testing.T) {
		sts, err := defaultStatefulSetBuilder().Build()
		assert.NoError(t, err)
		customSts, err := defaultStatefulSetBuilder().AddVolumeClaimTemplates([]corev1.PersistentVolumeClaim{{
			ObjectMeta: metav1.ObjectMeta{
				Name: "my-volume-claim",
			},
		}}).Build()
		assert.NoError(t, err)

		mergedSts, err := MergeSpec(sts, &customSts.Spec)
		assert.NoError(t, err)
		assert.Len(t, mergedSts.Spec.VolumeClaimTemplates, 1)
		assert.Equal(t, "my-volume-claim", mergedSts.Spec.VolumeClaimTemplates[0].Name)
	})
	t.Run("Volume Claim Templates are added to existing StatefulSet", func(t *testing.T) {
		sts, err := defaultStatefulSetBuilder().AddVolumeClaimTemplates([]corev1.PersistentVolumeClaim{{
			ObjectMeta: metav1.ObjectMeta{
				Name: "my-volume-claim-0",
			},
		}}).Build()

		assert.NoError(t, err)
		customSts, err := defaultStatefulSetBuilder().AddVolumeClaimTemplates([]corev1.PersistentVolumeClaim{{
			ObjectMeta: metav1.ObjectMeta{
				Name: "my-volume-claim-1",
			},
		}}).Build()
		assert.NoError(t, err)

		mergedSts, err := MergeSpec(sts, &customSts.Spec)
		assert.NoError(t, err)
		assert.Len(t, mergedSts.Spec.VolumeClaimTemplates, 2)
		assert.Equal(t, "my-volume-claim-0", mergedSts.Spec.VolumeClaimTemplates[0].Name)
		assert.Equal(t, "my-volume-claim-1", mergedSts.Spec.VolumeClaimTemplates[1].Name)
	})

	t.Run("Volume Claim Templates are changed by name", func(t *testing.T) {
		sts, err := defaultStatefulSetBuilder().AddVolumeClaimTemplates([]corev1.PersistentVolumeClaim{{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "my-volume-claim-0",
				Namespace: "first-ns",
			},
		}}).Build()

		assert.NoError(t, err)
		customSts, err := defaultStatefulSetBuilder().AddVolumeClaimTemplates([]corev1.PersistentVolumeClaim{{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "my-volume-claim-0",
				Namespace: "new-ns",
			},
		}}).Build()
		assert.NoError(t, err)

		mergedSts, err := MergeSpec(sts, &customSts.Spec)
		assert.NoError(t, err)
		assert.Len(t, mergedSts.Spec.VolumeClaimTemplates, 1)
		assert.Equal(t, "my-volume-claim-0", mergedSts.Spec.VolumeClaimTemplates[0].Name)
		assert.Equal(t, "new-ns", mergedSts.Spec.VolumeClaimTemplates[0].Namespace)
	})

	t.Run("Volume Claims are added", func(t *testing.T) {
		sts, err := defaultStatefulSetBuilder().
			SetPodTemplateSpec(getDefaultPodSpec()).
			AddVolumeAndMount(VolumeMountData{
				MountPath: "path",
				Name:      "vol-0",
				ReadOnly:  false,
				Volume: corev1.Volume{
					Name: "vol-0",
				},
			}, "container-0").Build()
		assert.NoError(t, err)
		customSts, err := defaultStatefulSetBuilder().
			SetPodTemplateSpec(getDefaultPodSpec()).
			AddVolumeAndMount(VolumeMountData{
				MountPath: "path",
				Name:      "vol-1",
				ReadOnly:  false,
				Volume: corev1.Volume{
					Name: "vol-1",
				},
			}, "container-0").
			AddVolumeAndMount(VolumeMountData{
				MountPath: "path",
				Name:      "vol-2",
				ReadOnly:  false,
				Volume: corev1.Volume{
					Name: "vol-2",
				},
			}, "container-0").Build()
		assert.NoError(t, err)

		mergedSts, err := MergeSpec(sts, &customSts.Spec)
		assert.NoError(t, err)

		assert.Len(t, mergedSts.Spec.Template.Spec.Volumes, 3)
		for i, vol := range mergedSts.Spec.Template.Spec.Volumes {
			assert.Equal(t, fmt.Sprintf("vol-%d", i), vol.Name)
		}
	})

	t.Run("Custom StatefulSet zero values don't override operator configured ones", func(t *testing.T) {
		sts, err := defaultStatefulSetBuilder().SetServiceName("service-name").Build()
		assert.NoError(t, err)
		customSts, err := defaultStatefulSetBuilder().SetServiceName("").Build()
		assert.NoError(t, err)
		mergedSts, err := MergeSpec(sts, &customSts.Spec)
		assert.NoError(t, err)
		assert.Equal(t, mergedSts.Spec.ServiceName, "service-name")
	})
}

func TestGetMergedDefaultPodSpecTemplate(t *testing.T) {
	var err error

	dbPodSpecTemplate := getDefaultPodSpec()
	var mergedPodSpecTemplate corev1.PodTemplateSpec

	// nothing to merge
	mergedPodSpecTemplate, err = getMergedDefaultPodSpecTemplate(dbPodSpecTemplate, &corev1.PodTemplateSpec{})
	assert.NoError(t, err)
	assert.Equal(t, mergedPodSpecTemplate, dbPodSpecTemplate)
	assert.Len(t, mergedPodSpecTemplate.Spec.Containers, 1)
	assertContainersEqualBarResources(t, mergedPodSpecTemplate.Spec.Containers[0], dbPodSpecTemplate.Spec.Containers[0])

	extraContainer := corev1.Container{
		Name:  "extra-container",
		Image: "container-image",
	}

	newPodSpecTemplate := corev1.PodTemplateSpec{
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{extraContainer},
		},
	}

	// with a side car container
	mergedPodSpecTemplate, err = getMergedDefaultPodSpecTemplate(dbPodSpecTemplate, &newPodSpecTemplate)
	assert.NoError(t, err)
	assert.Len(t, mergedPodSpecTemplate.Spec.Containers, 2)
	assertContainersEqualBarResources(t, mergedPodSpecTemplate.Spec.Containers[0], dbPodSpecTemplate.Spec.Containers[0])
	assertContainersEqualBarResources(t, mergedPodSpecTemplate.Spec.Containers[1], extraContainer)
}

func assertContainersEqualBarResources(t *testing.T, self corev1.Container, other corev1.Container) {
	// Copied fields from k8s.io/api/core/v1/types.go
	assert.Equal(t, self.Name, other.Name)
	assert.Equal(t, self.Image, other.Image)
	assert.True(t, reflect.DeepEqual(self.Command, other.Command))
	assert.True(t, reflect.DeepEqual(self.Args, other.Args))
	assert.Equal(t, self.WorkingDir, other.WorkingDir)
	assert.True(t, reflect.DeepEqual(self.Ports, other.Ports))
	assert.True(t, reflect.DeepEqual(self.EnvFrom, other.EnvFrom))
	assert.True(t, reflect.DeepEqual(self.Env, other.Env))
	// skip Resources
	// assert.True(t, reflect.DeepEqual(self.Resources, other.Resources))
	assert.True(t, reflect.DeepEqual(self.VolumeMounts, other.VolumeMounts))
	assert.True(t, reflect.DeepEqual(self.VolumeDevices, other.VolumeDevices))
	assert.Equal(t, self.LivenessProbe, other.LivenessProbe)
	assert.Equal(t, self.ReadinessProbe, other.ReadinessProbe)
	assert.Equal(t, self.Lifecycle, other.Lifecycle)
	assert.Equal(t, self.TerminationMessagePath, other.TerminationMessagePath)
	assert.Equal(t, self.TerminationMessagePolicy, other.TerminationMessagePolicy)
	assert.Equal(t, self.ImagePullPolicy, other.ImagePullPolicy)
	assert.Equal(t, self.SecurityContext, other.SecurityContext)
	assert.Equal(t, self.Stdin, other.Stdin)
	assert.Equal(t, self.StdinOnce, other.StdinOnce)
	assert.Equal(t, self.TTY, other.TTY)
}
