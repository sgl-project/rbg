/*
Copyright 2025.

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

package rollout

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	workloadsv1alpha1 "sigs.k8s.io/rbgs/api/workloads/v1alpha1"
)

func TestValidateRolloutDiff(t *testing.T) {
	tests := []struct {
		name        string
		args        []string
		revision    int64
		expectError bool
	}{
		{
			name:        "valid args with name and positive revision",
			args:        []string{"test-rbg"},
			revision:    1,
			expectError: false,
		},
		{
			name:        "empty args",
			args:        []string{},
			revision:    1,
			expectError: true,
		},
		{
			name:        "empty name",
			args:        []string{""},
			revision:    1,
			expectError: true,
		},
		{
			name:        "zero revision",
			args:        []string{"test-rbg"},
			revision:    0,
			expectError: true,
		},
		{
			name:        "negative revision",
			args:        []string{"test-rbg"},
			revision:    -1,
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rolloutOpts.revision = tt.revision
			err := validateRolloutDiff(tt.args)

			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestRunRolloutDiff(t *testing.T) {
	rbg := &workloadsv1alpha1.RoleBasedGroup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-rbg",
			Namespace: "default",
			UID:       "12345",
		},
	}
	var revisions []*appsv1.ControllerRevision
	for i := 0; i < 5; i++ {
		revisions = append(revisions, &appsv1.ControllerRevision{
			ObjectMeta: metav1.ObjectMeta{
				Name:              fmt.Sprintf("rev-%d", i),
				Namespace:         "default",
				CreationTimestamp: metav1.Now(),
				Labels: map[string]string{
					workloadsv1alpha1.SetNameLabelKey: "test-rbg",
				},
			},
			Data:     runtime.RawExtension{Raw: []byte(fmt.Sprintf("{\"role\":{\"name\":\"role-%d\"}}", i))},
			Revision: int64(i),
		})
	}
	fakeClient := getFakeK8sClient(revisions)
	fakeRgbClient := getFakeRgbClient([]*workloadsv1alpha1.RoleBasedGroup{rbg})
	old := rolloutOpts
	defer func() {
		rolloutOpts = old
	}()
	rolloutOpts.revision = 1
	err := runRolloutDiff(context.TODO(), fakeRgbClient, fakeClient, "test-rbg", "default")
	assert.NoError(t, err)
}
