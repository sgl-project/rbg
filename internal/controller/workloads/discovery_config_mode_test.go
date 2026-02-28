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
	workloadsv1alpha1 "sigs.k8s.io/rbgs/api/workloads/v1alpha1"
	"sigs.k8s.io/rbgs/pkg/discovery"
	"sigs.k8s.io/rbgs/test/wrappers"
	"sigs.k8s.io/yaml"
)

func TestEnsureDiscoveryConfigMode(t *testing.T) {
	t.Run("existing workload uses legacy mode", func(t *testing.T) {
		scheme := runtime.NewScheme()
		_ = workloadsv1alpha1.AddToScheme(scheme)
		_ = appsv1.AddToScheme(scheme)
		_ = corev1.AddToScheme(scheme)

		rbg := wrappers.BuildBasicRoleBasedGroup("test-rbg", "default").Obj()
		sts := &appsv1.StatefulSet{
			ObjectMeta: metav1.ObjectMeta{
				Name:      rbg.GetWorkloadName(&rbg.Spec.Roles[0]),
				Namespace: rbg.Namespace,
			},
		}
		legacyRoleCM := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      rbg.GetWorkloadName(&rbg.Spec.Roles[0]),
				Namespace: rbg.Namespace,
			},
		}
		client := fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(rbg, sts, legacyRoleCM).Build()
		reconciler := &RoleBasedGroupReconciler{client: client, scheme: scheme}

		current := &workloadsv1alpha1.RoleBasedGroup{}
		if err := client.Get(context.Background(), types.NamespacedName{Name: rbg.Name, Namespace: rbg.Namespace}, current); err != nil {
			t.Fatalf("get rbg error: %v", err)
		}

		if err := reconciler.ensureDiscoveryConfigMode(context.Background(), current); err != nil {
			t.Fatalf("ensureDiscoveryConfigMode() error = %v", err)
		}
		if got := current.GetDiscoveryConfigMode(); got != workloadsv1alpha1.LegacyDiscoveryConfigMode {
			t.Fatalf("mode = %s, want %s", got, workloadsv1alpha1.LegacyDiscoveryConfigMode)
		}
	})

	t.Run("new workload uses refine mode", func(t *testing.T) {
		scheme := runtime.NewScheme()
		_ = workloadsv1alpha1.AddToScheme(scheme)
		_ = appsv1.AddToScheme(scheme)
		_ = corev1.AddToScheme(scheme)

		rbg := wrappers.BuildBasicRoleBasedGroup("test-rbg", "default").Obj()
		client := fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(rbg).Build()
		reconciler := &RoleBasedGroupReconciler{client: client, scheme: scheme}

		current := &workloadsv1alpha1.RoleBasedGroup{}
		if err := client.Get(context.Background(), types.NamespacedName{Name: rbg.Name, Namespace: rbg.Namespace}, current); err != nil {
			t.Fatalf("get rbg error: %v", err)
		}

		if err := reconciler.ensureDiscoveryConfigMode(context.Background(), current); err != nil {
			t.Fatalf("ensureDiscoveryConfigMode() error = %v", err)
		}
		if got := current.GetDiscoveryConfigMode(); got != workloadsv1alpha1.RefineDiscoveryConfigMode {
			t.Fatalf("mode = %s, want %s", got, workloadsv1alpha1.RefineDiscoveryConfigMode)
		}
	})
}

func TestReconcileRefinedDiscoveryConfigMap(t *testing.T) {
	t.Run("refine mode creates shared configmap for stateful role", func(t *testing.T) {
		scheme := runtime.NewScheme()
		_ = workloadsv1alpha1.AddToScheme(scheme)
		_ = appsv1.AddToScheme(scheme)
		_ = corev1.AddToScheme(scheme)

		rbg := wrappers.BuildBasicRoleBasedGroup("test-rbg", "default").Obj()
		rbg.SetDiscoveryConfigMode(workloadsv1alpha1.RefineDiscoveryConfigMode)
		rbg.Spec.Roles = append(rbg.Spec.Roles, workloadsv1alpha1.RoleSpec{
			Name:     "router",
			Replicas: ptr.To(int32(1)),
			Workload: workloadsv1alpha1.WorkloadSpec{
				APIVersion: "apps/v1",
				Kind:       "Deployment",
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

	t.Run("legacy mode does not reconcile configmap", func(t *testing.T) {
		scheme := runtime.NewScheme()
		_ = workloadsv1alpha1.AddToScheme(scheme)
		_ = appsv1.AddToScheme(scheme)
		_ = corev1.AddToScheme(scheme)

		rbg := wrappers.BuildBasicRoleBasedGroup("test-rbg", "default").Obj()
		rbg.SetDiscoveryConfigMode(workloadsv1alpha1.LegacyDiscoveryConfigMode)

		client := fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(rbg).Build()
		reconciler := &RoleBasedGroupReconciler{client: client, scheme: scheme}

		if err := reconciler.reconcileRefinedDiscoveryConfigMap(context.Background(), rbg); err != nil {
			t.Fatalf("reconcileRefinedDiscoveryConfigMap() error = %v", err)
		}

		cm := &corev1.ConfigMap{}
		err := client.Get(
			context.Background(),
			types.NamespacedName{Name: rbg.Name, Namespace: rbg.Namespace},
			cm,
		)
		if !apierrors.IsNotFound(err) {
			t.Fatalf("configmap should not exist in legacy mode, err = %v", err)
		}
	})

	t.Run("refine mode with stateless-only roles skips configmap", func(t *testing.T) {
		scheme := runtime.NewScheme()
		_ = workloadsv1alpha1.AddToScheme(scheme)
		_ = appsv1.AddToScheme(scheme)
		_ = corev1.AddToScheme(scheme)

		rbg := wrappers.BuildBasicRoleBasedGroup("test-rbg", "default").
			WithRoles([]workloadsv1alpha1.RoleSpec{
				{
					Name:     "router",
					Replicas: ptr.To(int32(1)),
					Workload: workloadsv1alpha1.WorkloadSpec{
						APIVersion: "apps/v1",
						Kind:       "Deployment",
					},
				},
			}).Obj()
		rbg.SetDiscoveryConfigMode(workloadsv1alpha1.RefineDiscoveryConfigMode)

		client := fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(rbg).Build()
		reconciler := &RoleBasedGroupReconciler{client: client, scheme: scheme}

		if err := reconciler.reconcileRefinedDiscoveryConfigMap(context.Background(), rbg); err != nil {
			t.Fatalf("reconcileRefinedDiscoveryConfigMap() error = %v", err)
		}

		cm := &corev1.ConfigMap{}
		err := client.Get(
			context.Background(),
			types.NamespacedName{Name: rbg.Name, Namespace: rbg.Namespace},
			cm,
		)
		if !apierrors.IsNotFound(err) {
			t.Fatalf("configmap should not exist for stateless-only refine rbg, err = %v", err)
		}
	})
}
