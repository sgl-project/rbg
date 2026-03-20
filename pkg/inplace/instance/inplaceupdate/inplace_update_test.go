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

package inplaceupdate

import (
	"errors"
	"testing"

	apps "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	inplaceapi "sigs.k8s.io/rbgs/api/workloads/pub/inplace_update"

	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
)

// fakeClientAdapter implements clientdapter.Adapter for testing
type fakeClientAdapter struct {
	getRoleInstanceFunc          func(namespace, name string) (*workloadsv1alpha2.RoleInstance, error)
	updateRoleInstanceFunc       func(instance *workloadsv1alpha2.RoleInstance) (*workloadsv1alpha2.RoleInstance, error)
	updateRoleInstanceStatusFunc func(instance *workloadsv1alpha2.RoleInstance) error
}

func (f *fakeClientAdapter) GetRoleInstance(namespace, name string) (*workloadsv1alpha2.RoleInstance, error) {
	if f.getRoleInstanceFunc != nil {
		return f.getRoleInstanceFunc(namespace, name)
	}
	return nil, errors.New("not implemented")
}

func (f *fakeClientAdapter) UpdateRoleInstance(instance *workloadsv1alpha2.RoleInstance) (*workloadsv1alpha2.RoleInstance, error) {
	if f.updateRoleInstanceFunc != nil {
		return f.updateRoleInstanceFunc(instance)
	}
	return nil, errors.New("not implemented")
}

func (f *fakeClientAdapter) UpdateRoleInstanceStatus(instance *workloadsv1alpha2.RoleInstance) error {
	if f.updateRoleInstanceStatusFunc != nil {
		return f.updateRoleInstanceStatusFunc(instance)
	}
	return errors.New("not implemented")
}

// fakeRevisionAdapter implements revisionadapter.Interface for testing
type fakeRevisionAdapter struct {
	equalToRevisionHashFunc func(controllerKey string, obj metav1.Object, hash string) bool
	writeRevisionHashFunc   func(obj metav1.Object, hash string)
}

func (f *fakeRevisionAdapter) EqualToRevisionHash(controllerKey string, obj metav1.Object, hash string) bool {
	if f.equalToRevisionHashFunc != nil {
		return f.equalToRevisionHashFunc(controllerKey, obj, hash)
	}
	return false
}

func (f *fakeRevisionAdapter) WriteRevisionHash(obj metav1.Object, hash string) {
	if f.writeRevisionHashFunc != nil {
		f.writeRevisionHashFunc(obj, hash)
	}
}

func TestRealControlRefresh(t *testing.T) {
	tests := []struct {
		name     string
		instance *workloadsv1alpha2.RoleInstance
		opts     *UpdateOptions
		adapter  *fakeClientAdapter
		expected bool
	}{
		{
			name: "instance update not completed",
			instance: &workloadsv1alpha2.RoleInstance{
				ObjectMeta: metav1.ObjectMeta{Generation: 2},
				Status:     workloadsv1alpha2.RoleInstanceStatus{ObservedGeneration: 1},
			},
			opts:     &UpdateOptions{},
			adapter:  &fakeClientAdapter{},
			expected: false,
		},
		{
			name: "instance without readiness gate",
			instance: &workloadsv1alpha2.RoleInstance{
				ObjectMeta: metav1.ObjectMeta{Generation: 1},
				Status:     workloadsv1alpha2.RoleInstanceStatus{ObservedGeneration: 1},
			},
			opts:     &UpdateOptions{},
			adapter:  &fakeClientAdapter{},
			expected: false,
		},
		{
			name: "instance with readiness gate and update completed",
			instance: &workloadsv1alpha2.RoleInstance{
				ObjectMeta: metav1.ObjectMeta{Generation: 1},
				Status:     workloadsv1alpha2.RoleInstanceStatus{ObservedGeneration: 1},
				Spec: workloadsv1alpha2.RoleInstanceSpec{
					ReadinessGates: []workloadsv1alpha2.RoleInstanceReadinessGate{
						{ConditionType: workloadsv1alpha2.RoleInstanceInPlaceUpdateReady},
					},
				},
			},
			opts: &UpdateOptions{},
			adapter: &fakeClientAdapter{
				getRoleInstanceFunc: func(namespace, name string) (*workloadsv1alpha2.RoleInstance, error) {
					return &workloadsv1alpha2.RoleInstance{
						ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
					}, nil
				},
				updateRoleInstanceStatusFunc: func(instance *workloadsv1alpha2.RoleInstance) error {
					return nil
				},
			},
			expected: true,
		},
		{
			name: "instance with readiness gate but update status fails",
			instance: &workloadsv1alpha2.RoleInstance{
				ObjectMeta: metav1.ObjectMeta{Generation: 1},
				Status:     workloadsv1alpha2.RoleInstanceStatus{ObservedGeneration: 1},
				Spec: workloadsv1alpha2.RoleInstanceSpec{
					ReadinessGates: []workloadsv1alpha2.RoleInstanceReadinessGate{
						{ConditionType: workloadsv1alpha2.RoleInstanceInPlaceUpdateReady},
					},
				},
			},
			opts: &UpdateOptions{},
			adapter: &fakeClientAdapter{
				updateRoleInstanceStatusFunc: func(instance *workloadsv1alpha2.RoleInstance) error {
					return errors.New("update failed")
				},
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			control := &realControl{
				clientAdapter:   tt.adapter,
				revisionAdapter: &fakeRevisionAdapter{},
			}

			result := control.Refresh(tt.instance, tt.opts)

			if tt.expected {
				if result.RefreshErr != nil {
					t.Errorf("Refresh() error = %v, want nil", result.RefreshErr)
				}
			} else {
				if result.RefreshErr == nil && tt.adapter.updateRoleInstanceStatusFunc != nil {
					t.Errorf("Refresh() error = nil, want non-nil")
				}
			}
		})
	}
}

