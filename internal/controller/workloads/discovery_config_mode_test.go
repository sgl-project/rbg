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

package workloads

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/rbgs/api/workloads/constants"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	"sigs.k8s.io/rbgs/pkg/discovery"
	wrappersv2 "sigs.k8s.io/rbgs/test/wrappers/v1alpha2"
	"sigs.k8s.io/yaml"
)

func roleBasedGroupSetControllerRef() metav1.OwnerReference {
	return metav1.OwnerReference{
		APIVersion: workloadsv1alpha2.GroupVersion.String(),
		Kind:       "RoleBasedGroupSet",
		Name:       "test-rbgset",
		UID:        "rbgset-test-uid",
		Controller: ptr.To(true),
	}
}

// A set adopts an orphan between the RoleBasedGroup controller's read and its
// write. The mode derived from the stale read must not land, and the retry must
// leave the mode the adoption stamped: refine, or the legacy a template declares.
// Without the stamp the retry would pin a child of a legacy template to refine and
// apply its discovery ConfigMap before the metadata sync restored legacy.
func TestEnsureDiscoveryConfigMode_AdoptionBetweenReadAndWrite(t *testing.T) {
	tests := []struct {
		name         string
		templateMode constants.DiscoveryConfigMode
		wantMode     constants.DiscoveryConfigMode
	}{
		{name: "template declares no mode", wantMode: constants.RefineDiscoveryConfigMode},
		{
			name:         "template declares legacy",
			templateMode: constants.LegacyDiscoveryConfigMode,
			wantMode:     constants.LegacyDiscoveryConfigMode,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			scheme := runtime.NewScheme()
			_ = workloadsv1alpha2.AddToScheme(scheme)
			_ = corev1.AddToScheme(scheme)

			orphan := wrappersv2.BuildBasicRoleBasedGroup("test-rbg", "default").Obj()
			orphan.Status.ObservedGeneration = 1
			c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(orphan).WithObjects(orphan).Build()
			reconciler := &RoleBasedGroupReconciler{client: c, scheme: scheme}
			key := types.NamespacedName{Name: orphan.Name, Namespace: orphan.Namespace}

			// The reconcile reads the orphan: no controller, no mode, and status.
			stale := &workloadsv1alpha2.RoleBasedGroup{}
			require.NoError(t, c.Get(ctx, key, stale))

			// The set controller adopts it before the reconcile writes.
			set := &workloadsv1alpha2.RoleBasedGroupSet{
				ObjectMeta: metav1.ObjectMeta{Name: "test-set", Namespace: orphan.Namespace, UID: "set-uid"},
			}
			if tt.templateMode != "" {
				set.Spec.GroupTemplate.Annotations = map[string]string{
					constants.DiscoveryConfigModeAnnotationKey: string(tt.templateMode),
				}
			}
			adopted := stale.DeepCopy()
			require.NoError(t, (&RoleBasedGroupSetReconciler{scheme: scheme}).claimGroupSetOrphan(set, adopted))
			require.NoError(t, c.Update(ctx, adopted))

			_, err := reconciler.ensureDiscoveryConfigMode(ctx, stale)
			require.Error(t, err, "the write derived from the stale read must not go through")
			assert.True(t, apierrors.IsConflict(err), "want a conflict, got %v", err)

			persisted := &workloadsv1alpha2.RoleBasedGroup{}
			require.NoError(t, c.Get(ctx, key, persisted))
			require.True(t, metav1.IsControlledBy(persisted, set))
			assert.Equal(t, tt.wantMode, persisted.GetDiscoveryConfigMode(),
				"the adoption must stamp the mode in the write that changes the owner")

			// The retry reads the adopted child and has nothing left to decide.
			_, err = reconciler.ensureDiscoveryConfigMode(ctx, persisted)
			require.NoError(t, err)
			require.NoError(t, c.Get(ctx, key, persisted))
			assert.Equal(t, tt.wantMode, persisted.GetDiscoveryConfigMode())
		})
	}
}

