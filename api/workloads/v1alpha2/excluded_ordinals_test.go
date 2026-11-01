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

	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/rbgs/api/workloads/constants"
)

func TestExcludedOrdinalsAdmission(t *testing.T) {
	for _, tc := range []struct {
		name, value, pattern, backend string
		managed, internal, bad        bool
	}{
		{name: "native stateful", value: "1,3,8-10"},
		{name: "empty input"},
		{name: "normalized ranges", value: "2-4,1,2,3-8"},
		{name: "negative", value: "-1", bad: true},
		{name: "empty entry", value: "1,,2", bad: true},
		{name: "reversed", value: "3-1", bad: true},
		{name: "overflow", value: "2147483648", bad: true},
		{name: "stateless", pattern: string(constants.StatelessPattern), value: "1", bad: true},
		{name: "non RIS", backend: constants.StatefulSetWorkloadType, value: "1", bad: true},
		{name: "RBGSet child", managed: true, value: "1", bad: true},
		{name: "internal key", internal: true, bad: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			role := RoleSpec{Name: "worker", Annotations: map[string]string{constants.ExcludedOrdinalsAnnotationKey: tc.value}}
			if tc.pattern != "" {
				role.Annotations[constants.RoleInstancePatternKey] = tc.pattern
			}
			if tc.backend != "" {
				role.Annotations[constants.RoleWorkloadTypeAnnotationKey] = tc.backend
			}
			if tc.internal {
				role.Annotations[constants.CommittedSkipOrdinalsAnnotationKey] = "1"
			}
			rbg := &RoleBasedGroup{ObjectMeta: metav1.ObjectMeta{Name: "group"}, Spec: RoleBasedGroupSpec{Roles: []RoleSpec{role}}}
			if tc.managed {
				rbg.OwnerReferences = []metav1.OwnerReference{{APIVersion: GroupVersion.String(), Kind: "RoleBasedGroupSet", Name: "set"}}
			}
			validator := &RoleBasedGroupValidator{EnableDeprecatedWorkloadTypes: true, Client: fake.NewClientBuilder().Build()}
			_, err := validator.ValidateCreate(context.Background(), rbg)
			if tc.bad {
				require.True(t, apierrors.IsInvalid(err), "%v", err)
				require.Contains(t, err.Error(), "spec.roles[0].annotations")
			} else {
				require.NoError(t, err)
			}
			_, err = validator.ValidateUpdate(context.Background(), rbg, rbg.DeepCopy())
			if tc.bad {
				require.True(t, apierrors.IsInvalid(err), "%v", err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestRBGSetExcludedOrdinalsAdmission(t *testing.T) {
	rbgs := &RoleBasedGroupSet{ObjectMeta: metav1.ObjectMeta{Name: "set"}, Spec: RoleBasedGroupSetSpec{GroupTemplate: RoleBasedGroupTemplateSpec{Spec: RoleBasedGroupSpec{Roles: []RoleSpec{{Name: "worker", Annotations: map[string]string{constants.ExcludedOrdinalsAnnotationKey: ""}}}}}}}
	validator := &RoleBasedGroupSetValidator{}
	_, err := validator.ValidateCreate(context.Background(), rbgs)
	require.True(t, apierrors.IsInvalid(err))
	require.Contains(t, err.Error(), "spec.groupTemplate.spec.roles[0]")
	_, err = validator.ValidateUpdate(context.Background(), rbgs, rbgs.DeepCopy())
	require.True(t, apierrors.IsInvalid(err))
	delete(rbgs.Spec.GroupTemplate.Spec.Roles[0].Annotations, constants.ExcludedOrdinalsAnnotationKey)
	_, err = validator.ValidateCreate(context.Background(), rbgs)
	require.NoError(t, err)
}
