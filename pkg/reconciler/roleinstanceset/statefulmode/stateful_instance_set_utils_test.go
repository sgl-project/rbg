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

package statefulmode

import (
	"strconv"
	"testing"

	apps "k8s.io/api/apps/v1"

	"sigs.k8s.io/rbgs/api/workloads/constants"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
)

// TestNewVersionedInstanceIdentityIsPerOrdinal covers the instances of one set being
// built in a single reconcile: each must carry its own ordinal in its pod template,
// and the set's template must come out of it with no identity at all.
//
// Every instance is built before anything is asserted. Checking one right after
// constructing it would pass even on a shared template, which is only clobbered once
// the next ordinal is built.
func TestNewVersionedInstanceIdentityIsPerOrdinal(t *testing.T) {
	set := newRevisionTestSet("nginx:1.0")

	const replicas = 2
	instances := make([]*workloadsv1alpha2.RoleInstance, replicas)
	for ordinal := range instances {
		instances[ordinal] = newVersionedInstance(set, set, "current-rev", "update-rev", ordinal, nil)
	}

	for ordinal, instance := range instances {
		labels := instance.Spec.Components[0].Template.Labels

		wantName := "test-set-" + strconv.Itoa(ordinal)
		if got := labels[apps.StatefulSetPodNameLabel]; got != wantName {
			t.Errorf("ordinal %d: pod template %s is %q, expected %q",
				ordinal, apps.StatefulSetPodNameLabel, got, wantName)
		}
		wantIndex := strconv.Itoa(ordinal)
		if got := labels[constants.RoleInstanceIndexLabelKey]; got != wantIndex {
			t.Errorf("ordinal %d: pod template %s is %q, expected %q",
				ordinal, constants.RoleInstanceIndexLabelKey, got, wantIndex)
		}
	}

	setLabels := set.Spec.RoleInstanceTemplate.Components[0].Template.Labels
	for _, key := range []string{apps.StatefulSetPodNameLabel, constants.RoleInstanceIndexLabelKey} {
		if value, found := setLabels[key]; found {
			t.Errorf("the set's shared template was given %s=%q, which belongs to one ordinal only",
				key, value)
		}
	}
}