func TestRealControlCanUpdateInPlace(t *testing.T) {
	tests := []struct {
		name        string
		oldRevision *apps.ControllerRevision
		newRevision *apps.ControllerRevision
		opts        *UpdateOptions
		expected    bool
	}{
		{
			name:        "nil revisions",
			oldRevision: nil,
			newRevision: nil,
			opts:        nil,
			expected:    false,
		},
		{
			name:        "valid revisions with default calculate spec",
			oldRevision: &apps.ControllerRevision{ObjectMeta: metav1.ObjectMeta{Name: "old"}},
			newRevision: &apps.ControllerRevision{ObjectMeta: metav1.ObjectMeta{Name: "new"}},
			opts:        nil,
			expected:    false, // defaultCalculateInPlaceUpdateSpec returns nil for invalid revisions
		},
		{
			name:        "valid revisions with custom calculate spec",
			oldRevision: &apps.ControllerRevision{ObjectMeta: metav1.ObjectMeta{Name: "old"}},
			newRevision: &apps.ControllerRevision{ObjectMeta: metav1.ObjectMeta{Name: "new"}},
			opts: &UpdateOptions{
				CalculateSpec: func(oldRevision, newRevision *apps.ControllerRevision, opts *UpdateOptions) *UpdateSpec {
					return &UpdateSpec{Revision: "test"}
				},
			},
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			control := &realControl{
				clientAdapter:   &fakeClientAdapter{},
				revisionAdapter: &fakeRevisionAdapter{},
			}

			result := control.CanUpdateInPlace(tt.oldRevision, tt.newRevision, tt.opts)

			if result != tt.expected {
				t.Errorf("CanUpdateInPlace() = %v, want %v", result, tt.expected)
			}
		})
	}
}

