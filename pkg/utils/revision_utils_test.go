package utils

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	workloadsv1alpha1 "sigs.k8s.io/rbgs/api/workloads/v1alpha1"
)

func TestListRevisions(t *testing.T) {
	// Define test scheme
	scheme := runtime.NewScheme()
	_ = appsv1.AddToScheme(scheme)

	// Test parent object
	parent := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-sts",
			Namespace: "default",
			UID:       "parent-uid",
		},
	}

	// Test data
	ownedRevision := &appsv1.ControllerRevision{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "owned-revision",
			Namespace: "default",
			Labels: map[string]string{
				"app": "test",
			},
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: "apps/v1",
					Kind:       "StatefulSet",
					Name:       "test-sts",
					UID:        "parent-uid",
					Controller: ptr.To(true),
				},
			},
		},
		Revision: 1,
	}

	unownedRevision := &appsv1.ControllerRevision{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "unowned-revision",
			Namespace: "default",
			Labels: map[string]string{
				"app": "test",
			},
		},
		Revision: 2,
	}

	differentOwnerRevision := &appsv1.ControllerRevision{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "different-owner-revision",
			Namespace: "default",
			Labels: map[string]string{
				"app": "test",
			},
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: "apps/v1",
					Kind:       "StatefulSet",
					Name:       "other-sts",
					UID:        "other-uid",
					Controller: ptr.To(true),
				},
			},
		},
		Revision: 3,
	}

	wrongNamespaceRevision := &appsv1.ControllerRevision{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "wrong-namespace-revision",
			Namespace: "other",
			Labels: map[string]string{
				"app": "test",
			},
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: "apps/v1",
					Kind:       "StatefulSet",
					Name:       "test-sts",
					UID:        "parent-uid",
					Controller: ptr.To(true),
				},
			},
		},
		Revision: 4,
	}

	differentLabelRevision := &appsv1.ControllerRevision{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "different-label-revision",
			Namespace: "default",
			Labels: map[string]string{
				"app": "other",
			},
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: "apps/v1",
					Kind:       "StatefulSet",
					Name:       "test-sts",
					UID:        "parent-uid",
					Controller: ptr.To(true),
				},
			},
		},
		Revision: 5,
	}

	tests := []struct {
		name          string
		parent        metav1.Object
		selector      labels.Selector
		existingObjs  []runtime.Object
		expectedCount int
		expectErr     bool
	}{
		{
			name:     "list revisions with matching selector and ownership",
			parent:   parent,
			selector: labels.SelectorFromSet(map[string]string{"app": "test"}),
			existingObjs: []runtime.Object{
				ownedRevision,
				unownedRevision,
				differentOwnerRevision,
				wrongNamespaceRevision,
				differentLabelRevision,
			},
			expectedCount: 2, // ownedRevision and unownedRevision
			expectErr:     false,
		},
		{
			name:     "list revisions with no matching labels",
			parent:   parent,
			selector: labels.SelectorFromSet(map[string]string{"app": "nonexistent"}),
			existingObjs: []runtime.Object{
				ownedRevision,
				unownedRevision,
			},
			expectedCount: 0,
			expectErr:     false,
		},
		{
			name:         "list revisions with no existing revisions",
			parent:       parent,
			selector:     labels.SelectorFromSet(map[string]string{"app": "test"}),
			existingObjs: []runtime.Object{
				// No revisions
			},
			expectedCount: 0,
			expectErr:     false,
		},
		{
			name:     "list revisions with nil selector",
			parent:   parent,
			selector: nil,
			existingObjs: []runtime.Object{
				ownedRevision,
				unownedRevision,
			},
			expectedCount: 0, // Nil selector matches nothing
			expectErr:     false,
		},
	}

	for _, tt := range tests {
		t.Run(
			tt.name, func(t *testing.T) {
				// Setup
				client := fake.NewClientBuilder().
					WithScheme(scheme).
					WithRuntimeObjects(tt.existingObjs...).
					Build()

				// Execute
				result, err := ListRevisions(context.Background(), client, tt.parent, tt.selector)

				// Verify
				if tt.expectErr {
					assert.Error(t, err)
					assert.Nil(t, result)
				} else {
					assert.NoError(t, err)
					assert.Len(t, result, tt.expectedCount)

					// Verify that all returned revisions are either owned by parent or unowned
					for _, revision := range result {
						ref := metav1.GetControllerOf(revision)
						// Should be either unowned (nil ref) or owned by parent
						if ref != nil {
							assert.Equal(t, tt.parent.GetUID(), ref.UID)
						}

						// Should match the label selector if it's not nil
						if tt.selector != nil && !tt.selector.Empty() {
							matches := tt.selector.Matches(labels.Set(revision.Labels))
							assert.True(t, matches, "Revision %s should match selector", revision.Name)
						}
					}
				}
			},
		)
	}
}

