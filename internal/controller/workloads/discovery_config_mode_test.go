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
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	"sigs.k8s.io/rbgs/pkg/constants"
	"sigs.k8s.io/rbgs/pkg/discovery"
	wrappersv2 "sigs.k8s.io/rbgs/test/wrappers/v1alpha2"
	"sigs.k8s.io/yaml"
)

func TestReconcileDiscoveryConfigMap(t *testing.T) {
	t.Run("creates shared configmap for stateful role", func(t *testing.T) {
		scheme := runtime.NewScheme()
		_ = workloadsv1alpha2.AddToScheme(scheme)
		_ = appsv1.AddToScheme(scheme)
		_ = corev1.AddToScheme(scheme)

		rbg := wrappersv2.BuildBasicRoleBasedGroup("test-rbg", "default").Obj()
		rbg.Spec.Roles = append(rbg.Spec.Roles, workloadsv1alpha2.RoleSpec{
			Name:     "router",
			Replicas: ptr.To(int32(1)),
			Workload: workloadsv1alpha2.WorkloadSpec{
				APIVersion: "apps/v1",
				Kind:       "Deployment",
			},
		})
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

		var cfg discovery.ClusterConfig
		if err := yaml.Unmarshal([]byte(cm.Data["config.yaml"]), &cfg); err != nil {
			t.Fatalf("unmarshal shared configmap data error: %v", err)
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
					Workload: workloadsv1alpha2.WorkloadSpec{
						APIVersion: "apps/v1",
						Kind:       "Deployment",
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
