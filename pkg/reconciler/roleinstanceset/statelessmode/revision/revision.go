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
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/klog/v2"
	"k8s.io/kubernetes/pkg/controller/history"
	"k8s.io/utils/lru"

	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	"sigs.k8s.io/rbgs/pkg/reconciler/roleinstanceset/statelessmode/core"
	"sigs.k8s.io/rbgs/pkg/reconciler/roleinstanceset/statelessmode/utils"
)

var (
	patchCodec = scheme.Codecs.LegacyCodec(workloadsv1alpha2.SchemeGroupVersion)
)

// Interface is a interface to new and apply ControllerRevision.
type Interface interface {
	NewRevision(set *workloadsv1alpha2.RoleInstanceSet, revision int64, collisionCount *int32) (*apps.ControllerRevision, error)
	ApplyRevision(set *workloadsv1alpha2.RoleInstanceSet, revision *apps.ControllerRevision) (*workloadsv1alpha2.RoleInstanceSet, error)
	SetMatchesRevision(set *workloadsv1alpha2.RoleInstanceSet, proposedRevision *apps.ControllerRevision, existingRevision *apps.ControllerRevision, cache *lru.Cache) bool
}

// NewRevisionControl create a normal revision control.
func NewRevisionControl() Interface {
	return &realControl{}
}

type realControl struct {
}

func (c *realControl) NewRevision(set *workloadsv1alpha2.RoleInstanceSet, revision int64, collisionCount *int32) (*apps.ControllerRevision, error) {
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
	if cr.Annotations == nil {
		cr.Annotations = make(map[string]string)
	}
	for key, value := range set.Annotations {
		cr.Annotations[key] = value
	}
	return cr, nil
}

// getPatch returns a merge patch that can be applied to restore a InstanceSet to a
// previous version. If the returned error is nil the patch is valid. The current state that we save is just the
// InstanceTemplate. We can modify this later to encompass more state (or less) and remain compatible with previously
// recorded patches.
func (c *realControl) getPatch(set *workloadsv1alpha2.RoleInstanceSet, coreControl core.Control) ([]byte, error) {
	str, err := runtime.Encode(patchCodec, set)
	if err != nil {
		return nil, err
	}
	var raw map[string]interface{}
	_ = json.Unmarshal(str, &raw)
	objCopy := make(map[string]interface{})
	specCopy := make(map[string]interface{})
	spec := raw["spec"].(map[string]interface{})
	template := spec["roleInstanceTemplate"].(map[string]interface{})

	coreControl.SetRevisionTemplate(specCopy, template)
	objCopy["spec"] = specCopy
	patch, err := json.Marshal(objCopy)
	return patch, err
}

func (c *realControl) ApplyRevision(set *workloadsv1alpha2.RoleInstanceSet, revision *apps.ControllerRevision) (*workloadsv1alpha2.RoleInstanceSet, error) {
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

// revisionEqualityCacheKey uniquely identifies a semantic equality result by
// combining the RoleInstanceSet UID, its generation, and the ResourceVersion
// of the existing ControllerRevision being compared.
type revisionEqualityCacheKey struct {
	setUID                  types.UID
	setGeneration           int64
	revisionResourceVersion string
}

// SetMatchesRevision returns true if the proposedRevision (generated from the
// current set spec) semantically matches the existingRevision, even when their
// raw bytes differ due to serialization changes across client-go versions.
// It works by applying the existing revision back to the set, re-generating a
// patch with the current serialization format, and comparing the raw bytes.
// Results are cached in the provided LRU cache to avoid expensive
// reconstruction on every reconcile.
func (c *realControl) SetMatchesRevision(
	set *workloadsv1alpha2.RoleInstanceSet,
	proposedRevision *apps.ControllerRevision,
	existingRevision *apps.ControllerRevision,
	cache *lru.Cache,
) bool {
	if existingRevision == nil || proposedRevision == nil || cache == nil {
		return false
	}
	cacheKey := revisionEqualityCacheKey{
		setUID:                  set.UID,
		setGeneration:           set.Generation,
		revisionResourceVersion: existingRevision.ResourceVersion,
	}
	if _, ok := cache.Get(cacheKey); ok {
		return true
	}
	restoredSet, err := c.ApplyRevision(set, existingRevision)
	if err != nil {
		klog.V(4).InfoS("SetMatchesRevision: ApplyRevision failed, falling back to new revision creation",
			"set", klog.KRef(set.Namespace, set.Name), "err", err)
		return false
	}
	coreControl := core.New(restoredSet)
	reconstructedPatch, err := c.getPatch(restoredSet, coreControl)
	if err != nil {
		klog.V(4).InfoS("SetMatchesRevision: getPatch failed, falling back to new revision creation",
			"set", klog.KRef(set.Namespace, set.Name), "err", err)
		return false
	}
	if bytes.Equal(proposedRevision.Data.Raw, reconstructedPatch) {
		cache.Add(cacheKey, struct{}{})
		return true
	}
	return false
}
