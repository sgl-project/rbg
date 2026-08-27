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

package statefulmode

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apps "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/utils/lru"
	"k8s.io/utils/ptr"

	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	testutils "sigs.k8s.io/rbgs/test/utils"
)

const maxRevisionEqualityCacheEntries = 10000

// The package-level patchCodec resolves types against the client-go global
// scheme, mirroring the registration cmd/rbgs/main.go performs at startup.
func init() {
	_ = workloadsv1alpha2.AddToScheme(clientgoscheme.Scheme)
}

func newRevisionTestSet(image string) *workloadsv1alpha2.RoleInstanceSet {
	return &workloadsv1alpha2.RoleInstanceSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "test-set",
			Namespace:  "default",
			UID:        "test-set-uid",
			Generation: 1,
		},
		Spec: workloadsv1alpha2.RoleInstanceSetSpec{
			RoleInstanceTemplate: workloadsv1alpha2.RoleInstanceTemplate{
				RoleInstanceSpec: workloadsv1alpha2.RoleInstanceSpec{
					Components: []workloadsv1alpha2.RoleInstanceComponent{
						{
							Name: "worker",
							Template: corev1.PodTemplateSpec{
								Spec: corev1.PodSpec{
									Containers: []corev1.Container{{Name: "app", Image: image}},
								},
							},
						},
					},
				},
			},
		},
	}
}

func TestSetMatchesRevision(t *testing.T) {
	set := newRevisionTestSet("nginx:1.0")

	proposedRevision, err := newRevision(set, 1, ptr.To(int32(0)))
	assert.NoError(t, err)

	t.Run("IdenticalBytes_SemanticallyEqual", func(t *testing.T) {
		cache := lru.New(maxRevisionEqualityCacheEntries)
		existingRevision := &apps.ControllerRevision{
			ObjectMeta: metav1.ObjectMeta{Name: "existing-rev", ResourceVersion: "200"},
			Data:       runtime.RawExtension{Raw: proposedRevision.Data.Raw},
		}
		assert.True(t, SetMatchesRevision(set, proposedRevision, existingRevision, cache),
			"identical patch bytes should be semantically equal")
	})

	t.Run("LegacyCreationTimestamp_SemanticallyEqual", func(t *testing.T) {
		cache := lru.New(maxRevisionEqualityCacheEntries)
		legacyPatch, err := testutils.WithLegacyCreationTimestamp(proposedRevision.Data.Raw)
		require.NoError(t, err)
		assert.NotEqual(t, proposedRevision.Data.Raw, legacyPatch, "drift injection must actually change the bytes")

		existingRevision := &apps.ControllerRevision{
			ObjectMeta: metav1.ObjectMeta{Name: "existing-rev", ResourceVersion: "200"},
			Data:       runtime.RawExtension{Raw: legacyPatch},
		}
		assert.True(t, SetMatchesRevision(set, proposedRevision, existingRevision, cache),
			"legacy creationTimestamp serialization should still match semantically")
	})

	t.Run("TrulyDifferentSpec_ReturnsFalse", func(t *testing.T) {
		cache := lru.New(maxRevisionEqualityCacheEntries)
		differentRevision, err := newRevision(newRevisionTestSet("nginx:999"), 1, ptr.To(int32(0)))
		assert.NoError(t, err)
		existingRevision := &apps.ControllerRevision{
			ObjectMeta: metav1.ObjectMeta{Name: "existing-rev", ResourceVersion: "200"},
			Data:       runtime.RawExtension{Raw: differentRevision.Data.Raw},
		}
		assert.False(t, SetMatchesRevision(set, proposedRevision, existingRevision, cache),
			"revisions with truly different specs should not match")
	})

	t.Run("CacheHit_SecondCallReturnsTrue", func(t *testing.T) {
		cache := lru.New(maxRevisionEqualityCacheEntries)
		existingRevision := &apps.ControllerRevision{
			ObjectMeta: metav1.ObjectMeta{Name: "existing-rev", ResourceVersion: "200"},
			Data:       runtime.RawExtension{Raw: proposedRevision.Data.Raw},
		}
		assert.True(t, SetMatchesRevision(set, proposedRevision, existingRevision, cache))
		assert.True(t, SetMatchesRevision(set, proposedRevision, existingRevision, cache))
		assert.Equal(t, 1, cache.Len())
	})

	t.Run("NilExistingRevision_ReturnsFalse", func(t *testing.T) {
		cache := lru.New(maxRevisionEqualityCacheEntries)
		assert.False(t, SetMatchesRevision(set, proposedRevision, nil, cache))
	})

	t.Run("NilProposedRevision_ReturnsFalse", func(t *testing.T) {
		cache := lru.New(maxRevisionEqualityCacheEntries)
		existingRevision := &apps.ControllerRevision{
			ObjectMeta: metav1.ObjectMeta{Name: "existing-rev", ResourceVersion: "200"},
			Data:       runtime.RawExtension{Raw: proposedRevision.Data.Raw},
		}
		assert.False(t, SetMatchesRevision(set, nil, existingRevision, cache))
	})

	t.Run("NilCache_ReturnsFalseNoPanic", func(t *testing.T) {
		existingRevision := &apps.ControllerRevision{
			ObjectMeta: metav1.ObjectMeta{Name: "existing-rev", ResourceVersion: "200"},
			Data:       runtime.RawExtension{Raw: proposedRevision.Data.Raw},
		}
		assert.False(t, SetMatchesRevision(set, proposedRevision, existingRevision, nil))
	})

	t.Run("CorruptedExistingRevision_ReturnsFalse", func(t *testing.T) {
		cache := lru.New(maxRevisionEqualityCacheEntries)
		existingRevision := &apps.ControllerRevision{
			ObjectMeta: metav1.ObjectMeta{Name: "existing-rev", ResourceVersion: "200"},
			Data:       runtime.RawExtension{Raw: []byte("invalid-json")},
		}
		assert.False(t, SetMatchesRevision(set, proposedRevision, existingRevision, cache),
			"corrupted revision data should return false, not panic")
		assert.Equal(t, 0, cache.Len())
	})
}
