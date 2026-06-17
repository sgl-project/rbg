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
	"testing"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"sigs.k8s.io/rbgs/api/workloads/constants"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
)

// ---------------------------------------------------------------------------
// NodeBindingStore
// ---------------------------------------------------------------------------

func TestNodeBindingStore_AddAndLoad(t *testing.T) {
	store := NewNodeBindingStore()

	store.Add("uid/prefill-0", "node-A")

	got := store.Load("uid/prefill-0")
	assert.Equal(t, []string{"node-A"}, got.UnsortedList())
}

func TestNodeBindingStore_AddIdempotent(t *testing.T) {
	store := NewNodeBindingStore()

	store.Add("uid/prefill-0", "node-A")
	store.Add("uid/prefill-0", "node-A") // duplicate

	got := store.Load("uid/prefill-0")
	assert.Len(t, got, 1)
	assert.Equal(t, []string{"node-A"}, got.UnsortedList())
}

func TestNodeBindingStore_AddMultipleNodes(t *testing.T) {
	store := NewNodeBindingStore()

	// Component-level: multiple Pods add different nodes under the same key
	store.Add("uid/prefill-Prefill", "node-A")
	store.Add("uid/prefill-Prefill", "node-B")
	store.Add("uid/prefill-Prefill", "node-C")

	got := store.Load("uid/prefill-Prefill")
	assert.Len(t, got, 3)
	assert.ElementsMatch(t, []string{"node-A", "node-B", "node-C"}, got.UnsortedList())
}

func TestNodeBindingStore_LoadMissing(t *testing.T) {
	store := NewNodeBindingStore()

	got := store.Load("uid/nonexistent")
	assert.Nil(t, got)
	assert.Len(t, got, 0) // nil set has len 0
}

func TestNodeBindingStore_EvictByUID(t *testing.T) {
	store := NewNodeBindingStore()

	// Two UIDs with multiple sub-keys each.
	store.Add("uid-1/prefill-0", "node-A")
	store.Add("uid-1/prefill-1", "node-B")
	store.Add("uid-2/decode-0", "node-C")

	// Evict uid-1 — should remove all its bindings.
	store.EvictByUID("uid-1")
	assert.Nil(t, store.Load("uid-1/prefill-0"))
	assert.Nil(t, store.Load("uid-1/prefill-1"))

	// uid-2 should be unaffected.
	got := store.Load("uid-2/decode-0")
	assert.Equal(t, []string{"node-C"}, got.UnsortedList())
}

func TestNodeBindingStore_EvictByUID_NonExistent(t *testing.T) {
	store := NewNodeBindingStore()
	// Should not panic.
	store.EvictByUID("nonexistent")
}

func TestNodeBindingStore_EvictByUID_AddAfterEvict(t *testing.T) {
	store := NewNodeBindingStore()

	store.Add("uid-1/prefill-0", "node-A")
	store.EvictByUID("uid-1")
	assert.Nil(t, store.Load("uid-1/prefill-0"))

	// Re-adding after eviction should work.
	store.Add("uid-1/prefill-0", "node-B")
	got := store.Load("uid-1/prefill-0")
	assert.Equal(t, []string{"node-B"}, got.UnsortedList())
}

func TestNodeBindingStore_Len(t *testing.T) {
	store := NewNodeBindingStore()
	assert.Equal(t, 0, store.Len())

	store.Add("uid-1/prefill-0", "node-A")
	store.Add("uid-1/prefill-1", "node-B")
	assert.Equal(t, 1, store.Len(), "two sub-keys under same UID = 1 RBG entry")

	store.Add("uid-2/decode-0", "node-C")
	assert.Equal(t, 2, store.Len(), "two distinct UIDs = 2 RBG entries")

	store.EvictByUID("uid-1")
	assert.Equal(t, 1, store.Len(), "uid-1 evicted, only uid-2 remains")

	store.EvictByUID("uid-2")
	assert.Equal(t, 0, store.Len(), "all UIDs evicted")
}

// ---------------------------------------------------------------------------
// splitKey
// ---------------------------------------------------------------------------

func TestSplitKey_Valid(t *testing.T) {
	uid, subKey, ok := splitKey("uid-123/prefill-0")
	assert.True(t, ok)
	assert.Equal(t, "uid-123", uid)
	assert.Equal(t, "prefill-0", subKey)
}

func TestSplitKey_NoSlash(t *testing.T) {
	_, _, ok := splitKey("noslash")
	assert.False(t, ok)
}

func TestSplitKey_EmptyParts(t *testing.T) {
	_, _, ok := splitKey("/subkey")
	assert.False(t, ok)

	_, _, ok = splitKey("uid/")
	assert.False(t, ok)
}

// ---------------------------------------------------------------------------
// buildKey
// ---------------------------------------------------------------------------

func TestBuildKey_Pod(t *testing.T) {
	instance := &workloadsv1alpha2.RoleInstance{
		ObjectMeta: metav1.ObjectMeta{
			Labels: map[string]string{
				constants.RBGOwnerUIDLabelKey: "uid-123",
			},
		},
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "my-rbg-prefill-0"},
	}

	key := buildKey(constants.InplaceSchedulingGranularityPod, instance, pod)
	assert.Equal(t, "uid-123/my-rbg-prefill-0", key)
}

func TestBuildKey_Component(t *testing.T) {
	instance := &workloadsv1alpha2.RoleInstance{
		ObjectMeta: metav1.ObjectMeta{
			Labels: map[string]string{
				constants.RBGOwnerUIDLabelKey: "uid-123",
				constants.RoleNameLabelKey:    "prefill",
			},
		},
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Labels: map[string]string{
				constants.ComponentNameLabelKey: "Prefill",
			},
		},
	}

	key := buildKey(constants.InplaceSchedulingGranularityComponent, instance, pod)
	assert.Equal(t, "uid-123/prefill-Prefill", key)
}

func TestBuildKey_MissingRBGUID(t *testing.T) {
	instance := &workloadsv1alpha2.RoleInstance{
		ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{}},
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "pod-0"},
	}

	key := buildKey(constants.InplaceSchedulingGranularityPod, instance, pod)
	assert.Equal(t, "", key)
}

