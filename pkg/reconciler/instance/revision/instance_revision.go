package revision

import (
	"encoding/json"

	apps "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/strategicpatch"
	"k8s.io/kubernetes/pkg/controller/history"

	"sigs.k8s.io/rbgs/api/workloads/v1alpha1"
	"sigs.k8s.io/rbgs/client-go/clientset/versioned/scheme"
	instancecore "sigs.k8s.io/rbgs/pkg/reconciler/instance/core"
	instanceutil "sigs.k8s.io/rbgs/pkg/reconciler/instance/utils"
)

var (
	patchCodec = scheme.Codecs.LegacyCodec(v1alpha1.SchemeGroupVersion)
)

type Interface interface {
	NewRevision(instance *v1alpha1.Instance, revision int64, collisionCount *int32) (*apps.ControllerRevision, error)
	ApplyRevision(instance *v1alpha1.Instance, revision *apps.ControllerRevision) (*v1alpha1.Instance, error)
}

// NewRevisionControl create a normal revision control.
func NewRevisionControl() Interface {
	return &realControl{}
}

type realControl struct {
}

func (c *realControl) NewRevision(instance *v1alpha1.Instance, revision int64, collisionCount *int32) (*apps.ControllerRevision, error) {
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

func (c *realControl) ApplyRevision(instance *v1alpha1.Instance, revision *apps.ControllerRevision) (*v1alpha1.Instance, error) {
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

func (c *realControl) buildPatch(instance *v1alpha1.Instance, coreControl instancecore.Control) ([]byte, error) {
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