func TestRealControlUpdate(t *testing.T) {
	tests := []struct {
		name        string
		instance    *workloadsv1alpha2.RoleInstance
		oldRevision *apps.ControllerRevision
		newRevision *apps.ControllerRevision
		opts        *UpdateOptions
		adapter     *fakeClientAdapter
		expected    bool
	}{
		{
			name:        "nil calculate spec",
			instance:    &workloadsv1alpha2.RoleInstance{},
			oldRevision: &apps.ControllerRevision{},
			newRevision: &apps.ControllerRevision{},
			opts: &UpdateOptions{
				CalculateSpec: func(oldRevision, newRevision *apps.ControllerRevision, opts *UpdateOptions) *UpdateSpec {
					return nil
				},
			},
			adapter:  &fakeClientAdapter{},
			expected: false,
		},
		{
			name:        "valid calculate spec without readiness gate",
			instance:    &workloadsv1alpha2.RoleInstance{},
			oldRevision: &apps.ControllerRevision{},
			newRevision: &apps.ControllerRevision{},
			opts: &UpdateOptions{
				CalculateSpec: func(oldRevision, newRevision *apps.ControllerRevision, opts *UpdateOptions) *UpdateSpec {
					return &UpdateSpec{Revision: "test"}
				},
				PatchSpecToRoleInstance: func(instance *workloadsv1alpha2.RoleInstance, spec *UpdateSpec, state *inplaceapi.InPlaceUpdateState) (*workloadsv1alpha2.RoleInstance, error) {
					return instance, nil
				},
			},
			adapter: &fakeClientAdapter{
				getRoleInstanceFunc: func(namespace, name string) (*workloadsv1alpha2.RoleInstance, error) {
					return &workloadsv1alpha2.RoleInstance{
						ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
					}, nil
				},
				updateRoleInstanceFunc: func(instance *workloadsv1alpha2.RoleInstance) (*workloadsv1alpha2.RoleInstance, error) {
					instance.ResourceVersion = "newVersion"
					return instance, nil
				},
			},
			expected: true,
		},
		{
			name: "valid calculate spec with readiness gate",
			instance: &workloadsv1alpha2.RoleInstance{
				Spec: workloadsv1alpha2.RoleInstanceSpec{
					ReadinessGates: []workloadsv1alpha2.RoleInstanceReadinessGate{
						{ConditionType: workloadsv1alpha2.RoleInstanceInPlaceUpdateReady},
					},
				},
			},
			oldRevision: &apps.ControllerRevision{},
			newRevision: &apps.ControllerRevision{},
			opts: &UpdateOptions{
				CalculateSpec: func(oldRevision, newRevision *apps.ControllerRevision, opts *UpdateOptions) *UpdateSpec {
					return &UpdateSpec{Revision: "test"}
				},
				PatchSpecToRoleInstance: func(instance *workloadsv1alpha2.RoleInstance, spec *UpdateSpec, state *inplaceapi.InPlaceUpdateState) (*workloadsv1alpha2.RoleInstance, error) {
					return instance, nil
				},
			},
			adapter: &fakeClientAdapter{
				getRoleInstanceFunc: func(namespace, name string) (*workloadsv1alpha2.RoleInstance, error) {
					return &workloadsv1alpha2.RoleInstance{
						ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
					}, nil
				},
				updateRoleInstanceStatusFunc: func(instance *workloadsv1alpha2.RoleInstance) error {
					return nil
				},
				updateRoleInstanceFunc: func(instance *workloadsv1alpha2.RoleInstance) (*workloadsv1alpha2.RoleInstance, error) {
					instance.ResourceVersion = "new-version"
					return instance, nil
				},
			},
			expected: true,
		},
		{
			name:        "update instance fails",
			instance:    &workloadsv1alpha2.RoleInstance{},
			oldRevision: &apps.ControllerRevision{},
			newRevision: &apps.ControllerRevision{},
			opts: &UpdateOptions{
				CalculateSpec: func(oldRevision, newRevision *apps.ControllerRevision, opts *UpdateOptions) *UpdateSpec {
					return &UpdateSpec{Revision: "test"}
				},
				PatchSpecToRoleInstance: func(instance *workloadsv1alpha2.RoleInstance, spec *UpdateSpec, state *inplaceapi.InPlaceUpdateState) (*workloadsv1alpha2.RoleInstance, error) {
					return instance, nil
				},
			},
			adapter: &fakeClientAdapter{
				getRoleInstanceFunc: func(namespace, name string) (*workloadsv1alpha2.RoleInstance, error) {
					return &workloadsv1alpha2.RoleInstance{
						ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
					}, nil
				},
				updateRoleInstanceFunc: func(instance *workloadsv1alpha2.RoleInstance) (*workloadsv1alpha2.RoleInstance, error) {
					return nil, errors.New("update failed")
				},
			},
			expected: true, // InPlaceUpdate is true even when update fails
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			control := &realControl{
				clientAdapter:   tt.adapter,
				revisionAdapter: &fakeRevisionAdapter{},
			}

			result := control.Update(tt.instance, tt.oldRevision, tt.newRevision, tt.opts)

			if tt.expected {
				if !result.InPlaceUpdate {
					t.Errorf("Update() InPlaceUpdate = false, want true")
				}
				// For the failing update case, we expect an error and empty resource version
				if tt.name == "update instance fails" {
					if result.UpdateErr == nil {
						t.Errorf("Update() UpdateErr = nil, want non-nil")
					}
					if result.NewResourceVersion != "" {
						t.Errorf("Update() NewResourceVersion = %v, want empty", result.NewResourceVersion)
					}
				} else {
					if result.UpdateErr != nil {
						t.Errorf("Update() UpdateErr = %v, want nil", result.UpdateErr)
					}
					if result.NewResourceVersion == "" {
						t.Errorf("Update() NewResourceVersion = empty, want non-empty")
					}
				}
			} else {
				if result.InPlaceUpdate {
					t.Errorf("Update() InPlaceUpdate = true, want false")
				}
			}
		})
	}
}

