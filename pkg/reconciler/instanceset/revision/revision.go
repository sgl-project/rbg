package revision

import (
	"encoding/json"

	apps "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/strategicpatch"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/kubernetes/pkg/controller/history"

	appsv1alpha1 "sigs.k8s.io/rbgs/api/workloads/v1alpha1"
	"sigs.k8s.io/rbgs/pkg/reconciler/instanceset/core"
	"sigs.k8s.io/rbgs/pkg/reconciler/instanceset/utils"
)

var (
	patchCodec = scheme.Codecs.LegacyCodec(appsv1alpha1.SchemeGroupVersion)
)

// Interface is a interface to new and apply ControllerRevision.
type Interface interface {
	NewRevision(set *appsv1alpha1.InstanceSet, revision int64, collisionCount *int32) (*apps.ControllerRevision, error)
	ApplyRevision(set *appsv1alpha1.InstanceSet, revision *apps.ControllerRevision) (*appsv1alpha1.InstanceSet, error)
}

// NewRevisionControl create a normal revision control.
func NewRevisionControl() Interface {
	return &realControl{}
}

type realControl struct {
}

func (c *realControl) NewRevision(set *appsv1alpha1.InstanceSet, revision int64, collisionCount *int32) (*apps.ControllerRevision, error) {
	coreControl := core.New(set)
	patch, err := c.getPatch(set, coreControl)
	if err != nil {
		return nil, err
	}
	cr, err := history.NewControllerRevision(set,
		utils.ControllerKind,
		utils.GenSelectorLabel(set),
		runtime.RawExtension{Raw: patch},
		revision,
		collisionCount)
	if err != nil {
		return nil, err
	}
	if cr.ObjectMeta.Annotations == nil {
		cr.ObjectMeta.Annotations = make(map[string]string)
	}
	for key, value := range set.Annotations {
		cr.ObjectMeta.Annotations[key] = value
	}
	return cr, nil
}

// getPatch returns a merge patch that can be applied to restore a InstanceSet to a
// previous version. If the returned error is nil the patch is valid. The current state that we save is just the
// InstanceTemplate. We can modify this later to encompass more state (or less) and remain compatible with previously
// recorded patches.
func (c *realControl) getPatch(set *appsv1alpha1.InstanceSet, coreControl core.Control) ([]byte, error) {
	str, err := runtime.Encode(patchCodec, set)
	if err != nil {
		return nil, err
	}
	var raw map[string]interface{}
	_ = json.Unmarshal(str, &raw)
	objCopy := make(map[string]interface{})
	specCopy := make(map[string]interface{})
	spec := raw["spec"].(map[string]interface{})
	template := spec["instanceTemplate"].(map[string]interface{})

	coreControl.SetRevisionTemplate(specCopy, template)
	objCopy["spec"] = specCopy
	patch, err := json.Marshal(objCopy)
	return patch, err
}

func (c *realControl) ApplyRevision(set *appsv1alpha1.InstanceSet, revision *apps.ControllerRevision) (*appsv1alpha1.InstanceSet, error) {
	clone := set.DeepCopy()
	cloneBytes, err := runtime.Encode(patchCodec, clone)
	if err != nil {
		return nil, err
	}
	patched, err := strategicpatch.StrategicMergePatch(cloneBytes, revision.Data.Raw, set)
	if err != nil {
		return nil, err
	}
	coreControl := core.New(clone)
	return coreControl.ApplyRevisionPatch(patched)
}
