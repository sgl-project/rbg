package utils

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestListRevisions(t *testing.T) {
	// Define test scheme
	scheme := runtime.NewScheme()
	_ = appsv1.AddToScheme(scheme)

	// Test parent object
	parent := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-sts",
			Namespace: "default",
			UID:       "parent-uid",
		},
	}

	// Test data
	ownedRevision := &appsv1.ControllerRevision{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "owned-revision",
			Namespace: "default",
			Labels: map[string]string{
				"app": "test",
			},
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: "apps/v1",
					Kind:       "StatefulSet",
					Name:       "test-sts",
					UID:        "parent-uid",
					Controller: ptr.To(true),
				},
			},
		},
		Revision: 1,
	}

	unownedRevision := &appsv1.ControllerRevision{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "unowned-revision",
			Namespace: "default",
			Labels: map[string]string{
				"app": "test",
			},
		},
		Revision: 2,
	}

	differentOwnerRevision := &appsv1.ControllerRevision{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "different-owner-revision",
			Namespace: "default",
			Labels: map[string]string{
				"app": "test",
			},
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: "apps/v1",
					Kind:       "StatefulSet",
					Name:       "other-sts",
					UID:        "other-uid",
					Controller: ptr.To(true),
				},
			},
		},
		Revision: 3,
	}

	wrongNamespaceRevision := &appsv1.ControllerRevision{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "wrong-namespace-revision",
			Namespace: "other",
			Labels: map[string]string{
				"app": "test",
			},
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: "apps/v1",
					Kind:       "StatefulSet",
					Name:       "test-sts",
					UID:        "parent-uid",
					Controller: ptr.To(true),
				},
			},
		},
		Revision: 4,
	}

	differentLabelRevision := &appsv1.ControllerRevision{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "different-label-revision",
			Namespace: "default",
			Labels: map[string]string{
				"app": "other",
			},
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: "apps/v1",
					Kind:       "StatefulSet",
					Name:       "test-sts",
					UID:        "parent-uid",
					Controller: ptr.To(true),
				},
			},
		},
		Revision: 5,
	}

	tests := []struct {
		name          string
		parent        metav1.Object
		selector      labels.Selector
		existingObjs  []runtime.Object
		expectedCount int
		expectErr     bool
	}{
		{
			name:     "list revisions with matching selector and ownership",
			parent:   parent,
			selector: labels.SelectorFromSet(map[string]string{"app": "test"}),
			existingObjs: []runtime.Object{
				ownedRevision,
				unownedRevision,
				differentOwnerRevision,
				wrongNamespaceRevision,
				differentLabelRevision,
			},
			expectedCount: 2, // ownedRevision and unownedRevision
			expectErr:     false,
		},
		{
			name:     "list revisions with no matching labels",
			parent:   parent,
			selector: labels.SelectorFromSet(map[string]string{"app": "nonexistent"}),
			existingObjs: []runtime.Object{
				ownedRevision,
				unownedRevision,
			},
			expectedCount: 0,
			expectErr:     false,
		},
		{
			name:         "list revisions with no existing revisions",
			parent:       parent,
			selector:     labels.SelectorFromSet(map[string]string{"app": "test"}),
			existingObjs: []runtime.Object{
				// No revisions
			},
			expectedCount: 0,
			expectErr:     false,
		},
		{
			name:     "list revisions with nil selector",
			parent:   parent,
			selector: nil,
			existingObjs: []runtime.Object{
				ownedRevision,
				unownedRevision,
			},
			expectedCount: 0, // Nil selector matches nothing
			expectErr:     false,
		},
	}

	for _, tt := range tests {
		t.Run(
			tt.name, func(t *testing.T) {
				// Setup
				client := fake.NewClientBuilder().
					WithScheme(scheme).
					WithRuntimeObjects(tt.existingObjs...).
					Build()

				// Execute
				result, err := ListRevisions(context.Background(), client, tt.parent, tt.selector)

				// Verify
				if tt.expectErr {
					assert.Error(t, err)
					assert.Nil(t, result)
				} else {
					assert.NoError(t, err)
					assert.Len(t, result, tt.expectedCount)

					// Verify that all returned revisions are either owned by parent or unowned
					for _, revision := range result {
						ref := metav1.GetControllerOf(revision)
						// Should be either unowned (nil ref) or owned by parent
						if ref != nil {
							assert.Equal(t, tt.parent.GetUID(), ref.UID)
						}

						// Should match the label selector if it's not nil
						if tt.selector != nil && !tt.selector.Empty() {
							matches := tt.selector.Matches(labels.Set(revision.Labels))
							assert.True(t, matches, "Revision %s should match selector", revision.Name)
						}
					}
				}
			},
		)
	}
}