func TestBuildKey_ComponentMissingRoleName(t *testing.T) {
	instance := &workloadsv1alpha2.RoleInstance{
		ObjectMeta: metav1.ObjectMeta{
			Labels: map[string]string{
				constants.RBGOwnerUIDLabelKey: "uid-123",
				// RoleNameLabelKey missing
			},
		},
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Labels: map[string]string{
				constants.ComponentNameLabelKey: "Prefill",
			},
		},
	}

	key := buildKey(constants.InplaceSchedulingGranularityComponent, instance, pod)
	assert.Equal(t, "", key)
}

func TestBuildKey_ComponentMissingComponentName(t *testing.T) {
	instance := &workloadsv1alpha2.RoleInstance{
		ObjectMeta: metav1.ObjectMeta{
			Labels: map[string]string{
				constants.RBGOwnerUIDLabelKey: "uid-123",
				constants.RoleNameLabelKey:    "prefill",
			},
		},
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Labels: map[string]string{
				// ComponentNameLabelKey missing
			},
		},
	}

	key := buildKey(constants.InplaceSchedulingGranularityComponent, instance, pod)
	assert.Equal(t, "", key)
}

func TestBuildKey_UnknownGranularity(t *testing.T) {
	instance := &workloadsv1alpha2.RoleInstance{
		ObjectMeta: metav1.ObjectMeta{
			Labels: map[string]string{
				constants.RBGOwnerUIDLabelKey: "uid-123",
			},
		},
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "pod-0"},
	}

	key := buildKey("InvalidGranularity", instance, pod)
	assert.Equal(t, "", key)
}

// ---------------------------------------------------------------------------
// resolveGranularity
// ---------------------------------------------------------------------------

func TestResolveGranularity_ExplicitAnnotation(t *testing.T) {
	instance := &workloadsv1alpha2.RoleInstance{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{
				constants.RoleInplaceSchedulingGranularityAnnotationKey: constants.InplaceSchedulingGranularityComponent,
			},
			Labels: map[string]string{
				// Has index label (Stateful), but explicit annotation overrides
				constants.RoleInstanceIndexLabelKey: "0",
			},
		},
	}

	g := resolveGranularity(instance)
	assert.Equal(t, constants.InplaceSchedulingGranularityComponent, g)
}

func TestResolveGranularity_StatefulDefaultsToPod(t *testing.T) {
	instance := &workloadsv1alpha2.RoleInstance{
		ObjectMeta: metav1.ObjectMeta{
			Labels: map[string]string{
				constants.RoleInstanceIndexLabelKey: "0",
			},
		},
	}

	g := resolveGranularity(instance)
	assert.Equal(t, constants.InplaceSchedulingGranularityPod, g)
}

func TestResolveGranularity_StatelessDefaultsToComponent(t *testing.T) {
	instance := &workloadsv1alpha2.RoleInstance{
		ObjectMeta: metav1.ObjectMeta{
			Labels: map[string]string{
				// No RoleInstanceIndexLabelKey → Stateless
			},
		},
	}

	g := resolveGranularity(instance)
	assert.Equal(t, constants.InplaceSchedulingGranularityComponent, g)
}

// ---------------------------------------------------------------------------
// isPodRunningAndReady
// ---------------------------------------------------------------------------

func TestIsPodRunningAndReady(t *testing.T) {
	tests := []struct {
		name     string
		pod      *corev1.Pod
		expected bool
	}{
		{
			name: "Running and Ready",
			pod: &corev1.Pod{
				Status: corev1.PodStatus{
					Phase: corev1.PodRunning,
					Conditions: []corev1.PodCondition{
						{Type: corev1.PodReady, Status: corev1.ConditionTrue},
					},
				},
			},
			expected: true,
		},
		{
			name: "Running but Not Ready",
			pod: &corev1.Pod{
				Status: corev1.PodStatus{
					Phase: corev1.PodRunning,
					Conditions: []corev1.PodCondition{
						{Type: corev1.PodReady, Status: corev1.ConditionFalse},
					},
				},
			},
			expected: false,
		},
		{
			name: "Pending phase",
			pod: &corev1.Pod{
				Status: corev1.PodStatus{Phase: corev1.PodPending},
			},
			expected: false,
		},
		{
			name: "Failed phase",
			pod: &corev1.Pod{
				Status: corev1.PodStatus{Phase: corev1.PodFailed},
			},
			expected: false,
		},
		{
			name: "Running but no Ready condition",
			pod: &corev1.Pod{
				Status: corev1.PodStatus{
					Phase:      corev1.PodRunning,
					Conditions: []corev1.PodCondition{},
				},
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, isPodRunningAndReady(tt.pod))
		})
	}
}

// ---------------------------------------------------------------------------
// RecordNodeBindings
// ---------------------------------------------------------------------------

func makeRunningReadyPod(name, nodeName, componentName string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
			Labels: map[string]string{
				constants.ComponentNameLabelKey: componentName,
			},
		},
		Spec: corev1.PodSpec{NodeName: nodeName},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			Conditions: []corev1.PodCondition{
				{Type: corev1.PodReady, Status: corev1.ConditionTrue},
			},
		},
	}
}

func statefulInstance(rbgUID, roleName string) *workloadsv1alpha2.RoleInstance {
	return &workloadsv1alpha2.RoleInstance{
		ObjectMeta: metav1.ObjectMeta{
			Labels: map[string]string{
				constants.RBGOwnerUIDLabelKey:       rbgUID,
				constants.RoleNameLabelKey:          roleName,
				constants.RoleInstanceIndexLabelKey: "0",
			},
		},
	}
}

func statelessInstance(rbgUID, roleName string) *workloadsv1alpha2.RoleInstance {
	return &workloadsv1alpha2.RoleInstance{
		ObjectMeta: metav1.ObjectMeta{
			Labels: map[string]string{
				constants.RBGOwnerUIDLabelKey: rbgUID,
				constants.RoleNameLabelKey:    roleName,
				// No RoleInstanceIndexLabelKey → Stateless
			},
		},
	}
}

