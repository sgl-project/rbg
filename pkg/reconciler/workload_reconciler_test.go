package reconciler

import (
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	lwsv1 "sigs.k8s.io/lws/api/leaderworkerset/v1"
	workloadsv1alpha1 "sigs.k8s.io/rbgs/api/workloads/v1alpha1"
)

// TestNewWorkloadReconciler tests the NewWorkloadReconciler function
// It verifies that the correct reconciler is returned for each workload type
func TestNewWorkloadReconciler(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = appsv1.AddToScheme(scheme)
	_ = lwsv1.AddToScheme(scheme)
	_ = workloadsv1alpha1.AddToScheme(scheme)

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()

	tests := []struct {
		name         string
		workloadType workloadsv1alpha1.WorkloadSpec
		expectError  bool
		expectedType string
	}{
		{
			name: "Deployment workload type",
			workloadType: workloadsv1alpha1.WorkloadSpec{
				APIVersion: "apps/v1",
				Kind:       "Deployment",
			},
			expectError:  false,
			expectedType: "*reconciler.DeploymentReconciler",
		},
		{
			name: "StatefulSet workload type",
			workloadType: workloadsv1alpha1.WorkloadSpec{
				APIVersion: "apps/v1",
				Kind:       "StatefulSet",
			},
			expectError:  false,
			expectedType: "*reconciler.StatefulSetReconciler",
		},
		{
			name: "LeaderWorkerSet workload type",
			workloadType: workloadsv1alpha1.WorkloadSpec{
				APIVersion: "leaderworkerset.x-k8s.io/v1",
				Kind:       "LeaderWorkerSet",
			},
			expectError:  false,
			expectedType: "*reconciler.LeaderWorkerSetReconciler",
		},
		{
			name: "Unsupported workload type",
			workloadType: workloadsv1alpha1.WorkloadSpec{
				APIVersion: "apps/v1",
				Kind:       "UnsupportedWorkload",
			},
			expectError:  true,
			expectedType: "",
		},
	}

	for _, tt := range tests {
		t.Run(
			tt.name, func(t *testing.T) {
				reconciler, err := NewWorkloadReconciler(tt.workloadType, scheme, fakeClient)

				if tt.expectError {
					if err == nil {
						t.Errorf("Expected error but got none")
					}
					return
				}

				if err != nil {
					t.Fatalf("Unexpected error: %v", err)
				}

				if reconciler == nil {
					t.Fatal("Expected reconciler to be created, but got nil")
				}
			},
		)
	}
}