func TestGetHighestRevision(t *testing.T) {
	// Test data
	revision1 := &appsv1.ControllerRevision{
		ObjectMeta: metav1.ObjectMeta{
			Name: "revision-1",
		},
		Revision: 1,
	}

	revision5 := &appsv1.ControllerRevision{
		ObjectMeta: metav1.ObjectMeta{
			Name: "revision-5",
		},
		Revision: 5,
	}

	revision10 := &appsv1.ControllerRevision{
		ObjectMeta: metav1.ObjectMeta{
			Name: "revision-10",
		},
		Revision: 10,
	}

	tests := []struct {
		name             string
		revisions        []*appsv1.ControllerRevision
		expectedRevision *appsv1.ControllerRevision
	}{
		{
			name:             "empty revisions list",
			revisions:        []*appsv1.ControllerRevision{},
			expectedRevision: nil,
		},
		{
			name:             "nil revisions list",
			revisions:        nil,
			expectedRevision: nil,
		},
		{
			name: "single revision",
			revisions: []*appsv1.ControllerRevision{
				revision5,
			},
			expectedRevision: revision5,
		},
		{
			name: "multiple revisions - find highest",
			revisions: []*appsv1.ControllerRevision{
				revision1,
				revision5,
				revision10,
			},
			expectedRevision: revision10,
		},
		{
			name: "multiple revisions - highest in middle",
			revisions: []*appsv1.ControllerRevision{
				revision1,
				revision10,
				revision5,
			},
			expectedRevision: revision10,
		},
		{
			name: "multiple revisions - duplicate highest",
			revisions: []*appsv1.ControllerRevision{
				revision1,
				revision10,
				revision5,
				revision10,
			},
			expectedRevision: revision10, // Returns one of the highest (the last one encountered)
		},
		{
			name: "all revisions have same value",
			revisions: []*appsv1.ControllerRevision{
				revision5,
				revision5,
				revision5,
			},
			expectedRevision: revision5,
		},
	}

	for _, tt := range tests {
		t.Run(
			tt.name, func(t *testing.T) {
				result := GetHighestRevision(tt.revisions)

				if tt.expectedRevision == nil {
					assert.Nil(t, result)
				} else {
					assert.NotNil(t, result)
					assert.Equal(t, tt.expectedRevision.Revision, result.Revision)
				}
			},
		)
	}
}

