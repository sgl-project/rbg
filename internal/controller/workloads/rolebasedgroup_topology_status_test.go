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
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	"sigs.k8s.io/rbgs/pkg/scheduler"
	gangcommon "sigs.k8s.io/rbgs/pkg/scheduler/common"
)

func TestSetPlacementConditionsRecordsClassifiedResolutionFailure(t *testing.T) {
	client, r := topologyStatusTestClient(t, &workloadsv1alpha2.RoleBasedGroup{
		ObjectMeta: metav1.ObjectMeta{Name: "rbg", Namespace: "default"},
	})
	rbg := &workloadsv1alpha2.RoleBasedGroup{
		ObjectMeta: metav1.ObjectMeta{Name: "rbg", Namespace: "default"},
	}
	cause := gangcommon.NewTopologyTranslationError("unknown topology role")

	if err := r.setPlacementConditions(context.Background(), rbg, nil, cause, nil); err != nil {
		t.Fatal(err)
	}

	updated := &workloadsv1alpha2.RoleBasedGroup{}
	if err := client.Get(context.Background(), types.NamespacedName{Name: rbg.Name, Namespace: rbg.Namespace}, updated); err != nil {
		t.Fatal(err)
	}
	topologyTranslated := findCondition(updated.Status.Conditions, string(workloadsv1alpha2.RoleBasedGroupTopologyTranslated))
	if topologyTranslated == nil || topologyTranslated.Status != metav1.ConditionFalse {
		t.Fatalf("expected TopologyTranslated=False, got %#v", topologyTranslated)
	}
}

func TestSetPlacementConditionsDoesNotExposeTopologyForGangOnlyFailure(t *testing.T) {
	_, r := topologyStatusTestClient(t, &workloadsv1alpha2.RoleBasedGroup{
		ObjectMeta: metav1.ObjectMeta{Name: "rbg", Namespace: "default"},
		Status: workloadsv1alpha2.RoleBasedGroupStatus{Conditions: []metav1.Condition{{
			Type:               string(workloadsv1alpha2.RoleBasedGroupTopologyTranslated),
			Status:             metav1.ConditionTrue,
			LastTransitionTime: metav1.Now(),
			Reason:             "TopologyTranslated",
			Message:            "stale",
		}}},
	})
	rbg := &workloadsv1alpha2.RoleBasedGroup{
		ObjectMeta: metav1.ObjectMeta{Name: "rbg", Namespace: "default"},
		Status: workloadsv1alpha2.RoleBasedGroupStatus{Conditions: []metav1.Condition{{
			Type:               string(workloadsv1alpha2.RoleBasedGroupTopologyTranslated),
			Status:             metav1.ConditionTrue,
			LastTransitionTime: metav1.Now(),
			Reason:             "TopologyTranslated",
			Message:            "stale",
		}}},
	}
	cause := gangcommon.NewIncompatibleGangConfigError("gang configuration cannot be satisfied")

	if err := r.setPlacementConditions(context.Background(), rbg, nil, cause, nil); err != nil {
		t.Fatal(err)
	}

	if findCondition(rbg.Status.Conditions, string(workloadsv1alpha2.RoleBasedGroupTopologyTranslated)) != nil {
		t.Fatal("expected no topology condition for a gang-only failure")
	}
}

func TestSetPlacementConditionsUsesPreferredAbsorbedReason(t *testing.T) {
	client, r := topologyStatusTestClient(t, &workloadsv1alpha2.RoleBasedGroup{
		ObjectMeta: metav1.ObjectMeta{Name: "rbg", Namespace: "default"},
	})
	rbg := &workloadsv1alpha2.RoleBasedGroup{
		ObjectMeta: metav1.ObjectMeta{Name: "rbg", Namespace: "default"},
	}

	if err := r.setPlacementConditions(
		context.Background(), rbg, &gangcommon.PlacementPlan{Root: &gangcommon.PlacementGroup{
			Topology: &workloadsv1alpha2.TopologyConstraint{
				Pack: &workloadsv1alpha2.TopologyPackConstraint{Required: ptr.To("rack")},
			},
		}}, nil, &scheduler.PlacementRenderResult{PreferredAbsorbed: true}); err != nil {
		t.Fatal(err)
	}

	updated := &workloadsv1alpha2.RoleBasedGroup{}
	if err := client.Get(context.Background(), types.NamespacedName{Name: rbg.Name, Namespace: rbg.Namespace}, updated); err != nil {
		t.Fatal(err)
	}
	condition := findCondition(updated.Status.Conditions, string(workloadsv1alpha2.RoleBasedGroupTopologyTranslated))
	if condition == nil || condition.Status != metav1.ConditionTrue || condition.Reason != PreferredAbsorbed {
		t.Fatalf("expected PreferredAbsorbed reason, got %#v", condition)
	}
}

func topologyStatusTestClient(
	t *testing.T,
	rbg *workloadsv1alpha2.RoleBasedGroup,
) (client.Client, *RoleBasedGroupReconciler) {
	t.Helper()
	testScheme := runtime.NewScheme()
	if err := workloadsv1alpha2.AddToScheme(testScheme); err != nil {
		t.Fatal(err)
	}
	if err := scheme.AddToScheme(testScheme); err != nil {
		t.Fatal(err)
	}
	client := fake.NewClientBuilder().
		WithScheme(testScheme).
		WithObjects(rbg).
		WithStatusSubresource(&workloadsv1alpha2.RoleBasedGroup{}).
		Build()
	return client, &RoleBasedGroupReconciler{
		client:   client,
		recorder: record.NewFakeRecorder(10),
	}
}

func findCondition(conditions []metav1.Condition, conditionType string) *metav1.Condition {
	for i := range conditions {
		if conditions[i].Type == conditionType {
			return &conditions[i]
		}
	}
	return nil
}
