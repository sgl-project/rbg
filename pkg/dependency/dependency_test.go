package dependency

import (
	"context"
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/klog/v2"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	workloadsv1alpha "sigs.k8s.io/rbgs/api/workloads/v1alpha1"
	"sigs.k8s.io/rbgs/pkg/reconciler"
)

// TestDependencyOrder tests the DependencyOrder function with various dependency scenarios
func TestDependencyOrder(t *testing.T) {
	tests := []struct {
		name         string
		dependencies map[string][]string
		want         [][]string
		wantErr      bool
	}{
		{
			name: "simple chain",
			dependencies: map[string][]string{
				"a": {"b"},
				"b": {"c"},
				"c": {},
			},
			want: [][]string{
				{"c"},
				{"b"},
				{"a"},
			},
			wantErr: false,
		},
		{
			name: "no dependencies",
			dependencies: map[string][]string{
				"a": {},
				"b": {},
				"c": {},
			},
			want: [][]string{
				{"a", "b", "c"},
			},
			wantErr: false,
		},
		{
			name: "cycle detection",
			dependencies: map[string][]string{
				"a": {"b"},
				"b": {"c"},
				"c": {"a"},
			},
			want:    nil,
			wantErr: true,
		},
		{
			name: "cycle detection",
			dependencies: map[string][]string{
				"a": {"b"},
				"b": {},
				"c": {"d"},
				"d": {"c"},
			},
			want:    nil,
			wantErr: true,
		},
		{
			name: "same dependencies",
			dependencies: map[string][]string{
				"a": {"c"},
				"b": {"c"},
				"c": {},
			},
			want: [][]string{
				{"c"},
				{"a", "b"},
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(
			tt.name, func(t *testing.T) {
				ctx := log.IntoContext(context.TODO(), klog.NewKlogr())
				got, err := dependencyOrder(ctx, tt.dependencies)
				if tt.wantErr && err == nil {
					t.Errorf("DependencyOrder() expected error for cycle")
				}
				if !tt.wantErr && !reflect.DeepEqual(got, tt.want) {
					t.Errorf("DependencyOrder() = %v, want %v", got, tt.want)
				}
			},
		)
	}
}

func TestDefaultDependencyManager_SortRoles_EmptyRoles(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = workloadsv1alpha.AddToScheme(scheme)

	client := fake.NewClientBuilder().Build()
	manager := NewDefaultDependencyManager(scheme, client)

	tests := []struct {
		name     string
		rbg      *workloadsv1alpha.RoleBasedGroup
		wantErr  bool
		expected [][]*workloadsv1alpha.RoleSpec
	}{
		{
			name:    "empty roles",
			rbg:     &workloadsv1alpha.RoleBasedGroup{},
			wantErr: false,
		},
		{
			name: "no dependency",
			rbg: &workloadsv1alpha.RoleBasedGroup{
				Spec: workloadsv1alpha.RoleBasedGroupSpec{
					Roles: []workloadsv1alpha.RoleSpec{
						{
							Name: "role1",
						},
						{
							Name: "role2",
						},
					},
				},
			},
			wantErr: false,
		},
		{
			name: "valid dependency",
			rbg: &workloadsv1alpha.RoleBasedGroup{
				Spec: workloadsv1alpha.RoleBasedGroupSpec{
					Roles: []workloadsv1alpha.RoleSpec{
						{
							Name:         "role1",
							Dependencies: []string{"role2"},
						},
						{
							Name: "role2",
						},
					},
				},
			},
			wantErr: false,
			expected: [][]*workloadsv1alpha.RoleSpec{
				{
					{Name: "role2"},
				},
				{
					{Name: "role1", Dependencies: []string{"role2"}},
				},
			},
		},
		{
			name: "invalid dependency",
			rbg: &workloadsv1alpha.RoleBasedGroup{
				Spec: workloadsv1alpha.RoleBasedGroupSpec{
					Roles: []workloadsv1alpha.RoleSpec{
						{
							Name:         "role1",
							Dependencies: []string{"role2"},
						},
						{
							Name:         "role2",
							Dependencies: []string{"role1"},
						},
					},
				},
			},
			wantErr: true,
		},
		{
			name: "same dependency",
			rbg: &workloadsv1alpha.RoleBasedGroup{
				Spec: workloadsv1alpha.RoleBasedGroupSpec{
					Roles: []workloadsv1alpha.RoleSpec{
						{
							Name:         "role1",
							Dependencies: []string{"role3"},
						},
						{
							Name:         "role2",
							Dependencies: []string{"role3"},
						},
						{
							Name: "role3",
						},
					},
				},
			},
			wantErr: false,
			expected: [][]*workloadsv1alpha.RoleSpec{
				{
					{Name: "role3"},
				},
				{
					{Name: "role1", Dependencies: []string{"role3"}},
					{Name: "role2", Dependencies: []string{"role3"}},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(
			tt.name, func(t *testing.T) {
				result, err := manager.SortRoles(context.TODO(), tt.rbg)
				assert.Equal(t, tt.wantErr, err != nil)

				if tt.expected != nil {
					assert.Equal(t, tt.expected, result)
				}
			},
		)
	}
}

func TestDefaultDependencyManager_CheckDependencyReady(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = workloadsv1alpha.AddToScheme(scheme)

	tests := []struct {
		name        string
		sts         *appsv1.StatefulSet
		wantErr     bool
		expectReady bool
	}{
		{
			name: "dependency ready",
			sts: &appsv1.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-rbg-role2",
					Namespace: "default",
				},
				Spec: appsv1.StatefulSetSpec{
					Replicas: ptr.To(int32(2)),
				},
				Status: appsv1.StatefulSetStatus{
					ReadyReplicas: 2,
					Replicas:      2,
				},
			},
			wantErr:     false,
			expectReady: true,
		},
		{
			name: "dependency not ready",
			sts: &appsv1.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-rbg-role2",
					Namespace: "default",
				},
				Spec: appsv1.StatefulSetSpec{
					Replicas: ptr.To(int32(2)),
				},
				Status: appsv1.StatefulSetStatus{
					ReadyReplicas: 1,
					Replicas:      2,
				},
			},
			wantErr:     false,
			expectReady: false,
		},
	}

	rbg := &workloadsv1alpha.RoleBasedGroup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-rbg",
			Namespace: "default",
		},
		Spec: workloadsv1alpha.RoleBasedGroupSpec{
			Roles: []workloadsv1alpha.RoleSpec{
				{
					Name:         "role1",
					Dependencies: []string{"role2"},
					Workload: workloadsv1alpha.WorkloadSpec{
						APIVersion: "apps/v1",
						Kind:       "StatefulSet",
					},
				},
				{
					Name: "role2",
					Workload: workloadsv1alpha.WorkloadSpec{
						APIVersion: "apps/v1",
						Kind:       "StatefulSet",
					},
					Replicas: ptr.To(int32(2)),
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(
			tt.name, func(t *testing.T) {
				client := fake.NewClientBuilder().WithObjects(tt.sts).Build()
				dependencyManager := NewDefaultDependencyManager(scheme, client)

				ctx := log.IntoContext(context.TODO(), zap.New().WithValues("env", "test"))
				data := reconciler.NewRBGReconcileData(rbg, func(eventType, reason, message string) {})
				ready, err := dependencyManager.CheckDependencyReady(ctx, data, &rbg.Spec.Roles[0])
				assert.Equal(t, err != nil, tt.wantErr)
				assert.Equal(t, ready, tt.expectReady)
			},
		)
	}
}
