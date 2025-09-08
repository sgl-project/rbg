package utils

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	lwsv1 "sigs.k8s.io/lws/api/leaderworkerset/v1"
	workloadsv1alpha1 "sigs.k8s.io/rbgs/api/workloads/v1alpha1"
)

func TestCheckOwnerReference(t *testing.T) {
	targetGVK := schema.GroupVersionKind{
		Group:   "workloads.x-k8s.io",
		Version: "v1alpha1",
		Kind:    "RoleBasedGroup",
	}

	tests := []struct {
		name        string
		refs        []metav1.OwnerReference
		targetGVK   schema.GroupVersionKind // Optional override
		expected    bool
		description string
	}{
		// --------------------------
		// Basic Scenarios
		// --------------------------
		{
			name:        "empty owner references",
			refs:        []metav1.OwnerReference{},
			expected:    false,
			description: "Should return false for empty list",
		},
		{
			name: "no matching references",
			refs: []metav1.OwnerReference{
				{
					APIVersion: "apps/v1",
					Kind:       "Deployment",
				},
			},
			expected:    false,
			description: "Should ignore completely unrelated OwnerReferences",
		},
		{
			name: "exact match",
			refs: []metav1.OwnerReference{
				{
					APIVersion: "workloads.x-k8s.io/v1alpha1",
					Kind:       "RoleBasedGroup",
				},
			},
			expected:    true,
			description: "Should return true for exact GVK match",
		},

		// --------------------------
		// Partial Match Scenarios
		// --------------------------
		{
			name: "group mismatch",
			refs: []metav1.OwnerReference{
				{
					APIVersion: "wrong-group/v1alpha1",
					Kind:       "RoleBasedGroup",
				},
			},
			expected:    false,
			description: "Should fail when Group mismatches",
		},
		{
			name: "version mismatch",
			refs: []metav1.OwnerReference{
				{
					APIVersion: "workloads.x-k8s.io/v1beta1",
					Kind:       "RoleBasedGroup",
				},
			},
			expected:    false,
			description: "Should fail when Version mismatches",
		},
		{
			name: "kind mismatch",
			refs: []metav1.OwnerReference{
				{
					APIVersion: "workloads.x-k8s.io/v1alpha1",
					Kind:       "WrongKind",
				},
			},
			expected:    false,
			description: "Should fail when Kind mismatches",
		},

		// --------------------------
		// Special Format Handling
		// --------------------------
		{
			name: "core group (no group)",
			refs: []metav1.OwnerReference{
				{
					APIVersion: "v1", // Core group (Group="")
					Kind:       "Pod",
				},
			},
			targetGVK: schema.GroupVersionKind{
				Group:   "",
				Version: "v1",
				Kind:    "Pod",
			},
			expected:    true,
			description: "Should handle core group (empty Group) correctly",
		},
		{
			name: "invalid apiVersion format",
			refs: []metav1.OwnerReference{
				{
					APIVersion: "workloads.x-k8s.io/v1alpha1/extra",
					Kind:       "RoleBasedGroup",
				},
			},
			expected:    false,
			description: "Should skip invalid APIVersion formats",
		},
		{
			name: "case-sensitive check",
			refs: []metav1.OwnerReference{
				{
					APIVersion: "Workloads.X-K8S.Io/v1alpha1", // Case mismatch
					Kind:       "RoleBasedGroup",
				},
			},
			expected:    false,
			description: "GVK matching should be case-sensitive",
		},

		// --------------------------
		// Multiple Entries Scenarios
		// --------------------------
		{
			name: "multiple refs with match",
			refs: []metav1.OwnerReference{
				{
					APIVersion: "apps/v1",
					Kind:       "Deployment",
				},
				{
					APIVersion: "workloads.x-k8s.io/v1alpha1",
					Kind:       "RoleBasedGroup", // Match here
				},
			},
			expected:    true,
			description: "Should find match in multiple OwnerReferences",
		},
		{
			name: "multiple refs without match",
			refs: []metav1.OwnerReference{
				{
					APIVersion: "batch/v1",
					Kind:       "Job",
				},
				{
					APIVersion: "wrong-group/v1alpha1",
					Kind:       "RoleBasedGroup",
				},
			},
			expected:    false,
			description: "Should return false when no matches exist",
		},
	}

	for _, tt := range tests {
		t.Run(
			tt.name, func(t *testing.T) {
				// Use custom targetGVK if specified
				currentTargetGVK := targetGVK
				if !tt.targetGVK.Empty() {
					currentTargetGVK = tt.targetGVK
				}

				actual := CheckOwnerReference(tt.refs, currentTargetGVK)
				if actual != tt.expected {
					t.Errorf(
						"Test Case: %s\nDetails: %s\nExpected: %v, Actual: %v",
						tt.name,
						tt.description,
						tt.expected,
						actual,
					)
				}
			},
		)
	}
}

