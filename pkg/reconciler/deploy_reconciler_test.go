package reconciler

import (
	"context"
	"fmt"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	workloadsv1alpha1 "sigs.k8s.io/rbgs/api/workloads/v1alpha1"
)

var expectedRevisionHash = "revision-hash-value"

// TestDeploymentReconciler_Reconciler tests the Reconciler method of DeploymentReconciler
func TestDeploymentReconciler_Reconciler(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = appsv1.AddToScheme(scheme)
	_ = workloadsv1alpha1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)

	deployRole := &workloadsv1alpha1.RoleSpec{
		Name:     "test-role",
		Replicas: ptr.To(int32(3)),
		Workload: workloadsv1alpha1.WorkloadSpec{
			APIVersion: "apps/v1",
			Kind:       "Deployment",
		},
	}

	rbg := &workloadsv1alpha1.RoleBasedGroup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-rbg",
			Namespace: "default",
			UID:       "test-uid",
			Labels: map[string]string{
				workloadsv1alpha1.SetNameLabelKey: "test-rbg",
			},
		},
		Spec: workloadsv1alpha1.RoleBasedGroupSpec{
			Roles: []workloadsv1alpha1.RoleSpec{
				*deployRole,
			},
		},
	}

	tests := []struct {
		name         string
		client       client.Client
		rbg          *workloadsv1alpha1.RoleBasedGroup
		role         *workloadsv1alpha1.RoleSpec
		expectError  bool
		expectCreate bool
		expectUpdate bool
	}{
		{
			name:         "create new deployment",
			client:       fake.NewClientBuilder().WithScheme(scheme).Build(),
			rbg:          rbg,
			role:         deployRole,
			expectError:  false,
			expectCreate: true,
		},
		{
			name: "update existing deployment",
			client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(
				&appsv1.Deployment{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-rbg-test-role",
						Namespace: "default",
					},
					Spec: appsv1.DeploymentSpec{
						Replicas: ptr.To[int32](1),
						Selector: &metav1.LabelSelector{
							MatchLabels: map[string]string{
								"app": "test",
							},
						},
						Template: corev1.PodTemplateSpec{
							ObjectMeta: metav1.ObjectMeta{
								Labels: map[string]string{
									"app": "test",
								},
							},
							Spec: corev1.PodSpec{
								Containers: []corev1.Container{
									{
										Name:  "test-container",
										Image: "test-image:v1",
									},
								},
							},
						},
					},
				},
			).Build(),
			rbg:          rbg,
			role:         deployRole,
			expectError:  false,
			expectUpdate: true,
		},
	}

	for _, tt := range tests {
		t.Run(
			tt.name, func(t *testing.T) {
				r := &DeploymentReconciler{
					scheme: tt.client.Scheme(),
					client: tt.client,
				}

				ctx := context.Background()
				err := r.Reconciler(ctx, tt.rbg, tt.role, nil, expectedRevisionHash)

				if (err != nil) != tt.expectError {
					t.Errorf("DeploymentReconciler.Reconciler() error = %v, expectError %v", err, tt.expectError)
					return
				}

				if !tt.expectError {
					// Check if deployment was created/updated
					deploy := &appsv1.Deployment{}
					err := tt.client.Get(
						ctx, types.NamespacedName{
							Name:      tt.rbg.GetWorkloadName(tt.role),
							Namespace: tt.rbg.Namespace,
						}, deploy,
					)

					if err != nil && !apierrors.IsNotFound(err) {
						t.Errorf("Failed to get deployment: %v", err)
					}

					if tt.expectCreate && apierrors.IsNotFound(err) {
						t.Error("Expected deployment to be created, but it was not found")
					}

					if !tt.expectCreate && tt.expectUpdate {
						if err != nil {
							t.Errorf("Expected deployment to exist for update, but got error: %v", err)
						}
					}

					roleHashKey := fmt.Sprintf(workloadsv1alpha1.RoleRevisionLabelKeyFmt, tt.role.Name)
					if expectedRevisionHash != deploy.Labels[roleHashKey] {
						t.Errorf("Expected revision hash %s, got %s",
							expectedRevisionHash, deploy.Labels[roleHashKey])
					}
				}
			},
		)
	}
}

