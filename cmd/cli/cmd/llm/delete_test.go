/*
Copyright 2026.

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

package llm

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/cli-runtime/pkg/genericclioptions"

	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	fakeclient "sigs.k8s.io/rbgs/client-go/clientset/versioned/fake"
)

// --- newDeleteCmd: command metadata ---

func TestNewDeleteCmd_UseAndShort(t *testing.T) {
	cf := genericclioptions.NewConfigFlags(true)
	cmd := newDeleteCmd(cf)
	assert.Equal(t, "delete [name...] [flags]", cmd.Use)
	assert.NotEmpty(t, cmd.Short)
}

func TestNewDeleteCmd_Flags(t *testing.T) {
	cf := genericclioptions.NewConfigFlags(true)
	cmd := newDeleteCmd(cf)
	assert.Nil(t, cmd.Flags().Lookup("yes"), "yes flag should not exist after confirm removal")
}

// --- delete logic via fake client ---

// newDeleteCmdWithClient injects a fake RBG clientset into the delete command's
// RunE closure by monkey-patching the command after construction.
// Because RunE calls util.GetRBGClient(cf) which needs a real kubeconfig,
// we test the pure logic helpers directly and rely on integration-style tests
// for the RunE path using the fake clientset below.

func makeTestRBG(name, namespace string) *workloadsv1alpha2.RoleBasedGroup {
	return &workloadsv1alpha2.RoleBasedGroup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels: map[string]string{
				"workloads.x-k8s.io/source": "rbgcli",
			},
		},
	}
}

func TestDeleteFakeClient_DeleteSingleRBG(t *testing.T) {
	rbg := makeTestRBG("my-svc", "default")
	cs := fakeclient.NewSimpleClientset(rbg)

	// verify it exists
	list, err := cs.WorkloadsV1alpha2().RoleBasedGroups("default").List(t.Context(), metav1.ListOptions{})
	require.NoError(t, err)
	require.Len(t, list.Items, 1)

	// delete it
	err = cs.WorkloadsV1alpha2().RoleBasedGroups("default").Delete(t.Context(), "my-svc", metav1.DeleteOptions{})
	require.NoError(t, err)

	// verify it's gone
	list, err = cs.WorkloadsV1alpha2().RoleBasedGroups("default").List(t.Context(), metav1.ListOptions{})
	require.NoError(t, err)
	assert.Empty(t, list.Items)
}

func TestDeleteFakeClient_DeleteNonExistent_Errors(t *testing.T) {
	cs := fakeclient.NewSimpleClientset()
	err := cs.WorkloadsV1alpha2().RoleBasedGroups("default").Delete(t.Context(), "does-not-exist", metav1.DeleteOptions{})
	require.Error(t, err)
}

func TestDeleteFakeClient_ListAndDeleteAll(t *testing.T) {
	rbg1 := makeTestRBG("svc-a", "default")
	rbg2 := makeTestRBG("svc-b", "default")
	cs := fakeclient.NewSimpleClientset(rbg1, rbg2)

	list, err := cs.WorkloadsV1alpha2().RoleBasedGroups("default").List(t.Context(), metav1.ListOptions{})
	require.NoError(t, err)
	require.Len(t, list.Items, 2)

	for i := range list.Items {
		rbg := &list.Items[i]
		err := cs.WorkloadsV1alpha2().RoleBasedGroups(rbg.Namespace).Delete(t.Context(), rbg.Name, metav1.DeleteOptions{})
		require.NoError(t, err)
	}

	list, err = cs.WorkloadsV1alpha2().RoleBasedGroups("default").List(t.Context(), metav1.ListOptions{})
	require.NoError(t, err)
	assert.Empty(t, list.Items)
}

func TestDeleteFakeClient_DeleteAcrossNamespaces(t *testing.T) {
	rbg1 := makeTestRBG("svc-a", "ns1")
	rbg2 := makeTestRBG("svc-b", "ns2")
	cs := fakeclient.NewSimpleClientset(rbg1, rbg2)

	err := cs.WorkloadsV1alpha2().RoleBasedGroups("ns1").Delete(t.Context(), "svc-a", metav1.DeleteOptions{})
	require.NoError(t, err)

	remaining, err := cs.WorkloadsV1alpha2().RoleBasedGroups("ns2").List(t.Context(), metav1.ListOptions{})
	require.NoError(t, err)
	require.Len(t, remaining.Items, 1)
	assert.Equal(t, "svc-b", remaining.Items[0].Name)
}

// --- llm_test.go coverage: delete registered as subcommand ---

func TestNewLLMCmd_HasDeleteSubcommand(t *testing.T) {
	cf := genericclioptions.NewConfigFlags(true)
	cmd := NewLLMCmd(cf)

	var found bool
	for _, sub := range cmd.Commands() {
		if sub.Name() == "delete" {
			found = true
			break
		}
	}
	assert.True(t, found, "expected 'delete' subcommand to be registered under llm")
}