func TestListRevisionsAndFindHighestIntegration(t *testing.T) {
	// Integration test for ListRevisions and GetHighestRevision working together
	scheme := runtime.NewScheme()
	_ = appsv1.AddToScheme(scheme)

	parent := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-sts",
			Namespace: "default",
			UID:       "parent-uid",
		},
	}

	// Revisions with different revision numbers
	revision1 := &appsv1.ControllerRevision{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "revision-1",
			Namespace: "default",
			Labels: map[string]string{
				"app": "test",
			},
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: "apps/v1",
					Kind:       "StatefulSet",
					Name:       "test-sts",
					UID:        "parent-uid",
					Controller: ptr.To(true),
				},
			},
		},
		Revision: 1,
	}

	revision3 := &appsv1.ControllerRevision{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "revision-3",
			Namespace: "default",
			Labels: map[string]string{
				"app": "test",
			},
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: "apps/v1",
					Kind:       "StatefulSet",
					Name:       "test-sts",
					UID:        "parent-uid",
					Controller: ptr.To(true),
				},
			},
		},
		Revision: 3,
	}

	revision2 := &appsv1.ControllerRevision{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "revision-2",
			Namespace: "default",
			Labels: map[string]string{
				"app": "test",
			},
		}, // Unowned revision
		Revision: 2,
	}

	client := fake.NewClientBuilder().
		WithScheme(scheme).
		WithRuntimeObjects([]runtime.Object{revision1, revision2, revision3}...).
		Build()

	selector := labels.SelectorFromSet(map[string]string{"app": "test"})

	// List revisions
	revisions, err := ListRevisions(context.Background(), client, parent, selector)
	assert.NoError(t, err)
	assert.Len(t, revisions, 3) // All 3 match the criteria

	// Find highest revision
	highest := GetHighestRevision(revisions)
	assert.NotNil(t, highest)
	assert.Equal(t, int64(3), highest.Revision)
	assert.Equal(t, "revision-3", highest.Name)
}

func TestGetPatchAndRestore(t *testing.T) {
	v1 := &workloadsv1alpha1.RoleBasedGroup{
		ObjectMeta: metav1.ObjectMeta{
			Name: "role-lws",
		},
		Spec: workloadsv1alpha1.RoleBasedGroupSpec{
			Roles: []workloadsv1alpha1.RoleSpec{
				{
					Name:     "role-sts",
					Replicas: ptr.To(int32(1)),
					Workload: workloadsv1alpha1.WorkloadSpec{
						APIVersion: "apps/v1",
						Kind:       "StatefulSet",
					},
					Template: &v1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Labels: map[string]string{
								"app": "nginx",
							},
						},
						Spec: v1.PodSpec{
							Containers: []v1.Container{
								{
									Name:  "nginx",
									Image: "1.0.0",
								},
							},
						},
					},
				},
				{
					Name:     "role-lws",
					Replicas: ptr.To(int32(1)),
					Workload: workloadsv1alpha1.WorkloadSpec{
						APIVersion: "leaderworkerset.x-k8s.io/v1",
						Kind:       "LeaderWorkerSet",
					},
				},
			},
			PodGroupPolicy: &workloadsv1alpha1.PodGroupPolicy{
				PodGroupPolicySource: workloadsv1alpha1.PodGroupPolicySource{
					KubeScheduling: &workloadsv1alpha1.KubeSchedulingPodGroupPolicySource{
						ScheduleTimeoutSeconds: ptr.To(int32(300)),
					},
				},
			},
		},
	}
	patchV1, _ := getRBGPatch(v1)

	v2 := v1.DeepCopy()
	v2.Spec.Roles[0].Replicas = ptr.To(int32(2))
	v2.Spec.Roles[0].Template.Spec.Containers[0].Image = "nginx:1.19.0"

	patchV1ControllerRevision := &appsv1.ControllerRevision{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-rbg-v1",
		},
		Data: runtime.RawExtension{
			Raw: patchV1,
		},
	}
	restoreV1, _ := ApplyRevision(v2, patchV1ControllerRevision)
	assert.Equal(t, v2.Spec.Roles[0].Replicas, restoreV1.Spec.Roles[0].Replicas)
	assert.True(t, reflect.DeepEqual(v2.Spec.PodGroupPolicy, restoreV1.Spec.PodGroupPolicy))
	v1.Spec.Roles[0].Replicas = ptr.To(int32(2))
	assert.True(t, reflect.DeepEqual(v1.Spec.Roles, restoreV1.Spec.Roles))
}