func TestRecordNodeBindings_Stateful_PodGranularity(t *testing.T) {
	store := NewNodeBindingStore()
	instance := statefulInstance("uid-1", "prefill")

	pods := []*corev1.Pod{
		makeRunningReadyPod("prefill-0", "node-A", "Prefill"),
		makeRunningReadyPod("prefill-1", "node-B", "Prefill"),
	}

	RecordNodeBindings(store, instance, pods)

	// Pod-level keys
	got0 := store.Load("uid-1/prefill-0")
	assert.Equal(t, []string{"node-A"}, got0.UnsortedList())

	got1 := store.Load("uid-1/prefill-1")
	assert.Equal(t, []string{"node-B"}, got1.UnsortedList())
}

func TestRecordNodeBindings_Stateless_ComponentGranularity(t *testing.T) {
	store := NewNodeBindingStore()
	instance := statelessInstance("uid-1", "prefill")

	pods := []*corev1.Pod{
		makeRunningReadyPod("abc-Prefill-0", "node-A", "Prefill"),
		makeRunningReadyPod("abc-Prefill-1", "node-B", "Prefill"),
	}

	RecordNodeBindings(store, instance, pods)

	// Component-level key: both Pods accumulate under the same key
	got := store.Load("uid-1/prefill-Prefill")
	assert.Len(t, got, 2)
	assert.ElementsMatch(t, []string{"node-A", "node-B"}, got.UnsortedList())
}

func TestRecordNodeBindings_SkipsNonReadyPods(t *testing.T) {
	store := NewNodeBindingStore()
	instance := statefulInstance("uid-1", "prefill")

	pods := []*corev1.Pod{
		makeRunningReadyPod("prefill-0", "node-A", "Prefill"),
		// Running but NOT Ready
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:   "prefill-1",
				Labels: map[string]string{constants.ComponentNameLabelKey: "Prefill"},
			},
			Spec: corev1.PodSpec{NodeName: "node-B"},
			Status: corev1.PodStatus{
				Phase: corev1.PodRunning,
				Conditions: []corev1.PodCondition{
					{Type: corev1.PodReady, Status: corev1.ConditionFalse},
				},
			},
		},
		// No NodeName (unscheduled)
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:   "prefill-2",
				Labels: map[string]string{constants.ComponentNameLabelKey: "Prefill"},
			},
			Status: corev1.PodStatus{Phase: corev1.PodPending},
		},
	}

	RecordNodeBindings(store, instance, pods)

	// Only prefill-0 should be recorded
	got0 := store.Load("uid-1/prefill-0")
	assert.Equal(t, []string{"node-A"}, got0.UnsortedList())

	assert.Nil(t, store.Load("uid-1/prefill-1"))
	assert.Nil(t, store.Load("uid-1/prefill-2"))
}

func TestRecordNodeBindings_SkipsWhenNoRBGUID(t *testing.T) {
	store := NewNodeBindingStore()
	instance := &workloadsv1alpha2.RoleInstance{
		ObjectMeta: metav1.ObjectMeta{
			Labels: map[string]string{
				// RBGOwnerUIDLabelKey missing
				constants.RoleInstanceIndexLabelKey: "0",
			},
		},
	}

	pods := []*corev1.Pod{
		makeRunningReadyPod("prefill-0", "node-A", "Prefill"),
	}

	RecordNodeBindings(store, instance, pods)
	assert.Nil(t, store.Load("prefill-0"))
}

// ---------------------------------------------------------------------------
// InjectInPlaceScheduling
// ---------------------------------------------------------------------------

func TestInjectInPlaceScheduling_Preferred_PodGranularity(t *testing.T) {
	store := NewNodeBindingStore()
	store.Add("uid-1/prefill-0", "node-A")

	instance := statefulInstance("uid-1", "prefill")
	instance.Annotations = map[string]string{
		constants.RoleInplaceSchedulingAnnotationKey: constants.InplaceSchedulingPreferred,
	}

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "prefill-0",
			Labels: map[string]string{
				constants.ComponentNameLabelKey: "Prefill",
			},
		},
	}

	InjectInPlaceScheduling(pod, instance, store)

	assert.NotNil(t, pod.Spec.Affinity)
	assert.NotNil(t, pod.Spec.Affinity.NodeAffinity)
	terms := pod.Spec.Affinity.NodeAffinity.PreferredDuringSchedulingIgnoredDuringExecution
	assert.Len(t, terms, 1)
	assert.Equal(t, int32(100), terms[0].Weight)
	assert.Equal(t, hostnameLabelKey, terms[0].Preference.MatchExpressions[0].Key)
	assert.Equal(t, []string{"node-A"}, terms[0].Preference.MatchExpressions[0].Values)
	// Required should NOT be set
	assert.Nil(t, pod.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution)
}

func TestInjectInPlaceScheduling_Required_PodGranularity(t *testing.T) {
	store := NewNodeBindingStore()
	store.Add("uid-1/prefill-0", "node-A")

	instance := statefulInstance("uid-1", "prefill")
	instance.Annotations = map[string]string{
		constants.RoleInplaceSchedulingAnnotationKey: constants.InplaceSchedulingRequired,
	}

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "prefill-0",
			Labels: map[string]string{
				constants.ComponentNameLabelKey: "Prefill",
			},
		},
	}

	InjectInPlaceScheduling(pod, instance, store)

	assert.NotNil(t, pod.Spec.Affinity)
	// Preferred should NOT be set
	assert.Empty(t, pod.Spec.Affinity.NodeAffinity.PreferredDuringSchedulingIgnoredDuringExecution)
	// Required should be set
	req := pod.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution
	assert.NotNil(t, req)
	assert.Len(t, req.NodeSelectorTerms, 1)
	assert.Equal(t, hostnameLabelKey, req.NodeSelectorTerms[0].MatchExpressions[0].Key)
	assert.Equal(t, []string{"node-A"}, req.NodeSelectorTerms[0].MatchExpressions[0].Values)
}

