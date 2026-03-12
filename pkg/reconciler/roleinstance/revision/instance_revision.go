package revision

import (
	"encoding/json"

	apps "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/strategicpatch"
	"k8s.io/kubernetes/pkg/controller/history"

	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	"sigs.k8s.io/rbgs/client-go/clientset/versioned/scheme"
	instancecore "sigs.k8s.io/rbgs/pkg/reconciler/roleinstance/core"
	instanceutil "sigs.k8s.io/rbgs/pkg/reconciler/roleinstance/utils"
)

var (
	patchCodec = scheme.Codecs.LegacyCodec(workloadsv1alpha2.SchemeGroupVersion)
)

type Interface interface {
	NewRevision(instance *workloadsv1alpha2.RoleInstance, revision int64, collisionCount *int32) (*apps.ControllerRevision, error)
	ApplyRevision(instance *workloadsv1alpha2.RoleInstance, revision *apps.ControllerRevision) (*workloadsv1alpha2.RoleInstance, error)
}

// NewRevisionControl create a normal revision control.
func NewRevisionControl() Interface {
	return &realControl{}
}

type realControl struct {
}

func (c *realControl) NewRevision(instance *workloadsv1alpha2.RoleInstance, revision int64, collisionCount *int32) (*apps.ControllerRevision, error) {
	coreControl := instancecore.New(instance)
	patch, err := c.buildPatch(instance, coreControl)
	if err != nil {
		return nil, err
	}
	cr, err := history.NewControllerRevision(instance,
		instanceutil.ControllerKind,
		instanceutil.GetSelectorMatchLabels(instance.Name),
		runtime.RawExtension{Raw: patch},
		revision,
		collisionCount,
	)
	if err != nil {
		return nil, err
	}
	if cr.ObjectMeta.Annotations == nil {
		cr.ObjectMeta.Annotations = make(map[string]string)
	}
	for k, v := range instance.Annotations {
		cr.ObjectMeta.Annotations[k] = v
	}
	return cr, nil
}

func (c *realControl) ApplyRevision(instance *workloadsv1alpha2.RoleInstance, revision *apps.ControllerRevision) (*workloadsv1alpha2.RoleInstance, error) {
	clone := instance.DeepCopy()
	cloneBytes, err := runtime.Encode(patchCodec, clone)
	if err != nil {
		return nil, err
	}
	patched, err := strategicpatch.StrategicMergePatch(cloneBytes, revision.Data.Raw, clone)
	if err != nil {
		return nil, err
	}
	coreControl := instancecore.New(instance)
	return coreControl.ApplyRevisionPatch(patched)
}

func (c *realControl) buildPatch(instance *workloadsv1alpha2.RoleInstance, coreControl instancecore.Control) ([]byte, error) {
	str, err := runtime.Encode(patchCodec, instance)
	if err != nil {
		return nil, err
	}
	var raw map[string]interface{}
	if err = json.Unmarshal(str, &raw); err != nil {
		return nil, err
	}
	objCopy := make(map[string]interface{})
	specCopy := make(map[string]interface{})
	spec := raw["spec"].(map[string]interface{})
	componentTemplates := spec["components"].([]interface{})

	coreControl.SetRevisionTemplate(specCopy, componentTemplates)
	objCopy["spec"] = specCopy
	patch, err := json.Marshal(objCopy)
	return patch, err
}