func TestCleanExpiredRevision(t *testing.T) {
	ctx := context.Background()
	rbg := &workloadsv1alpha1.RoleBasedGroup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-rbg",
			Namespace: "default",
		},
	}

	// Test case 1: Number of revisions does not exceed the limit (<=5)
	t.Run("NoExceedRevisions", func(t *testing.T) {
		var revisions []*appsv1.ControllerRevision
		for i := 0; i < 5; i++ {
			revisions = append(revisions, &appsv1.ControllerRevision{
				ObjectMeta: metav1.ObjectMeta{
					Name:              fmt.Sprintf("rev-%d", i),
					Namespace:         "default",
					CreationTimestamp: metav1.Now(),
					Labels: map[string]string{
						workloadsv1alpha1.SetNameLabelKey: "test-rbg",
					},
				},
				Revision: int64(i),
			})
		}

		// Use a fake client that supports delete operations
		fakeClientWithObjects := fake.NewClientBuilder().
			WithRuntimeObjects(func(revs []*appsv1.ControllerRevision) []runtime.Object {
				objs := make([]runtime.Object, len(revs))
				for i, rev := range revs {
					objs[i] = rev
				}
				return objs
			}(revisions)...).
			Build()

		result, err := CleanExpiredRevision(ctx, fakeClientWithObjects, rbg)
		assert.NoError(t, err)
		assert.Len(t, result, 5, "Should retain all revisions")
	})

	// Test case 2: Number of revisions exceeds the limit, need to delete the oldest ones
	t.Run("ExceedRevisions", func(t *testing.T) {
		var revisions []*appsv1.ControllerRevision
		// Create 10 revisions, 5 of which should be deleted
		for i := 0; i < 10; i++ {
			revisions = append(revisions, &appsv1.ControllerRevision{
				ObjectMeta: metav1.ObjectMeta{
					Name:              fmt.Sprintf("rev-%d", i),
					Namespace:         "default",
					CreationTimestamp: metav1.Unix(int64(i), 0), // Created in chronological order
					Labels: map[string]string{
						workloadsv1alpha1.SetNameLabelKey: "test-rbg",
					},
				},
				Revision: int64(i),
			})
		}

		// Use a fake client that supports delete operations
		fakeClientWithObjects := fake.NewClientBuilder().
			WithRuntimeObjects(func(revs []*appsv1.ControllerRevision) []runtime.Object {
				objs := make([]runtime.Object, len(revs))
				for i, rev := range revs {
					objs[i] = rev
				}
				return objs
			}(revisions)...).
			Build()

		result, err := CleanExpiredRevision(ctx, fakeClientWithObjects, rbg)
		assert.NoError(t, err)
		assert.Len(t, result, 5, "Should only retain the latest 5 revisions")

		// Verify that the retained revisions are #5-#10
		for i, rev := range result {
			expectedIndex := i + 5
			assert.Equal(t, fmt.Sprintf("rev-%d", expectedIndex), rev.Name)
		}
	})
}

func TestNewRevision(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = workloadsv1alpha1.AddToScheme(scheme)
	_ = appsv1.AddToScheme(scheme)
	ctx := context.Background()
	client := fake.NewClientBuilder().WithScheme(scheme).Build()

	rbg := getRBG()

	revision1, err := NewRevision(ctx, client, rbg, nil)
	assert.NoError(t, err)
	assert.NotNil(t, revision1)

	for i := 0; i < 10; i++ {
		revision2, err := NewRevision(ctx, client, rbg, nil)
		assert.NoError(t, err)
		assert.NotNil(t, revision2)

		assert.Equal(t, revision1.Name, revision2.Name)
		assert.Equal(t, revision1.Labels, revision2.Labels)
		assert.Equal(t, revision1.Data.Raw, revision2.Data.Raw)
		assert.Equal(t, revision1.Revision, revision2.Revision)

		assert.Contains(t, revision1.Labels, workloadsv1alpha1.SetNameLabelKey)
		assert.Contains(t, revision1.Labels, workloadsv1alpha1.RevisionLabelKey)
		assert.Equal(t, rbg.Name, revision1.Labels[workloadsv1alpha1.SetNameLabelKey])
	}
}

