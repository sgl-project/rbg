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
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	workloadsv1alpha1 "sigs.k8s.io/rbgs/api/workloads/v1alpha1"
	rbgclient "sigs.k8s.io/rbgs/client-go/clientset/versioned"
	fakerbgclient "sigs.k8s.io/rbgs/client-go/clientset/versioned/fake"
)

func TestNewRolloutCmd(t *testing.T) {
	cf := genericclioptions.NewConfigFlags(true)
	cmd := NewRolloutCmd(cf)

	assert.Equal(t, "rollout [SUBCOMMAND]", cmd.Use)
	assert.True(t, cmd.DisableAutoGenTag)
	assert.True(t, cmd.SilenceUsage)

	assert.Equal(t, 3, len(cmd.Commands()))

	commands := make(map[string]*cobra.Command)
	for _, c := range cmd.Commands() {
		commands[c.Name()] = c
	}

	assert.Contains(t, commands, "history")
	assert.Contains(t, commands, "diff")
	assert.Contains(t, commands, "undo")
}

func TestSortRevisionsStable(t *testing.T) {
	rev1 := &appsv1.ControllerRevision{
		ObjectMeta: metav1.ObjectMeta{Name: "rev1"},
		Revision:   1,
	}
	rev2 := &appsv1.ControllerRevision{
		ObjectMeta: metav1.ObjectMeta{Name: "rev2"},
		Revision:   2,
	}

	items := []*appsv1.ControllerRevision{rev2, rev1}
	sorted := sortRevisionsStable(items)
	if sorted[0].Name != "rev1" || sorted[1].Name != "rev2" {
		t.Errorf("Revision sort failed")
	}

	ts1 := metav1.NewTime(time.Date(2023, 1, 1, 0, 0, 0, 0, time.UTC))
	ts2 := metav1.NewTime(time.Date(2023, 1, 2, 0, 0, 0, 0, time.UTC))
	revA := &appsv1.ControllerRevision{
		ObjectMeta: metav1.ObjectMeta{Name: "A", CreationTimestamp: ts1},
		Revision:   1,
	}
	revB := &appsv1.ControllerRevision{
		ObjectMeta: metav1.ObjectMeta{Name: "B", CreationTimestamp: ts2},
		Revision:   1,
	}
	items = []*appsv1.ControllerRevision{revB, revA}
	sorted = sortRevisionsStable(items)
	if sorted[0].Name != "A" {
		t.Errorf("CreationTimestamp sort failed")
	}
}

func getFakeRgbClient(rbgs []*workloadsv1alpha1.RoleBasedGroup) rbgclient.Interface {
	objs := []runtime.Object{}
	for _, rbg := range rbgs {
		objs = append(objs, rbg)
	}
	return fakerbgclient.NewSimpleClientset(objs...)
}

func getFakeK8sClient(revisions []*appsv1.ControllerRevision) kubernetes.Interface {
	objs := []runtime.Object{}
	for _, revision := range revisions {
		objs = append(objs, revision)
	}
	return fake.NewSimpleClientset(objs...)
}
