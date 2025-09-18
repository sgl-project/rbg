package scale

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	workloadsv1alpha "sigs.k8s.io/rbgs/api/workloads/v1alpha1"
)

func TestGenerateScalingAdapterName(t *testing.T) {
	tests := []struct {
		name     string
		rbgName  string
		roleName string
		expected string
	}{
		{
			name:     "generate scalingAdapter name",
			rbgName:  "rbg1",
			roleName: "worker",
			expected: "rbg1-worker",
		},
	}

	for _, tt := range tests {
		t.Run(
			tt.name, func(t *testing.T) {
				result := GenerateScalingAdapterName(tt.rbgName, tt.roleName)
				if result != tt.expected {
					t.Errorf(
						"GenerateScalingAdapterName(%q, %q) = %q, expected %q",
						tt.rbgName, tt.roleName, result, tt.expected,
					)
				}
			},
		)
	}
}

func TestIsScalingAdapterManagedByRBG(t *testing.T) {
	rbgUID := types.UID("test-rbg-uid-123")
	otherUID := types.UID("other-uid-456")

	testRBG := &workloadsv1alpha.RoleBasedGroup{
		ObjectMeta: metav1.ObjectMeta{
			UID: rbgUID,
		},
	}

	scalingAdapterWithMatchingOwner := &workloadsv1alpha.RoleBasedGroupScalingAdapter{
		ObjectMeta: metav1.ObjectMeta{
			OwnerReferences: []metav1.OwnerReference{
				{
					UID: rbgUID,
				},
			},
		},
	}

	scalingAdapterWithNonMatchingOwner := &workloadsv1alpha.RoleBasedGroupScalingAdapter{
		ObjectMeta: metav1.ObjectMeta{
			OwnerReferences: []metav1.OwnerReference{
				{
					UID: otherUID,
				},
			},
		},
	}

	scalingAdapterWithEmptyOwners := &workloadsv1alpha.RoleBasedGroupScalingAdapter{
		ObjectMeta: metav1.ObjectMeta{
			OwnerReferences: []metav1.OwnerReference{},
		},
	}

	tests := []struct {
		name           string
		scalingAdapter *workloadsv1alpha.RoleBasedGroupScalingAdapter
		rbg            *workloadsv1alpha.RoleBasedGroup
		expected       bool
	}{
		{
			name:           "Both nil inputs",
			scalingAdapter: nil,
			rbg:            nil,
			expected:       false,
		},
		{
			name:           "ScalingAdapter is nil",
			scalingAdapter: nil,
			rbg:            testRBG,
			expected:       false,
		},
		{
			name:           "RBG is nil",
			scalingAdapter: scalingAdapterWithMatchingOwner,
			rbg:            nil,
			expected:       false,
		},
		{
			name:           "Found matching OwnerReference",
			scalingAdapter: scalingAdapterWithMatchingOwner,
			rbg:            testRBG,
			expected:       true,
		},
		{
			name:           "No matching OwnerReference",
			scalingAdapter: scalingAdapterWithNonMatchingOwner,
			rbg:            testRBG,
			expected:       false,
		},
		{
			name:           "Empty OwnerReferences list",
			scalingAdapter: scalingAdapterWithEmptyOwners,
			rbg:            testRBG,
			expected:       false,
		},
		{
			name: "Multiple OwnerReferences with match",
			scalingAdapter: &workloadsv1alpha.RoleBasedGroupScalingAdapter{
				ObjectMeta: metav1.ObjectMeta{
					OwnerReferences: []metav1.OwnerReference{
						{
							UID: otherUID,
						},
						{
							UID: rbgUID,
						},
					},
				},
			},
			rbg:      testRBG,
			expected: true,
		},
		{
			name: "Multiple OwnerReferences without match",
			scalingAdapter: &workloadsv1alpha.RoleBasedGroupScalingAdapter{
				ObjectMeta: metav1.ObjectMeta{
					OwnerReferences: []metav1.OwnerReference{
						{
							UID: otherUID,
						},
						{
							UID: types.UID("another-uid-789"),
						},
					},
				},
			},
			rbg:      testRBG,
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(
			tt.name, func(t *testing.T) {
				result := IsScalingAdapterManagedByRBG(tt.scalingAdapter, tt.rbg)
				if result != tt.expected {
					t.Errorf("IsScalingAdapterManagedByRBG() = %v, want %v", result, tt.expected)
				}
			},
		)
	}
}

func TestIsScalingAdapterEnable(t *testing.T) {
	tests := []struct {
		name     string
		roleSpec *workloadsv1alpha.RoleSpec
		expected bool
	}{
		{
			name:     "RoleSpec is nil",
			roleSpec: nil,
			expected: false,
		},
		{
			name: "ScalingAdapter is nil",
			roleSpec: &workloadsv1alpha.RoleSpec{
				ScalingAdapter: nil,
			},
			expected: false,
		},
		{
			name: "Enable scalingAdapter",
			roleSpec: &workloadsv1alpha.RoleSpec{
				ScalingAdapter: &workloadsv1alpha.ScalingAdapter{
					Enable: true,
				},
			},
			expected: true,
		},
		{
			name: "Disable scalingAdapter",
			roleSpec: &workloadsv1alpha.RoleSpec{
				ScalingAdapter: &workloadsv1alpha.ScalingAdapter{
					Enable: false,
				},
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(
			tt.name, func(t *testing.T) {
				result := IsScalingAdapterEnable(tt.roleSpec)
				if result != tt.expected {
					t.Errorf("IsScalingAdapterEnable() = %v, expected %v", result, tt.expected)
				}
			},
		)
	}
}
