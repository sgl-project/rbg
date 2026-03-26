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

package llm

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/cli-runtime/pkg/genericclioptions"

	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	llmmeta "sigs.k8s.io/rbgs/cmd/cli/cmd/llm/metadata"
)

// --- newListCmd: command metadata ---

func TestNewListCmd_UseAndShort(t *testing.T) {
	cf := genericclioptions.NewConfigFlags(true)
	cmd := newListCmd(cf)
	assert.Equal(t, "list [flags]", cmd.Use)
	assert.NotEmpty(t, cmd.Short)
}

func TestNewListCmd_Flags(t *testing.T) {
	cf := genericclioptions.NewConfigFlags(true)
	cmd := newListCmd(cf)

	allNsFlag := cmd.Flags().Lookup("all-namespaces")
	require.NotNil(t, allNsFlag)
	assert.Equal(t, "false", allNsFlag.DefValue)
}

func TestNewListCmd_NoArgs(t *testing.T) {
	cf := genericclioptions.NewConfigFlags(true)
	cmd := newListCmd(cf)
	// list takes no positional args — Args validator should be nil (accepts anything)
	// verify the command does not enforce exact arg count
	assert.Nil(t, cmd.Args)
}

// --- extractServiceInfos ---

func makeRBGWithMeta(name, namespace string, meta llmmeta.RunMetadata, replicas int32) workloadsv1alpha2.RoleBasedGroup {
	metaJSON, _ := json.Marshal(meta)
	rep := replicas
	return workloadsv1alpha2.RoleBasedGroup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Annotations: map[string]string{
				llmmeta.RunCommandMetadataAnnotationKey: string(metaJSON),
			},
		},
		Spec: workloadsv1alpha2.RoleBasedGroupSpec{
			Roles: []workloadsv1alpha2.RoleSpec{
				{Replicas: &rep},
			},
		},
	}
}

func TestExtractServiceInfos_WithMetadata(t *testing.T) {
	meta := llmmeta.RunMetadata{
		ModelID:  "Qwen/Qwen3-0.8B",
		Engine:   "vllm",
		Mode:     "standard",
		Revision: "main",
	}
	rbgList := &workloadsv1alpha2.RoleBasedGroupList{
		Items: []workloadsv1alpha2.RoleBasedGroup{
			makeRBGWithMeta("svc-a", "ns-a", meta, 2),
		},
	}

	infos := extractServiceInfos(rbgList)
	require.Len(t, infos, 1)
	assert.Equal(t, "svc-a", infos[0].Name)
	assert.Equal(t, "ns-a", infos[0].Namespace)
	assert.Equal(t, "Qwen/Qwen3-0.8B", infos[0].Model)
	assert.Equal(t, "vllm", infos[0].Engine)
	assert.Equal(t, "standard", infos[0].Mode)
	assert.Equal(t, "main", infos[0].Revision)
	assert.Equal(t, int32(2), infos[0].Replicas)
	assert.Equal(t, "Pending", infos[0].Status) // no conditions
}

func TestExtractServiceInfos_MissingAnnotation(t *testing.T) {
	rbgList := &workloadsv1alpha2.RoleBasedGroupList{
		Items: []workloadsv1alpha2.RoleBasedGroup{
			{
				ObjectMeta: metav1.ObjectMeta{Name: "svc-b", Namespace: "default"},
			},
		},
	}

	infos := extractServiceInfos(rbgList)
	require.Len(t, infos, 1)
	assert.Equal(t, "unknown", infos[0].Model)
	assert.Equal(t, "unknown", infos[0].Engine)
	assert.Equal(t, "unknown", infos[0].Mode)
	assert.Equal(t, "unknown", infos[0].Revision)
}

func TestExtractServiceInfos_InvalidAnnotationJSON(t *testing.T) {
	rbgList := &workloadsv1alpha2.RoleBasedGroupList{
		Items: []workloadsv1alpha2.RoleBasedGroup{
			{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "svc-c",
					Namespace: "default",
					Annotations: map[string]string{
						llmmeta.RunCommandMetadataAnnotationKey: "not-valid-json",
					},
				},
			},
		},
	}

	infos := extractServiceInfos(rbgList)
	require.Len(t, infos, 1)
	assert.Equal(t, "unknown", infos[0].Model)
}

