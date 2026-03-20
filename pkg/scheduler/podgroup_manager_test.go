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

package scheduler

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/rbgs/api/workloads/constants"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	"sigs.k8s.io/rbgs/pkg/utils"
	wrappersv2 "sigs.k8s.io/rbgs/test/wrappers/v1alpha2"
	schedv1alpha1 "sigs.k8s.io/scheduler-plugins/apis/scheduling/v1alpha1"
	volcanoschedulingv1beta1 "volcano.sh/apis/pkg/apis/scheduling/v1beta1"
)

func TestPodGroupScheduler_Reconcile(t *testing.T) {
	// Define test scheme
	scheme := runtime.NewScheme()
	_ = workloadsv1alpha2.AddToScheme(scheme)
	_ = schedv1alpha1.AddToScheme(scheme)
	_ = volcanoschedulingv1beta1.AddToScheme(scheme)
	_ = apiextensionsv1.AddToScheme(scheme)

	gvk := utils.GetRbgGVK()
	rbgName := "test-rbg"
	rbgNamespace := "default"
	podGroup := &schedv1alpha1.PodGroup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      rbgName,
			Namespace: rbgNamespace,
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion:         gvk.GroupVersion().String(),
					Kind:               gvk.Kind,
					Name:               rbgName,
					UID:                "rbg-test-uid",
					Controller:         ptr.To[bool](true),
					BlockOwnerDeletion: ptr.To[bool](true),
				},
			},
		},
		Spec: schedv1alpha1.PodGroupSpec{
			MinMember:              3,
			ScheduleTimeoutSeconds: ptr.To(int32(300)),
		},
	}

	runtimeController := builder.TypedBuilder[reconcile.Request]{}
	watchedWorkload := sync.Map{}

	tests := []struct {
		name        string
		client      client.Client
		rbg         *workloadsv1alpha2.RoleBasedGroup
		pluginType  SchedulerPluginType
		apiReader   client.Reader
		preFunc     func()
		expectPG    bool
		expectError bool
	}{
		{
			name:       "create pod group when kube gang scheduling enabled and pod group not exists",
			client:     fake.NewClientBuilder().WithScheme(scheme).Build(),
			pluginType: KubeSchedulerPlugin,
			rbg: wrappersv2.BuildBasicRoleBasedGroup(rbgName, rbgNamespace).
				WithAnnotations(map[string]string{
					constants.GangSchedulingAnnotationKey: "true",
				}).Obj(),
			apiReader: fake.NewClientBuilder().WithScheme(scheme).WithObjects(
				&apiextensionsv1.CustomResourceDefinition{
					ObjectMeta: metav1.ObjectMeta{Name: KubePodGroupCrdName},
					Status: apiextensionsv1.CustomResourceDefinitionStatus{
						Conditions: []apiextensionsv1.CustomResourceDefinitionCondition{
							{
								Type:   apiextensionsv1.Established,
								Status: apiextensionsv1.ConditionTrue,
							},
						},
					},
				},
			).Build(),
			expectPG:    true,
			expectError: false,
		},
		{
			name:       "create pod group when volcano gang scheduling enabled and pod group not exists",
			client:     fake.NewClientBuilder().WithScheme(scheme).Build(),
			pluginType: VolcanoSchedulerPlugin,
			rbg: wrappersv2.BuildBasicRoleBasedGroup(rbgName, rbgNamespace).
				WithAnnotations(map[string]string{
					constants.GangSchedulingAnnotationKey:           "true",
					constants.GangSchedulingVolcanoPriorityClassKey: "high-priority",
					constants.GangSchedulingVolcanoQueueKey:         "gpu-queue",
				}).Obj(),
			apiReader: fake.NewClientBuilder().WithScheme(scheme).WithObjects(
				&apiextensionsv1.CustomResourceDefinition{
					ObjectMeta: metav1.ObjectMeta{Name: VolcanoPodGroupCrdName},
					Status: apiextensionsv1.CustomResourceDefinitionStatus{
						Conditions: []apiextensionsv1.CustomResourceDefinitionCondition{
							{
								Type:   apiextensionsv1.Established,
								Status: apiextensionsv1.ConditionTrue,
							},
						},
					},
				},
			).Build(),
			expectPG:    true,
			expectError: false,
		},
		{
			name:       "gang scheduling disabled (no annotation)",
			client:     fake.NewClientBuilder().WithScheme(scheme).Build(),
			pluginType: KubeSchedulerPlugin,
			rbg:        wrappersv2.BuildBasicRoleBasedGroup(rbgName, rbgNamespace).Obj(),
			preFunc: func() {
				watchedWorkload.LoadOrStore(KubePodGroupCrdName, struct{}{})
				runtimeController.Owns(&schedv1alpha1.PodGroup{})
			},
			expectPG:    false,
			expectError: false,
		},
		{
			name:       "delete pod group when gang scheduling disabled and pod group exists",
			client:     fake.NewClientBuilder().WithScheme(scheme).WithObjects(podGroup).Build(),
			pluginType: KubeSchedulerPlugin,
			rbg:        wrappersv2.BuildBasicRoleBasedGroup(rbgName, rbgNamespace).Obj(),
			preFunc: func() {
				watchedWorkload.LoadOrStore(KubePodGroupCrdName, struct{}{})
				runtimeController.Owns(&schedv1alpha1.PodGroup{})
			},
			expectPG:    false,
			expectError: false,
		},
		{
			name:       "update pod group when kube gang scheduling enabled and min member changed",
			client:     fake.NewClientBuilder().WithScheme(scheme).WithObjects(podGroup).Build(),
			pluginType: KubeSchedulerPlugin,
			rbg: &workloadsv1alpha2.RoleBasedGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name:      rbgName,
					Namespace: rbgNamespace,
					UID:       "rbg-test-uid",
					Annotations: map[string]string{
						constants.GangSchedulingAnnotationKey:             "true",
						constants.GangSchedulingScheduleTimeoutSecondsKey: "30",
					},
				},
				Spec: workloadsv1alpha2.RoleBasedGroupSpec{
					Roles: []workloadsv1alpha2.RoleSpec{
						{
							Name:     "role1",
							Replicas: ptr.To[int32](5),
						},
					},
				},
			},
			apiReader: fake.NewClientBuilder().WithScheme(scheme).WithObjects(
				&apiextensionsv1.CustomResourceDefinition{
					ObjectMeta: metav1.ObjectMeta{Name: KubePodGroupCrdName},
					Status: apiextensionsv1.CustomResourceDefinitionStatus{
						Conditions: []apiextensionsv1.CustomResourceDefinitionCondition{
							{
								Type:   apiextensionsv1.Established,
								Status: apiextensionsv1.ConditionTrue,
							},
						},
					},
				},
			).Build(),
			expectPG:    true,
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(
			tt.name, func(t *testing.T) {
				mgr, err := NewPodGroupManager(tt.pluginType, tt.client)
				require.NoError(t, err)
				ctx := log.IntoContext(context.TODO(), zap.New().WithValues("env", "test"))
				if tt.preFunc != nil {
					tt.preFunc()
				}
				err = mgr.ReconcilePodGroup(ctx, tt.rbg, &runtimeController, &watchedWorkload, tt.apiReader)

				// Verify
				if (err != nil) != tt.expectError {
					t.Errorf("PodGroupManager.ReconcilePodGroup() error = %v, expectError %v", err, tt.expectError)
				}

				// Check if pod group exists or not
				var obj client.Object
				if tt.pluginType == VolcanoSchedulerPlugin {
					pg := &volcanoschedulingv1beta1.PodGroup{}
					err = tt.client.Get(
						context.Background(), types.NamespacedName{
							Name:      tt.rbg.Name,
							Namespace: tt.rbg.Namespace,
						}, pg,
					)
					obj = pg
				} else {
					pg := &schedv1alpha1.PodGroup{}
					err = tt.client.Get(
						context.Background(), types.NamespacedName{
							Name:      tt.rbg.Name,
							Namespace: tt.rbg.Namespace,
						}, pg,
					)
					obj = pg
				}

				if tt.expectPG {
					assert.NoError(t, err)

					switch pg := obj.(type) {
					case *volcanoschedulingv1beta1.PodGroup:
						assert.Equal(t, tt.rbg.Name, pg.Name)
						assert.Equal(t, tt.rbg.Namespace, pg.Namespace)
						assert.Equal(t, int32(tt.rbg.GetGroupSize()), pg.Spec.MinMember)

						// Check owner reference
						assert.Len(t, pg.OwnerReferences, 1)
						assert.Equal(t, tt.rbg.Name, pg.OwnerReferences[0].Name)
						assert.Equal(t, "RoleBasedGroup", pg.OwnerReferences[0].Kind)
					case *schedv1alpha1.PodGroup:
						assert.Equal(t, tt.rbg.Name, pg.Name)
						assert.Equal(t, tt.rbg.Namespace, pg.Namespace)
						assert.Equal(t, int32(tt.rbg.GetGroupSize()), pg.Spec.MinMember)

						// Check owner reference
						assert.Len(t, pg.OwnerReferences, 1)
						assert.Equal(t, tt.rbg.Name, pg.OwnerReferences[0].Name)
						assert.Equal(t, "RoleBasedGroup", pg.OwnerReferences[0].Kind)
					}
				} else {
					assert.Error(t, err)
					assert.Contains(t, err.Error(), "not found")
				}
			},
		)
	}
}
