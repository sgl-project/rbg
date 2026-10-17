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
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"sigs.k8s.io/rbgs/api/workloads/constants"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	"sigs.k8s.io/rbgs/pkg/scale"
)

func TestScalingAdapterReplicasOverride_ForeignAdapterIgnored(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)
	_ = workloadsv1alpha2.AddToScheme(scheme)

	rbg := &workloadsv1alpha2.RoleBasedGroup{
		ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: "default", UID: "new-rbg-uid"},
		Spec: workloadsv1alpha2.RoleBasedGroupSpec{
			Roles: []workloadsv1alpha2.RoleSpec{{
				Name:           "worker",
				Replicas:       ptr.To[int32](2),
				ScalingAdapter: &workloadsv1alpha2.ScalingAdapter{Enable: true},
			}},
		},
	}

	tests := []struct {
		name             string
		adapterOwnerUID  string
		expectedReplicas int32
	}{
		{
			name:             "own adapter overrides replicas",
			adapterOwnerUID:  "new-rbg-uid",
			expectedReplicas: 9,
		},
		{
			name:             "foreign adapter is ignored",
			adapterOwnerUID:  "old-rbg-uid",
			expectedReplicas: 2,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			adapter := &workloadsv1alpha2.RoleBasedGroupScalingAdapter{
				ObjectMeta: metav1.ObjectMeta{
					Name:      scale.GenerateScalingAdapterName(rbg.Name, "worker"),
					Namespace: rbg.Namespace,
					OwnerReferences: []metav1.OwnerReference{{
						APIVersion: "workloads.x-k8s.io/v1alpha2",
						Kind:       "RoleBasedGroup",
						Name:       rbg.Name,
						UID:        types.UID(tt.adapterOwnerUID),
					}},
				},
				Spec: workloadsv1alpha2.RoleBasedGroupScalingAdapterSpec{
					Replicas:       ptr.To[int32](9),
					ScaleTargetRef: &workloadsv1alpha2.AdapterScaleTargetRef{Name: rbg.Name, Role: "worker"},
				},
				Status: workloadsv1alpha2.RoleBasedGroupScalingAdapterStatus{Phase: constants.AdapterPhaseBound},
			}

			testRBG := rbg.DeepCopy()
			c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(adapter).Build()
			r := &RoleBasedGroupReconciler{client: c, recorder: record.NewFakeRecorder(10)}

			require.NoError(t, r.applyRBGSAReplicasOverride(context.Background(), testRBG))
			assert.Equal(t, tt.expectedReplicas, *testRBG.Spec.Roles[0].Replicas)
		})
	}
}

func TestReconcileScalingAdapter_UnownedAdapterRecreated(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = workloadsv1alpha2.AddToScheme(scheme)

	rbg := &workloadsv1alpha2.RoleBasedGroup{
		TypeMeta:   metav1.TypeMeta{APIVersion: "workloads.x-k8s.io/v1alpha2", Kind: "RoleBasedGroup"},
		ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: "default", UID: "new-rbg-uid"},
	}
	enabledRole := &workloadsv1alpha2.RoleSpec{
		Name:           "worker",
		ScalingAdapter: &workloadsv1alpha2.ScalingAdapter{Enable: true},
	}
	adapterName := scale.GenerateScalingAdapterName(rbg.Name, enabledRole.Name)

	makeAdapter := func(ownerUID types.UID, replicas *int32) *workloadsv1alpha2.RoleBasedGroupScalingAdapter {
		adapter := &workloadsv1alpha2.RoleBasedGroupScalingAdapter{
			ObjectMeta: metav1.ObjectMeta{Name: adapterName, Namespace: rbg.Namespace},
			Spec: workloadsv1alpha2.RoleBasedGroupScalingAdapterSpec{
				Replicas:       replicas,
				ScaleTargetRef: &workloadsv1alpha2.AdapterScaleTargetRef{Name: rbg.Name, Role: enabledRole.Name},
			},
		}
		if ownerUID != "" {
			adapter.OwnerReferences = []metav1.OwnerReference{{
				APIVersion: rbg.APIVersion,
				Kind:       rbg.Kind,
				Name:       rbg.Name,
				UID:        ownerUID,
			}}
		}
		return adapter
	}

	tests := []struct {
		name     string
		existing *workloadsv1alpha2.RoleBasedGroupScalingAdapter
	}{
		{
			name:     "foreign adapter is replaced",
			existing: makeAdapter("old-rbg-uid", ptr.To[int32](9)),
		},
		{
			name:     "orphan adapter is replaced",
			existing: makeAdapter("", ptr.To[int32](9)),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(tt.existing).Build()
			r := &RoleBasedGroupReconciler{client: c}

			require.NoError(t, r.ReconcileScalingAdapter(context.Background(), rbg, enabledRole))

			after := &workloadsv1alpha2.RoleBasedGroupScalingAdapter{}
			require.NoError(t, c.Get(context.Background(), client.ObjectKey{Name: adapterName, Namespace: rbg.Namespace}, after))
			assert.True(t, after.ContainsRBGOwner(rbg), "recreated adapter must be owned by the current RBG")
			assert.Nil(t, after.Spec.Replicas, "recreated adapter must not inherit the predecessor's replicas")
			assert.Equal(t, buildScalingAdapterLabels(enabledRole, rbg.Name, enabledRole.Name), after.Labels)
		})
	}
}