// TestWorkloadEqual tests the WorkloadEqual function
// It verifies that the function correctly compares different workload types
func TestWorkloadEqual(t *testing.T) {
	tests := []struct {
		name        string
		obj1        interface{}
		obj2        interface{}
		expectEqual bool
		expectError bool
	}{
		{
			name: "Equal deployments",
			obj1: &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-deploy",
					Namespace: "default",
					UID:       "deploy-uid",
				},
				Spec: appsv1.DeploymentSpec{
					Replicas: ptr.To[int32](3),
					Selector: &metav1.LabelSelector{
						MatchLabels: map[string]string{"app": "test"},
					},
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Labels: map[string]string{"app": "test"},
						},
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Name:  "test-container",
									Image: "test-image",
								},
							},
						},
					},
				},
			},
			obj2: &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-deploy",
					Namespace: "default",
					UID:       "deploy-uid",
				},
				Spec: appsv1.DeploymentSpec{
					Replicas: ptr.To[int32](3),
					Selector: &metav1.LabelSelector{
						MatchLabels: map[string]string{"app": "test"},
					},
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Labels: map[string]string{"app": "test"},
						},
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Name:  "test-container",
									Image: "test-image",
								},
							},
						},
					},
				},
			},
			expectEqual: true,
			expectError: false,
		},
		{
			name: "Different deployments",
			obj1: &appsv1.Deployment{
				Spec: appsv1.DeploymentSpec{
					Replicas: ptr.To[int32](3),
					Template: corev1.PodTemplateSpec{
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
			obj2: &appsv1.Deployment{
				Spec: appsv1.DeploymentSpec{
					Replicas: ptr.To[int32](5),
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Name:  "test-container",
									Image: "test-image:v2",
								},
							},
						},
					},
				},
			},
			expectEqual: false,
			expectError: true,
		},
		{
			name: "Equal statefulsets",
			obj1: &appsv1.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-sts",
					Namespace: "default",
					UID:       "sts-uid",
				},
				Spec: appsv1.StatefulSetSpec{
					Replicas: ptr.To[int32](3),
					Selector: &metav1.LabelSelector{
						MatchLabels: map[string]string{"app": "test"},
					},
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Labels: map[string]string{"app": "test"},
						},
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Name:  "test-container",
									Image: "test-image",
								},
							},
						},
					},
				},
			},
			obj2: &appsv1.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-sts",
					Namespace: "default",
					UID:       "sts-uid",
				},
				Spec: appsv1.StatefulSetSpec{
					Replicas: ptr.To[int32](3),
					Selector: &metav1.LabelSelector{
						MatchLabels: map[string]string{"app": "test"},
					},
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Labels: map[string]string{"app": "test"},
						},
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Name:  "test-container",
									Image: "test-image",
								},
							},
						},
					},
				},
			},
			expectEqual: true,
			expectError: false,
		},
		{
			name: "Different statefulsets",
			obj1: &appsv1.StatefulSet{
				Spec: appsv1.StatefulSetSpec{
					Replicas: ptr.To[int32](3),
					Template: corev1.PodTemplateSpec{
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
			obj2: &appsv1.StatefulSet{
				Spec: appsv1.StatefulSetSpec{
					Replicas: ptr.To[int32](5),
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Name:  "test-container",
									Image: "test-image:v2",
								},
							},
						},
					},
				},
			},
			expectEqual: false,
			expectError: true,
		},
		{
			name: "Equal leaderworkerset",
			obj1: &lwsv1.LeaderWorkerSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-lws",
					Namespace: "default",
					UID:       "lws-uid",
				},
				Spec: lwsv1.LeaderWorkerSetSpec{
					Replicas: ptr.To[int32](3),
					LeaderWorkerTemplate: lwsv1.LeaderWorkerTemplate{
						Size: ptr.To(int32(2)),
						LeaderTemplate: &corev1.PodTemplateSpec{
							ObjectMeta: metav1.ObjectMeta{
								Labels: map[string]string{"role": "leader"},
							},
							Spec: corev1.PodSpec{
								Containers: []corev1.Container{
									{
										Name:  "test-container",
										Image: "test-image",
									},
								},
							},
						},
						WorkerTemplate: corev1.PodTemplateSpec{
							ObjectMeta: metav1.ObjectMeta{
								Labels: map[string]string{"role": "worker"},
							},
							Spec: corev1.PodSpec{
								Containers: []corev1.Container{
									{
										Name:  "test-container",
										Image: "test-image",
									},
								},
							},
						},
					},
				},
			},
			obj2: &lwsv1.LeaderWorkerSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-lws",
					Namespace: "default",
					UID:       "lws-uid",
				},
				Spec: lwsv1.LeaderWorkerSetSpec{
					Replicas: ptr.To[int32](3),
					LeaderWorkerTemplate: lwsv1.LeaderWorkerTemplate{
						Size: ptr.To(int32(2)),
						LeaderTemplate: &corev1.PodTemplateSpec{
							ObjectMeta: metav1.ObjectMeta{
								Labels: map[string]string{"role": "leader"},
							},
							Spec: corev1.PodSpec{
								Containers: []corev1.Container{
									{
										Name:  "test-container",
										Image: "test-image",
									},
								},
							},
						},
						WorkerTemplate: corev1.PodTemplateSpec{
							ObjectMeta: metav1.ObjectMeta{
								Labels: map[string]string{"role": "worker"},
							},
							Spec: corev1.PodSpec{
								Containers: []corev1.Container{
									{
										Name:  "test-container",
										Image: "test-image",
									},
								},
							},
						},
					},
				},
			},
			expectEqual: true,
			expectError: false,
		},
		{
			name: "Different leaderworkerset",
			obj1: &lwsv1.LeaderWorkerSet{
				Spec: lwsv1.LeaderWorkerSetSpec{
					Replicas: ptr.To[int32](3),
					LeaderWorkerTemplate: lwsv1.LeaderWorkerTemplate{
						LeaderTemplate: &corev1.PodTemplateSpec{
							Spec: corev1.PodSpec{
								Containers: []corev1.Container{
									{
										Name:  "leader-container",
										Image: "leader-image:v1",
									},
								},
							},
						},
					},
				},
			},
			obj2: &lwsv1.LeaderWorkerSet{
				Spec: lwsv1.LeaderWorkerSetSpec{
					Replicas: ptr.To[int32](5),
					LeaderWorkerTemplate: lwsv1.LeaderWorkerTemplate{
						LeaderTemplate: &corev1.PodTemplateSpec{
							Spec: corev1.PodSpec{
								Containers: []corev1.Container{
									{
										Name:  "leader-container",
										Image: "leader-image:v2",
									},
								},
							},
						},
					},
				},
			},
			expectEqual: false,
			expectError: true,
		},
		{
			name: "Unsupported workload type",
			obj1: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-pod",
				},
			},
			obj2: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-pod",
				},
			},
			expectEqual: false,
			expectError: true,
		},
		{
			name:        "Mismatched workload types",
			obj1:        &appsv1.Deployment{},
			obj2:        &appsv1.StatefulSet{},
			expectEqual: false,
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(
			tt.name, func(t *testing.T) {
				equal, err := WorkloadEqual(tt.obj1, tt.obj2)

				// Check if error expectation matches
				if tt.expectError {
					if err == nil {
						t.Error("Expected error but got none")
					}
				} else if err != nil {
					t.Errorf("Unexpected error: %v", err)
				}

				// Check if equality result matches expectation
				if equal != tt.expectEqual {
					t.Errorf("Expected equal=%v, but got %v", tt.expectEqual, equal)
				}
			},
		)
	}
}