func TestInjectInPlaceScheduling_Preferred_ComponentGranularity(t *testing.T) {
	store := NewNodeBindingStore()
	store.Add("uid-1/prefill-Prefill", "node-A")
	store.Add("uid-1/prefill-Prefill", "node-B")

	instance := statelessInstance("uid-1", "prefill")
	instance.Annotations = map[string]string{
		constants.RoleInplaceSchedulingAnnotationKey: constants.InplaceSchedulingPreferred,
	}

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "def-Prefill-0",
			Labels: map[string]string{
				constants.ComponentNameLabelKey: "Prefill",
			},
		},
	}

	InjectInPlaceScheduling(pod, instance, store)

	assert.NotNil(t, pod.Spec.Affinity)
	terms := pod.Spec.Affinity.NodeAffinity.PreferredDuringSchedulingIgnoredDuringExecution
	assert.Len(t, terms, 1)
	// Both nodes should be in the values (sorted)
	assert.Equal(t, []string{"node-A", "node-B"}, terms[0].Preference.MatchExpressions[0].Values)
}

func TestInjectInPlaceScheduling_NoAnnotation(t *testing.T) {
	store := NewNodeBindingStore()
	store.Add("uid-1/prefill-0", "node-A")

	instance := statefulInstance("uid-1", "prefill")
	// No RoleInplaceSchedulingAnnotationKey

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "prefill-0"},
	}

	InjectInPlaceScheduling(pod, instance, store)
	assert.Nil(t, pod.Spec.Affinity)
}

func TestInjectInPlaceScheduling_InvalidMode(t *testing.T) {
	store := NewNodeBindingStore()
	store.Add("uid-1/prefill-0", "node-A")

	instance := statefulInstance("uid-1", "prefill")
	instance.Annotations = map[string]string{
		constants.RoleInplaceSchedulingAnnotationKey: "InvalidMode",
	}

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "prefill-0"},
	}

	InjectInPlaceScheduling(pod, instance, store)
	assert.Nil(t, pod.Spec.Affinity)
}

func TestInjectInPlaceScheduling_ExclusiveTopologySkips(t *testing.T) {
	store := NewNodeBindingStore()
	store.Add("uid-1/prefill-0", "node-A")

	instance := statefulInstance("uid-1", "prefill")
	instance.Annotations = map[string]string{
		constants.RoleInplaceSchedulingAnnotationKey: constants.InplaceSchedulingPreferred,
	}

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "prefill-0",
			Annotations: map[string]string{
				constants.GroupExclusiveTopologyKey: "kubernetes.io/hostname",
			},
		},
	}

	InjectInPlaceScheduling(pod, instance, store)
	assert.Nil(t, pod.Spec.Affinity)
}

func TestInjectInPlaceScheduling_NoBinding(t *testing.T) {
	store := NewNodeBindingStore()
	// Empty store — no bindings

	instance := statefulInstance("uid-1", "prefill")
	instance.Annotations = map[string]string{
		constants.RoleInplaceSchedulingAnnotationKey: constants.InplaceSchedulingPreferred,
	}

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "prefill-0",
			Labels: map[string]string{
				constants.ComponentNameLabelKey: "Prefill",
			},
		},
	}

	InjectInPlaceScheduling(pod, instance, store)
	// No binding and no avoid → no affinity terms injected.
	// ensureNodeAffinity is called early, so Affinity may be non-nil,
	// but it must contain no scheduling terms.
	if pod.Spec.Affinity != nil && pod.Spec.Affinity.NodeAffinity != nil {
		na := pod.Spec.Affinity.NodeAffinity
		assert.Nil(t, na.RequiredDuringSchedulingIgnoredDuringExecution)
		assert.Empty(t, na.PreferredDuringSchedulingIgnoredDuringExecution)
	}
}

func TestInjectInPlaceScheduling_ComponentMissingLabel(t *testing.T) {
	store := NewNodeBindingStore()
	store.Add("uid-1/prefill-Prefill", "node-A")

	instance := statelessInstance("uid-1", "prefill")
	instance.Annotations = map[string]string{
		constants.RoleInplaceSchedulingAnnotationKey: constants.InplaceSchedulingPreferred,
	}

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "def-Prefill-0",
			Labels: map[string]string{
				// ComponentNameLabelKey missing → buildKey returns ""
			},
		},
	}

	InjectInPlaceScheduling(pod, instance, store)
	assert.Nil(t, pod.Spec.Affinity)
}

func TestInjectInPlaceScheduling_PreservesExistingAffinity(t *testing.T) {
	store := NewNodeBindingStore()
	store.Add("uid-1/prefill-0", "node-A")

	instance := statefulInstance("uid-1", "prefill")
	instance.Annotations = map[string]string{
		constants.RoleInplaceSchedulingAnnotationKey: constants.InplaceSchedulingPreferred,
	}

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "prefill-0",
			Labels: map[string]string{
				constants.ComponentNameLabelKey: "Prefill",
			},
		},
		Spec: corev1.PodSpec{
			Affinity: &corev1.Affinity{
				NodeAffinity: &corev1.NodeAffinity{
					PreferredDuringSchedulingIgnoredDuringExecution: []corev1.PreferredSchedulingTerm{
						{
							Weight: 50,
							Preference: corev1.NodeSelectorTerm{
								MatchExpressions: []corev1.NodeSelectorRequirement{
									{
										Key:      "gpu-type",
										Operator: corev1.NodeSelectorOpIn,
										Values:   []string{"A100"},
									},
								},
							},
						},
					},
				},
			},
		},
	}

	InjectInPlaceScheduling(pod, instance, store)

	// Original term should be preserved
	terms := pod.Spec.Affinity.NodeAffinity.PreferredDuringSchedulingIgnoredDuringExecution
	assert.Len(t, terms, 2)
	assert.Equal(t, int32(50), terms[0].Weight)
	assert.Equal(t, "gpu-type", terms[0].Preference.MatchExpressions[0].Key)
	// Injected term
	assert.Equal(t, int32(100), terms[1].Weight)
	assert.Equal(t, hostnameLabelKey, terms[1].Preference.MatchExpressions[0].Key)
}

// ---------------------------------------------------------------------------
// End-to-end: Record then Inject (integration-style)
// ---------------------------------------------------------------------------