// TestDeploymentReconciler_CheckWorkloadReady tests the CheckWorkloadReady method of DeploymentReconciler
func TestDeploymentReconciler_CheckWorkloadReady(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = appsv1.AddToScheme(scheme)
	_ = workloadsv1alpha1.AddToScheme(scheme)

	replicas := int32(3)
	deployRole := &workloadsv1alpha1.RoleSpec{
		Name:     "test-role",
		Replicas: &replicas,
		Workload: workloadsv1alpha1.WorkloadSpec{
			APIVersion: "apps/v1",
			Kind:       "Deployment",
		},
	}
	rbg := &workloadsv1alpha1.RoleBasedGroup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-rbg",
			Namespace: "default",
			UID:       "test-uid",
			Labels: map[string]string{
				workloadsv1alpha1.SetNameLabelKey: "test-rbg",
			},
		},
		Spec: workloadsv1alpha1.RoleBasedGroupSpec{
			Roles: []workloadsv1alpha1.RoleSpec{
				*deployRole,
			},
		},
	}

	tests := []struct {
		name        string
		client      client.Client
		rbg         *workloadsv1alpha1.RoleBasedGroup
		role        *workloadsv1alpha1.RoleSpec
		expected    bool
		expectError bool
	}{
		{
			name: "deployment ready",
			client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(
				&appsv1.Deployment{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-rbg-test-role",
						Namespace: "default",
					},
					Spec: appsv1.DeploymentSpec{
						Replicas: &replicas,
					},
					Status: appsv1.DeploymentStatus{
						ReadyReplicas: replicas,
					},
				},
			).Build(),
			rbg:         rbg,
			role:        deployRole,
			expected:    true,
			expectError: false,
		},
		{
			name: "deployment not ready",
			client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(
				&appsv1.Deployment{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-rbg-test-role",
						Namespace: "default",
					},
					Spec: appsv1.DeploymentSpec{
						Replicas: &replicas,
					},
					Status: appsv1.DeploymentStatus{
						ReadyReplicas: 1,
						Replicas:      replicas,
					},
				},
			).Build(),
			rbg:         rbg,
			role:        deployRole,
			expected:    false,
			expectError: false,
		},
		{
			name:        "deployment not found",
			client:      fake.NewClientBuilder().WithScheme(scheme).Build(),
			rbg:         rbg,
			role:        deployRole,
			expected:    false,
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(
			tt.name, func(t *testing.T) {
				r := &DeploymentReconciler{
					scheme: tt.client.Scheme(),
					client: tt.client,
				}

				ctx := context.Background()
				got, err := r.CheckWorkloadReady(ctx, tt.rbg, tt.role)

				if (err != nil) != tt.expectError {
					t.Errorf(
						"DeploymentReconciler.CheckWorkloadReady() error = %v, expectError %v", err, tt.expectError,
					)
					return
				}

				if got != tt.expected {
					t.Errorf("DeploymentReconciler.CheckWorkloadReady() = %v, expected %v", got, tt.expected)
				}
			},
		)
	}
}

// TestDeploymentReconciler_CleanupOrphanedWorkloads tests the CleanupOrphanedWorkloads method
func TestDeploymentReconciler_CleanupOrphanedWorkloads(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = appsv1.AddToScheme(scheme)
	_ = workloadsv1alpha1.AddToScheme(scheme)

	replicas := int32(3)
	deployRole := &workloadsv1alpha1.RoleSpec{
		Name:     "test-role",
		Replicas: &replicas,
		Workload: workloadsv1alpha1.WorkloadSpec{
			APIVersion: "apps/v1",
			Kind:       "Deployment",
		},
	}
	rbg := &workloadsv1alpha1.RoleBasedGroup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-rbg",
			Namespace: "default",
			UID:       "test-uid",
			Labels: map[string]string{
				workloadsv1alpha1.SetNameLabelKey: "test-rbg",
			},
		},
		Spec: workloadsv1alpha1.RoleBasedGroupSpec{
			Roles: []workloadsv1alpha1.RoleSpec{
				*deployRole,
			},
		},
	}

	tests := []struct {
		name        string
		client      client.Client
		rbg         *workloadsv1alpha1.RoleBasedGroup
		expectError bool
	}{
		{
			name: "cleanup orphaned deployment",
			client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(
				&appsv1.Deployment{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "orphaned-deploy",
						Namespace: "default",
						Labels: map[string]string{
							workloadsv1alpha1.SetNameLabelKey: "test-rbg",
						},
						OwnerReferences: []metav1.OwnerReference{
							{
								APIVersion: workloadsv1alpha1.GroupVersion.String(),
								Kind:       "RoleBasedGroup",
								Name:       "test-rbg",
								Controller: ptr.To[bool](true),
								UID:        "test-uid",
							},
						},
					},
				},
				&appsv1.Deployment{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-rbg-test-role",
						Namespace: "default",
						Labels: map[string]string{
							workloadsv1alpha1.SetNameLabelKey: "test-rbg",
						},
						OwnerReferences: []metav1.OwnerReference{
							{
								APIVersion: workloadsv1alpha1.GroupVersion.String(),
								Kind:       "RoleBasedGroup",
								Name:       "test-rbg",
								UID:        "test-uid",
							},
						},
					},
				},
			).Build(),
			rbg:         rbg,
			expectError: false,
		},
		{
			name: "list error",
			client: fake.NewClientBuilder().WithScheme(scheme).WithInterceptorFuncs(
				interceptor.Funcs{
					List: func(
						ctx context.Context, client client.WithWatch, list client.ObjectList, opts ...client.ListOption,
					) error {
						return apierrors.NewInternalError(fmt.Errorf("fake internal error"))
					},
				},
			).Build(),
			rbg: &workloadsv1alpha1.RoleBasedGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-rbg",
					Namespace: "default",
				},
			},
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(
			tt.name, func(t *testing.T) {
				r := &DeploymentReconciler{
					scheme: tt.client.Scheme(),
					client: tt.client,
				}

				ctx := context.Background()
				err := r.CleanupOrphanedWorkloads(ctx, tt.rbg)

				if (err != nil) != tt.expectError {
					t.Errorf(
						"DeploymentReconciler.CleanupOrphanedWorkloads() error = %v, expectError %v", err,
						tt.expectError,
					)
				}
			},
		)
	}
}