func TestReconcileScalingAdapter_DisabledDeletesForeignAdapter(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = workloadsv1alpha2.AddToScheme(scheme)

	rbg := &workloadsv1alpha2.RoleBasedGroup{
		ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: "default", UID: "new-rbg-uid"},
	}
	disabledRole := &workloadsv1alpha2.RoleSpec{Name: "worker"}

	adapter := &workloadsv1alpha2.RoleBasedGroupScalingAdapter{
		ObjectMeta: metav1.ObjectMeta{
			Name:      scale.GenerateScalingAdapterName(rbg.Name, disabledRole.Name),
			Namespace: rbg.Namespace,
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: "workloads.x-k8s.io/v1alpha2",
				Kind:       "RoleBasedGroup",
				Name:       rbg.Name,
				UID:        "old-rbg-uid",
			}},
		},
	}

	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(adapter).Build()
	r := &RoleBasedGroupReconciler{client: c}

	require.NoError(t, r.ReconcileScalingAdapter(context.Background(), rbg, disabledRole))

	err := c.Get(context.Background(), client.ObjectKeyFromObject(adapter), &workloadsv1alpha2.RoleBasedGroupScalingAdapter{})
	assert.Error(t, err, "a leftover adapter must be deleted when scaling is disabled, whoever owns it")
}

func TestReconcileScalingAdapter_OwnAdapterDeleted(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = workloadsv1alpha2.AddToScheme(scheme)

	rbg := &workloadsv1alpha2.RoleBasedGroup{
		ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: "default", UID: "new-rbg-uid"},
	}
	role := &workloadsv1alpha2.RoleSpec{Name: "worker"}

	adapter := &workloadsv1alpha2.RoleBasedGroupScalingAdapter{
		ObjectMeta: metav1.ObjectMeta{
			Name:      scale.GenerateScalingAdapterName(rbg.Name, role.Name),
			Namespace: rbg.Namespace,
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: "workloads.x-k8s.io/v1alpha2",
				Kind:       "RoleBasedGroup",
				Name:       rbg.Name,
				UID:        rbg.UID,
			}},
		},
	}

	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(adapter).Build()
	r := &RoleBasedGroupReconciler{client: c}

	err := r.ReconcileScalingAdapter(context.Background(), rbg, role)
	require.NoError(t, err)

	err = c.Get(context.Background(), client.ObjectKeyFromObject(adapter), &workloadsv1alpha2.RoleBasedGroupScalingAdapter{})
	assert.True(t, err != nil && strings.Contains(err.Error(), "not found"), "own adapter with disabled scaling should be deleted")
}

// TestReconcileScalingAdapter_RecreateUsesUIDPrecondition verifies that when we
// replace a foreign/orphaned adapter, the delete carries the observed UID as a
// precondition. Without it a stale cache could cause the reconcile to delete a
// newer live adapter that was created under the same name.
func TestReconcileScalingAdapter_RecreateUsesUIDPrecondition(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = workloadsv1alpha2.AddToScheme(scheme)

	rbg := &workloadsv1alpha2.RoleBasedGroup{
		TypeMeta:   metav1.TypeMeta{APIVersion: "workloads.x-k8s.io/v1alpha2", Kind: "RoleBasedGroup"},
		ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: "default", UID: "new-rbg-uid"},
	}
	enabledRole := &workloadsv1alpha2.RoleSpec{
		Name:           "worker",
		ScalingAdapter: &workloadsv1alpha2.ScalingAdapter{Enable: true},
	}
	adapterName := scale.GenerateScalingAdapterName(rbg.Name, enabledRole.Name)

	existing := &workloadsv1alpha2.RoleBasedGroupScalingAdapter{
		ObjectMeta: metav1.ObjectMeta{
			Name:      adapterName,
			Namespace: rbg.Namespace,
			UID:       "old-rbg-uid",
		},
		Spec: workloadsv1alpha2.RoleBasedGroupScalingAdapterSpec{
			Replicas:       ptr.To[int32](9),
			ScaleTargetRef: &workloadsv1alpha2.AdapterScaleTargetRef{Name: rbg.Name, Role: enabledRole.Name},
		},
	}

	var capturedUID *types.UID
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(existing).WithInterceptorFuncs(interceptor.Funcs{
		Delete: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
			var delOpts client.DeleteOptions
			delOpts.ApplyOptions(opts)
			if delOpts.Raw != nil && delOpts.Raw.Preconditions != nil {
				capturedUID = delOpts.Raw.Preconditions.UID
			}
			return c.Delete(ctx, obj, opts...)
		},
	}).Build()
	r := &RoleBasedGroupReconciler{client: c}

	require.NoError(t, r.ReconcileScalingAdapter(context.Background(), rbg, enabledRole))

	require.NotNil(t, capturedUID, "delete must include a UID precondition")
	assert.Equal(t, existing.UID, *capturedUID, "delete precondition must match the observed adapter UID")

	after := &workloadsv1alpha2.RoleBasedGroupScalingAdapter{}
	require.NoError(t, c.Get(context.Background(), client.ObjectKey{Name: adapterName, Namespace: rbg.Namespace}, after))
	assert.True(t, after.ContainsRBGOwner(rbg), "adapter must be recreated and owned by the current RBG")
}