func TestRecordAndInject_Stateful_PodGranularity(t *testing.T) {
	store := NewNodeBindingStore()
	instance := statefulInstance("uid-1", "prefill")
	instance.Annotations = map[string]string{
		constants.RoleInplaceSchedulingAnnotationKey: constants.InplaceSchedulingPreferred,
	}

	// Phase 1: Record bindings from running Pods
	oldPods := []*corev1.Pod{
		makeRunningReadyPod("prefill-0", "node-A", "Prefill"),
		makeRunningReadyPod("prefill-1", "node-B", "Prefill"),
	}
	RecordNodeBindings(store, instance, oldPods)

	// Phase 2: Inject into a new Pod with the same name
	newPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "prefill-0",
			Labels: map[string]string{
				constants.ComponentNameLabelKey: "Prefill",
			},
		},
	}
	InjectInPlaceScheduling(newPod, instance, store)

	// Should target node-A
	terms := newPod.Spec.Affinity.NodeAffinity.PreferredDuringSchedulingIgnoredDuringExecution
	assert.Len(t, terms, 1)
	assert.Equal(t, []string{"node-A"}, terms[0].Preference.MatchExpressions[0].Values)
}

func TestRecordAndInject_Stateless_ComponentGranularity(t *testing.T) {
	store := NewNodeBindingStore()
	instance := statelessInstance("uid-1", "prefill")
	instance.Annotations = map[string]string{
		constants.RoleInplaceSchedulingAnnotationKey: constants.InplaceSchedulingPreferred,
	}

	// Phase 1: Record bindings from old Pods (abc prefix)
	oldPods := []*corev1.Pod{
		makeRunningReadyPod("abc-Prefill-0", "node-A", "Prefill"),
		makeRunningReadyPod("abc-Prefill-1", "node-B", "Prefill"),
	}
	RecordNodeBindings(store, instance, oldPods)

	// Phase 2: Inject into a surge Pod (def prefix — different name!)
	newPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "def-Prefill-0",
			Labels: map[string]string{
				constants.ComponentNameLabelKey: "Prefill",
			},
		},
	}
	InjectInPlaceScheduling(newPod, instance, store)

	// Should target both node-A and node-B (component-level)
	terms := newPod.Spec.Affinity.NodeAffinity.PreferredDuringSchedulingIgnoredDuringExecution
	assert.Len(t, terms, 1)
	assert.Equal(t, []string{"node-A", "node-B"}, terms[0].Preference.MatchExpressions[0].Values)
}

// ---------------------------------------------------------------------------
// Avoid annotation (anti-affinity)
// ---------------------------------------------------------------------------

func TestInjectInPlaceScheduling_AvoidAnnotation(t *testing.T) {
	store := NewNodeBindingStore()
	store.Add("uid-1/prefill-0", "node-A")

	instance := statefulInstance("uid-1", "prefill")
	instance.Annotations = map[string]string{
		constants.RoleInplaceSchedulingAnnotationKey:      constants.InplaceSchedulingPreferred,
		constants.RoleInplaceSchedulingAvoidAnnotationKey: "workloads.x-k8s.io/heavy-tenant",
	}

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "prefill-0",
			Labels: map[string]string{
				constants.ComponentNameLabelKey: "Prefill",
			},
		},
	}

	InjectInPlaceScheduling(pod, instance, store)

	na := pod.Spec.Affinity.NodeAffinity

	// Avoid term: Required + DoesNotExist
	req := na.RequiredDuringSchedulingIgnoredDuringExecution
	assert.NotNil(t, req)
	assert.Len(t, req.NodeSelectorTerms, 1)
	assert.Equal(t, "workloads.x-k8s.io/heavy-tenant", req.NodeSelectorTerms[0].MatchExpressions[0].Key)
	assert.Equal(t, corev1.NodeSelectorOpDoesNotExist, req.NodeSelectorTerms[0].MatchExpressions[0].Operator)
	assert.Empty(t, req.NodeSelectorTerms[0].MatchExpressions[0].Values)

	// Inplace affinity term: Preferred weight 100
	prefTerms := na.PreferredDuringSchedulingIgnoredDuringExecution
	assert.Len(t, prefTerms, 1)
	assert.Equal(t, int32(100), prefTerms[0].Weight)
	assert.Equal(t, hostnameLabelKey, prefTerms[0].Preference.MatchExpressions[0].Key)
}

func TestInjectInPlaceScheduling_AvoidWithoutBinding(t *testing.T) {
	store := NewNodeBindingStore()
	// Empty store — no bindings

	instance := statefulInstance("uid-1", "prefill")
	instance.Annotations = map[string]string{
		constants.RoleInplaceSchedulingAnnotationKey:      constants.InplaceSchedulingPreferred,
		constants.RoleInplaceSchedulingAvoidAnnotationKey: "workloads.x-k8s.io/heavy-tenant",
	}

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "prefill-0",
			Labels: map[string]string{
				constants.ComponentNameLabelKey: "Prefill",
			},
		},
	}

	InjectInPlaceScheduling(pod, instance, store)

	// Avoid term should still be injected even without binding
	assert.NotNil(t, pod.Spec.Affinity)
	req := pod.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution
	assert.NotNil(t, req)
	assert.Len(t, req.NodeSelectorTerms, 1)
	assert.Equal(t, "workloads.x-k8s.io/heavy-tenant", req.NodeSelectorTerms[0].MatchExpressions[0].Key)
	assert.Equal(t, corev1.NodeSelectorOpDoesNotExist, req.NodeSelectorTerms[0].MatchExpressions[0].Operator)

	// No preferred term (binding was empty, so affinity injection was skipped)
	assert.Empty(t, pod.Spec.Affinity.NodeAffinity.PreferredDuringSchedulingIgnoredDuringExecution)
}

