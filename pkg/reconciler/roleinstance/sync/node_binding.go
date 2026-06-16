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

package sync

import (
	"fmt"
	"strings"
	"sync"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/klog/v2"

	"sigs.k8s.io/rbgs/api/workloads/constants"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
)

// NodeBindingStore is a two-level in-memory store that maps binding keys to
// sets of node names. The outer key is the RBG UID, enabling O(1) eviction
// of all bindings for a deleted RBG via EvictByUID.
//
// Inner structure per UID:
//
//	Pod-level:       {podName}              → set of size 1
//	Component-level: {roleName}-{component} → set of all nodes that have
//	                                         hosted the same component type
//
// Using the RBG's real Kubernetes object UID (RBGOwnerUIDLabelKey) ensures
// that when an RBG is deleted and recreated with the same name, the new RBG
// gets a different UID and does not inherit stale bindings.
//
// Concurrency: a single sync.RWMutex protects both map levels. All operations
// (Add, Load, EvictByUID) hold the lock for very short durations (map lookups
// and set inserts), so contention is negligible in practice.
type NodeBindingStore struct {
	mu       sync.RWMutex
	bindings map[string]*rbgBindings
}

// rbgBindings holds all node bindings for a single RBG, keyed by sub-key
// (pod name or role-component name). Each RBG's bindings are isolated so
// that EvictByUID can remove them all in O(1).
type rbgBindings struct {
	keys map[string]sets.Set[string]
}

// NewNodeBindingStore creates a new empty NodeBindingStore.
func NewNodeBindingStore() *NodeBindingStore {
	return &NodeBindingStore{
		bindings: make(map[string]*rbgBindings),
	}
}

// Add inserts nodeName into the set for key. The key format is
// "{rbgUID}/{subKey}". Idempotent — calling it repeatedly with the same
// (key, node) pair is a no-op.
func (s *NodeBindingStore) Add(key, nodeName string) {
	uid, subKey, ok := splitKey(key)
	if !ok {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	rb := s.bindings[uid]
	if rb == nil {
		rb = &rbgBindings{keys: make(map[string]sets.Set[string])}
		s.bindings[uid] = rb
	}
	if rb.keys[subKey] == nil {
		rb.keys[subKey] = sets.New[string]()
	}
	rb.keys[subKey].Insert(nodeName)
}

// Load returns a clone of the node name set for key. Returns nil if no
// binding exists. The returned set is a snapshot safe for concurrent
// iteration — callers do not need to hold any lock.
// The key format is "{rbgUID}/{subKey}".
func (s *NodeBindingStore) Load(key string) sets.Set[string] {
	uid, subKey, ok := splitKey(key)
	if !ok {
		return nil
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	rb := s.bindings[uid]
	if rb == nil {
		return nil
	}
	v := rb.keys[subKey]
	if v == nil {
		return nil
	}
	return v.Clone()
}

// EvictByUID removes all bindings for the given RBG UID in O(1).
// Intended to be called from an RBG delete event handler so that
// bindings are cleaned up immediately when an RBG is deleted.
func (s *NodeBindingStore) EvictByUID(uid string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, existed := s.bindings[uid]; existed {
		klog.InfoS("in-place scheduling: evicted all node bindings for RBG", "uid", uid)
		delete(s.bindings, uid)
	}
}

// Len returns the number of RBG UIDs tracked in the store.
// Exposed primarily for testing and debugging.
func (s *NodeBindingStore) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.bindings)
}

// splitKey splits a "{uid}/{subKey}" string into its components.
// Returns ("", "", false) if the key does not contain a "/".
func splitKey(key string) (uid, subKey string, ok bool) {
	uid, subKey, ok = strings.Cut(key, "/")
	if !ok || uid == "" || subKey == "" {
		return "", "", false
	}
	return uid, subKey, true
}

