/*
Copyright 2021 The Kruise Authors.

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

package readiness

import (
	"context"
	"testing"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	appspub "sigs.k8s.io/rbgs/api/workloads/v1alpha1"
)

func TestReadiness(t *testing.T) {
	instance0 := &appspub.Instance{
		ObjectMeta: metav1.ObjectMeta{Namespace: metav1.NamespaceDefault, Name: "instance-0"},
		Spec: appspub.InstanceSpec{
			ReadinessGates: []appspub.InstanceReadinessGate{{ConditionType: appspub.InstanceCustomReady}},
		},
	}

	instance1 := &appspub.Instance{
		ObjectMeta: metav1.ObjectMeta{Namespace: metav1.NamespaceDefault, Name: "instance-1"},
		Spec: appspub.InstanceSpec{
			ReadinessGates: []appspub.InstanceReadinessGate{},
		},
	}

	_ = appspub.AddToScheme(clientgoscheme.Scheme)
	fakeClient := fake.NewClientBuilder().
		WithScheme(clientgoscheme.Scheme).
		WithObjects(instance0, instance1).
		WithStatusSubresource(instance0, instance1).
		Build()

	msg0 := Message{UserAgent: "ua1", Key: "foo"}
	msg1 := Message{UserAgent: "ua1", Key: "bar"}

	controller := New(fakeClient)
	AddNotReadyKey := controller.AddNotReadyKey
	RemoveNotReadyKey := controller.RemoveNotReadyKey

	if err := AddNotReadyKey(instance0, msg0); err != nil {
		t.Fatal(err)
	}
	if err := AddNotReadyKey(instance0, msg1); err != nil {
		t.Fatal(err)
	}
	if err := AddNotReadyKey(instance1, msg0); err != nil {
		t.Fatal(err)
	}
	if err := AddNotReadyKey(instance1, msg1); err != nil {
		t.Fatal(err)
	}

	newInstance0 := &appspub.Instance{}
	if err := fakeClient.Get(context.TODO(), types.NamespacedName{Namespace: instance0.Namespace, Name: instance0.Name}, newInstance0); err != nil {
		t.Fatal(err)
	}
	if !alreadyHasKey(newInstance0, msg0, appspub.InstanceCustomReady) || !alreadyHasKey(newInstance0, msg1, appspub.InstanceCustomReady) {
		t.Fatalf("expect already has key, but not")
	}
	condition := GetReadinessCondition(newInstance0)
	if condition.Status != v1.ConditionFalse {
		t.Fatalf("expect condition false, but not")
	}

	newInstance1 := &appspub.Instance{}
	if err := fakeClient.Get(context.TODO(), types.NamespacedName{Namespace: instance1.Namespace, Name: instance1.Name}, newInstance1); err != nil {
		t.Fatal(err)
	}
	if alreadyHasKey(newInstance1, msg0, appspub.InstanceCustomReady) || alreadyHasKey(newInstance1, msg1, appspub.InstanceCustomReady) {
		t.Fatalf("expect not have key, but it does")
	}
	if condition = GetReadinessCondition(newInstance1); condition != nil {
		t.Fatalf("expect condition nil, but exists: %v", condition)
	}

	if err := RemoveNotReadyKey(newInstance0, msg0); err != nil {
		t.Fatal(err)
	}
	newInstance0 = &appspub.Instance{}
	if err := fakeClient.Get(context.TODO(), types.NamespacedName{Namespace: instance0.Namespace, Name: instance0.Name}, newInstance0); err != nil {
		t.Fatal(err)
	}
	if !alreadyHasKey(newInstance0, msg1, appspub.InstanceCustomReady) {
		t.Fatalf("expect already has key, but not")
	}
	if alreadyHasKey(newInstance0, msg0, appspub.InstanceCustomReady) {
		t.Fatalf("expect not have key, but it does")
	}
	condition = GetReadinessCondition(newInstance0)
	if condition.Status != v1.ConditionFalse {
		t.Fatalf("expect condition false, but not")
	}

	if err := RemoveNotReadyKey(newInstance0, msg1); err != nil {
		t.Fatal(err)
	}
	newInstance0 = &appspub.Instance{}
	if err := fakeClient.Get(context.TODO(), types.NamespacedName{Namespace: instance0.Namespace, Name: instance0.Name}, newInstance0); err != nil {
		t.Fatal(err)
	}
	if alreadyHasKey(newInstance0, msg0, appspub.InstanceCustomReady) || alreadyHasKey(newInstance0, msg1, appspub.InstanceCustomReady) {
		t.Fatalf("expect not have key, but it does")
	}
	condition = GetReadinessCondition(newInstance0)
	if condition.Status != v1.ConditionTrue {
		t.Fatalf("expect condition true, but not")
	}
}