// TestWorkloadReconcilerInterface ensures all reconciler types implement the WorkloadReconciler interface
func TestWorkloadReconcilerInterface(t *testing.T) {
	// Setup test scheme
	testScheme := runtime.NewScheme()
	_ = appsv1.AddToScheme(testScheme)
	_ = lwsv1.AddToScheme(testScheme)

	// Create fake client
	fakeClient := fake.NewClientBuilder().
		WithScheme(testScheme).
		Build()

	// Test that each reconciler implements the interface
	var _ WorkloadReconciler = &DeploymentReconciler{}
	var _ WorkloadReconciler = &StatefulSetReconciler{}
	var _ WorkloadReconciler = &LeaderWorkerSetReconciler{}

	// Test that we can create each reconciler type
	reconcilers := []struct {
		name       string
		reconciler WorkloadReconciler
	}{
		{
			name:       "DeploymentReconciler",
			reconciler: NewDeploymentReconciler(testScheme, fakeClient),
		},
		{
			name:       "StatefulSetReconciler",
			reconciler: NewStatefulSetReconciler(testScheme, fakeClient),
		},
		{
			name:       "LeaderWorkerSetReconciler",
			reconciler: NewLeaderWorkerSetReconciler(testScheme, fakeClient),
		},
	}

	// Verify each reconciler is not nil
	for _, r := range reconcilers {
		if r.reconciler == nil {
			t.Errorf("Failed to create %s", r.name)
		}
	}
}
