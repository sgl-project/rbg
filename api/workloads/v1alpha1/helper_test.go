package v1alpha1

import (
	"testing"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestRoleBasedGroup_GenGroupUniqueKey(t *testing.T) {
	rbg := &RoleBasedGroup{
		ObjectMeta: metav1.ObjectMeta{Name: "test-rbg", Namespace: "test-ns"},
	}
	key := rbg.GenGroupUniqueKey()
	assert.Len(t, key, 40) // SHA1 hex = 40
	assert.Equal(t, key, rbg.GenGroupUniqueKey())
}

func TestRoleBasedGroup_GetExclusiveKey(t *testing.T) {
	tests := []struct {
		name        string
		annotations map[string]string
		wantKey     string
		wantFound   bool
	}{
		{"empty", nil, "", false},
		{"set", map[string]string{ExclusiveKeyAnnotationKey: "kubernetes.io/hostname"}, "kubernetes.io/hostname", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rbg := &RoleBasedGroup{
				ObjectMeta: metav1.ObjectMeta{Annotations: tt.annotations},
			}
			got, found := rbg.GetExclusiveKey()
			assert.Equal(t, tt.wantKey, got)
			assert.Equal(t, tt.wantFound, found)
		})
	}
}
