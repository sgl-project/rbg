package inplaceupdate

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/appscode/jsonpatch"
	apps "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/util/retry"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"sigs.k8s.io/rbgs/api/workloads/v1alpha1"
	inplaceutils "sigs.k8s.io/rbgs/pkg/inplace/pod"
	clientdapter "sigs.k8s.io/rbgs/pkg/inplace/pod/clientadapter"
	podinplaceupdate "sigs.k8s.io/rbgs/pkg/inplace/pod/inplaceupdate"
	instanceutil "sigs.k8s.io/rbgs/pkg/reconciler/instance/utils"
	"sigs.k8s.io/rbgs/pkg/utils/revisionadapter"
)

// Interface for managing pods in-place update.
type Interface interface {
	CanUpdateInPlace(ctx context.Context, oldRevision, newRevision *apps.ControllerRevision, opts *podinplaceupdate.UpdateOptions) bool
	Refresh(ctx context.Context, pod *v1.Pod, opts *podinplaceupdate.UpdateOptions) podinplaceupdate.RefreshResult
	Update(ctx context.Context, pod *v1.Pod, oldRevision, newRevision *apps.ControllerRevision, opts *podinplaceupdate.UpdateOptions) podinplaceupdate.UpdateResult
}

func New(c client.Client) Interface {
	return &realControl{
		inplaceControl: podinplaceupdate.New(c, revisionadapter.NewDefaultImpl()),
		adp:            clientdapter.NewAdapter(c),
	}
}

type realControl struct {
	inplaceControl podinplaceupdate.Interface
	adp            clientdapter.Adapter
}

func (c *realControl) CanUpdateInPlace(ctx context.Context, oldRevision, newRevision *apps.ControllerRevision, opts *podinplaceupdate.UpdateOptions) bool {
	componentRevisions, err := c.groupComponentControllerRevision(ctx, oldRevision, newRevision)
	if err != nil {
		return false
	}
	for componentName := range componentRevisions {
		rvHistory := componentRevisions[componentName]
		if isComponentExtensionSpecChanged(rvHistory) {
			return false
		}
		if !c.componentCanUpdateInPlace(
			rvHistory.GetOldPodTemplateRevision(), rvHistory.GetNewPodTemplateRevision(), opts) {
			return false
		}
	}
	return true
}

func (c *realControl) Refresh(ctx context.Context, pod *v1.Pod, opts *podinplaceupdate.UpdateOptions) podinplaceupdate.RefreshResult {
	return c.inplaceControl.Refresh(pod, opts)
}

func (c *realControl) Update(ctx context.Context, pod *v1.Pod, oldRevision, newRevision *apps.ControllerRevision, opts *podinplaceupdate.UpdateOptions) podinplaceupdate.UpdateResult {
	componentsRvHistory, err := c.groupComponentControllerRevision(ctx, oldRevision, newRevision)
	if err != nil {
		return podinplaceupdate.UpdateResult{UpdateErr: err}
	}
	componentName := instanceutil.GetPodComponentName(pod)
	rvHistory, ok := componentsRvHistory[componentName]
	if !ok {
		return podinplaceupdate.UpdateResult{UpdateErr: fmt.Errorf("find new type component %s pod", componentName)}
	}
	if isComponentExtensionSpecChanged(rvHistory) {
		return podinplaceupdate.UpdateResult{}
	}
	oldPodTemplateRevision, newPodTemplateRevision := rvHistory.GetOldPodTemplateRevision(), rvHistory.GetNewPodTemplateRevision()
	changed, err := c.isPodTemplateChanged(oldPodTemplateRevision, newPodTemplateRevision)
	if err != nil {
		return podinplaceupdate.UpdateResult{UpdateErr: err}
	}
	if changed {
		return c.inplaceControl.Update(pod, oldPodTemplateRevision, newPodTemplateRevision, opts)
	}
	newResourceVersion, err := c.onlyUpdateRevision(pod, newRevision)
	if err != nil {
		return podinplaceupdate.UpdateResult{InPlaceUpdate: true, UpdateErr: err}
	}
	return podinplaceupdate.UpdateResult{InPlaceUpdate: true, NewResourceVersion: newResourceVersion}
}

func isComponentExtensionSpecChanged(rH revisionHistory) bool {
	oldSVCName := rH.oldRevision.componentExtensionSpecRevision["serviceName"]
	newSVCName := rH.newRevision.componentExtensionSpecRevision["serviceName"]
	return oldSVCName != newSVCName
}

func (c *realControl) componentCanUpdateInPlace(oldRevision, newRevision *apps.ControllerRevision, opts *podinplaceupdate.UpdateOptions) bool {
	if opts == nil {
		opts = podinplaceupdate.SetOptionsDefaults(opts)
	}
	return opts.CalculateSpec(oldRevision, newRevision, opts) != nil
}

