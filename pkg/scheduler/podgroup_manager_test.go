package scheduler

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	workloadsv1alpha "sigs.k8s.io/rbgs/api/workloads/v1alpha1"
	"sigs.k8s.io/rbgs/test/wrappers"
	schedv1alpha1 "sigs.k8s.io/scheduler-plugins/apis/scheduling/v1alpha1"
	volcanoschedulingv1beta1 "volcano.sh/apis/pkg/apis/scheduling/v1beta1"
)

func TestPodGroupScheduler_Reconcile(t *testing.T) {
	// Define test scheme
	scheme := runtime.NewScheme()
	_ = workloadsv1alpha.AddToScheme(scheme)
	_ = schedv1alpha1.AddToScheme(scheme)
	_ = volcanoschedulingv1beta1.AddToScheme(scheme)

	podGroup := &schedv1alpha1.PodGroup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-rbg",
			Namespace: "default",
		},
		Spec: schedv1alpha1.PodGroupSpec{
			MinMember:              3,
			ScheduleTimeoutSeconds: ptr.To(int32(300)),
		},
	}

	tests := []struct {
		name        string
		client      client.Client
		rbg         *workloadsv1alpha.RoleBasedGroup
		expectPG    bool
		expectError bool
	}{
		{
			name:   "create pod group when gang scheduling enabled and pod group not exists",
			client: fake.NewClientBuilder().WithScheme(scheme).Build(),
			rbg: wrappers.BuildBasicRoleBasedGroup("test-rbg", "default").
				WithKubeGangScheduling(true).Obj(),
			expectPG:    true,
			expectError: false,
		},
		{
			name:   "rbg with nil PodGroupPolicy",
			client: fake.NewClientBuilder().WithScheme(scheme).Build(),
			rbg: &workloadsv1alpha.RoleBasedGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-rbg",
					Namespace: "default",
				},
				Spec: workloadsv1alpha.RoleBasedGroupSpec{
					PodGroupPolicy: nil,
					Roles: []workloadsv1alpha.RoleSpec{
						{
							Name:     "role1",
							Replicas: ptr.To[int32](5), // Updated replica count
						},
					},
				},
			},
			expectPG:    false,
			expectError: false,
		},
		{
			name:   "rbg with nil KubeScheduling",
			client: fake.NewClientBuilder().WithScheme(scheme).Build(),
			rbg: &workloadsv1alpha.RoleBasedGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-rbg",
					Namespace: "default",
				},
				Spec: workloadsv1alpha.RoleBasedGroupSpec{
					PodGroupPolicy: &workloadsv1alpha.PodGroupPolicy{
						PodGroupPolicySource: workloadsv1alpha.PodGroupPolicySource{
							KubeScheduling: nil,
						},
					},
					Roles: []workloadsv1alpha.RoleSpec{
						{
							Name:     "role1",
							Replicas: ptr.To[int32](5), // Updated replica count
						},
					},
				},
			},
			expectPG:    false,
			expectError: false,
		},
		{
			name:   "update pod group when gang scheduling enabled and pod group exists with different min member",
			client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(podGroup).Build(),
			rbg: &workloadsv1alpha.RoleBasedGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-rbg",
					Namespace: "default",
				},
				Spec: workloadsv1alpha.RoleBasedGroupSpec{
					PodGroupPolicy: &workloadsv1alpha.PodGroupPolicy{
						PodGroupPolicySource: workloadsv1alpha.PodGroupPolicySource{
							KubeScheduling: &workloadsv1alpha.KubeSchedulingPodGroupPolicySource{
								ScheduleTimeoutSeconds: ptr.To(int32(30)),
							},
						},
					},
					Roles: []workloadsv1alpha.RoleSpec{
						{
							Name:     "role1",
							Replicas: ptr.To[int32](5), // Updated replica count
						},
					},
				},
			},
			expectPG:    true,
			expectError: false,
		},
		{
			name:   "delete pod group when gang scheduling disabled and pod group exists",
			client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(podGroup).Build(),
			rbg: wrappers.BuildBasicRoleBasedGroup("test-rbg", "default").
				WithKubeGangScheduling(false).Obj(),
			expectPG:    false,
			expectError: false,
		},
		{
			name:   "do nothing when gang scheduling disabled and pod group not exists",
			client: fake.NewClientBuilder().WithScheme(scheme).Build(),
			rbg: wrappers.BuildBasicRoleBasedGroup("test-rbg", "default").
				WithKubeGangScheduling(false).Obj(),
			expectPG:    false,
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(
			tt.name, func(t *testing.T) {

				scheduler := NewPodGroupScheduler(tt.client)
				ctx := log.IntoContext(context.TODO(), zap.New().WithValues("env", "test"))
				err := scheduler.Reconcile(ctx, tt.rbg)

				// Verify
				if (err != nil) != tt.expectError {
					t.Errorf("PodGroupScheduler.Reconcile() error = %v, expectError %v", err, tt.expectError)
				}

				// Check if pod group exists or not
				pg := &schedv1alpha1.PodGroup{}
				err = scheduler.client.Get(
					context.Background(), types.NamespacedName{
						Name:      tt.rbg.Name,
						Namespace: tt.rbg.Namespace,
					}, pg,
				)

				if tt.expectPG {
					assert.NoError(t, err)
					assert.Equal(t, tt.rbg.Name, pg.Name)
					assert.Equal(t, tt.rbg.Namespace, pg.Namespace)
					assert.Equal(t, int32(tt.rbg.GetGroupSize()), pg.Spec.MinMember)

					// Check owner reference
					assert.Len(t, pg.OwnerReferences, 1)
					assert.Equal(t, tt.rbg.Name, pg.OwnerReferences[0].Name)
					assert.Equal(t, "RoleBasedGroup", pg.OwnerReferences[0].Kind)
				} else {
					assert.Error(t, err)
					assert.Contains(t, err.Error(), "not found")
				}
			},
		)
	}
}