func TestExtractServiceInfos_Empty(t *testing.T) {
	infos := extractServiceInfos(&workloadsv1alpha2.RoleBasedGroupList{})
	assert.Empty(t, infos)
}

func TestExtractServiceInfos_MultipleItems(t *testing.T) {
	meta := llmmeta.RunMetadata{ModelID: "m1", Engine: "vllm", Mode: "standard", Revision: "main"}
	rbgList := &workloadsv1alpha2.RoleBasedGroupList{
		Items: []workloadsv1alpha2.RoleBasedGroup{
			makeRBGWithMeta("a", "ns", meta, 1),
			makeRBGWithMeta("b", "ns", meta, 3),
		},
	}

	infos := extractServiceInfos(rbgList)
	require.Len(t, infos, 2)
	assert.Equal(t, "a", infos[0].Name)
	assert.Equal(t, "b", infos[1].Name)
	assert.Equal(t, int32(3), infos[1].Replicas)
}

// --- extractRBGStatus ---

func TestExtractRBGStatus_NoConditions(t *testing.T) {
	rbg := &workloadsv1alpha2.RoleBasedGroup{}
	assert.Equal(t, "Pending", extractRBGStatus(rbg))
}

func TestExtractRBGStatus_WithCondition(t *testing.T) {
	rbg := &workloadsv1alpha2.RoleBasedGroup{
		Status: workloadsv1alpha2.RoleBasedGroupStatus{
			Conditions: []metav1.Condition{
				{Type: "Ready"},
			},
		},
	}
	assert.Equal(t, "Ready", extractRBGStatus(rbg))
}

// --- listPrintTable ---

func TestListPrintTable_Empty(t *testing.T) {
	cmd := newListCmd(genericclioptions.NewConfigFlags(true))
	var buf bytes.Buffer
	cmd.SetOut(&buf)

	listPrintTable(cmd, []serviceInfo{}, false)
	assert.Contains(t, buf.String(), "No resources found.")
}

func TestListPrintTable_WithoutNamespace(t *testing.T) {
	cmd := newListCmd(genericclioptions.NewConfigFlags(true))
	var buf bytes.Buffer
	cmd.SetOut(&buf)

	services := []serviceInfo{
		{Name: "svc-a", Namespace: "default", Model: "m1", Engine: "vllm", Mode: "standard", Revision: "main", Replicas: 1, Status: "Ready"},
	}
	listPrintTable(cmd, services, false)
	out := buf.String()
	assert.Contains(t, out, "NAME")
	assert.Contains(t, out, "svc-a")
	assert.NotContains(t, out, "NAMESPACE")
}

func TestListPrintTable_WithNamespace(t *testing.T) {
	cmd := newListCmd(genericclioptions.NewConfigFlags(true))
	var buf bytes.Buffer
	cmd.SetOut(&buf)

	services := []serviceInfo{
		{Name: "svc-a", Namespace: "ns-x", Model: "m1", Engine: "vllm", Mode: "standard", Revision: "main", Replicas: 1, Status: "Ready"},
	}
	listPrintTable(cmd, services, true)
	out := buf.String()
	assert.Contains(t, out, "NAMESPACE")
	assert.Contains(t, out, "ns-x")
}

func TestListPrintTable_MultipleRows(t *testing.T) {
	cmd := newListCmd(genericclioptions.NewConfigFlags(true))
	var buf bytes.Buffer
	cmd.SetOut(&buf)

	services := []serviceInfo{
		{Name: "svc-a", Model: "m1", Engine: "vllm", Mode: "standard", Revision: "main", Replicas: 1, Status: "Ready"},
		{Name: "svc-b", Model: "m2", Engine: "sglang", Mode: "latency", Revision: "v1", Replicas: 2, Status: "Pending"},
	}
	listPrintTable(cmd, services, false)
	out := buf.String()
	lines := strings.Split(strings.TrimSpace(out), "\n")
	// header + 2 data rows
	assert.Len(t, lines, 3)
}