func TestCheckCrdExists(t *testing.T) {
	// Create a scheme and register CRD type
	scheme := runtime.NewScheme()
	_ = apiextensionsv1.AddToScheme(scheme)

	tests := []struct {
		name        string
		crdName     string
		crd         *apiextensionsv1.CustomResourceDefinition
		expectError bool
		errorCheck  func(error) bool
		description string
	}{
		{
			name:        "CRD not found",
			crdName:     "test-crd",
			crd:         nil,
			expectError: true,
			errorCheck: func(err error) bool {
				return apierrors.IsNotFound(err)
			},
			description: "Should return not found error when CRD doesn't exist",
		},
		{
			name:    "CRD exists and established",
			crdName: "test-crd",
			crd: &apiextensionsv1.CustomResourceDefinition{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-crd",
				},
				Status: apiextensionsv1.CustomResourceDefinitionStatus{
					Conditions: []apiextensionsv1.CustomResourceDefinitionCondition{
						{
							Type:   apiextensionsv1.Established,
							Status: apiextensionsv1.ConditionTrue,
						},
					},
				},
			},
			expectError: false,
			description: "Should return nil when CRD exists and is established",
		},
		{
			name:    "CRD exists but not established",
			crdName: "test-crd",
			crd: &apiextensionsv1.CustomResourceDefinition{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-crd",
				},
				Status: apiextensionsv1.CustomResourceDefinitionStatus{
					Conditions: []apiextensionsv1.CustomResourceDefinitionCondition{
						{
							Type:   apiextensionsv1.Established,
							Status: apiextensionsv1.ConditionFalse,
						},
					},
				},
			},
			expectError: true,
			errorCheck: func(err error) bool {
				return err != nil && strings.Contains(err.Error(), "CRD test-crd exists but not established")
			},
			description: "Should return error when CRD exists but is not established",
		},
		{
			name:    "CRD exists with no conditions",
			crdName: "test-crd",
			crd: &apiextensionsv1.CustomResourceDefinition{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-crd",
				},
				Status: apiextensionsv1.CustomResourceDefinitionStatus{
					Conditions: []apiextensionsv1.CustomResourceDefinitionCondition{},
				},
			},
			expectError: true,
			errorCheck: func(err error) bool {
				return err != nil && err.Error() == "CRD test-crd exists but not established"
			},
			description: "Should return error when CRD exists but has no conditions",
		},
		{
			name:    "CRD exists with other condition types",
			crdName: "test-crd",
			crd: &apiextensionsv1.CustomResourceDefinition{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-crd",
				},
				Status: apiextensionsv1.CustomResourceDefinitionStatus{
					Conditions: []apiextensionsv1.CustomResourceDefinitionCondition{
						{
							Type:   apiextensionsv1.NamesAccepted,
							Status: apiextensionsv1.ConditionTrue,
						},
					},
				},
			},
			expectError: true,
			errorCheck: func(err error) bool {
				return err != nil && err.Error() == "CRD test-crd exists but not established"
			},
			description: "Should return error when CRD exists but only has non-established conditions",
		},
	}

	for _, tt := range tests {
		t.Run(
			tt.name, func(t *testing.T) {
				// Create fake client
				builder := fake.NewClientBuilder().WithScheme(scheme)

				// Add CRD if provided
				if tt.crd != nil {
					builder = builder.WithObjects(tt.crd)
				}

				client := builder.Build()

				// Test CheckCrdExists
				err := CheckCrdExists(client, tt.crdName)

				if tt.expectError {
					assert.Error(t, err, tt.description)
					if tt.errorCheck != nil {
						assert.True(t, tt.errorCheck(err), "Error check failed: %v", err)
					}
				} else {
					assert.NoError(t, err, tt.description)
				}
			},
		)
	}
}

// TestGVKConstants tests that the GVK constants match the expected values
func TestGVKConstants(t *testing.T) {
	tests := []struct {
		name     string
		actual   schema.GroupVersionKind
		expected schema.GroupVersionKind
	}{
		{
			name:   "RoleBasedGroup GVK",
			actual: GetRbgGVK(),
			expected: schema.GroupVersionKind{
				Group:   workloadsv1alpha1.GroupVersion.Group,
				Version: workloadsv1alpha1.GroupVersion.Version,
				Kind:    "RoleBasedGroup",
			},
		},
		{
			name:   "RoleBasedGroupScalingAdapter GVK",
			actual: GetRbgScalingAdapterGVK(),
			expected: schema.GroupVersionKind{
				Group:   workloadsv1alpha1.GroupVersion.Group,
				Version: workloadsv1alpha1.GroupVersion.Version,
				Kind:    "RoleBasedGroupScalingAdapter",
			},
		},
		{
			name:   "LeaderWorkerSet GVK",
			actual: GetLwsGVK(),
			expected: schema.GroupVersionKind{
				Group:   lwsv1.GroupVersion.Group,
				Version: lwsv1.GroupVersion.Version,
				Kind:    "LeaderWorkerSet",
			},
		},
	}

	for _, tt := range tests {
		t.Run(
			tt.name, func(t *testing.T) {
				if tt.actual != tt.expected {
					t.Errorf("%s = %v, want %v", tt.name, tt.actual, tt.expected)
				}
			},
		)
	}
}
