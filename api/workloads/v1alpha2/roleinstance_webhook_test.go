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
	"encoding/json"
	"strings"
	"testing"

	admissionv1 "k8s.io/api/admission/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

func deleteContext(t *testing.T, options *metav1.DeleteOptions) context.Context {
	t.Helper()
	raw := []byte{}
	if options != nil {
		encoded, err := json.Marshal(options)
		if err != nil {
			t.Fatal(err)
		}
		raw = encoded
	}
	request := admission.Request{
		AdmissionRequest: admissionv1.AdmissionRequest{
			Operation: admissionv1.Delete,
			Options:   runtime.RawExtension{Raw: raw},
		},
	}
	return admission.NewContextWithRequest(context.Background(), request)
}

func armedInstance() *RoleInstance {
	return &RoleInstance{
		ObjectMeta: metav1.ObjectMeta{Name: "instance", Namespace: "default"},
		Spec: RoleInstanceSpec{
			Lifecycle: &RoleInstanceLifecycle{
				PreDelete: &RoleInstanceLifecycleHook{
					SignalLabels: map[string]string{"consumer.example/drain": "true"},
				},
			},
		},
	}
}

func TestRoleInstanceValidatorRejectsForegroundAndOrphan(t *testing.T) {
	foreground := metav1.DeletePropagationForeground
	orphan := metav1.DeletePropagationOrphan
	deprecatedOrphan := true
	tests := []struct {
		name    string
		options *metav1.DeleteOptions
		wantErr string
	}{
		{
			name:    "foreground rejected",
			options: &metav1.DeleteOptions{PropagationPolicy: &foreground},
			wantErr: "foreground propagation",
		},
		{
			name:    "orphan rejected",
			options: &metav1.DeleteOptions{PropagationPolicy: &orphan},
			wantErr: "orphan propagation",
		},
		{
			name:    "deprecated orphanDependents rejected",
			options: &metav1.DeleteOptions{OrphanDependents: &deprecatedOrphan}, //nolint:staticcheck // old clients still send the deprecated field; it must be rejected
			wantErr: "orphan propagation",
		},
		{
			name:    "background allowed",
			options: &metav1.DeleteOptions{PropagationPolicy: ptrDelete(metav1.DeletePropagationBackground)},
		},
		{
			name:    "default allowed",
			options: &metav1.DeleteOptions{},
		},
	}
	validator := &RoleInstanceValidator{}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := validator.ValidateDelete(deleteContext(t, test.options), armedInstance())
			if test.wantErr == "" {
				if err != nil {
					t.Fatalf("ValidateDelete() error = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("ValidateDelete() error = %v, want containing %q", err, test.wantErr)
			}
		})
	}
}

func TestRoleInstanceValidatorAllowsUnarmedInstances(t *testing.T) {
	foreground := metav1.DeletePropagationForeground
	instance := &RoleInstance{ObjectMeta: metav1.ObjectMeta{Name: "instance", Namespace: "default"}}
	validator := &RoleInstanceValidator{}
	_, err := validator.ValidateDelete(
		deleteContext(t, &metav1.DeleteOptions{PropagationPolicy: &foreground}), instance)
	if err != nil {
		t.Fatalf("ValidateDelete() error = %v, want nil for an instance without preDelete", err)
	}
}

func ptrDelete(policy metav1.DeletionPropagation) *metav1.DeletionPropagation {
	return &policy
}