func TestRealControlUpdateCondition(t *testing.T) {
	tests := []struct {
		name      string
		instance  *workloadsv1alpha2.RoleInstance
		condition workloadsv1alpha2.RoleInstanceCondition
		adapter   *fakeClientAdapter
		expected  bool
	}{
		{
			name: "successful condition update",
			instance: &workloadsv1alpha2.RoleInstance{
				ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
			},
			condition: workloadsv1alpha2.RoleInstanceCondition{
				Type:   workloadsv1alpha2.RoleInstanceInPlaceUpdateReady,
				Status: corev1.ConditionTrue,
			},
			adapter: &fakeClientAdapter{
				getRoleInstanceFunc: func(namespace, name string) (*workloadsv1alpha2.RoleInstance, error) {
					return &workloadsv1alpha2.RoleInstance{
						ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
					}, nil
				},
				updateRoleInstanceStatusFunc: func(instance *workloadsv1alpha2.RoleInstance) error {
					return nil
				},
			},
			expected: true,
		},
		{
			name: "get instance fails",
			instance: &workloadsv1alpha2.RoleInstance{
				ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
			},
			condition: workloadsv1alpha2.RoleInstanceCondition{
				Type:   workloadsv1alpha2.RoleInstanceInPlaceUpdateReady,
				Status: corev1.ConditionTrue,
			},
			adapter: &fakeClientAdapter{
				getRoleInstanceFunc: func(namespace, name string) (*workloadsv1alpha2.RoleInstance, error) {
					return nil, errors.New("get failed")
				},
			},
			expected: false,
		},
		{
			name: "update status fails",
			instance: &workloadsv1alpha2.RoleInstance{
				ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
			},
			condition: workloadsv1alpha2.RoleInstanceCondition{
				Type:   workloadsv1alpha2.RoleInstanceInPlaceUpdateReady,
				Status: corev1.ConditionTrue,
			},
			adapter: &fakeClientAdapter{
				getRoleInstanceFunc: func(namespace, name string) (*workloadsv1alpha2.RoleInstance, error) {
					return &workloadsv1alpha2.RoleInstance{
						ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
					}, nil
				},
				updateRoleInstanceStatusFunc: func(instance *workloadsv1alpha2.RoleInstance) error {
					return errors.New("update failed")
				},
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			control := &realControl{
				clientAdapter:   tt.adapter,
				revisionAdapter: &fakeRevisionAdapter{},
			}

			err := control.updateCondition(tt.instance, tt.condition)

			if tt.expected {
				if err != nil {
					t.Errorf("updateCondition() error = %v, want nil", err)
				}
			} else {
				if err == nil {
					t.Errorf("updateCondition() error = nil, want non-nil")
				}
			}
		})
	}
}