// buildKey constructs the binding store key based on the given granularity.
//
// Pod-level key: {rbgUID}/{podName}
//
//	Each Pod has a unique key; the set always contains exactly one node.
//
// Component-level key: {rbgUID}/{roleName}-{componentName}
//
//	Multiple Pods of the same component type share the same key, so their
//	nodes accumulate into a set.
//
// Returns an empty string if the RBG UID cannot be determined.
func buildKey(granularity string, instance *workloadsv1alpha2.RoleInstance, pod *v1.Pod) string {
	rbgUID := instance.Labels[constants.RBGOwnerUIDLabelKey]
	if rbgUID == "" {
		return ""
	}

	switch granularity {
	case constants.InplaceSchedulingGranularityPod:
		return fmt.Sprintf("%s/%s", rbgUID, pod.Name)

	case constants.InplaceSchedulingGranularityComponent:
		roleName := instance.Labels[constants.RoleNameLabelKey]
		componentName := pod.Labels[constants.ComponentNameLabelKey]
		if roleName == "" || componentName == "" {
			return ""
		}
		return fmt.Sprintf("%s/%s-%s", rbgUID, roleName, componentName)

	default:
		return ""
	}
}

// resolveGranularity determines the binding granularity from the instance.
// When the granularity annotation is not set, it auto-detects:
//   - Stateless mode (no role-instance-index label) → Component
//   - Stateful mode  (has role-instance-index label) → Pod
func resolveGranularity(instance *workloadsv1alpha2.RoleInstance) string {
	if g := instance.Annotations[constants.RoleInplaceSchedulingGranularityAnnotationKey]; g != "" {
		return g
	}
	// Stateful RoleInstances carry the role-instance-index label;
	// Stateless ones do not. Use this to distinguish the two modes.
	if _, ok := instance.Labels[constants.RoleInstanceIndexLabelKey]; !ok {
		return constants.InplaceSchedulingGranularityComponent
	}
	return constants.InplaceSchedulingGranularityPod
}

// RecordNodeBindings iterates over the given pods and records node assignments
// into the binding store for pods that are Running and Ready.
//
// The granularity is auto-detected from the instance annotations (or explicitly
// configured). The same Add API is used for both granularities — the key format
// determines whether the binding is per-Pod or per-Component.
//
// This is designed to piggyback on the existing getOwnedPods result — no
// additional API calls.
func RecordNodeBindings(store *NodeBindingStore, instance *workloadsv1alpha2.RoleInstance, pods []*v1.Pod) {
	granularity := resolveGranularity(instance)

	for _, pod := range pods {
		if pod.Spec.NodeName == "" {
			continue
		}
		if !isPodRunningAndReady(pod) {
			continue
		}

		key := buildKey(granularity, instance, pod)
		if key == "" {
			continue
		}

		// Log when a key is first recorded — helps operators debug scheduling
		// decisions without noise at default log levels.
		if store.Load(key) == nil {
			klog.V(4).InfoS("in-place scheduling: recording new node binding",
				"key", key, "node", pod.Spec.NodeName,
				"granularity", granularity,
				"pod", klog.KObj(pod))
		}
		store.Add(key, pod.Spec.NodeName)
	}
}

// isPodRunningAndReady reports whether the pod is in Running phase and has
// the Ready condition set to True.
func isPodRunningAndReady(pod *v1.Pod) bool {
	if pod.Status.Phase != v1.PodRunning {
		return false
	}
	for _, cond := range pod.Status.Conditions {
		if cond.Type == v1.PodReady {
			return cond.Status == v1.ConditionTrue
		}
	}
	return false
}

const hostnameLabelKey = "kubernetes.io/hostname"

