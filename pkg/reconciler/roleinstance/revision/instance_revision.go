/*
Copyright 2026 The RBG Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package revision

import (
	"bytes"
	"encoding/json"

	apps "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/strategicpatch"
	"k8s.io/klog/v2"
	"k8s.io/kubernetes/pkg/controller/history"
	"k8s.io/utils/lru"

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
	SetMatchesRevision(instance *workloadsv1alpha2.RoleInstance, proposedRevision *apps.ControllerRevision, existingRevision *apps.ControllerRevision, cache *lru.Cache) bool
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
	if cr.Annotations == nil {
		cr.Annotations = make(map[string]string)
	}
	for k, v := range instance.Annotations {
		cr.Annotations[k] = v
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

// revisionEqualityCacheKey uniquely identifies a semantic equality result by
// combining the RoleInstance UID, its generation, and the ResourceVersion
// of the existing ControllerRevision being compared.
type revisionEqualityCacheKey struct {
	instanceUID             types.UID
	instanceGeneration      int64
	revisionResourceVersion string
}

// SetMatchesRevision returns true if the proposedRevision (generated from the
// current instance spec) semantically matches the existingRevision, even when
// their raw bytes differ due to serialization changes across client-go
// versions. It works by applying the existing revision back to the instance,
// re-generating a patch with the current serialization format, and comparing
// the raw bytes. Results are cached in the provided LRU cache to avoid
// expensive reconstruction on every reconcile.
func (c *realControl) SetMatchesRevision(
	instance *workloadsv1alpha2.RoleInstance,
	proposedRevision *apps.ControllerRevision,
	existingRevision *apps.ControllerRevision,
	cache *lru.Cache,
) bool {
	if existingRevision == nil || proposedRevision == nil || cache == nil {
		return false
	}
	cacheKey := revisionEqualityCacheKey{
		instanceUID:             instance.UID,
		instanceGeneration:      instance.Generation,
		revisionResourceVersion: existingRevision.ResourceVersion,
	}
	if _, ok := cache.Get(cacheKey); ok {
		return true
	}
	restoredInstance, err := c.ApplyRevision(instance, existingRevision)
	if err != nil {
		klog.V(4).InfoS("SetMatchesRevision: ApplyRevision failed, falling back to new revision creation",
			"instance", klog.KRef(instance.Namespace, instance.Name), "err", err)
		return false
	}
	coreControl := instancecore.New(restoredInstance)
	reconstructedPatch, err := c.buildPatch(restoredInstance, coreControl)
	if err != nil {
		klog.V(4).InfoS("SetMatchesRevision: buildPatch failed, falling back to new revision creation",
			"instance", klog.KRef(instance.Namespace, instance.Name), "err", err)
		return false
	}
	if bytes.Equal(proposedRevision.Data.Raw, reconstructedPatch) {
		cache.Add(cacheKey, struct{}{})
		return true
	}
	return false
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
