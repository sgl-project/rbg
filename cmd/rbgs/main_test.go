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

package main

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	lwsv1 "sigs.k8s.io/lws/api/leaderworkerset/v1"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	schev1alpha1 "sigs.k8s.io/scheduler-plugins/apis/scheduling/v1alpha1"
)

func TestInitFunction(t *testing.T) {
	// Test that schemes registered in init function
	assert.NotNil(t, scheme)

	// Verify that various schemes are added correctly
	assert.NoError(t, clientgoscheme.AddToScheme(scheme))
	assert.NoError(t, lwsv1.AddToScheme(scheme))
	assert.NoError(t, schev1alpha1.AddToScheme(scheme))
	assert.NoError(t, workloadsv1alpha2.AddToScheme(scheme))
}

func TestPrintVersion(t *testing.T) {
	// printVersion only prints information, here we simply verify it doesn't panic
	assert.NotPanics(
		t, func() {
			printVersion()
		},
	)
}

func TestMainSchemeRegistration(t *testing.T) {
	// Verify that all required schemes are properly registered
	testClient := fake.NewClientBuilder().WithScheme(scheme).Build()

	// Verify that various types of objects can be created
	rbg := &workloadsv1alpha2.RoleBasedGroup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-rbg",
			Namespace: "default",
		},
	}

	err := testClient.Create(context.Background(), rbg)
	assert.NoError(t, err)

	lws := &lwsv1.LeaderWorkerSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-lws",
			Namespace: "default",
		},
	}

	err = testClient.Create(context.Background(), lws)
	assert.NoError(t, err)

	// Verify that the scheme includes core types
	assert.True(t, scheme.Recognizes(corev1.SchemeGroupVersion.WithKind("Pod")))
	assert.True(t, scheme.Recognizes(appsv1.SchemeGroupVersion.WithKind("StatefulSet")))
	assert.True(t, scheme.Recognizes(workloadsv1alpha2.GroupVersion.WithKind("RoleBasedGroup")))
}
