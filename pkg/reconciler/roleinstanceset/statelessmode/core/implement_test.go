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

package core

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
)

func TestGenInstanceFromTemplateStripsUpdatePolicy(t *testing.T) {
	// spec.updatePolicy is a stateful-mode, controller-owned snapshot. The
	// RoleInstanceSpec is embedded in the user-editable RoleInstanceSet
	// template, so a directly written value must not leak into a stateless
	// RoleInstance and change the shared update path's behavior.
	template := &workloadsv1alpha2.RoleInstanceTemplate{
		RoleInstanceSpec: workloadsv1alpha2.RoleInstanceSpec{
			UpdatePolicy: &workloadsv1alpha2.RoleInstanceUpdatePolicy{
				Type: workloadsv1alpha2.RoleInstanceUpdatePolicyRecreatePod,
			},
			Components: []workloadsv1alpha2.RoleInstanceComponent{{
				Name: "main",
				Size: ptr.To(int32(1)),
			}},
		},
	}
	set := &workloadsv1alpha2.RoleInstanceSet{
		ObjectMeta: metav1.ObjectMeta{Name: "stateless", Namespace: "default"},
	}

	instance, err := GenInstanceFromTemplate(template, set, nil)
	if err != nil {
		t.Fatalf("GenInstanceFromTemplate() error = %v", err)
	}
	if instance.Spec.UpdatePolicy != nil {
		t.Fatalf("stateless instance must not carry a template updatePolicy, got %+v", instance.Spec.UpdatePolicy)
	}
}