type componentRevision struct {
	podTemplateRevision            *apps.ControllerRevision
	componentExtensionSpecRevision map[string]interface{}
}

type revisionHistory struct {
	oldRevision *componentRevision
	newRevision *componentRevision
}

func (r *revisionHistory) GetOldPodTemplateRevision() *apps.ControllerRevision {
	return r.oldRevision.podTemplateRevision
}

func (r *revisionHistory) GetNewPodTemplateRevision() *apps.ControllerRevision {
	return r.newRevision.podTemplateRevision
}

func (c *realControl) groupComponentControllerRevision(ctx context.Context, oldRevision, newRevision *apps.ControllerRevision) (map[string]revisionHistory, error) {
	logger := log.FromContext(ctx)
	oldRolesRevisions, err := c.splitComponentControllerRevision(oldRevision)
	if err != nil {
		logger.Error(err, "fail to split old role controllerRevision", "oldRevision", klog.KObj(oldRevision))
		return nil, err
	}
	newRolesRevisions, err := c.splitComponentControllerRevision(newRevision)
	if err != nil {
		logger.Error(err, "fail to split new role controllerRevision", "newRevision", klog.KObj(newRevision))
		return nil, err
	}
	groupRevisions := make(map[string]revisionHistory, len(newRolesRevisions))
	for roleType := range newRolesRevisions {
		groupRevisions[roleType] = revisionHistory{
			oldRevision: oldRolesRevisions[roleType],
			newRevision: newRolesRevisions[roleType],
		}
	}
	return groupRevisions, nil
}

func (c *realControl) splitComponentControllerRevision(revision *apps.ControllerRevision) (map[string]*componentRevision, error) {
	var raw map[string]interface{}
	if err := json.Unmarshal(revision.Data.Raw, &raw); err != nil {
		return nil, err
	}
	spec := raw["spec"].(map[string]interface{})
	components := spec["components"].([]interface{})

	revisions := make(map[string]*componentRevision, len(components))
	for i := range components {
		component := components[i].(map[string]interface{})
		componentName := component["name"].(string)
		objCopy := make(map[string]interface{})
		objCopy["spec"] = map[string]interface{}{
			"template": component["template"],
		}
		patch, err := json.Marshal(objCopy)
		if err != nil {
			return nil, err
		}
		delete(component, "template")
		revisions[componentName] = &componentRevision{
			podTemplateRevision: &apps.ControllerRevision{
				ObjectMeta: revision.ObjectMeta,
				Data:       runtime.RawExtension{Raw: patch},
			},
			componentExtensionSpecRevision: component,
		}
	}
	return revisions, nil
}

func (c *realControl) isPodTemplateChanged(oldRevision, newRevision *apps.ControllerRevision) (bool, error) {
	ops, err := jsonpatch.CreatePatch(oldRevision.Data.Raw, newRevision.Data.Raw)
	if err != nil {
		return false, err
	}
	return len(ops) != 0, nil
}

func (c *realControl) onlyUpdateRevision(pod *v1.Pod, newRevision *apps.ControllerRevision) (string, error) {
	if err := c.updateInstanceReadyCondition(pod); err != nil {
		return "", err
	}
	var NewResourceVersion string
	retryErr := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		clone, err := c.adp.GetPod(pod.Namespace, pod.Name)
		if err != nil {
			return err
		}
		if clone.Labels == nil {
			clone.Labels = make(map[string]string)
		}
		clone.Labels[apps.ControllerRevisionHashLabelKey] = inplaceutils.GetShortHash(newRevision.Name)
		err = c.adp.UpdatePod(clone)
		NewResourceVersion = clone.ResourceVersion
		return err
	})
	return NewResourceVersion, retryErr
}

func (c *realControl) updateInstanceReadyCondition(pod *v1.Pod) error {
	if !instanceutil.PodContainsReadinessGate(pod, v1alpha1.InstancePodReadyConditionType) {
		return nil
	}
	newCondition := v1.PodCondition{
		Type:               v1alpha1.InstancePodReadyConditionType,
		LastTransitionTime: metav1.NewTime(time.Now()),
		Status:             v1.ConditionFalse,

		Reason: "StartInstanceUpdate",
	}
	return retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		clone, err := c.adp.GetPod(pod.Namespace, pod.Name)
		if err != nil {
			return err
		}

		inplaceutils.SetPodCondition(clone, newCondition)
		// We only update the ready condition to False, and let Kubelet update it to True
		if newCondition.Status == v1.ConditionFalse {
			inplaceutils.UpdatePodReadyCondition(clone)
		}
		return c.adp.UpdatePodStatus(clone)
	})
}
