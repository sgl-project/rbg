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
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	"sigs.k8s.io/rbgs/api/workloads/constants"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
)

func TestGetSelector(t *testing.T) {
	instance := &workloadsv1alpha2.RoleInstance{
		ObjectMeta: metav1.ObjectMeta{
			Name: "shared-service-selection-decode-0",
		},
	}

	selector := GetSelector(instance)
	if selector.Empty() {
		t.Fatal("expected a non-empty selector")
	}

	expected := constants.RoleInstanceNameLabelKey + "=" + instance.Name
	if got := selector.String(); got != expected {
		t.Fatalf("expected selector %q, got %q", expected, got)
	}

	if !selector.Matches(labels.Set{constants.RoleInstanceNameLabelKey: instance.Name}) {
		t.Fatal("expected selector to match its own RoleInstance label")
	}
	if selector.Matches(labels.Set{constants.RoleInstanceNameLabelKey: "another-instance"}) {
		t.Fatal("expected selector not to match another RoleInstance")
	}
	if selector.Matches(labels.Set{}) {
		t.Fatal("expected selector not to match objects without the RoleInstance label")
	}
}