func TestEnsureDiscoveryConfigMode(t *testing.T) {
	type testCase struct {
		name               string
		mutateRBG          func(*workloadsv1alpha2.RoleBasedGroup)
		buildExtraObjects  func(*workloadsv1alpha2.RoleBasedGroup) []runtime.Object
		wantRequeue        bool
		wantMode           constants.DiscoveryConfigMode
		wantModeAnnotation bool
	}

	tests := []testCase{
		{
			name: "missing annotation with legacy role configmap should set legacy mode without requeue",
			buildExtraObjects: func(rbg *workloadsv1alpha2.RoleBasedGroup) []runtime.Object {
				return []runtime.Object{
					&corev1.ConfigMap{
						ObjectMeta: metav1.ObjectMeta{
							Name:      rbg.GetWorkloadName(&rbg.Spec.Roles[0]),
							Namespace: rbg.Namespace,
						},
					},
				}
			},
			wantRequeue:        false,
			wantMode:           constants.LegacyDiscoveryConfigMode,
			wantModeAnnotation: true,
		},
		{
			name:               "missing annotation without legacy signal should set refine mode without requeue",
			wantRequeue:        false,
			wantMode:           constants.RefineDiscoveryConfigMode,
			wantModeAnnotation: true,
		},
		{
			name: "existing legacy annotation should not requeue",
			mutateRBG: func(rbg *workloadsv1alpha2.RoleBasedGroup) {
				rbg.SetDiscoveryConfigMode(constants.LegacyDiscoveryConfigMode)
			},
			wantRequeue:        false,
			wantMode:           constants.LegacyDiscoveryConfigMode,
			wantModeAnnotation: true,
		},
		{
			name: "existing refine annotation should not requeue",
			mutateRBG: func(rbg *workloadsv1alpha2.RoleBasedGroup) {
				rbg.SetDiscoveryConfigMode(constants.RefineDiscoveryConfigMode)
			},
			wantRequeue:        false,
			wantMode:           constants.RefineDiscoveryConfigMode,
			wantModeAnnotation: true,
		},
		{
			name: "missing annotation with observed status should set legacy mode",
			mutateRBG: func(rbg *workloadsv1alpha2.RoleBasedGroup) {
				rbg.Status.ObservedGeneration = 1
			},
			wantRequeue:        false,
			wantMode:           constants.LegacyDiscoveryConfigMode,
			wantModeAnnotation: true,
		},
		{
			name: "RoleBasedGroupSet child with observed status and no annotation should set refine mode",
			mutateRBG: func(rbg *workloadsv1alpha2.RoleBasedGroup) {
				rbg.Status.ObservedGeneration = 1
				rbg.OwnerReferences = []metav1.OwnerReference{roleBasedGroupSetControllerRef()}
			},
			wantRequeue:        false,
			wantMode:           constants.RefineDiscoveryConfigMode,
			wantModeAnnotation: true,
		},
		{
			name: "RoleBasedGroupSet child with an explicit legacy mode should keep it",
			mutateRBG: func(rbg *workloadsv1alpha2.RoleBasedGroup) {
				rbg.Status.ObservedGeneration = 1
				rbg.OwnerReferences = []metav1.OwnerReference{roleBasedGroupSetControllerRef()}
				rbg.SetDiscoveryConfigMode(constants.LegacyDiscoveryConfigMode)
			},
			wantRequeue:        false,
			wantMode:           constants.LegacyDiscoveryConfigMode,
			wantModeAnnotation: true,
		},
		{
			name: "child of another kind with observed status should derive legacy mode",
			mutateRBG: func(rbg *workloadsv1alpha2.RoleBasedGroup) {
				rbg.Status.ObservedGeneration = 1
				ref := roleBasedGroupSetControllerRef()
				ref.Kind = "Other"
				rbg.OwnerReferences = []metav1.OwnerReference{ref}
			},
			wantRequeue:        false,
			wantMode:           constants.LegacyDiscoveryConfigMode,
			wantModeAnnotation: true,
		},
		{
			name: "child of a RoleBasedGroupSet in another API group with observed status should derive legacy mode",
			mutateRBG: func(rbg *workloadsv1alpha2.RoleBasedGroup) {
				rbg.Status.ObservedGeneration = 1
				ref := roleBasedGroupSetControllerRef()
				ref.APIVersion = "example.com/v1alpha2"
				rbg.OwnerReferences = []metav1.OwnerReference{ref}
			},
			wantRequeue:        false,
			wantMode:           constants.LegacyDiscoveryConfigMode,
			wantModeAnnotation: true,
		},
		{
			name: "RBG a RoleBasedGroupSet owns without controlling it should derive legacy mode",
			mutateRBG: func(rbg *workloadsv1alpha2.RoleBasedGroup) {
				rbg.Status.ObservedGeneration = 1
				ref := roleBasedGroupSetControllerRef()
				ref.Controller = ptr.To(false)
				rbg.OwnerReferences = []metav1.OwnerReference{ref}
			},
			wantRequeue:        false,
			wantMode:           constants.LegacyDiscoveryConfigMode,
			wantModeAnnotation: true,
		},
		{
			name: "RoleBasedGroupSet child of another served version with observed status should set refine mode",
			mutateRBG: func(rbg *workloadsv1alpha2.RoleBasedGroup) {
				rbg.Status.ObservedGeneration = 1
				ref := roleBasedGroupSetControllerRef()
				ref.APIVersion = workloadsv1alpha2.GroupVersion.Group + "/v1alpha1"
				rbg.OwnerReferences = []metav1.OwnerReference{ref}
			},
			wantRequeue:        false,
			wantMode:           constants.RefineDiscoveryConfigMode,
			wantModeAnnotation: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			_ = workloadsv1alpha2.AddToScheme(scheme)
			_ = appsv1.AddToScheme(scheme)
			_ = corev1.AddToScheme(scheme)

			rbg := wrappersv2.BuildBasicRoleBasedGroup("test-rbg", "default").Obj()
			if tt.mutateRBG != nil {
				tt.mutateRBG(rbg)
			}

			objs := []runtime.Object{rbg}
			if tt.buildExtraObjects != nil {
				objs = append(objs, tt.buildExtraObjects(rbg)...)
			}

			client := fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(objs...).Build()
			reconciler := &RoleBasedGroupReconciler{client: client, scheme: scheme}

			current := &workloadsv1alpha2.RoleBasedGroup{}
			key := types.NamespacedName{Name: rbg.Name, Namespace: rbg.Namespace}
			if err := client.Get(context.Background(), key, current); err != nil {
				t.Fatalf("get rbg error: %v", err)
			}

			requeue, err := reconciler.ensureDiscoveryConfigMode(context.Background(), current)
			if err != nil {
				t.Fatalf("ensureDiscoveryConfigMode() error = %v", err)
			}
			if requeue != tt.wantRequeue {
				t.Fatalf("requeue = %v, want %v", requeue, tt.wantRequeue)
			}

			persisted := &workloadsv1alpha2.RoleBasedGroup{}
			if err := client.Get(context.Background(), key, persisted); err != nil {
				t.Fatalf("get persisted rbg error: %v", err)
			}
			if got := persisted.GetDiscoveryConfigMode(); got != tt.wantMode {
				t.Fatalf("mode = %s, want %s", got, tt.wantMode)
			}
			_, hasModeAnnotation := persisted.Annotations[constants.DiscoveryConfigModeAnnotationKey]
			if hasModeAnnotation != tt.wantModeAnnotation {
				t.Fatalf("has discovery-config-mode annotation = %v, want %v", hasModeAnnotation, tt.wantModeAnnotation)
			}
		})
	}
}