// InjectInPlaceScheduling injects nodeAffinity into the pod based on the
// in-place scheduling annotation and the node binding store. It is called
// during pod creation in createPods.
//
// The granularity is auto-detected (or explicitly configured). For Pod-level,
// the affinity targets a single node; for Component-level, it targets all
// nodes that have hosted the same component type.
//
// In Preferred mode, a preferred scheduling term (weight 100) is injected.
// In Required mode, a hard constraint is injected.
//
// The injection is skipped when:
//   - The in-place scheduling annotation is not set on the instance.
//   - The annotation mode is not "Preferred" or "Required" (with a warning).
//   - The pod has exclusive topology affinity (to avoid conflicts).
//   - No node binding exists for this Pod (initial creation or controller just restarted).
//   - The granularity is Component but the Pod has no component-name label.
func InjectInPlaceScheduling(pod *v1.Pod, instance *workloadsv1alpha2.RoleInstance, store *NodeBindingStore) {
	// 1. Check annotation and validate mode
	mode := instance.Annotations[constants.RoleInplaceSchedulingAnnotationKey]
	if mode == "" {
		return
	}
	if mode != constants.InplaceSchedulingPreferred && mode != constants.InplaceSchedulingRequired {
		klog.InfoS("in-place scheduling: unrecognized mode, skipping injection",
			"instance", klog.KObj(instance), "mode", mode,
			"validValues", fmt.Sprintf("%q or %q", constants.InplaceSchedulingPreferred, constants.InplaceSchedulingRequired))
		return
	}

	// 2. Check exclusive topology conflict
	if pod.Annotations[constants.GroupExclusiveTopologyKey] != "" {
		return
	}

	// 3. Build avoid expression if configured (used in step 6).
	// In Required mode, this expression is merged into the same NodeSelectorTerm
	// as the in-place hostname constraint (AND semantics within a single term).
	// In Preferred mode (or when no binding exists), it is injected as a
	// standalone required term — the scheduler ANDs required and preferred types.
	var avoidExpr *v1.NodeSelectorRequirement
	if avoidLabelKey := instance.Annotations[constants.RoleInplaceSchedulingAvoidAnnotationKey]; avoidLabelKey != "" {
		avoidExpr = &v1.NodeSelectorRequirement{
			Key:      avoidLabelKey,
			Operator: v1.NodeSelectorOpDoesNotExist,
		}
	}

	// 4. Determine granularity and build key
	granularity := resolveGranularity(instance)
	key := buildKey(granularity, instance, pod)
	if key == "" {
		return
	}

	// 5. Load binding
	nodes := store.Load(key)
	if len(nodes) == 0 {
		// No binding — if avoid is configured, inject it as a standalone
		// hard constraint so the pod still avoids labelled nodes.
		if avoidExpr != nil {
			ensureNodeAffinity(pod)
			na := pod.Spec.Affinity.NodeAffinity
			if na.RequiredDuringSchedulingIgnoredDuringExecution == nil {
				na.RequiredDuringSchedulingIgnoredDuringExecution = &v1.NodeSelector{}
			}
			na.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms = append(
				na.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms,
				v1.NodeSelectorTerm{MatchExpressions: []v1.NodeSelectorRequirement{*avoidExpr}},
			)
		}
		return
	}

	// 6. Inject affinity
	ensureNodeAffinity(pod)
	nodeAffinity := pod.Spec.Affinity.NodeAffinity
	values := sets.List(nodes) // sorted for deterministic output

	switch mode {
	case constants.InplaceSchedulingPreferred:
		// In Preferred mode, avoid (if any) is a separate required term.
		// The scheduler ANDs required and preferred affinity types.
		if avoidExpr != nil {
			if nodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution == nil {
				nodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution = &v1.NodeSelector{}
			}
			nodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms = append(
				nodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms,
				v1.NodeSelectorTerm{MatchExpressions: []v1.NodeSelectorRequirement{*avoidExpr}},
			)
		}
		term := v1.PreferredSchedulingTerm{
			Weight: 100,
			Preference: v1.NodeSelectorTerm{
				MatchExpressions: []v1.NodeSelectorRequirement{
					{
						Key:      hostnameLabelKey,
						Operator: v1.NodeSelectorOpIn,
						Values:   values,
					},
				},
			},
		}
		nodeAffinity.PreferredDuringSchedulingIgnoredDuringExecution = append(
			nodeAffinity.PreferredDuringSchedulingIgnoredDuringExecution, term)

	case constants.InplaceSchedulingRequired:
		// In Required mode, merge avoid into the SAME NodeSelectorTerm as the
		// hostname constraint. Within a single term, MatchExpressions are ANDed.
		// Separate terms would be ORed, which is semantically wrong here.
		expressions := []v1.NodeSelectorRequirement{
			{
				Key:      hostnameLabelKey,
				Operator: v1.NodeSelectorOpIn,
				Values:   values,
			},
		}
		if avoidExpr != nil {
			expressions = append(expressions, *avoidExpr)
		}
		if nodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution == nil {
			nodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution = &v1.NodeSelector{}
		}
		nodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms = append(
			nodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms,
			v1.NodeSelectorTerm{MatchExpressions: expressions},
		)
	}
}

// ensureNodeAffinity initializes the Affinity and NodeAffinity structs if nil.
func ensureNodeAffinity(pod *v1.Pod) {
	if pod.Spec.Affinity == nil {
		pod.Spec.Affinity = &v1.Affinity{}
	}
	if pod.Spec.Affinity.NodeAffinity == nil {
		pod.Spec.Affinity.NodeAffinity = &v1.NodeAffinity{}
	}
}
