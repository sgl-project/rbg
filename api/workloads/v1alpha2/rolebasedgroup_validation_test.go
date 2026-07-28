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
	"testing"

	"github.com/stretchr/testify/assert"
	"k8s.io/utils/ptr"

	"sigs.k8s.io/rbgs/api/workloads/constants"
)

func roleWithWorkloadType(name, wt string) RoleSpec {
	annotations := map[string]string{}
	if wt != "" {
		annotations[constants.RoleWorkloadTypeAnnotationKey] = wt
	}
	return RoleSpec{
		Name:        name,
		Annotations: annotations,
		Replicas:    ptr.To[int32](1),
	}
}

func TestValidateWorkloadTypes(t *testing.T) {
	tests := []struct {
		name    string
		rbg     *RoleBasedGroup
		wantErr bool
	}{
		{
			name: "only RoleInstanceSet roles - no error",
			rbg: &RoleBasedGroup{
				Spec: RoleBasedGroupSpec{
					Roles: []RoleSpec{
						roleWithWorkloadType("worker", constants.RoleInstanceSetWorkloadType),
					},
				},
			},
			wantErr: false,
		},
		{
			name: "Deployment role - error",
			rbg: &RoleBasedGroup{
				Spec: RoleBasedGroupSpec{
					Roles: []RoleSpec{
						roleWithWorkloadType("worker", constants.DeploymentWorkloadType),
					},
				},
			},
			wantErr: true,
		},
		{
			name: "StatefulSet role - error",
			rbg: &RoleBasedGroup{
				Spec: RoleBasedGroupSpec{
					Roles: []RoleSpec{
						roleWithWorkloadType("worker", constants.StatefulSetWorkloadType),
					},
				},
			},
			wantErr: true,
		},
		{
			name: "LeaderWorkerSet role - error",
			rbg: &RoleBasedGroup{
				Spec: RoleBasedGroupSpec{
					Roles: []RoleSpec{
						roleWithWorkloadType("worker", constants.LeaderWorkerSetWorkloadType),
					},
				},
			},
			wantErr: true,
		},
		{
			name: "mixed RoleInstanceSet and Deployment - error",
			rbg: &RoleBasedGroup{
				Spec: RoleBasedGroupSpec{
					Roles: []RoleSpec{
						roleWithWorkloadType("ris", constants.RoleInstanceSetWorkloadType),
						roleWithWorkloadType("deploy", constants.DeploymentWorkloadType),
					},
				},
			},
			wantErr: true,
		},
		{
			name: "no annotation defaults to RoleInstanceSet - no error",
			rbg: &RoleBasedGroup{
				Spec: RoleBasedGroupSpec{
					Roles: []RoleSpec{
						roleWithWorkloadType("worker", ""),
					},
				},
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateWorkloadTypes(tt.rbg)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestValidateWorkloadTypesUpdate(t *testing.T) {
	tests := []struct {
		name    string
		oldRBG  *RoleBasedGroup
		newRBG  *RoleBasedGroup
		wantErr bool
	}{
		{
			name: "existing legacy role unchanged - no error",
			oldRBG: &RoleBasedGroup{
				Spec: RoleBasedGroupSpec{
					Roles: []RoleSpec{
						roleWithWorkloadType("worker", constants.DeploymentWorkloadType),
					},
				},
			},
			newRBG: &RoleBasedGroup{
				Spec: RoleBasedGroupSpec{
					Roles: []RoleSpec{
						roleWithWorkloadType("worker", constants.DeploymentWorkloadType),
					},
				},
			},
			wantErr: false,
		},
		{
			name: "migration from legacy to RoleInstanceSet - no error",
			oldRBG: &RoleBasedGroup{
				Spec: RoleBasedGroupSpec{
					Roles: []RoleSpec{
						roleWithWorkloadType("worker", constants.DeploymentWorkloadType),
					},
				},
			},
			newRBG: &RoleBasedGroup{
				Spec: RoleBasedGroupSpec{
					Roles: []RoleSpec{
						roleWithWorkloadType("worker", constants.RoleInstanceSetWorkloadType),
					},
				},
			},
			wantErr: false,
		},
		{
			name: "new legacy role introduced - error",
			oldRBG: &RoleBasedGroup{
				Spec: RoleBasedGroupSpec{
					Roles: []RoleSpec{
						roleWithWorkloadType("worker", constants.RoleInstanceSetWorkloadType),
					},
				},
			},
			newRBG: &RoleBasedGroup{
				Spec: RoleBasedGroupSpec{
					Roles: []RoleSpec{
						roleWithWorkloadType("worker", constants.RoleInstanceSetWorkloadType),
						roleWithWorkloadType("deploy", constants.DeploymentWorkloadType),
					},
				},
			},
			wantErr: true,
		},
		{
			name: "no legacy in either - no error",
			oldRBG: &RoleBasedGroup{
				Spec: RoleBasedGroupSpec{
					Roles: []RoleSpec{
						roleWithWorkloadType("worker", constants.RoleInstanceSetWorkloadType),
					},
				},
			},
			newRBG: &RoleBasedGroup{
				Spec: RoleBasedGroupSpec{
					Roles: []RoleSpec{
						roleWithWorkloadType("worker", constants.RoleInstanceSetWorkloadType),
					},
				},
			},
			wantErr: false,
		},
		{
			name: "existing legacy role renamed - error (new role name)",
			oldRBG: &RoleBasedGroup{
				Spec: RoleBasedGroupSpec{
					Roles: []RoleSpec{
						roleWithWorkloadType("old-name", constants.DeploymentWorkloadType),
					},
				},
			},
			newRBG: &RoleBasedGroup{
				Spec: RoleBasedGroupSpec{
					Roles: []RoleSpec{
						roleWithWorkloadType("new-name", constants.DeploymentWorkloadType),
					},
				},
			},
			wantErr: true,
		},
		{
			name: "existing StatefulSet plus new Deployment - error",
			oldRBG: &RoleBasedGroup{
				Spec: RoleBasedGroupSpec{
					Roles: []RoleSpec{
						roleWithWorkloadType("sts", constants.StatefulSetWorkloadType),
					},
				},
			},
			newRBG: &RoleBasedGroup{
				Spec: RoleBasedGroupSpec{
					Roles: []RoleSpec{
						roleWithWorkloadType("sts", constants.StatefulSetWorkloadType),
						roleWithWorkloadType("deploy", constants.DeploymentWorkloadType),
					},
				},
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateWorkloadTypesUpdate(tt.oldRBG, tt.newRBG)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}