func TestGetHighestRevision(t *testing.T) {
	// Test data
	revision1 := &appsv1.ControllerRevision{
		ObjectMeta: metav1.ObjectMeta{
			Name: "revision-1",
		},
		Revision: 1,
	}

	revision5 := &appsv1.ControllerRevision{
		ObjectMeta: metav1.ObjectMeta{
			Name: "revision-5",
		},
		Revision: 5,
	}

	revision10 := &appsv1.ControllerRevision{
		ObjectMeta: metav1.ObjectMeta{
			Name: "revision-10",
		},
		Revision: 10,
	}

	tests := []struct {
		name             string
		revisions        []*appsv1.ControllerRevision
		expectedRevision *appsv1.ControllerRevision
	}{
		{
			name:             "empty revisions list",
			revisions:        []*appsv1.ControllerRevision{},
			expectedRevision: nil,
		},
		{
			name:             "nil revisions list",
			revisions:        nil,
			expectedRevision: nil,
		},
		{
			name: "single revision",
			revisions: []*appsv1.ControllerRevision{
				revision5,
			},
			expectedRevision: revision5,
		},
		{
			name: "multiple revisions - find highest",
			revisions: []*appsv1.ControllerRevision{
				revision1,
				revision5,
				revision10,
			},
			expectedRevision: revision10,
		},
		{
			name: "multiple revisions - highest in middle",
			revisions: []*appsv1.ControllerRevision{
				revision1,
				revision10,
				revision5,
			},
			expectedRevision: revision10,
		},
		{
			name: "multiple revisions - duplicate highest",
			revisions: []*appsv1.ControllerRevision{
				revision1,
				revision10,
				revision5,
				revision10,
			},
			expectedRevision: revision10, // Returns one of the highest (the last one encountered)
		},
		{
			name: "all revisions have same value",
			revisions: []*appsv1.ControllerRevision{
				revision5,
				revision5,
				revision5,
			},
			expectedRevision: revision5,
		},
	}

	for _, tt := range tests {
		t.Run(
			tt.name, func(t *testing.T) {
				result := GetHighestRevision(tt.revisions)

				if tt.expectedRevision == nil {
					assert.Nil(t, result)
				} else {
					assert.NotNil(t, result)
					assert.Equal(t, tt.expectedRevision.Revision, result.Revision)
				}
			},
		)
	}
}

func TestListRevisionsAndFindHighestIntegration(t *testing.T) {
	// Integration test for ListRevisions and GetHighestRevision working together
	scheme := runtime.NewScheme()
	_ = appsv1.AddToScheme(scheme)

	parent := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-sts",
			Namespace: "default",
			UID:       "parent-uid",
		},
	}

	// Revisions with different revision numbers
	revision1 := &appsv1.ControllerRevision{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "revision-1",
			Namespace: "default",
			Labels: map[string]string{
				"app": "test",
			},
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: "apps/v1",
					Kind:       "StatefulSet",
					Name:       "test-sts",
					UID:        "parent-uid",
					Controller: ptr.To(true),
				},
			},
		},
		Revision: 1,
	}

	revision3 := &appsv1.ControllerRevision{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "revision-3",
			Namespace: "default",
			Labels: map[string]string{
				"app": "test",
			},
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: "apps/v1",
					Kind:       "StatefulSet",
					Name:       "test-sts",
					UID:        "parent-uid",
					Controller: ptr.To(true),
				},
			},
		},
		Revision: 3,
	}

	revision2 := &appsv1.ControllerRevision{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "revision-2",
			Namespace: "default",
			Labels: map[string]string{
				"app": "test",
			},
		}, // Unowned revision
		Revision: 2,
	}

	client := fake.NewClientBuilder().
		WithScheme(scheme).
		WithRuntimeObjects([]runtime.Object{revision1, revision2, revision3}...).
		Build()

	selector := labels.SelectorFromSet(map[string]string{"app": "test"})

	// List revisions
	revisions, err := ListRevisions(context.Background(), client, parent, selector)
	assert.NoError(t, err)
	assert.Len(t, revisions, 3) // All 3 match the criteria

	// Find highest revision
	highest := GetHighestRevision(revisions)
	assert.NotNil(t, highest)
	assert.Equal(t, int64(3), highest.Revision)
	assert.Equal(t, "revision-3", highest.Name)
}
