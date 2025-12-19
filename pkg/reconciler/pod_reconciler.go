package reconciler

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/strategicpatch"
	coreapplyv1 "k8s.io/client-go/applyconfigurations/core/v1"
	"k8s.io/kubernetes/pkg/api/legacyscheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	workloadsv1alpha1 "sigs.k8s.io/rbgs/api/workloads/v1alpha1"
	"sigs.k8s.io/rbgs/pkg/discovery"
	"sigs.k8s.io/rbgs/pkg/scheduler"
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
		// KEP-8: Resolve role template (supports both traditional mode and templateRef)
		if role.UsesRoleTemplate() {
			// Template mode: find template and apply patch
			roleTemplate, err := rbg.FindRoleTemplate(role.GetEffectiveTemplateName())
			if err != nil {
				return nil, fmt.Errorf("failed to find roleTemplate: %w", err)
			}

			merged, err := applyStrategicMergePatch(roleTemplate.Template, role.TemplatePatch)
			if err != nil {
				return nil, fmt.Errorf("failed to apply templatePatch: %w", err)
			}
			podTemplateSpec = merged
		} else if role.TemplateSource.Template != nil {
			// Traditional mode: use role.Template directly (pointer type after migration)
			podTemplateSpec = *role.TemplateSource.Template.DeepCopy()
		}
		// else: podTemplateSpec stays as zero value, same behavior as before when Template was a value type
	}
	podAnnotations := podTemplateSpec.Annotations
	if podAnnotations == nil {
		podAnnotations = make(map[string]string)
	}
	// inject objects
	injector := discovery.NewDefaultInjector(r.scheme, r.client)
	if r.injectObjects == nil {
		r.injectObjects = []string{"config", "sidecar", "common_env"}
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
	if utils.ContainsString(r.injectObjects, "common_env") {
		if err := injector.InjectEnv(ctx, &podTemplateSpec, rbg, role); err != nil {
			return nil, fmt.Errorf("failed to inject env vars: %w", err)
		}
	}
	if utils.ContainsString(r.injectObjects, "lws_env") {
		if err := injector.InjectLeaderWorkerSetEnv(ctx, &podTemplateSpec, rbg, role); err != nil {
			return nil, fmt.Errorf("failed to inject env vars: %w", err)
		}
	}

	// Set Exclusive topology
	if topologyKey, found := rbg.GetExclusiveKey(); found {
		if podAnnotations[workloadsv1alpha1.DisableExclusiveKeyAnnotationKey] == "" {
			podAnnotations[workloadsv1alpha1.ExclusiveKeyAnnotationKey] = topologyKey
			uniqueKey := rbg.GenGroupUniqueKey()
			err := setExclusiveAffinities(
				&podTemplateSpec, uniqueKey, topologyKey, workloadsv1alpha1.SetGroupUniqueHashLabelKey,
			)
			if err != nil {
				return nil, err
			}
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

	podTemplateApplyConfiguration.WithLabels(podLabels).WithAnnotations(podAnnotations)
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

// SetExclusiveAffinities set the pod affinity/anti-affinity
func setExclusiveAffinities(pod *corev1.PodTemplateSpec,
	uniqueKey string,
	topologyKey string,
	podAffinityKey string) error {
	if len(topologyKey) == 0 {
		return fmt.Errorf("topology key can't be nil")
	}
	if exclusiveAffinityApplied(*pod, topologyKey) {
		return nil
	}
	if pod.Spec.Affinity == nil {
		pod.Spec.Affinity = &corev1.Affinity{}
	}
	if pod.Spec.Affinity.PodAffinity == nil {
		pod.Spec.Affinity.PodAffinity = &corev1.PodAffinity{}
	}
	if pod.Spec.Affinity.PodAntiAffinity == nil {
		pod.Spec.Affinity.PodAntiAffinity = &corev1.PodAntiAffinity{}
	}

	// Pod affinity ensures the pods of this set land on the same topology domain.
	pod.Spec.Affinity.PodAffinity.RequiredDuringSchedulingIgnoredDuringExecution =
		append(
			pod.Spec.Affinity.PodAffinity.RequiredDuringSchedulingIgnoredDuringExecution,
			corev1.PodAffinityTerm{
				LabelSelector: &metav1.LabelSelector{
					MatchExpressions: []metav1.LabelSelectorRequirement{
						{
							Key:      podAffinityKey,
							Operator: metav1.LabelSelectorOpIn,
							Values:   []string{uniqueKey},
						},
					},
				},
				TopologyKey: topologyKey,
			},
		)
	// Pod anti-affinity ensures exclusively this set lands on the topology, preventing multiple sets per topology domain.
	pod.Spec.Affinity.PodAntiAffinity.RequiredDuringSchedulingIgnoredDuringExecution =
		append(
			pod.Spec.Affinity.PodAntiAffinity.RequiredDuringSchedulingIgnoredDuringExecution,
			corev1.PodAffinityTerm{
				LabelSelector: &metav1.LabelSelector{
					MatchExpressions: []metav1.LabelSelectorRequirement{
						{
							Key:      podAffinityKey,
							Operator: metav1.LabelSelectorOpExists,
						},
						{
							Key:      podAffinityKey,
							Operator: metav1.LabelSelectorOpNotIn,
							Values:   []string{uniqueKey},
						},
					},
				},
				TopologyKey: topologyKey,
			},
		)
	return nil
}

func exclusiveAffinityApplied(podTemplateSpec corev1.PodTemplateSpec, topologyKey string) bool {
	if podTemplateSpec.Spec.Affinity == nil ||
		podTemplateSpec.Spec.Affinity.PodAffinity == nil ||
		podTemplateSpec.Spec.Affinity.PodAntiAffinity == nil {
		return false
	}
	hasAffinity := false
	hasAntiAffinity := false
	for _, podAffinityTerm := range podTemplateSpec.
		Spec.Affinity.PodAffinity.RequiredDuringSchedulingIgnoredDuringExecution {
		if podAffinityTerm.TopologyKey == topologyKey {
			hasAffinity = true
		}
	}
	for _, term := range podTemplateSpec.Spec.Affinity.PodAntiAffinity.RequiredDuringSchedulingIgnoredDuringExecution {
		if term.TopologyKey == topologyKey {
			hasAntiAffinity = true
		}
	}
	return hasAffinity && hasAntiAffinity
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
	if equal, err := containersEqual(spec1.InitContainers, spec2.InitContainers); !equal {
		return false, fmt.Errorf("podTemplate initContainers not equal: %s", err.Error())
	}

	if equal, err := containersEqual(spec1.Containers, spec2.Containers); !equal {
		return false, fmt.Errorf("podTemplate containers not equal: %s", err.Error())
	}

	if equal, err := volumesEqual(spec1.Volumes, spec2.Volumes); !equal {
		return false, fmt.Errorf("podTemplate volumes not equal: %s", err.Error())
	}

	return true, nil
}

func containersEqual(containers1, containers2 []corev1.Container) (bool, error) {
	if len(containers1) != len(containers2) {
		return false, fmt.Errorf("containers length not equal")
	}

	sortedContainers1 := sortContainers(containers1)
	sortedContainers2 := sortContainers(containers2)

	normalizeByApiServerDefaults := func(c corev1.Container) corev1.Container {
		c.Env = utils.FilterSystemEnvs(c.Env)
		p := &corev1.Pod{Spec: corev1.PodSpec{Containers: []corev1.Container{c}}}
		legacyscheme.Scheme.Default(p)
		return p.Spec.Containers[0]
	}

	for i := range sortedContainers1 {
		if equal := reflect.DeepEqual(normalizeByApiServerDefaults(sortedContainers1[i]),
			normalizeByApiServerDefaults(sortedContainers2[i])); !equal {
			return false, fmt.Errorf("container %s not equal", sortedContainers1[i].Name)
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

// applyStrategicMergePatch applies a strategic merge patch to a PodTemplateSpec.
// If the patch is nil or empty, returns the base template unchanged.
// This function is used by both RoleTemplates (KEP-8) and LeaderWorkerSet patches.
func applyStrategicMergePatch(base corev1.PodTemplateSpec, patch runtime.RawExtension) (corev1.PodTemplateSpec, error) {
	if len(patch.Raw) == 0 {
		return base, nil
	}

	baseBytes, err := json.Marshal(base)
	if err != nil {
		return corev1.PodTemplateSpec{}, fmt.Errorf("failed to marshal base template: %w", err)
	}

	mergedBytes, err := strategicpatch.StrategicMergePatch(baseBytes, patch.Raw, &corev1.PodTemplateSpec{})
	if err != nil {
		return corev1.PodTemplateSpec{}, fmt.Errorf("failed to merge patch: %w", err)
	}

	result := &corev1.PodTemplateSpec{}
	if err := json.Unmarshal(mergedBytes, result); err != nil {
		return corev1.PodTemplateSpec{}, fmt.Errorf("failed to unmarshal merged template: %w", err)
	}

	return *result, nil
}