// TestDeploymentReconciler_RecreateWorkload tests the RecreateWorkload method
func TestDeploymentReconciler_RecreateWorkload(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = appsv1.AddToScheme(scheme)
	_ = workloadsv1alpha1.AddToScheme(scheme)

	replicas := int32(1)
	deployRole := &workloadsv1alpha1.RoleSpec{
		Name:     "test-role",
		Replicas: &replicas,
		Workload: workloadsv1alpha1.WorkloadSpec{
			APIVersion: "apps/v1",
			Kind:       "Deployment",
		},
	}
	rbg := &workloadsv1alpha1.RoleBasedGroup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-rbg",
			Namespace: "default",
			UID:       "test-uid",
			Labels: map[string]string{
				workloadsv1alpha1.SetNameLabelKey: "test-rbg",
			},
		},
		Spec: workloadsv1alpha1.RoleBasedGroupSpec{
			Roles: []workloadsv1alpha1.RoleSpec{
				*deployRole,
			},
		},
	}
	deploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-rbg-test-role",
			Namespace: "default",
			UID:       "deploy-uid",
		},
	}

	tests := []struct {
		name          string
		client        client.Client
		rbg           *workloadsv1alpha1.RoleBasedGroup
		role          *workloadsv1alpha1.RoleSpec
		mockReconcile bool
		expectError   bool
	}{
		{
			name:          "recreate existing deployment",
			client:        fake.NewClientBuilder().WithScheme(scheme).WithObjects(deploy).Build(),
			rbg:           rbg,
			role:          deployRole,
			mockReconcile: true,
			expectError:   false,
		},
		{
			name:   "deployment does not exist",
			client: fake.NewClientBuilder().WithScheme(scheme).Build(),
			rbg: &workloadsv1alpha1.RoleBasedGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-rbg",
					Namespace: "default",
				},
			},
			role: &workloadsv1alpha1.RoleSpec{
				Name:     "test-role",
				Replicas: &replicas,
			},
			mockReconcile: false,
			expectError:   false, // Should not error when deployment doesn't exist
		},
		{
			name: "delete error",
			client: fake.NewClientBuilder().WithScheme(scheme).WithInterceptorFuncs(
				interceptor.Funcs{
					Delete: func(
						ctx context.Context, client client.WithWatch, obj client.Object, opts ...client.DeleteOption,
					) error {
						return apierrors.NewInternalError(fmt.Errorf("fake client delete error"))
					},
				},
			).WithObjects(
				&appsv1.Deployment{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-rbg-test-role",
						Namespace: "default",
						UID:       "deploy-uid",
					},
				},
			).Build(),
			rbg: &workloadsv1alpha1.RoleBasedGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-rbg",
					Namespace: "default",
				},
			},
			role: &workloadsv1alpha1.RoleSpec{
				Name:     "test-role",
				Replicas: &replicas,
			},
			mockReconcile: false,
			expectError:   true,
		},
		{
			name:   "nil rbg",
			client: fake.NewClientBuilder().WithScheme(scheme).Build(),
			rbg:    nil,
			role: &workloadsv1alpha1.RoleSpec{
				Name:     "test-role",
				Replicas: &replicas,
			},
			mockReconcile: false,
			expectError:   false,
		},
		{
			name:   "nil role",
			client: fake.NewClientBuilder().WithScheme(scheme).Build(),
			rbg: &workloadsv1alpha1.RoleBasedGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-rbg",
					Namespace: "default",
				},
			},
			role:          nil,
			mockReconcile: false,
			expectError:   false,
		},
	}

	for _, tt := range tests {
		t.Run(
			tt.name, func(t *testing.T) {
				r := &DeploymentReconciler{
					scheme: tt.client.Scheme(),
					client: tt.client,
				}

				logger := zap.New().WithValues("env", "test")
				ctx := log.IntoContext(context.TODO(), logger)

				if tt.mockReconcile {
					// mock rbg controller reconcile
					go func() {
						for i := 0; i < 60; i++ {
							newDeploy := &appsv1.Deployment{
								ObjectMeta: metav1.ObjectMeta{
									Name:      "test-rbg-test-role",
									Namespace: "default",
								},
							}
							err := r.client.Create(ctx, newDeploy)
							if err != nil && !apierrors.IsAlreadyExists(err) {
								t.Logf("create failed: %v", err)
							}
							time.Sleep(5 * time.Second)
						}
					}()
				}

				err := r.RecreateWorkload(ctx, tt.rbg, tt.role)

				if (err != nil) != tt.expectError {
					t.Errorf("DeploymentReconciler.RecreateWorkload() error = %v, expectError %v", err, tt.expectError)
				}
			},
		)
	}
}