func TestInjectInPlaceScheduling_NoAvoidAnnotation(t *testing.T) {
	store := NewNodeBindingStore()
	store.Add("uid-1/prefill-0", "node-A")

	instance := statefulInstance("uid-1", "prefill")
	instance.Annotations = map[string]string{
		constants.RoleInplaceSchedulingAnnotationKey: constants.InplaceSchedulingPreferred,
		// No avoid annotation
	}

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "prefill-0",
			Labels: map[string]string{
				constants.ComponentNameLabelKey: "Prefill",
			},
		},
	}

	InjectInPlaceScheduling(pod, instance, store)

	// Only the inplace affinity term (Preferred), no avoid term (Required)
	terms := pod.Spec.Affinity.NodeAffinity.PreferredDuringSchedulingIgnoredDuringExecution
	assert.Len(t, terms, 1)
	assert.Equal(t, int32(100), terms[0].Weight)
	assert.Nil(t, pod.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution)
}

func TestInjectInPlaceScheduling_AvoidSkippedByExclusiveTopology(t *testing.T) {
	store := NewNodeBindingStore()

	instance := statefulInstance("uid-1", "prefill")
	instance.Annotations = map[string]string{
		constants.RoleInplaceSchedulingAnnotationKey:      constants.InplaceSchedulingPreferred,
		constants.RoleInplaceSchedulingAvoidAnnotationKey: "workloads.x-k8s.io/heavy-tenant",
	}

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "prefill-0",
			Annotations: map[string]string{
				constants.GroupExclusiveTopologyKey: "kubernetes.io/hostname",
			},
		},
	}

	InjectInPlaceScheduling(pod, instance, store)
	// Exclusive topology takes precedence — nothing injected (not even avoid)
	assert.Nil(t, pod.Spec.Affinity)
}

// TestInjectInPlaceScheduling_RequiredWithAvoid verifies that in Required mode,
// the avoid MatchExpression is merged into the SAME NodeSelectorTerm as the
// hostname constraint. This is critical: separate NodeSelectorTerms are ORed,
// but we need AND semantics (historical node AND no avoid label).
func TestInjectInPlaceScheduling_RequiredWithAvoid(t *testing.T) {
	store := NewNodeBindingStore()
	store.Add("uid-1/prefill-0", "node-A")

	instance := statefulInstance("uid-1", "prefill")
	instance.Annotations = map[string]string{
		constants.RoleInplaceSchedulingAnnotationKey:      constants.InplaceSchedulingRequired,
		constants.RoleInplaceSchedulingAvoidAnnotationKey: "workloads.x-k8s.io/heavy-tenant",
	}

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "prefill-0",
			Labels: map[string]string{
				constants.ComponentNameLabelKey: "Prefill",
			},
		},
	}

	InjectInPlaceScheduling(pod, instance, store)

	na := pod.Spec.Affinity.NodeAffinity

	// No preferred terms — Required mode does not inject preferred affinity.
	assert.Empty(t, na.PreferredDuringSchedulingIgnoredDuringExecution)

	// Exactly ONE NodeSelectorTerm containing TWO MatchExpressions (AND semantics).
	req := na.RequiredDuringSchedulingIgnoredDuringExecution
	assert.NotNil(t, req)
	assert.Len(t, req.NodeSelectorTerms, 1,
		"avoid and hostname must be in the SAME term (AND), not separate terms (OR)")

	exprs := req.NodeSelectorTerms[0].MatchExpressions
	assert.Len(t, exprs, 2)

	// First expression: hostname In [node-A]
	assert.Equal(t, hostnameLabelKey, exprs[0].Key)
	assert.Equal(t, corev1.NodeSelectorOpIn, exprs[0].Operator)
	assert.Equal(t, []string{"node-A"}, exprs[0].Values)

	// Second expression: avoid label DoesNotExist
	assert.Equal(t, "workloads.x-k8s.io/heavy-tenant", exprs[1].Key)
	assert.Equal(t, corev1.NodeSelectorOpDoesNotExist, exprs[1].Operator)
}

func TestInjectInPlaceScheduling_AvoidSkippedWhenNoMode(t *testing.T) {
	store := NewNodeBindingStore()

	instance := statefulInstance("uid-1", "prefill")
	instance.Annotations = map[string]string{
		// No RoleInplaceSchedulingAnnotationKey
		constants.RoleInplaceSchedulingAvoidAnnotationKey: "workloads.x-k8s.io/heavy-tenant",
	}

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "prefill-0"},
	}

	InjectInPlaceScheduling(pod, instance, store)
	// No mode annotation → entire injection skipped (including avoid)
	assert.Nil(t, pod.Spec.Affinity)
}

func TestInjectInPlaceScheduling_AvoidFoldWithPreExistingRequired_NoBinding(t *testing.T) {
	store := NewNodeBindingStore()
	// Empty store — no bindings

	instance := statefulInstance("uid-1", "prefill")
	instance.Annotations = map[string]string{
		constants.RoleInplaceSchedulingAnnotationKey:      constants.InplaceSchedulingPreferred,
		constants.RoleInplaceSchedulingAvoidAnnotationKey: "workloads.x-k8s.io/heavy-tenant",
	}

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "prefill-0",
			Labels: map[string]string{
				constants.ComponentNameLabelKey: "Prefill",
			},
		},
		Spec: corev1.PodSpec{
			Affinity: &corev1.Affinity{
				NodeAffinity: &corev1.NodeAffinity{
					RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{
						NodeSelectorTerms: []corev1.NodeSelectorTerm{
							{
								MatchExpressions: []corev1.NodeSelectorRequirement{
									{
										Key:      "topology.kubernetes.io/zone",
										Operator: corev1.NodeSelectorOpIn,
										Values:   []string{"us-east-1"},
									},
								},
							},
						},
					},
				},
			},
		},
	}

	InjectInPlaceScheduling(pod, instance, store)

	na := pod.Spec.Affinity.NodeAffinity
	req := na.RequiredDuringSchedulingIgnoredDuringExecution
	assert.NotNil(t, req)
	// Avoid is folded into the pre-existing required term (AND semantics).
	assert.Len(t, req.NodeSelectorTerms, 1,
		"avoid must be folded into existing term, not appended as a separate OR'd term")
	exprs := req.NodeSelectorTerms[0].MatchExpressions
	assert.Len(t, exprs, 2,
		"term should have zone (original) + avoid (folded)")
	assert.Equal(t, "topology.kubernetes.io/zone", exprs[0].Key)
	assert.Equal(t, "workloads.x-k8s.io/heavy-tenant", exprs[1].Key)
	assert.Equal(t, corev1.NodeSelectorOpDoesNotExist, exprs[1].Operator)
}

