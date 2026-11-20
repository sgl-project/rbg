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
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"sigs.k8s.io/rbgs/api/workloads/constants"
	"sigs.k8s.io/rbgs/pkg/features"
)

func TestRoleInstanceSetValidatorRejectsSignalKeyReassignment(t *testing.T) {
	features.SetRoleInstanceLifecycleHooks(true)
	t.Cleanup(func() { features.SetRoleInstanceLifecycleHooks(false) })

	scheme := runtime.NewScheme()
	if err := AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	oldSet := &RoleInstanceSet{
		ObjectMeta: metav1.ObjectMeta{Name: "set", Namespace: "default", UID: "set-uid"},
		Spec: RoleInstanceSetSpec{
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "rbg"}},
			RoleInstanceTemplate: RoleInstanceTemplate{
				RoleInstanceSpec: RoleInstanceSpec{
					Lifecycle: &RoleInstanceLifecycle{
						PreUpdate: &RoleInstanceLifecycleHook{
							SignalLabels: map[string]string{"example.com/signal": "true"},
						},
					},
				},
			},
		},
	}
	instance := &RoleInstance{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "instance",
			Namespace: "default",
			UID:       "instance-uid",
			Labels:    map[string]string{"app": "rbg"},
			Annotations: map[string]string{
				constants.RoleInstanceLifecycleHookSignalLabelKeys: "example.com/signal",
			},
			OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(
				oldSet, GroupVersion.WithKind("RoleInstanceSet"))},
		},
	}
	newSet := oldSet.DeepCopy()
	newSet.Spec.RoleInstanceTemplate.Lifecycle = &RoleInstanceLifecycle{
		PreDelete: &RoleInstanceLifecycleHook{
			SignalLabels: map[string]string{"example.com/signal": "true"},
		},
	}

	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(instance).Build()
	validator := &RoleInstanceSetValidator{Client: c}
	_, err := validator.ValidateUpdate(context.Background(), oldSet, newSet)
	if err == nil || !strings.Contains(err.Error(), "cannot be reassigned") {
		t.Fatalf("ValidateUpdate() error = %v, want signal-key reassignment rejection", err)
	}
}
