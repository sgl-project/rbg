package core

import (
	"encoding/json"
	"fmt"
	"regexp"

	"github.com/appscode/jsonpatch"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/kubernetes/pkg/apis/core/validation"
	"k8s.io/utils/integer"

	appsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	"sigs.k8s.io/rbgs/pkg/constants"
	inplaceutil "sigs.k8s.io/rbgs/pkg/inplace/instance"
	"sigs.k8s.io/rbgs/pkg/inplace/instance/inplaceupdate"
	"sigs.k8s.io/rbgs/pkg/reconciler/roleinstanceset/statelessmode/utils"
)

const (
	shortNameLimitation = 63
)

var (
	inPlaceUpdateTemplateSpecPatchRexp = regexp.MustCompile("/spec/template/spec/components/[0-9]+/size")
)

type commonControl struct {
	*appsv1alpha2.RoleInstanceSet
}

var _ Control = &commonControl{}

func (c *commonControl) IsInitializing() bool {
	return false
}

func (c *commonControl) Selector() labels.Selector {
	return labels.SelectorFromSet(map[string]string{
		constants.RoleInstanceOwnerLabelKey: string(c.UID),
	})
}

func (c *commonControl) SetRevisionTemplate(revisionSpec map[string]interface{}, template map[string]interface{}) {
	revisionSpec["roleInstanceTemplate"] = template
	template["$patch"] = "replace"
}

func (c *commonControl) ApplyRevisionPatch(patched []byte) (*appsv1alpha2.RoleInstanceSet, error) {
	restoredSet := &appsv1alpha2.RoleInstanceSet{}
	if err := json.Unmarshal(patched, restoredSet); err != nil {
		return nil, err
	}
	return restoredSet, nil
}

func (c *commonControl) IsReadyToScale() bool {
	return true
}

func (c *commonControl) NewVersionedInstances(currentSet, updateSet *appsv1alpha2.RoleInstanceSet,
	currentRevision, updateRevision string,
	expectedCreations, expectedCurrentCreations int,
	availableIDs []string,
) ([]*appsv1alpha2.RoleInstance, error) {
	var newInstances []*appsv1alpha2.RoleInstance
	if expectedCreations <= expectedCurrentCreations {
		newInstances = c.newVersionedInstances(currentSet, currentRevision, expectedCreations, &availableIDs)
	} else {
		newInstances = c.newVersionedInstances(currentSet, currentRevision, expectedCurrentCreations, &availableIDs)
		newInstances = append(newInstances, c.newVersionedInstances(updateSet, updateRevision, expectedCreations-expectedCurrentCreations, &availableIDs)...)
	}
	return newInstances, nil
}

func (c *commonControl) newVersionedInstances(set *appsv1alpha2.RoleInstanceSet, revision string, replicas int, availableIDs *[]string) []*appsv1alpha2.RoleInstance {
	var newInstances []*appsv1alpha2.RoleInstance
	for i := 0; i < replicas; i++ {
		if len(*availableIDs) == 0 {
			return newInstances
		}
		id := (*availableIDs)[0]
		*availableIDs = (*availableIDs)[1:]

		instance, _ := GenInstanceFromTemplate(&set.Spec.RoleInstanceTemplate, set, metav1.NewControllerRef(set, utils.ControllerKind))
		c.injectNewVersionedInstances(instance, set, revision, id)
		newInstances = append(newInstances, instance)
	}
	return newInstances
}

func (c *commonControl) injectNewVersionedInstances(instance *appsv1alpha2.RoleInstance, set *appsv1alpha2.RoleInstanceSet, revision string, id string) {
	// inject metadata
	instance.Name = generateInstanceName(set.Name, id)
	instance.Namespace = set.Namespace
	if instance.Labels == nil {
		instance.Labels = make(map[string]string)
	}
	instance.Labels[constants.RoleInstanceIDLabelKey] = id
	instance.Labels[constants.RoleInstanceOwnerLabelKey] = string(set.UID)
	utils.WriteRevisionHash(instance, revision)

	// inject spec
	inplaceupdate.InjectVersionedRoleInstanceSpec(instance)
}

func (c *commonControl) IsInstanceUpdatePaused(instance *appsv1alpha2.RoleInstance) bool {
	return false
}

func (c *commonControl) IsInstanceUpdateReady(instance *appsv1alpha2.RoleInstance, minReadySeconds int32) bool {
	if !utils.IsRunningAndAvailable(instance, minReadySeconds) {
		return false
	}
	condition := inplaceutil.GetRoleInstanceCondition(instance, appsv1alpha2.RoleInstanceReady)
	if condition != nil && condition.Status != v1.ConditionTrue {
		return false
	}
	return true
}

