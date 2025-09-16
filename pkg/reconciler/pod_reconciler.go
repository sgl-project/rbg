package reconciler

import (
	"context"
	"fmt"
	"reflect"
	"sigs.k8s.io/rbgs/pkg/scheduler"
	"sort"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	coreapplyv1 "k8s.io/client-go/applyconfigurations/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	workloadsv1alpha1 "sigs.k8s.io/rbgs/api/workloads/v1alpha1"
	"sigs.k8s.io/rbgs/pkg/discovery"
	"sigs.k8s.io/rbgs/pkg/utils"
)

type PodReconciler struct {
	scheme        *runtime.Scheme
	client        client.Client
	injectObjects []string
}

func NewPodReconciler(scheme *runtime.Scheme, client client.Client) *PodReconciler {
	return &PodReconciler{
		scheme: scheme,
		client: client,
	}
}

func (r *PodReconciler) SetInjectors(injectObjects []string) {
	r.injectObjects = injectObjects
}

func (r *PodReconciler) ConstructPodTemplateSpecApplyConfiguration(
	ctx context.Context,
	rbg *workloadsv1alpha1.RoleBasedGroup,
	role *workloadsv1alpha1.RoleSpec,
	podLabels map[string]string,
	podTmpls ...corev1.PodTemplateSpec,
) (*coreapplyv1.PodTemplateSpecApplyConfiguration, error) {
	var podTemplateSpec corev1.PodTemplateSpec
	if len(podTmpls) > 0 {
		podTemplateSpec = podTmpls[0]
	} else {
		podTemplateSpec = *role.Template.DeepCopy()
	}

	// inject objects
	injector := discovery.NewDefaultInjector(r.scheme, r.client)
	if r.injectObjects == nil {
		r.injectObjects = []string{"config", "sidecar", "env"}
	}
	if utils.ContainsString(r.injectObjects, "config") {
		if err := injector.InjectConfig(ctx, &podTemplateSpec, rbg, role); err != nil {
			return nil, fmt.Errorf("failed to inject config: %w", err)
		}
	}
	if utils.ContainsString(r.injectObjects, "sidecar") {
		// The sidecar containers also need rbg-related envs, so inject them first
		if err := injector.InjectSidecar(ctx, &podTemplateSpec, rbg, role); err != nil {
			return nil, fmt.Errorf("failed to inject sidecar: %w", err)
		}
	}
	if utils.ContainsString(r.injectObjects, "env") {
		if err := injector.InjectEnv(ctx, &podTemplateSpec, rbg, role); err != nil {
			return nil, fmt.Errorf("failed to inject env vars: %w", err)
		}
	}

	// construct pod template spec configuration
	obj, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&podTemplateSpec)
	if err != nil {
		return nil, err
	}
	var podTemplateApplyConfiguration *coreapplyv1.PodTemplateSpecApplyConfiguration
	err = runtime.DefaultUnstructuredConverter.FromUnstructured(obj, &podTemplateApplyConfiguration)
	if err != nil {
		return nil, err
	}

	scheduler.InjectPodGroupProtocol(rbg, podTemplateApplyConfiguration)

	return podTemplateApplyConfiguration, nil
}

func podTemplateSpecEqual(template1, template2 corev1.PodTemplateSpec) (bool, error) {
	if equal, err := objectMetaEqual(template1.ObjectMeta, template2.ObjectMeta); !equal {
		return false, fmt.Errorf("objectMeta not equal: %s", err.Error())
	}

	if equal, err := podSpecEqual(template1.Spec, template2.Spec); !equal {
		return false, fmt.Errorf("spec not equal: %s", err.Error())
	}

	return true, nil
}

func objectMetaEqual(meta1, meta2 metav1.ObjectMeta) (bool, error) {
	meta1Copy := meta1.DeepCopy()
	meta2Copy := meta2.DeepCopy()

	meta1Copy.Labels = utils.FilterSystemLabels(meta1Copy.Labels)
	meta2Copy.Labels = utils.FilterSystemLabels(meta2Copy.Labels)
	if !mapsEqual(meta1Copy.Labels, meta2Copy.Labels) {
		return false, fmt.Errorf("label not equal, old [%s], new [%s]", meta1Copy.Labels, meta2Copy.Labels)
	}

	meta1Copy.Annotations = utils.FilterSystemAnnotations(meta1Copy.Annotations)
	meta2Copy.Annotations = utils.FilterSystemAnnotations(meta2Copy.Annotations)
	if !mapsEqual(meta1Copy.Annotations, meta2Copy.Annotations) {
		return false, fmt.Errorf(
			"annotation not equal, old [%s], new [%s]", meta1Copy.Annotations, meta2Copy.Annotations,
		)
	}
	return true, nil
}

func podSpecEqual(spec1, spec2 corev1.PodSpec) (bool, error) {
	if len(spec1.Containers) != len(spec2.Containers) {
		return false, fmt.Errorf("pod template spec containers len not equal")
	}

	containers1 := sortContainers(spec1.Containers)
	containers2 := sortContainers(spec2.Containers)

	for i := range containers1 {
		if equal, err := containerEqual(containers1[i], containers2[i]); !equal {
			return false, fmt.Errorf("container not equal: %s", err.Error())
		}
	}

	if equal, err := volumesEqual(spec1.Volumes, spec2.Volumes); !equal {
		return false, fmt.Errorf("podTemplate volumes not equal: %s", err.Error())
	}

	return true, nil
}

