package scheduler

import (
	"testing"

	"github.com/stretchr/testify/assert"
	v1 "k8s.io/client-go/applyconfigurations/core/v1"

	workloadsv1alpha1 "sigs.k8s.io/rbgs/api/workloads/v1alpha1"
	"sigs.k8s.io/rbgs/test/wrappers"
)

func TestInjectPodGroupProtocol(t *testing.T) {
	tests := []struct {
		name                string
		rbg                 *workloadsv1alpha1.RoleBasedGroup
		expectLabels        map[string]string
		expectAnnotations   map[string]string
		expectNoLabels      bool
		expectNoAnnotations bool
	}{
		{
			name: "KubeGangScheduling",
			rbg: wrappers.BuildBasicRoleBasedGroup("test-rbg", "default").
				WithKubeGangScheduling(true).
				Obj(),
			expectLabels: map[string]string{
				KubePodGroupLabelKey: "test-rbg",
			},
		},
		{
			name: "VolcanoGangScheduling",
			rbg: wrappers.BuildBasicRoleBasedGroup("test-rbg", "default").
				WithVolcanoGangScheduling("high-priority", "gpu-queue").
				Obj(),
			expectAnnotations: map[string]string{
				VolcanoPodGroupAnnotationKey: "test-rbg",
			},
		},
		{
			name:                "NoGangScheduling",
			rbg:                 wrappers.BuildBasicRoleBasedGroup("test-rbg", "default").Obj(),
			expectNoLabels:      true,
			expectNoAnnotations: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pts := &v1.PodTemplateSpecApplyConfiguration{}

			// Always initialize with empty maps to avoid nil pointer issues
			pts.WithLabels(make(map[string]string))
			pts.WithAnnotations(make(map[string]string))

			originalLabelsLen := len(pts.Labels)
			originalAnnotationsLen := len(pts.Annotations)

			InjectPodGroupProtocol(tt.rbg, pts)

			// Check expected labels
			if tt.expectLabels != nil {
				assert.NotNil(t, pts.Labels)
				for key, value := range tt.expectLabels {
					assert.Equal(t, value, pts.Labels[key])
				}
			}

			// Check expected annotations
			if tt.expectAnnotations != nil {
				assert.NotNil(t, pts.Annotations)
				for key, value := range tt.expectAnnotations {
					assert.Equal(t, value, pts.Annotations[key])
				}
			}

			// Check no labels/annotations were added
			if tt.expectNoLabels {
				assert.Equal(t, originalLabelsLen, len(pts.Labels))
				_, hasKubeLabel := pts.Labels[KubePodGroupLabelKey]
				assert.False(t, hasKubeLabel)
			}

			if tt.expectNoAnnotations {
				assert.Equal(t, originalAnnotationsLen, len(pts.Annotations))
				_, hasVolcanoAnnotation := pts.Annotations[VolcanoPodGroupAnnotationKey]
				assert.False(t, hasVolcanoAnnotation)
			}
		})
	}
}

func TestGetPodGroupCrdName(t *testing.T) {
	tests := []struct {
		name         string
		rbg          *workloadsv1alpha1.RoleBasedGroup
		expectedName string
	}{
		{
			name: "KubeGangScheduling",
			rbg: wrappers.BuildBasicRoleBasedGroup("test-rbg", "default").
				WithKubeGangScheduling(true).Obj(),
			expectedName: KubePodGroupCrdName,
		},
		{
			name: "VolcanoGangScheduling",
			rbg: wrappers.BuildBasicRoleBasedGroup("test-rbg", "default").
				WithVolcanoGangScheduling("high-priority", "gpu-queue").Obj(),
			expectedName: VolcanoPodGroupCrdName,
		},
		{
			name:         "NoGangScheduling",
			rbg:          wrappers.BuildBasicRoleBasedGroup("test-rbg", "default").Obj(),
			expectedName: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := GetPodGroupCrdName(tt.rbg)
			assert.Equal(t, tt.expectedName, result)
		})
	}
}
