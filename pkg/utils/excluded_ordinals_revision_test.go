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

package utils

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
	apps "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/rbgs/api/workloads/constants"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
)

func TestExclusionsDoNotChangeRevision(t *testing.T) {
	base := &workloadsv1alpha2.RoleBasedGroup{ObjectMeta: metav1.ObjectMeta{Name: "group"}, Spec: workloadsv1alpha2.RoleBasedGroupSpec{Roles: []workloadsv1alpha2.RoleSpec{{Name: "worker", Replicas: ptr.To[int32](3)}}}}
	patch, err := getRBGPatch(base)
	require.NoError(t, err)
	hashes, err := GetRolesRevisionHash(&apps.ControllerRevision{Data: runtime.RawExtension{Raw: patch}})
	require.NoError(t, err)
	for _, value := range []string{"1", "1,3", "", "0-2147483647"} {
		changed := base.DeepCopy()
		changed.Spec.Roles[0].Annotations = map[string]string{constants.ExcludedOrdinalsAnnotationKey: value}
		got, err := getRBGPatch(changed)
		require.NoError(t, err)
		require.Equal(t, string(patch), string(got))
		gotHashes, err := GetRolesRevisionHash(&apps.ControllerRevision{Data: runtime.RawExtension{Raw: got}})
		require.NoError(t, err)
		require.Equal(t, hashes, gotHashes)
	}
	// Existing non-operational annotations still affect the template revision.
	changed := base.DeepCopy()
	changed.Spec.Roles[0].Annotations = map[string]string{"example.org/template": "changed"}
	got, err := getRBGPatch(changed)
	require.NoError(t, err)
	require.NotEqual(t, string(patch), string(got))
}

func TestRollbackPreservesCurrentExclusionsIncludingAbsence(t *testing.T) {
	for _, present := range []bool{true, false} {
		t.Run(map[bool]string{true: "present", false: "absent"}[present], func(t *testing.T) {
			current := &workloadsv1alpha2.RoleBasedGroup{Spec: workloadsv1alpha2.RoleBasedGroupSpec{Roles: []workloadsv1alpha2.RoleSpec{{Name: "worker", Replicas: ptr.To[int32](3)}}}}
			if present {
				current.Spec.Roles[0].Annotations = map[string]string{constants.ExcludedOrdinalsAnnotationKey: "2"}
			}
			// Include a historical operational value to verify rollback isolation even
			// for revisions stored before the feature was implemented.
			data, err := json.Marshal(map[string]interface{}{"spec": map[string]interface{}{"roles": []interface{}{map[string]string{"$patch": "replace"}, map[string]interface{}{"name": "worker", "annotations": map[string]string{constants.ExcludedOrdinalsAnnotationKey: "1", "example.org/template": "old"}}}}})
			require.NoError(t, err)
			restored, err := ApplyRevision(current, &apps.ControllerRevision{Data: runtime.RawExtension{Raw: data}})
			require.NoError(t, err)
			got, ok := restored.Spec.Roles[0].Annotations[constants.ExcludedOrdinalsAnnotationKey]
			require.Equal(t, present, ok)
			if present {
				require.Equal(t, "2", got)
			}
			require.Equal(t, int32(3), *restored.Spec.Roles[0].Replicas)
			require.Equal(t, "old", restored.Spec.Roles[0].Annotations["example.org/template"])
		})
	}
}
