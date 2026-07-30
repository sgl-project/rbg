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

package v1alpha2

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/rbgs/api/workloads/constants"
)

func TestValidateNoLegacyWorkloads(t *testing.T) {
	tests := []struct {
		name        string
		roles       []RoleSpec
		expectError bool
		errContains string
	}{
		{
			name: "RoleInstanceSet only - no error",
			roles: []RoleSpec{
				{Name: "worker"},
			},
			expectError: false,
		},
		{
			name: "Deployment workload type - error",
			roles: []RoleSpec{
				{
					Name:        "worker",
					Annotations: map[string]string{constants.RoleWorkloadTypeAnnotationKey: constants.DeploymentWorkloadType},
				},
			},
			expectError: true,
			errContains: "apps/v1/Deployment",
		},
		{
			name: "StatefulSet workload type - error",
			roles: []RoleSpec{
				{
					Name:        "worker",
					Annotations: map[string]string{constants.RoleWorkloadTypeAnnotationKey: constants.StatefulSetWorkloadType},
				},
			},
			expectError: true,
			errContains: "apps/v1/StatefulSet",
		},
		{
			name: "LeaderWorkerSet workload type - error",
			roles: []RoleSpec{
				{
					Name:        "worker",
					Annotations: map[string]string{constants.RoleWorkloadTypeAnnotationKey: constants.LeaderWorkerSetWorkloadType},
				},
			},
			expectError: true,
			errContains: "leaderworkerset.x-k8s.io/v1/LeaderWorkerSet",
		},
		{
			name: "mixed roles with one legacy - error",
			roles: []RoleSpec{
				{Name: "role-a"},
				{
					Name:        "role-b",
					Annotations: map[string]string{constants.RoleWorkloadTypeAnnotationKey: constants.DeploymentWorkloadType},
				},
			},
			expectError: true,
			errContains: "role-b",
		},
		{
			name: "multiple legacy roles - error with all",
			roles: []RoleSpec{
				{
					Name:        "role-a",
					Annotations: map[string]string{constants.RoleWorkloadTypeAnnotationKey: constants.DeploymentWorkloadType},
				},
				{
					Name:        "role-b",
					Annotations: map[string]string{constants.RoleWorkloadTypeAnnotationKey: constants.StatefulSetWorkloadType},
				},
			},
			expectError: true,
			errContains: "role-a",
		},
		{
			name:        "empty roles - no error",
			roles:       []RoleSpec{},
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateNoLegacyWorkloads(tt.roles)
			if tt.expectError {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errContains)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestRoleBasedGroupValidator_ValidateCreate_DisableV1alpha1Compatibility(t *testing.T) {
	tests := []struct {
		name                         string
		disableV1alpha1Compatibility bool
		rbg                          *RoleBasedGroup
		expectError                  bool
	}{
		{
			name:                         "compatibility disabled, RoleInstanceSet - no error",
			disableV1alpha1Compatibility: true,
			rbg: &RoleBasedGroup{
				ObjectMeta: metav1.ObjectMeta{Name: "test-rbg"},
				Spec: RoleBasedGroupSpec{
					Roles: []RoleSpec{
						{Name: "worker", Replicas: ptr.To(int32(1))},
					},
				},
			},
			expectError: false,
		},
		{
			name:                         "compatibility disabled, StatefulSet - error",
			disableV1alpha1Compatibility: true,
			rbg: &RoleBasedGroup{
				ObjectMeta: metav1.ObjectMeta{Name: "test-rbg"},
				Spec: RoleBasedGroupSpec{
					Roles: []RoleSpec{
						{
							Name:        "worker",
							Replicas:    ptr.To(int32(1)),
							Annotations: map[string]string{constants.RoleWorkloadTypeAnnotationKey: constants.StatefulSetWorkloadType},
						},
					},
				},
			},
			expectError: true,
		},
		{
			name:                         "compatibility enabled, StatefulSet - no error",
			disableV1alpha1Compatibility: false,
			rbg: &RoleBasedGroup{
				ObjectMeta: metav1.ObjectMeta{Name: "test-rbg"},
				Spec: RoleBasedGroupSpec{
					Roles: []RoleSpec{
						{
							Name:        "worker",
							Replicas:    ptr.To(int32(1)),
							Annotations: map[string]string{constants.RoleWorkloadTypeAnnotationKey: constants.StatefulSetWorkloadType},
						},
					},
				},
			},
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := &RoleBasedGroupValidator{
				DisableV1alpha1Compatibility: tt.disableV1alpha1Compatibility,
			}
			_, err := v.ValidateCreate(context.Background(), tt.rbg)
			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestRoleBasedGroupValidator_ValidateUpdate_DisableV1alpha1Compatibility(t *testing.T) {
	oldRBG := &RoleBasedGroup{
		ObjectMeta: metav1.ObjectMeta{Name: "test-rbg"},
		Spec: RoleBasedGroupSpec{
			Roles: []RoleSpec{
				{Name: "worker", Replicas: ptr.To(int32(1))},
			},
		},
	}

	t.Run("compatibility disabled, update to legacy workload - error", func(t *testing.T) {
		newRBG := &RoleBasedGroup{
			ObjectMeta: metav1.ObjectMeta{Name: "test-rbg"},
			Spec: RoleBasedGroupSpec{
				Roles: []RoleSpec{
					{
						Name:        "worker",
						Replicas:    ptr.To(int32(1)),
						Annotations: map[string]string{constants.RoleWorkloadTypeAnnotationKey: constants.StatefulSetWorkloadType},
					},
				},
			},
		}
		v := &RoleBasedGroupValidator{
			Client:                       fake.NewClientBuilder().Build(),
			DisableV1alpha1Compatibility: true,
		}
		_, err := v.ValidateUpdate(context.Background(), oldRBG, newRBG)
		assert.Error(t, err)
	})

	t.Run("compatibility disabled, update to RoleInstanceSet - no error", func(t *testing.T) {
		newRBG := &RoleBasedGroup{
			ObjectMeta: metav1.ObjectMeta{Name: "test-rbg"},
			Spec: RoleBasedGroupSpec{
				Roles: []RoleSpec{
					{Name: "worker", Replicas: ptr.To(int32(1))},
				},
			},
		}
		v := &RoleBasedGroupValidator{
			Client:                       fake.NewClientBuilder().Build(),
			DisableV1alpha1Compatibility: true,
		}
		_, err := v.ValidateUpdate(context.Background(), oldRBG, newRBG)
		assert.NoError(t, err)
	})

	t.Run("compatibility enabled, update to legacy workload - no error", func(t *testing.T) {
		newRBG := &RoleBasedGroup{
			ObjectMeta: metav1.ObjectMeta{Name: "test-rbg"},
			Spec: RoleBasedGroupSpec{
				Roles: []RoleSpec{
					{
						Name:        "worker",
						Replicas:    ptr.To(int32(1)),
						Annotations: map[string]string{constants.RoleWorkloadTypeAnnotationKey: constants.DeploymentWorkloadType},
					},
				},
			},
		}
		v := &RoleBasedGroupValidator{
			Client:                       fake.NewClientBuilder().Build(),
			DisableV1alpha1Compatibility: false,
		}
		_, err := v.ValidateUpdate(context.Background(), oldRBG, newRBG)
		assert.NoError(t, err)
	})
}

func TestRoleBasedGroupSetValidator_ValidateCreate_DisableV1alpha1Compatibility(t *testing.T) {
	tests := []struct {
		name                         string
		disableV1alpha1Compatibility bool
		rbgs                         *RoleBasedGroupSet
		expectError                  bool
	}{
		{
			name:                         "compatibility disabled, RoleInstanceSet - no error",
			disableV1alpha1Compatibility: true,
			rbgs: &RoleBasedGroupSet{
				ObjectMeta: metav1.ObjectMeta{Name: "test-rbgs"},
				Spec: RoleBasedGroupSetSpec{
					Replicas: ptr.To(int32(1)),
					GroupTemplate: RoleBasedGroupTemplateSpec{
						Spec: RoleBasedGroupSpec{
							Roles: []RoleSpec{
								{Name: "worker", Replicas: ptr.To(int32(1))},
							},
						},
					},
				},
			},
			expectError: false,
		},
		{
			name:                         "compatibility disabled, Deployment - error",
			disableV1alpha1Compatibility: true,
			rbgs: &RoleBasedGroupSet{
				ObjectMeta: metav1.ObjectMeta{Name: "test-rbgs"},
				Spec: RoleBasedGroupSetSpec{
					Replicas: ptr.To(int32(1)),
					GroupTemplate: RoleBasedGroupTemplateSpec{
						Spec: RoleBasedGroupSpec{
							Roles: []RoleSpec{
								{
									Name:        "worker",
									Replicas:    ptr.To(int32(1)),
									Annotations: map[string]string{constants.RoleWorkloadTypeAnnotationKey: constants.DeploymentWorkloadType},
								},
							},
						},
					},
				},
			},
			expectError: true,
		},
		{
			name:                         "compatibility enabled, Deployment - no error",
			disableV1alpha1Compatibility: false,
			rbgs: &RoleBasedGroupSet{
				ObjectMeta: metav1.ObjectMeta{Name: "test-rbgs"},
				Spec: RoleBasedGroupSetSpec{
					Replicas: ptr.To(int32(1)),
					GroupTemplate: RoleBasedGroupTemplateSpec{
						Spec: RoleBasedGroupSpec{
							Roles: []RoleSpec{
								{
									Name:        "worker",
									Replicas:    ptr.To(int32(1)),
									Annotations: map[string]string{constants.RoleWorkloadTypeAnnotationKey: constants.DeploymentWorkloadType},
								},
							},
						},
					},
				},
			},
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := &RoleBasedGroupSetValidator{
				DisableV1alpha1Compatibility: tt.disableV1alpha1Compatibility,
			}
			_, err := v.ValidateCreate(context.Background(), tt.rbgs)
			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestRoleBasedGroupSetValidator_ValidateUpdate_DisableV1alpha1Compatibility(t *testing.T) {
	oldRBGS := &RoleBasedGroupSet{
		ObjectMeta: metav1.ObjectMeta{Name: "test-rbgs"},
		Spec: RoleBasedGroupSetSpec{
			Replicas: ptr.To(int32(1)),
			GroupTemplate: RoleBasedGroupTemplateSpec{
				Spec: RoleBasedGroupSpec{
					Roles: []RoleSpec{
						{Name: "worker", Replicas: ptr.To(int32(1))},
					},
				},
			},
		},
	}

	t.Run("compatibility disabled, update to legacy workload - error", func(t *testing.T) {
		newRBGS := &RoleBasedGroupSet{
			ObjectMeta: metav1.ObjectMeta{Name: "test-rbgs"},
			Spec: RoleBasedGroupSetSpec{
				Replicas: ptr.To(int32(1)),
				GroupTemplate: RoleBasedGroupTemplateSpec{
					Spec: RoleBasedGroupSpec{
						Roles: []RoleSpec{
							{
								Name:        "worker",
								Replicas:    ptr.To(int32(1)),
								Annotations: map[string]string{constants.RoleWorkloadTypeAnnotationKey: constants.LeaderWorkerSetWorkloadType},
							},
						},
					},
				},
			},
		}
		v := &RoleBasedGroupSetValidator{DisableV1alpha1Compatibility: true}
		_, err := v.ValidateUpdate(context.Background(), oldRBGS, newRBGS)
		assert.Error(t, err)
	})

	t.Run("compatibility enabled, update to legacy workload - no error", func(t *testing.T) {
		newRBGS := &RoleBasedGroupSet{
			ObjectMeta: metav1.ObjectMeta{Name: "test-rbgs"},
			Spec: RoleBasedGroupSetSpec{
				Replicas: ptr.To(int32(1)),
				GroupTemplate: RoleBasedGroupTemplateSpec{
					Spec: RoleBasedGroupSpec{
						Roles: []RoleSpec{
							{
								Name:        "worker",
								Replicas:    ptr.To(int32(1)),
								Annotations: map[string]string{constants.RoleWorkloadTypeAnnotationKey: constants.LeaderWorkerSetWorkloadType},
							},
						},
					},
				},
			},
		}
		v := &RoleBasedGroupSetValidator{DisableV1alpha1Compatibility: false}
		_, err := v.ValidateUpdate(context.Background(), oldRBGS, newRBGS)
		assert.NoError(t, err)
	})
}
