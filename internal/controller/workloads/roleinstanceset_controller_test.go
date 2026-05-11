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

package workloads

import (
	"context"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"sigs.k8s.io/rbgs/api/workloads/constants"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
)

func newTestScheme() *runtime.Scheme {
	scheme := runtime.NewScheme()
	_ = workloadsv1alpha2.AddToScheme(scheme)
	_ = appsv1.AddToScheme(scheme)
	return scheme
}

func TestInstanceBelongsToPattern(t *testing.T) {
	setUID := types.UID("test-set-uid-1234")
	set := &workloadsv1alpha2.RoleInstanceSet{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-prefill",
			UID:  setUID,
		},
	}

	tests := []struct {
		name     string
		instance *workloadsv1alpha2.RoleInstance
		pattern  constants.InstancePatternType
		want     bool
	}{
		{
			name: "stateless instance matches StatelessPattern",
			instance: &workloadsv1alpha2.RoleInstance{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-prefill-7jj4f",
					Labels: map[string]string{
						constants.RoleInstanceOwnerLabelKey: string(setUID),
					},
				},
			},
			pattern: constants.StatelessPattern,
			want:    true,
		},
		{
			name: "stateless instance missing owner label does not match StatelessPattern",
			instance: &workloadsv1alpha2.RoleInstance{
				ObjectMeta: metav1.ObjectMeta{
					Name:   "test-prefill-7jj4f",
					Labels: map[string]string{},
				},
			},
			pattern: constants.StatelessPattern,
			want:    false,
		},
		{
			name: "stateless instance with wrong UID does not match StatelessPattern",
			instance: &workloadsv1alpha2.RoleInstance{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-prefill-7jj4f",
					Labels: map[string]string{
						constants.RoleInstanceOwnerLabelKey: "wrong-uid",
					},
				},
			},
			pattern: constants.StatelessPattern,
			want:    false,
		},
		{
			name: "stateful instance with ordinal matches StatefulPattern",
			instance: &workloadsv1alpha2.RoleInstance{
				ObjectMeta: metav1.ObjectMeta{
					Name:   "test-prefill-0",
					Labels: map[string]string{},
				},
			},
			pattern: constants.StatefulPattern,
			want:    true,
		},
		{
			name: "stateful instance with ordinal matches empty pattern (default stateful)",
			instance: &workloadsv1alpha2.RoleInstance{
				ObjectMeta: metav1.ObjectMeta{
					Name:   "test-prefill-1",
					Labels: map[string]string{},
				},
			},
			pattern: "",
			want:    true,
		},
		{
			name: "random-ID instance does not match StatefulPattern",
			instance: &workloadsv1alpha2.RoleInstance{
				ObjectMeta: metav1.ObjectMeta{
					Name:   "test-prefill-7jj4f",
					Labels: map[string]string{},
				},
			},
			pattern: constants.StatefulPattern,
			want:    false,
		},
		{
			name: "stateful instance with wrong parent name does not match StatefulPattern",
			instance: &workloadsv1alpha2.RoleInstance{
				ObjectMeta: metav1.ObjectMeta{
					Name:   "other-set-0",
					Labels: map[string]string{},
				},
			},
			pattern: constants.StatefulPattern,
			want:    false,
		},
		{
			name: "unknown pattern always matches",
			instance: &workloadsv1alpha2.RoleInstance{
				ObjectMeta: metav1.ObjectMeta{
					Name:   "anything",
					Labels: map[string]string{},
				},
			},
			pattern: "Unknown",
			want:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := instanceBelongsToPattern(tt.instance, set, tt.pattern)
			if got != tt.want {
				t.Errorf("instanceBelongsToPattern() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCleanupStaleInstances(t *testing.T) {
	setUID := types.UID("test-set-uid-1234")
	matchLabels := map[string]string{
		"app": "test-prefill",
	}

	newTestSet := func(pattern string) *workloadsv1alpha2.RoleInstanceSet {
		return &workloadsv1alpha2.RoleInstanceSet{
			ObjectMeta: metav1.ObjectMeta{
				Name:        "test-prefill",
				Namespace:   "default",
				UID:         setUID,
				Annotations: map[string]string{constants.RoleInstancePatternKey: pattern},
			},
			Spec: workloadsv1alpha2.RoleInstanceSetSpec{
				Selector: &metav1.LabelSelector{MatchLabels: matchLabels},
				Replicas: ptr.To(int32(2)),
			},
		}
	}

	// stateful instance: ordinal name, no RoleInstanceOwnerLabelKey
	newStatefulInstance := func(name string) *workloadsv1alpha2.RoleInstance {
		return &workloadsv1alpha2.RoleInstance{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: "default",
				Labels:    matchLabels,
				OwnerReferences: []metav1.OwnerReference{
					*metav1.NewControllerRef(newTestSet("Stateful"), workloadsv1alpha2.GroupVersion.WithKind("RoleInstanceSet")),
				},
			},
		}
	}

	// stateless instance: random-ID name, has RoleInstanceOwnerLabelKey
	newStatelessInstance := func(name string) *workloadsv1alpha2.RoleInstance {
		return &workloadsv1alpha2.RoleInstance{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: "default",
				Labels: func() map[string]string {
					l := map[string]string{
						constants.RoleInstanceOwnerLabelKey: string(setUID),
					}
					for k, v := range matchLabels {
						l[k] = v
					}
					return l
				}(),
				OwnerReferences: []metav1.OwnerReference{
					*metav1.NewControllerRef(newTestSet("Stateless"), workloadsv1alpha2.GroupVersion.WithKind("RoleInstanceSet")),
				},
			},
		}
	}

	// instance owned by another set (not controlled by our set)
	newForeignInstance := func(name string) *workloadsv1alpha2.RoleInstance {
		return &workloadsv1alpha2.RoleInstance{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: "default",
				Labels:    matchLabels,
				OwnerReferences: []metav1.OwnerReference{
					{
						APIVersion: "workloads.x-k8s.io/v1alpha2",
						Kind:       "RoleInstanceSet",
						Name:       "other-set",
						UID:        "other-uid",
						Controller: ptr.To(true),
					},
				},
			},
		}
	}

	tests := []struct {
		name             string
		setPattern       string
		initialInstances []*workloadsv1alpha2.RoleInstance
		wantRequeue      bool
		wantRemaining    int // expected number of instances remaining after cleanup
		wantDeletedNames []string
	}{
		{
			name:       "stateful set with matching instances: no cleanup needed",
			setPattern: string(constants.StatefulPattern),
			initialInstances: []*workloadsv1alpha2.RoleInstance{
				newStatefulInstance("test-prefill-0"),
				newStatefulInstance("test-prefill-1"),
			},
			wantRequeue:   false,
			wantRemaining: 2,
		},
		{
			name:       "stateless set with matching instances: no cleanup needed",
			setPattern: string(constants.StatelessPattern),
			initialInstances: []*workloadsv1alpha2.RoleInstance{
				newStatelessInstance("test-prefill-7jj4f"),
				newStatelessInstance("test-prefill-jdggk"),
			},
			wantRequeue:   false,
			wantRemaining: 2,
		},
		{
			name:       "switch from stateful to stateless: stale stateful instances deleted",
			setPattern: string(constants.StatelessPattern),
			initialInstances: []*workloadsv1alpha2.RoleInstance{
				newStatefulInstance("test-prefill-0"),
				newStatefulInstance("test-prefill-1"),
			},
			wantRequeue:      true,
			wantRemaining:    0,
			wantDeletedNames: []string{"test-prefill-0", "test-prefill-1"},
		},
		{
			name:       "switch from stateless to stateful: stale stateless instances deleted",
			setPattern: string(constants.StatefulPattern),
			initialInstances: []*workloadsv1alpha2.RoleInstance{
				newStatelessInstance("test-prefill-7jj4f"),
				newStatelessInstance("test-prefill-jdggk"),
			},
			wantRequeue:      true,
			wantRemaining:    0,
			wantDeletedNames: []string{"test-prefill-7jj4f", "test-prefill-jdggk"},
		},
		{
			name:       "mixed instances with stateless pattern: only stale stateful instances deleted",
			setPattern: string(constants.StatelessPattern),
			initialInstances: []*workloadsv1alpha2.RoleInstance{
				newStatelessInstance("test-prefill-7jj4f"),
				newStatefulInstance("test-prefill-0"),
			},
			wantRequeue:      true,
			wantRemaining:    1,
			wantDeletedNames: []string{"test-prefill-0"},
		},
		{
			name:       "mixed instances with stateful pattern: only stale stateless instances deleted",
			setPattern: string(constants.StatefulPattern),
			initialInstances: []*workloadsv1alpha2.RoleInstance{
				newStatefulInstance("test-prefill-0"),
				newStatelessInstance("test-prefill-7jj4f"),
			},
			wantRequeue:      true,
			wantRemaining:    1,
			wantDeletedNames: []string{"test-prefill-7jj4f"},
		},
		{
			name:       "foreign instances are not deleted",
			setPattern: string(constants.StatefulPattern),
			initialInstances: []*workloadsv1alpha2.RoleInstance{
				newStatefulInstance("test-prefill-0"),
				newForeignInstance("test-prefill-abc"),
			},
			wantRequeue:   false,
			wantRemaining: 2,
		},
		{
			name:             "no instances: no cleanup needed",
			setPattern:       string(constants.StatefulPattern),
			initialInstances: nil,
			wantRequeue:      false,
			wantRemaining:    0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scheme := newTestScheme()
			set := newTestSet(tt.setPattern)

			objs := []runtime.Object{set}
			for _, inst := range tt.initialInstances {
				objs = append(objs, inst)
			}

			c := fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(objs...).Build()
			r := &RoleInstanceSetReconciler{
				client:   c,
				recorder: record.NewFakeRecorder(0),
			}

			requeue, err := r.cleanupStaleInstances(context.Background(), set)
			if err != nil {
				t.Fatalf("cleanupStaleInstances() error = %v", err)
			}
			if requeue != tt.wantRequeue {
				t.Errorf("cleanupStaleInstances() requeue = %v, want %v", requeue, tt.wantRequeue)
			}

			// Verify remaining instances
			instanceList := &workloadsv1alpha2.RoleInstanceList{}
			if err := c.List(context.Background(), instanceList); err != nil {
				t.Fatalf("List instances error = %v", err)
			}
			if len(instanceList.Items) != tt.wantRemaining {
				t.Errorf("remaining instances = %d, want %d", len(instanceList.Items), tt.wantRemaining)
			}

			// Verify specific deletions
			if len(tt.wantDeletedNames) > 0 {
				remainingNames := map[string]bool{}
				for _, inst := range instanceList.Items {
					remainingNames[inst.Name] = true
				}
				for _, name := range tt.wantDeletedNames {
					if remainingNames[name] {
						t.Errorf("instance %s should have been deleted but still exists", name)
					}
				}
			}
		})
	}
}