func TestInjectInPlaceScheduling_AvoidFoldWithPreExistingRequired_Preferred(t *testing.T) {
	store := NewNodeBindingStore()
	store.Add("uid-1/prefill-0", "node-A")

	instance := statefulInstance("uid-1", "prefill")
	instance.Annotations = map[string]string{
		constants.RoleInplaceSchedulingAnnotationKey:      constants.InplaceSchedulingPreferred,
		constants.RoleInplaceSchedulingAvoidAnnotationKey: "workloads.x-k8s.io/heavy-tenant",
	}

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "prefill-0",
			Labels: map[string]string{
				constants.ComponentNameLabelKey: "Prefill",
			},
		},
		Spec: corev1.PodSpec{
			Affinity: &corev1.Affinity{
				NodeAffinity: &corev1.NodeAffinity{
					RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{
						NodeSelectorTerms: []corev1.NodeSelectorTerm{
							{
								MatchExpressions: []corev1.NodeSelectorRequirement{
									{
										Key:      "topology.kubernetes.io/zone",
										Operator: corev1.NodeSelectorOpIn,
										Values:   []string{"us-east-1"},
									},
								},
							},
						},
					},
				},
			},
		},
	}

	InjectInPlaceScheduling(pod, instance, store)

	na := pod.Spec.Affinity.NodeAffinity

	// Preferred term is injected
	assert.Len(t, na.PreferredDuringSchedulingIgnoredDuringExecution, 1)
	assert.Equal(t, int32(100), na.PreferredDuringSchedulingIgnoredDuringExecution[0].Weight)

	// Avoid is folded into the pre-existing required term (AND semantics).
	req := na.RequiredDuringSchedulingIgnoredDuringExecution
	assert.NotNil(t, req)
	assert.Len(t, req.NodeSelectorTerms, 1,
		"avoid must be folded into existing term, not appended as a separate OR'd term")
	exprs := req.NodeSelectorTerms[0].MatchExpressions
	assert.Len(t, exprs, 2,
		"term should have zone (original) + avoid (folded)")
	assert.Equal(t, "topology.kubernetes.io/zone", exprs[0].Key)
	assert.Equal(t, "workloads.x-k8s.io/heavy-tenant", exprs[1].Key)
	assert.Equal(t, corev1.NodeSelectorOpDoesNotExist, exprs[1].Operator)
}

func TestFoldAvoidIntoRequired_NoExistingTerms(t *testing.T) {
	na := &corev1.NodeAffinity{}
	avoidExprs := []corev1.NodeSelectorRequirement{
		{
			Key:      "workloads.x-k8s.io/heavy-tenant",
			Operator: corev1.NodeSelectorOpDoesNotExist,
		},
	}

	foldIntoRequired(na, avoidExprs)

	// When no required terms exist, a standalone avoid term is created.
	req := na.RequiredDuringSchedulingIgnoredDuringExecution
	assert.NotNil(t, req)
	assert.Len(t, req.NodeSelectorTerms, 1)
	assert.Len(t, req.NodeSelectorTerms[0].MatchExpressions, 1)
	assert.Equal(t, "workloads.x-k8s.io/heavy-tenant", req.NodeSelectorTerms[0].MatchExpressions[0].Key)
}

func TestFoldAvoidIntoRequired_WithExistingTerms(t *testing.T) {
	na := &corev1.NodeAffinity{
		RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{
			NodeSelectorTerms: []corev1.NodeSelectorTerm{
				{
					MatchExpressions: []corev1.NodeSelectorRequirement{
						{Key: "zone", Operator: corev1.NodeSelectorOpIn, Values: []string{"us-east-1"}},
					},
				},
				{
					MatchExpressions: []corev1.NodeSelectorRequirement{
						{Key: "zone", Operator: corev1.NodeSelectorOpIn, Values: []string{"us-west-2"}},
					},
				},
			},
		},
	}
	avoidExprs := []corev1.NodeSelectorRequirement{
		{
			Key:      "workloads.x-k8s.io/heavy-tenant",
			Operator: corev1.NodeSelectorOpDoesNotExist,
		},
	}

	foldIntoRequired(na, avoidExprs)

	// Avoid is folded into EACH existing term (AND semantics).
	req := na.RequiredDuringSchedulingIgnoredDuringExecution
	assert.Len(t, req.NodeSelectorTerms, 2)
	for i, term := range req.NodeSelectorTerms {
		assert.Len(t, term.MatchExpressions, 2, "term %d should have zone + avoid", i)
		assert.Equal(t, "zone", term.MatchExpressions[0].Key)
		assert.Equal(t, "workloads.x-k8s.io/heavy-tenant", term.MatchExpressions[1].Key)
		assert.Equal(t, corev1.NodeSelectorOpDoesNotExist, term.MatchExpressions[1].Operator)
	}
}

func TestInjectInPlaceScheduling_MultiKeyAvoid_Preferred(t *testing.T) {
	store := NewNodeBindingStore()
	store.Add("uid-1/prefill-0", "node-A")

	instance := statefulInstance("uid-1", "prefill")
	instance.Annotations = map[string]string{
		constants.RoleInplaceSchedulingAnnotationKey:      constants.InplaceSchedulingPreferred,
		constants.RoleInplaceSchedulingAvoidAnnotationKey: "key1, key2,key3",
	}

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "prefill-0",
			Labels: map[string]string{
				constants.ComponentNameLabelKey: "Prefill",
			},
		},
	}

	InjectInPlaceScheduling(pod, instance, store)

	na := pod.Spec.Affinity.NodeAffinity

	// Preferred term: hostname affinity
	assert.Len(t, na.PreferredDuringSchedulingIgnoredDuringExecution, 1)

	// Required terms: all 3 avoid keys folded in
	req := na.RequiredDuringSchedulingIgnoredDuringExecution
	assert.NotNil(t, req)
	assert.Len(t, req.NodeSelectorTerms, 1)
	exprs := req.NodeSelectorTerms[0].MatchExpressions
	assert.Len(t, exprs, 3)
	for i, want := range []string{"key1", "key2", "key3"} {
		assert.Equal(t, want, exprs[i].Key)
		assert.Equal(t, corev1.NodeSelectorOpDoesNotExist, exprs[i].Operator)
	}
}