func TestGetRolesRevisionHash(t *testing.T) {
	t.Run("EmptyRevision", func(t *testing.T) {
		revision := &appsv1.ControllerRevision{
			Data: runtime.RawExtension{Raw: []byte{}},
		}

		result, err := GetRolesRevisionHash(revision)
		assert.NoError(t, err)
		assert.Empty(t, result)
	})

	t.Run("ConsistentHashForSameRoleContent", func(t *testing.T) {
		scheme := runtime.NewScheme()
		_ = workloadsv1alpha1.AddToScheme(scheme)
		_ = appsv1.AddToScheme(scheme)
		ctx := context.Background()
		client := fake.NewClientBuilder().WithScheme(scheme).Build()

		rbg := getRBG()
		revision1, err := NewRevision(ctx, client, rbg, nil)
		assert.NoError(t, err)

		result1, err1 := GetRolesRevisionHash(revision1)
		assert.NoError(t, err1)
		assert.Contains(t, result1, "router")
		assert.Contains(t, result1, "decode")
		assert.Contains(t, result1, "prefill")
		assert.NotEmpty(t, result1["router"])
		assert.NotEmpty(t, result1["decode"])
		assert.NotEmpty(t, result1["prefill"])

		rbgDiff := getRBG()
		rbgDiff.Spec.Roles[0].Template.Labels["a"] = "b"
		revisionDiff, err := NewRevision(ctx, client, rbgDiff, nil)
		assert.NoError(t, err)
		resultDiff, err := GetRolesRevisionHash(revisionDiff)
		assert.NoError(t, err)
		assert.NotEqual(t, result1["router"], resultDiff["router"])
		assert.Equal(t, result1["decode"], resultDiff["decode"])
		assert.Equal(t, result1["prefill"], resultDiff["prefill"])

		for i := 0; i < 10; i++ {
			revision2, err2 := NewRevision(ctx, client, rbg, nil)
			assert.NoError(t, err2)
			result2, err2 := GetRolesRevisionHash(revision2)
			assert.NoError(t, err2)
			assert.Equal(t, result1, result2)
		}
	})
}

func TestRoleTemplateUpdatesAffectRevisionAndRoleHash(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = workloadsv1alpha1.AddToScheme(scheme)
	_ = appsv1.AddToScheme(scheme)
	ctx := context.Background()
	client := fake.NewClientBuilder().WithScheme(scheme).Build()

	rbg := getRBGWithRoleTemplates()

	revision1, err := NewRevision(ctx, client, rbg, nil)
	assert.NoError(t, err)
	assert.NotNil(t, revision1)

	var raw1 map[string]interface{}
	assert.NoError(t, json.Unmarshal(revision1.Data.Raw, &raw1))
	spec1, ok := raw1["spec"].(map[string]interface{})
	assert.True(t, ok, "revision spec should exist")
	roleTemplates1, ok := spec1["roleTemplates"].([]interface{})
	assert.True(t, ok, "roleTemplates should be persisted in revision payload")
	assert.GreaterOrEqual(t, len(roleTemplates1), 1)

	hashes1, err := GetRolesRevisionHash(revision1)
	assert.NoError(t, err)
	initialHash := hashes1["prefill"]
	assert.NotEmpty(t, initialHash)

	rbg.Spec.RoleTemplates[0].Template.Spec.Containers[0].Image = "nginx:2.0"

	revision2, err := NewRevision(ctx, client, rbg, revision1)
	assert.NoError(t, err)
	assert.NotNil(t, revision2)
	assert.NotEqual(t, revision1.Data.Raw, revision2.Data.Raw, "roleTemplate change should impact revision payload")

	hashes2, err := GetRolesRevisionHash(revision2)
	assert.NoError(t, err)
	assert.NotEqual(t, initialHash, hashes2["prefill"], "role hash should change when referenced template changes")
}