func (c *commonControl) GetInstancesSortFunc(instances []*appsv1alpha2.RoleInstance, waitUpdateIndexes []int) func(i, j int) bool {
	// not-ready < ready, unscheduled < scheduled, and pending < running
	return func(i, j int) bool {
		return utils.ActiveInstancesAvailableRank{
			Instances:     instances,
			AvailableFunc: func(instance *appsv1alpha2.RoleInstance) bool { return c.IsInstanceUpdateReady(instance, 0) },
		}.Less(waitUpdateIndexes[i], waitUpdateIndexes[j])
	}
}

func (c *commonControl) GetUpdateOptions() *inplaceupdate.UpdateOptions {
	opts := &inplaceupdate.UpdateOptions{}
	if c.Spec.UpdateStrategy.InPlaceUpdateStrategy != nil {
		opts.GracePeriodSeconds = c.Spec.UpdateStrategy.InPlaceUpdateStrategy.GracePeriodSeconds
	}
	return opts
}

func (c *commonControl) ValidateInstanceSetUpdate(oldSet, newSet *appsv1alpha2.RoleInstanceSet) error {
	if newSet.Spec.UpdateStrategy.Type != appsv1alpha2.InPlaceIfPossibleUpdateStrategyType {
		return nil
	}

	oldTempJSON, _ := json.Marshal(oldSet.Spec.RoleInstanceTemplate.RoleInstanceSpec)
	newTempJSON, _ := json.Marshal(newSet.Spec.RoleInstanceTemplate.RoleInstanceSpec)
	patches, err := jsonpatch.CreatePatch(oldTempJSON, newTempJSON)
	if err != nil {
		return fmt.Errorf("failed calculate patches between old/new template spec")
	}

	for _, p := range patches {
		if p.Operation == "replace" && inPlaceUpdateTemplateSpecPatchRexp.MatchString(p.Path) {
			return fmt.Errorf("do not allowed to update component size in spec for %s, but found %s %s",
				appsv1alpha2.InPlaceIfPossibleUpdateStrategyType, p.Operation, p.Path)
		}
	}
	return nil
}

func (c *commonControl) ExtraStatusCalculation(status *appsv1alpha2.RoleInstanceSetStatus, instances []*appsv1alpha2.RoleInstance) error {
	return nil
}

func generateInstanceName(prefix, id string) string {
	maxPrefixLen := integer.IntMax(integer.IntMin(len(prefix), shortNameLimitation-len(id)-1), 0)
	return fmt.Sprintf("%s-%s", prefix[:maxPrefixLen], id)
}

func GenInstanceFromTemplate(template *appsv1alpha2.RoleInstanceTemplate, set *appsv1alpha2.RoleInstanceSet, controllerRef *metav1.OwnerReference) (*appsv1alpha2.RoleInstance, error) {
	desiredLabels := genInstanceLabelSet(set)
	desiredFinalizers := genInstanceFinalizers(set)
	accessor, err := meta.Accessor(set)
	if err != nil {
		return nil, fmt.Errorf("parentObject does not have ObjectMeta, %v", err)
	}
	prefix := genInstanceNamePrefix(accessor.GetName())

	instance := &appsv1alpha2.RoleInstance{
		ObjectMeta: metav1.ObjectMeta{
			Labels:       desiredLabels,
			GenerateName: prefix,
			Finalizers:   desiredFinalizers,
		},
	}
	if controllerRef != nil {
		instance.OwnerReferences = append(instance.OwnerReferences, *controllerRef)
	}
	instance.Spec = *template.RoleInstanceSpec.DeepCopy()
	// Propagate gang-scheduling annotation from RoleInstanceSet to RoleInstance so that
	// the instance controller can enforce gang-scheduling constraints (e.g. orphan-pod check,
	// atomic pod recreation) without access to the parent RBG.
	if v, ok := set.Annotations[constants.RoleInstanceGangSchedulingAnnotationKey]; ok {
		if instance.Annotations == nil {
			instance.Annotations = make(map[string]string)
		}
		instance.Annotations[constants.RoleInstanceGangSchedulingAnnotationKey] = v
	}
	return instance, nil
}

func genInstanceLabelSet(set *appsv1alpha2.RoleInstanceSet) labels.Set {
	desiredLabels := make(labels.Set)
	for k, v := range set.Labels {
		desiredLabels[k] = v
	}
	return desiredLabels
}

func genInstanceFinalizers(set *appsv1alpha2.RoleInstanceSet) []string {
	desiredFinalizers := make([]string, len(set.Finalizers))
	copy(desiredFinalizers, set.Finalizers)
	return desiredFinalizers
}

func genInstanceNamePrefix(controllerName string) string {
	// use the dash (if the name isn't too long) to make the instance name a bit prettier
	prefix := fmt.Sprintf("%s-", controllerName)
	if len(validation.ValidatePodName(prefix, true)) != 0 {
		prefix = controllerName
	}
	return prefix
}