// TestDeploymentReconciler_constructDeployApplyConfiguration tests the constructDeployApplyConfiguration method
func TestDeploymentReconciler_constructDeployApplyConfiguration(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = appsv1.AddToScheme(scheme)
	_ = workloadsv1alpha1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)

	replicas := int32(1)
	deployRole := &workloadsv1alpha1.RoleSpec{
		Name:     "test-role",
		Replicas: &replicas,
		Workload: workloadsv1alpha1.WorkloadSpec{
			APIVersion: "apps/v1",
			Kind:       "Deployment",
		},
	}
	rbg := &workloadsv1alpha1.RoleBasedGroup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-rbg",
			Namespace: "default",
			UID:       "test-uid",
			Labels: map[string]string{
				workloadsv1alpha1.SetNameLabelKey: "test-rbg",
			},
		},
		Spec: workloadsv1alpha1.RoleBasedGroupSpec{
			Roles: []workloadsv1alpha1.RoleSpec{
				*deployRole,
			},
		},
	}

	tests := []struct {
		name        string
		rbg         *workloadsv1alpha1.RoleBasedGroup
		role        *workloadsv1alpha1.RoleSpec
		oldDeploy   *appsv1.Deployment
		expectError bool
	}{
		{
			name:        "construct deployment configuration",
			rbg:         rbg,
			role:        deployRole,
			oldDeploy:   &appsv1.Deployment{},
			expectError: false,
		},
		{
			name: "construct deployment with existing deployment",
			rbg:  rbg,
			role: deployRole,
			oldDeploy: &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					UID:       "deploy-uid",
					Name:      "test-rbg-test-role",
					Namespace: "rbg",
					Labels: map[string]string{
						workloadsv1alpha1.SetNameLabelKey: "test-rbg",
					},
				},
				Spec: appsv1.DeploymentSpec{
					Selector: &metav1.LabelSelector{
						MatchLabels: map[string]string{
							"existing": "label",
						},
					},
				},
			},
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(
			tt.name, func(t *testing.T) {
				client := fake.NewClientBuilder().WithScheme(scheme).Build()
				r := &DeploymentReconciler{
					scheme: client.Scheme(),
					client: client,
				}

				ctx := context.Background()
				_, err := r.constructDeployApplyConfiguration(ctx, tt.rbg, tt.role, tt.oldDeploy, nil, "revision-key")

				if (err != nil) != tt.expectError {
					t.Errorf(
						"DeploymentReconciler.constructDeployApplyConfiguration() error = %v, expectError %v", err,
						tt.expectError,
					)
				}
			},
		)
	}
}
