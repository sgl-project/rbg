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

func TestReconcileDiscoveryConfigMap(t *testing.T) {
	// Stateful roles mount the RBG-level ConfigMap in every mode, so the stored
	// discovery-config-mode annotation must not suppress it (#595). The annotation is
	// written as the wire value that older controllers stored.
	for _, tc := range []struct {
		name        string
		annotations map[string]string
		reconciled  bool
	}{
		{name: "legacy annotation", annotations: map[string]string{"rbg.workloads.x-k8s.io/discovery-config-mode": "legacy"}},
		{name: "refine annotation", annotations: map[string]string{"rbg.workloads.x-k8s.io/discovery-config-mode": "refine"}},
		{name: "no annotation on a group that already has status", reconciled: true},
	} {
		t.Run("creates configmap for stateful role with "+tc.name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			_ = workloadsv1alpha2.AddToScheme(scheme)
			_ = corev1.AddToScheme(scheme)

			rbg := wrappersv2.BuildBasicRoleBasedGroup("test-rbg", "default").WithAnnotations(tc.annotations).Obj()
			if tc.reconciled {
				rbg.Status.ObservedGeneration = 1
				rbg.Status.RoleStatuses = []workloadsv1alpha2.RoleStatus{{Name: "test-role"}}
			}

			client := fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(rbg).Build()
			reconciler := &RoleBasedGroupReconciler{client: client, scheme: scheme}

			if err := reconciler.reconcileDiscoveryConfigMap(context.Background(), rbg); err != nil {
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
			var cfg discovery.ClusterConfig
			if err := yaml.Unmarshal([]byte(cm.Data["config.yaml"]), &cfg); err != nil {
				t.Fatalf("unmarshal shared configmap data error: %v", err)
			}
			if _, ok := cfg.Roles["test-role"]; !ok {
				t.Fatalf("stateful role test-role should exist in config, got %v", cfg.Roles)
			}
		})
	}

	t.Run("creates shared configmap for stateful role", func(t *testing.T) {
		scheme := runtime.NewScheme()
		_ = workloadsv1alpha2.AddToScheme(scheme)
		_ = appsv1.AddToScheme(scheme)
		_ = corev1.AddToScheme(scheme)

		rbg := wrappersv2.BuildBasicRoleBasedGroup("test-rbg", "default").Obj()
		rbg.Spec.Roles = append(rbg.Spec.Roles, workloadsv1alpha2.RoleSpec{
			Name:     "router",
			Replicas: ptr.To(int32(1)),
			Annotations: map[string]string{
				constants.RoleWorkloadTypeAnnotationKey: "apps/v1/Deployment",
			},
		})

		client := fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(rbg).Build()
		reconciler := &RoleBasedGroupReconciler{client: client, scheme: scheme}

		if err := reconciler.reconcileDiscoveryConfigMap(context.Background(), rbg); err != nil {
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

		client := fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(rbg).Build()
		reconciler := &RoleBasedGroupReconciler{client: client, scheme: scheme}

		if err := reconciler.reconcileDiscoveryConfigMap(context.Background(), rbg); err != nil {
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

		client := fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(rbg).Build()
		reconciler := &RoleBasedGroupReconciler{client: client, scheme: scheme}

		if err := reconciler.reconcileDiscoveryConfigMap(context.Background(), rbg); err != nil {
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

		if err := reconciler.reconcileDiscoveryConfigMap(context.Background(), rbg); err != nil {
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