func TestRealControlUpdateInstanceInPlace(t *testing.T) {
	tests := []struct {
		name     string
		instance *workloadsv1alpha2.RoleInstance
		spec     *UpdateSpec
		opts     *UpdateOptions
		adapter  *fakeClientAdapter
		expected bool
	}{
		{
			name: "successful in-place update",
			instance: &workloadsv1alpha2.RoleInstance{
				ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
			},
			spec: &UpdateSpec{Revision: "test-rev"},
			opts: &UpdateOptions{
				PatchSpecToRoleInstance: func(instance *workloadsv1alpha2.RoleInstance, spec *UpdateSpec, state *inplaceapi.InPlaceUpdateState) (*workloadsv1alpha2.RoleInstance, error) {
					return instance, nil
				},
			},
			adapter: &fakeClientAdapter{
				getRoleInstanceFunc: func(namespace, name string) (*workloadsv1alpha2.RoleInstance, error) {
					return &workloadsv1alpha2.RoleInstance{
						ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
					}, nil
				},
				updateRoleInstanceFunc: func(instance *workloadsv1alpha2.RoleInstance) (*workloadsv1alpha2.RoleInstance, error) {
					instance.ResourceVersion = "new-version"
					return instance, nil
				},
			},
			expected: true,
		},
		{
			name: "get instance fails",
			instance: &workloadsv1alpha2.RoleInstance{
				ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
			},
			spec: &UpdateSpec{Revision: "test-rev"},
			opts: &UpdateOptions{
				PatchSpecToRoleInstance: func(instance *workloadsv1alpha2.RoleInstance, spec *UpdateSpec, state *inplaceapi.InPlaceUpdateState) (*workloadsv1alpha2.RoleInstance, error) {
					return instance, nil
				},
			},
			adapter: &fakeClientAdapter{
				getRoleInstanceFunc: func(namespace, name string) (*workloadsv1alpha2.RoleInstance, error) {
					return nil, errors.New("get failed")
				},
			},
			expected: false,
		},
		{
			name: "patch spec to instance fails",
			instance: &workloadsv1alpha2.RoleInstance{
				ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
			},
			spec: &UpdateSpec{Revision: "test-rev"},
			opts: &UpdateOptions{
				PatchSpecToRoleInstance: func(instance *workloadsv1alpha2.RoleInstance, spec *UpdateSpec, state *inplaceapi.InPlaceUpdateState) (*workloadsv1alpha2.RoleInstance, error) {
					return nil, errors.New("patch failed")
				},
			},
			adapter: &fakeClientAdapter{
				getRoleInstanceFunc: func(namespace, name string) (*workloadsv1alpha2.RoleInstance, error) {
					return &workloadsv1alpha2.RoleInstance{
						ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
					}, nil
				},
			},
			expected: false,
		},
		{
			name: "update instance fails",
			instance: &workloadsv1alpha2.RoleInstance{
				ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
			},
			spec: &UpdateSpec{Revision: "test-rev"},
			opts: &UpdateOptions{
				PatchSpecToRoleInstance: func(instance *workloadsv1alpha2.RoleInstance, spec *UpdateSpec, state *inplaceapi.InPlaceUpdateState) (*workloadsv1alpha2.RoleInstance, error) {
					return instance, nil
				},
			},
			adapter: &fakeClientAdapter{
				getRoleInstanceFunc: func(namespace, name string) (*workloadsv1alpha2.RoleInstance, error) {
					return &workloadsv1alpha2.RoleInstance{
						ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
					}, nil
				},
				updateRoleInstanceFunc: func(instance *workloadsv1alpha2.RoleInstance) (*workloadsv1alpha2.RoleInstance, error) {
					return nil, errors.New("update failed")
				},
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			control := &realControl{
				clientAdapter:   tt.adapter,
				revisionAdapter: &fakeRevisionAdapter{},
			}

			resourceVersion, err := control.updateRoleInstanceInPlace(tt.instance, tt.spec, tt.opts)

			if tt.expected {
				if err != nil {
					t.Errorf("updateRoleInstanceInPlace() error = %v, want nil", err)
				}
				if resourceVersion == "" {
					t.Errorf("updateRoleInstanceInPlace() resourceVersion = empty, want non-empty")
				}
			} else {
				if err == nil {
					t.Errorf("updateRoleInstanceInPlace() error = nil, want non-nil")
				}
			}
		})
	}
}

func TestUpdateResult(t *testing.T) {
	tests := []struct {
		name     string
		result   UpdateResult
		expected bool
	}{
		{
			name: "successful update",
			result: UpdateResult{
				InPlaceUpdate:      true,
				UpdateErr:          nil,
				NewResourceVersion: "v1",
			},
			expected: true,
		},
		{
			name: "failed update",
			result: UpdateResult{
				InPlaceUpdate:      true,
				UpdateErr:          errors.New("update failed"),
				NewResourceVersion: "",
			},
			expected: false,
		},
		{
			name: "no in-place update",
			result: UpdateResult{
				InPlaceUpdate:      false,
				UpdateErr:          nil,
				NewResourceVersion: "",
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			success := tt.result.InPlaceUpdate && tt.result.UpdateErr == nil && tt.result.NewResourceVersion != ""
			if success != tt.expected {
				t.Errorf("UpdateResult success = %v, want %v", success, tt.expected)
			}
		})
	}
}