func containerEqual(c1, c2 corev1.Container) (bool, error) {
	if c1.Name != c2.Name {
		return false, fmt.Errorf("container name not equal")
	}

	if c1.Image != c2.Image {
		return false, fmt.Errorf("container image not equal")
	}

	if !reflect.DeepEqual(c1.Command, c2.Command) {
		return false, fmt.Errorf("container command not equal")
	}

	if !reflect.DeepEqual(c1.Args, c2.Args) {
		return false, fmt.Errorf("container args not equal")
	}

	if !reflect.DeepEqual(c1.Ports, c2.Ports) {
		return false, fmt.Errorf("container ports not equal")
	}

	if !reflect.DeepEqual(c1.Resources, c2.Resources) {
		return false, fmt.Errorf("container resources not equal")
	}

	if c1.ImagePullPolicy != "" && c2.ImagePullPolicy != "" && c1.ImagePullPolicy != c2.ImagePullPolicy {
		return false, fmt.Errorf(
			"container image pull policy not equal, old: %s, new: %s", c1.ImagePullPolicy, c2.ImagePullPolicy,
		)
	}

	if equal, err := envVarsEqual(c1.Env, c2.Env); !equal {
		return false, fmt.Errorf("env not equal: %s", err.Error())
	}

	if equal, err := volumeMountsEqual(c1.VolumeMounts, c2.VolumeMounts); !equal {
		return false, fmt.Errorf("podTemplate volumes mounts not equal: %s", err.Error())
	}

	return true, nil

}

func envVarsEqual(env1, env2 []corev1.EnvVar) (bool, error) {
	env1 = utils.FilterSystemEnvs(env1)
	env2 = utils.FilterSystemEnvs(env2)
	if len(env1) != len(env2) {
		return false, fmt.Errorf("env vars len not equal")
	}

	sortedEnv1 := make([]corev1.EnvVar, len(env1))
	sortedEnv2 := make([]corev1.EnvVar, len(env2))
	copy(sortedEnv1, env1)
	copy(sortedEnv2, env2)

	// sort by name
	sort.Slice(
		sortedEnv1, func(i, j int) bool {
			return sortedEnv1[i].Name < sortedEnv1[j].Name
		},
	)
	sort.Slice(
		sortedEnv2, func(i, j int) bool {
			return sortedEnv2[i].Name < sortedEnv2[j].Name
		},
	)

	for i := range sortedEnv1 {
		if !reflect.DeepEqual(sortedEnv1[i].Value, sortedEnv2[i].Value) {
			return false, fmt.Errorf(
				"env vars %s value not equal, old: %v, new: %v", sortedEnv1[i].Name, sortedEnv1[i].Value,
				sortedEnv2[i].Value,
			)
		}
		if !reflect.DeepEqual(sortedEnv1[i].Name, sortedEnv2[i].Name) {
			return false, fmt.Errorf("env vars name not equal")
		}
	}

	return true, nil
}

func slicesEqualByName[T any](a, b []T, name func(T) string, itemType string) (bool, error) {
	if len(a) != len(b) {
		return false, fmt.Errorf("%s length not equal: %d vs %d", itemType, len(a), len(b))
	}
	cpyA := append([]T(nil), a...)
	cpyB := append([]T(nil), b...)

	sort.Slice(cpyA, func(i, j int) bool { return name(cpyA[i]) < name(cpyA[j]) })
	sort.Slice(cpyB, func(i, j int) bool { return name(cpyB[i]) < name(cpyB[j]) })

	for i := range cpyA {
		na := name(cpyA[i])
		nb := name(cpyB[i])
		if na != nb {
			return false, fmt.Errorf("%s name not equal at index %d: %q != %q", itemType, i, na, nb)
		}
	}
	return true, nil
}

func volumesEqual(vol1, vol2 []corev1.Volume) (bool, error) {
	return slicesEqualByName(vol1, vol2, func(v corev1.Volume) string { return v.Name }, "volume")
}

func volumeMountsEqual(vm1, vm2 []corev1.VolumeMount) (bool, error) {
	return slicesEqualByName(vm1, vm2, func(m corev1.VolumeMount) string { return m.Name }, "volume mount")
}

func sortContainers(containers []corev1.Container) []corev1.Container {
	sorted := make([]corev1.Container, len(containers))
	copy(sorted, containers)
	sort.Slice(
		sorted, func(i, j int) bool {
			return sorted[i].Name < sorted[j].Name
		},
	)
	return sorted
}

// mapsEqual compares two map[string]string.
// It returns true if both maps are nil or empty.
// Otherwise, it compares keys and values for equality.
func mapsEqual(map1, map2 map[string]string) bool {
	isMap1Empty := len(map1) == 0
	isMap2Empty := len(map2) == 0

	if isMap1Empty && isMap2Empty {
		return true
	}

	if isMap1Empty != isMap2Empty {
		return false
	}

	if len(map1) != len(map2) {
		return false
	}

	for k, v := range map1 {
		if val2, ok := map2[k]; !ok || val2 != v {
			return false
		}
	}

	return true
}