func TestReconcileRefinedDiscoveryConfigMap(t *testing.T) {
	t.Run("creates shared configmap for stateful role", func(t *testing.T) {
		scheme := runtime.NewScheme()
		_ = workloadsv1alpha2.AddToScheme(scheme)
		_ = appsv1.AddToScheme(scheme)
		_ = corev1.AddToScheme(scheme)

		rbg := wrappersv2.BuildBasicRoleBasedGroup("test-rbg", "default").Obj()
		rbg.SetDiscoveryConfigMode(constants.RefineDiscoveryConfigMode)
		rbg.Spec.Roles = append(rbg.Spec.Roles, workloadsv1alpha2.RoleSpec{
			Name:     "router",
			Replicas: ptr.To(int32(1)),
			Annotations: map[string]string{
				constants.RoleWorkloadTypeAnnotationKey: "apps/v1/Deployment",
			},
		})

		client := fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(rbg).Build()
		reconciler := &RoleBasedGroupReconciler{client: client, scheme: scheme}

		if err := reconciler.reconcileRefinedDiscoveryConfigMap(context.Background(), rbg); err != nil {
			t.Fatalf("reconcileRefinedDiscoveryConfigMap() error = %v", err)
		}

		cm := &corev1.ConfigMap{}
		if err := client.Get(
			context.Background(),
			types.NamespacedName{Name: rbg.Name, Namespace: rbg.Namespace},
			cm,
		); err != nil {
			t.Fatalf("get shared configmap error: %v", err)
		}

		var cfg discovery.ClusterConfig
		if err := yaml.Unmarshal([]byte(cm.Data["config.yaml"]), &cfg); err != nil {
			t.Fatalf("unmarshal shared configmap data error: %v", err)
		}

		if len(cfg.Roles) != 1 {
			t.Fatalf("stateful roles in config = %d, want 1", len(cfg.Roles))
		}
		if _, ok := cfg.Roles["test-role"]; !ok {
			t.Fatalf("stateful role test-role should exist in refined config")
		}
		if _, ok := cfg.Roles["router"]; ok {
			t.Fatalf("stateless role router should not exist in refined config")
		}
	})

	t.Run("skips configmap creation for stateless-only roles", func(t *testing.T) {
		scheme := runtime.NewScheme()
		_ = workloadsv1alpha2.AddToScheme(scheme)
		_ = appsv1.AddToScheme(scheme)
		_ = corev1.AddToScheme(scheme)

		rbg := wrappersv2.BuildBasicRoleBasedGroup("test-rbg", "default").
			WithRoles([]workloadsv1alpha2.RoleSpec{
				{
					Name:     "router",
					Replicas: ptr.To(int32(1)),
					Annotations: map[string]string{
						constants.RoleWorkloadTypeAnnotationKey: "apps/v1/Deployment",
					},
				},
			}).Obj()
		rbg.SetDiscoveryConfigMode(constants.RefineDiscoveryConfigMode)

		client := fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(rbg).Build()
		reconciler := &RoleBasedGroupReconciler{client: client, scheme: scheme}

		if err := reconciler.reconcileRefinedDiscoveryConfigMap(context.Background(), rbg); err != nil {
			t.Fatalf("reconcileDiscoveryConfigMap() error = %v", err)
		}

		cm := &corev1.ConfigMap{}
		err := client.Get(
			context.Background(),
			types.NamespacedName{Name: rbg.Name, Namespace: rbg.Namespace},
			cm,
		)
		if !apierrors.IsNotFound(err) {
			t.Fatalf("configmap should not exist for stateless-only rbg, err = %v", err)
		}
	})

	t.Run("creates configmap for stateful-only roles", func(t *testing.T) {
		scheme := runtime.NewScheme()
		_ = workloadsv1alpha2.AddToScheme(scheme)
		_ = appsv1.AddToScheme(scheme)
		_ = corev1.AddToScheme(scheme)

		rbg := wrappersv2.BuildBasicRoleBasedGroup("test-rbg", "default").Obj()
		// BuildBasicRole creates StatefulSet workload by default → stateful
		rbg.SetDiscoveryConfigMode(constants.RefineDiscoveryConfigMode)

		client := fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(rbg).Build()
		reconciler := &RoleBasedGroupReconciler{client: client, scheme: scheme}

		if err := reconciler.reconcileRefinedDiscoveryConfigMap(context.Background(), rbg); err != nil {
			t.Fatalf("reconcileDiscoveryConfigMap() error = %v", err)
		}

		cm := &corev1.ConfigMap{}
		if err := client.Get(
			context.Background(),
			types.NamespacedName{Name: rbg.Name, Namespace: rbg.Namespace},
			cm,
		); err != nil {
			t.Fatalf("get shared configmap error: %v", err)
		}

		if cm.Data["config.yaml"] == "" {
			t.Fatalf("configmap data should not be empty")
		}
	})

	t.Run("updates existing configmap", func(t *testing.T) {
		scheme := runtime.NewScheme()
		_ = workloadsv1alpha2.AddToScheme(scheme)
		_ = appsv1.AddToScheme(scheme)
		_ = corev1.AddToScheme(scheme)

		rbg := wrappersv2.BuildBasicRoleBasedGroup("test-rbg", "default").Obj()
		rbg.SetDiscoveryConfigMode(constants.RefineDiscoveryConfigMode)
		existingCM := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      rbg.Name,
				Namespace: rbg.Namespace,
			},
			Data: map[string]string{
				"config.yaml": "old-data",
			},
		}

		client := fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(rbg, existingCM).Build()
		reconciler := &RoleBasedGroupReconciler{client: client, scheme: scheme}

		if err := reconciler.reconcileRefinedDiscoveryConfigMap(context.Background(), rbg); err != nil {
			t.Fatalf("reconcileDiscoveryConfigMap() error = %v", err)
		}

		updatedCM := &corev1.ConfigMap{}
		if err := client.Get(
			context.Background(),
			types.NamespacedName{Name: rbg.Name, Namespace: rbg.Namespace},
			updatedCM,
		); err != nil {
			t.Fatalf("get updated configmap error: %v", err)
		}

		if updatedCM.Data["config.yaml"] == "old-data" {
			t.Fatalf("configmap data should have been updated")
		}
	})
}