func getRBG() *workloadsv1alpha1.RoleBasedGroup {
	return &workloadsv1alpha1.RoleBasedGroup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-rbg",
			Namespace: "default",
			Labels: map[string]string{
				"app":     "test",
				"version": "v1.0.0",
			},
			Annotations: map[string]string{
				"description": "A test RoleBasedGroup with full fields",
				"owner":       "test-team",
			},
		},
		Spec: workloadsv1alpha1.RoleBasedGroupSpec{
			Roles: []workloadsv1alpha1.RoleSpec{
				{
					Name:     "router",
					Replicas: ptr.To(int32(3)),
					Workload: workloadsv1alpha1.WorkloadSpec{
						APIVersion: "apps/v1",
						Kind:       "Deployment",
					},
					Template: &v1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Labels: map[string]string{
								"app": "router",
							},
							Annotations: map[string]string{
								"prometheus.io/scrape": "true",
								"prometheus.io/port":   "8080",
							},
						},
						Spec: v1.PodSpec{
							Containers: []v1.Container{
								{
									Name:  "router",
									Image: "nginx:1.21",
									Ports: []v1.ContainerPort{
										{
											Name:          "http",
											ContainerPort: 80,
											Protocol:      v1.ProtocolTCP,
										},
										{
											Name:          "https",
											ContainerPort: 443,
											Protocol:      v1.ProtocolTCP,
										},
									},
									Env: []v1.EnvVar{
										{
											Name:  "ENV",
											Value: "production",
										},
									},
									Resources: v1.ResourceRequirements{
										Limits: v1.ResourceList{
											v1.ResourceCPU:    resource.MustParse("500m"),
											v1.ResourceMemory: resource.MustParse("128Mi"),
										},
										Requests: v1.ResourceList{
											v1.ResourceCPU:    resource.MustParse("250m"),
											v1.ResourceMemory: resource.MustParse("64Mi"),
										},
									},
								},
							},
							Affinity: &v1.Affinity{
								PodAntiAffinity: &v1.PodAntiAffinity{
									PreferredDuringSchedulingIgnoredDuringExecution: []v1.WeightedPodAffinityTerm{
										{
											Weight: 100,
											PodAffinityTerm: v1.PodAffinityTerm{
												LabelSelector: &metav1.LabelSelector{
													MatchLabels: map[string]string{
														"app": "router",
													},
												},
												TopologyKey: "kubernetes.io/hostname",
											},
										},
									},
								},
							},
							Tolerations: []v1.Toleration{
								{
									Key:      "dedicated",
									Operator: v1.TolerationOpEqual,
									Value:    "router",
									Effect:   v1.TaintEffectNoSchedule,
								},
							},
						},
					},
				},
				{
					Name:     "decode",
					Replicas: ptr.To(int32(5)),
					Workload: workloadsv1alpha1.WorkloadSpec{
						APIVersion: "apps/v1",
						Kind:       "StatefulSet",
					},
					Template: &v1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Labels: map[string]string{
								"app":  "decode",
								"tier": "backend",
							},
						},
						Spec: v1.PodSpec{
							Containers: []v1.Container{
								{
									Name:  "decode",
									Image: "busybox:1.35",
									Command: []string{
										"sh",
										"-c",
										"sleep 300",
									},
									Resources: v1.ResourceRequirements{
										Limits: v1.ResourceList{
											v1.ResourceCPU:    resource.MustParse("200m"),
											v1.ResourceMemory: resource.MustParse("64Mi"),
										},
										Requests: v1.ResourceList{
											v1.ResourceCPU:    resource.MustParse("100m"),
											v1.ResourceMemory: resource.MustParse("32Mi"),
										},
									},
								},
							},
							NodeSelector: map[string]string{
								"disktype": "ssd",
							},
						},
					},
				},
				{
					Name:     "prefill",
					Replicas: ptr.To(int32(3)),
					Workload: workloadsv1alpha1.WorkloadSpec{
						APIVersion: "apps/v1",
						Kind:       "Deployment",
					},
					Template: &v1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Labels: map[string]string{
								"app":  "prefill",
								"tier": "backend",
							},
							Annotations: map[string]string{
								"prometheus.io/scrape": "true",
								"prometheus.io/port":   "8080",
							},
						},
						Spec: v1.PodSpec{
							Containers: []v1.Container{
								{
									Name:  "prefill",
									Image: "nginx:1.21",
									Ports: []v1.ContainerPort{
										{
											Name:          "http",
											ContainerPort: 180,
											Protocol:      v1.ProtocolTCP,
										},
										{
											Name:          "https",
											ContainerPort: 1443,
											Protocol:      v1.ProtocolTCP,
										},
									},
									Env: []v1.EnvVar{
										{
											Name:  "ENV",
											Value: "production",
										},
									},
									Resources: v1.ResourceRequirements{
										Limits: v1.ResourceList{
											v1.ResourceCPU:    resource.MustParse("500m"),
											v1.ResourceMemory: resource.MustParse("128Mi"),
										},
										Requests: v1.ResourceList{
											v1.ResourceCPU:    resource.MustParse("250m"),
											v1.ResourceMemory: resource.MustParse("64Mi"),
										},
									},
								},
							},
							Affinity: &v1.Affinity{
								PodAntiAffinity: &v1.PodAntiAffinity{
									PreferredDuringSchedulingIgnoredDuringExecution: []v1.WeightedPodAffinityTerm{
										{
											Weight: 100,
											PodAffinityTerm: v1.PodAffinityTerm{
												LabelSelector: &metav1.LabelSelector{
													MatchLabels: map[string]string{
														"app": "prefill",
													},
												},
												TopologyKey: "kubernetes.io/hostname",
											},
										},
									},
								},
							},
							Tolerations: []v1.Toleration{
								{
									Key:      "dedicated",
									Operator: v1.TolerationOpEqual,
									Value:    "prefill",
									Effect:   v1.TaintEffectNoSchedule,
								},
							},
						},
					},
				},
			},
			PodGroupPolicy: &workloadsv1alpha1.PodGroupPolicy{
				PodGroupPolicySource: workloadsv1alpha1.PodGroupPolicySource{
					KubeScheduling: &workloadsv1alpha1.KubeSchedulingPodGroupPolicySource{
						ScheduleTimeoutSeconds: ptr.To(int32(600)),
					},
				},
			},
		},
	}
}

func getRBGWithRoleTemplates() *workloadsv1alpha1.RoleBasedGroup {
	return &workloadsv1alpha1.RoleBasedGroup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "template-rbg",
			Namespace: "default",
		},
		Spec: workloadsv1alpha1.RoleBasedGroupSpec{
			RoleTemplates: []workloadsv1alpha1.RoleTemplate{
				{
					Name: "shared",
					Template: v1.PodTemplateSpec{
						Spec: v1.PodSpec{
							Containers: []v1.Container{
								{
									Name:  "app",
									Image: "nginx:1.0",
								},
							},
						},
					},
				},
			},
			Roles: []workloadsv1alpha1.RoleSpec{
				{
					Name:        "prefill",
					Replicas:    ptr.To(int32(1)),
					TemplateRef: &workloadsv1alpha1.TemplateRef{Name: "shared"},
					TemplatePatch: runtime.RawExtension{
						Raw: []byte(`{"spec":{"containers":[{"name":"app","command":["sleep","3600"]}]}}`),
					},
					Workload: workloadsv1alpha1.WorkloadSpec{
						APIVersion: "apps/v1",
						Kind:       "StatefulSet",
					},
				},
			},
		},
	}
}