func TestInjectInPlaceScheduling_MultiKeyAvoid_Required(t *testing.T) {
	store := NewNodeBindingStore()
	store.Add("uid-1/prefill-0", "node-A")

	instance := statefulInstance("uid-1", "prefill")
	instance.Annotations = map[string]string{
		constants.RoleInplaceSchedulingAnnotationKey:      constants.InplaceSchedulingRequired,
		constants.RoleInplaceSchedulingAvoidAnnotationKey: "key1,key2",
	}

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "prefill-0",
			Labels: map[string]string{
				constants.ComponentNameLabelKey: "Prefill",
			},
		},
	}

	InjectInPlaceScheduling(pod, instance, store)

	na := pod.Spec.Affinity.NodeAffinity
	req := na.RequiredDuringSchedulingIgnoredDuringExecution
	assert.NotNil(t, req)
	assert.Len(t, req.NodeSelectorTerms, 1)
	exprs := req.NodeSelectorTerms[0].MatchExpressions
	// hostname + 2 avoid keys = 3 expressions in a single term (AND)
	assert.Len(t, exprs, 3)
	assert.Equal(t, hostnameLabelKey, exprs[0].Key)
	assert.Equal(t, "key1", exprs[1].Key)
	assert.Equal(t, "key2", exprs[2].Key)
}

func TestFoldAvoidIntoRequired_MultiKey(t *testing.T) {
	na := &corev1.NodeAffinity{
		RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{
			NodeSelectorTerms: []corev1.NodeSelectorTerm{
				{
					MatchExpressions: []corev1.NodeSelectorRequirement{
						{Key: "zone", Operator: corev1.NodeSelectorOpIn, Values: []string{"us-east-1"}},
					},
				},
			},
		},
	}
	avoidExprs := []corev1.NodeSelectorRequirement{
		{Key: "avoid-a", Operator: corev1.NodeSelectorOpDoesNotExist},
		{Key: "avoid-b", Operator: corev1.NodeSelectorOpDoesNotExist},
	}

	foldIntoRequired(na, avoidExprs)

	req := na.RequiredDuringSchedulingIgnoredDuringExecution
	assert.Len(t, req.NodeSelectorTerms, 1)
	exprs := req.NodeSelectorTerms[0].MatchExpressions
	// zone + avoid-a + avoid-b = 3 expressions
	assert.Len(t, exprs, 3)
	assert.Equal(t, "zone", exprs[0].Key)
	assert.Equal(t, "avoid-a", exprs[1].Key)
	assert.Equal(t, "avoid-b", exprs[2].Key)
}

func TestInjectInPlaceScheduling_RequiredAvoidFoldWithPreExistingTerms(t *testing.T) {
	store := NewNodeBindingStore()
	store.Add("uid-1/prefill-0", "node-A")

	instance := statefulInstance("uid-1", "prefill")
	instance.Annotations = map[string]string{
		constants.RoleInplaceSchedulingAnnotationKey:      constants.InplaceSchedulingRequired,
		constants.RoleInplaceSchedulingAvoidAnnotationKey: "avoid-key",
	}

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "prefill-0",
			Labels: map[string]string{
				constants.ComponentNameLabelKey: "Prefill",
			},
		},
		Spec: corev1.PodSpec{
			Affinity: &corev1.Affinity{
				NodeAffinity: &corev1.NodeAffinity{
					RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{
						NodeSelectorTerms: []corev1.NodeSelectorTerm{
							{
								MatchExpressions: []corev1.NodeSelectorRequirement{
									{Key: "zone", Operator: corev1.NodeSelectorOpIn, Values: []string{"us-east-1"}},
								},
							},
						},
					},
				},
			},
		},
	}

	InjectInPlaceScheduling(pod, instance, store)

	req := pod.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution
	assert.NotNil(t, req)
	// All expressions folded into the user's existing term (AND semantics).
	// No separate term is appended — that would create OR semantics.
	assert.Len(t, req.NodeSelectorTerms, 1,
		"hostname + avoid must be folded into existing term, not appended as a separate OR'd term")

	exprs := req.NodeSelectorTerms[0].MatchExpressions
	// zone (original) + hostname (in-place affinity) + avoid (anti-affinity) = 3
	assert.Len(t, exprs, 3)
	assert.Equal(t, "zone", exprs[0].Key)
	assert.Equal(t, hostnameLabelKey, exprs[1].Key)
	assert.Equal(t, corev1.NodeSelectorOpIn, exprs[1].Operator)
	assert.Equal(t, "avoid-key", exprs[2].Key)
	assert.Equal(t, corev1.NodeSelectorOpDoesNotExist, exprs[2].Operator)
}

func TestInjectInPlaceScheduling_InvalidAvoidKeySkipped(t *testing.T) {
	store := NewNodeBindingStore()
	store.Add("uid-1/prefill-0", "node-A")

	instance := statefulInstance("uid-1", "prefill")
	instance.Annotations = map[string]string{
		constants.RoleInplaceSchedulingAnnotationKey:      constants.InplaceSchedulingPreferred,
		constants.RoleInplaceSchedulingAvoidAnnotationKey: "valid-key, invalid key with spaces, also/bad!",
	}

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "prefill-0",
			Labels: map[string]string{
				constants.ComponentNameLabelKey: "Prefill",
			},
		},
	}

	InjectInPlaceScheduling(pod, instance, store)

	na := pod.Spec.Affinity.NodeAffinity
	req := na.RequiredDuringSchedulingIgnoredDuringExecution
	assert.NotNil(t, req)
	// Only "valid-key" passes validation; the other two are skipped.
	assert.Len(t, req.NodeSelectorTerms, 1)
	assert.Len(t, req.NodeSelectorTerms[0].MatchExpressions, 1)
	assert.Equal(t, "valid-key", req.NodeSelectorTerms[0].MatchExpressions[0].Key)
}
