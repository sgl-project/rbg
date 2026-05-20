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

package componentdiscovery

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
)

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func makeComponent(name string, deps []string) workloadsv1alpha2.RoleInstanceComponent {
	comp := workloadsv1alpha2.RoleInstanceComponent{
		Name: name,
		Template: corev1.PodTemplateSpec{
			ObjectMeta: metav1.ObjectMeta{},
		},
	}
	if len(deps) > 0 {
		raw, _ := json.Marshal(ComponentDependsOnConfig{StartAfter: deps})
		comp.Annotations = map[string]string{
			ComponentDependsOnAnnotationKey: string(raw),
		}
	}
	return comp
}

// ---------------------------------------------------------------------------
// ParseAllComponentDependencies
// ---------------------------------------------------------------------------

func TestParseAllComponentDependencies_BothFields(t *testing.T) {
	// router: no deps
	// leader: startAfter router, deleteAfter nothing
	// worker: no startAfter, deleteAfter router
	leaderRaw, _ := json.Marshal(ComponentDependsOnConfig{StartAfter: []string{"router"}})
	workerRaw, _ := json.Marshal(ComponentDependsOnConfig{DeleteAfter: []string{"router"}})
	comps := []workloadsv1alpha2.RoleInstanceComponent{
		makeComponent("router", nil),
		{
			Name: "leader",
			Annotations: map[string]string{
				ComponentDependsOnAnnotationKey: string(leaderRaw),
			},
		},
		{
			Name: "worker",
			Annotations: map[string]string{
				ComponentDependsOnAnnotationKey: string(workerRaw),
			},
		},
	}
	startDeps, deleteDeps, err := ParseAllComponentDependencies(comps)
	require.NoError(t, err)

	// startDeps
	assert.Nil(t, startDeps["router"])
	assert.Equal(t, []string{"router"}, startDeps["leader"])
	assert.Nil(t, startDeps["worker"])

	// deleteDeps
	assert.Nil(t, deleteDeps["router"])
	assert.Nil(t, deleteDeps["leader"])
	assert.Equal(t, []string{"router"}, deleteDeps["worker"])
}

// ---------------------------------------------------------------------------
// ParseComponentDependencies (compatibility wrapper)
// ---------------------------------------------------------------------------

func TestParseComponentDependencies_NoDeps(t *testing.T) {
	comps := []workloadsv1alpha2.RoleInstanceComponent{
		makeComponent("router", nil),
		makeComponent("leader", nil),
		makeComponent("worker", nil),
	}
	deps, err := ParseComponentDependencies(comps)
	require.NoError(t, err)
	assert.Equal(t, 3, len(deps))
	assert.Nil(t, deps["router"])
	assert.Nil(t, deps["leader"])
	assert.Nil(t, deps["worker"])
}

func TestParseComponentDependencies_WithDeps(t *testing.T) {
	comps := []workloadsv1alpha2.RoleInstanceComponent{
		makeComponent("router", nil),
		makeComponent("leader", []string{"router"}),
		makeComponent("worker", []string{"router", "leader"}),
	}
	deps, err := ParseComponentDependencies(comps)
	require.NoError(t, err)
	assert.Nil(t, deps["router"])
	assert.Equal(t, []string{"router"}, deps["leader"])
	assert.Equal(t, []string{"router", "leader"}, deps["worker"])
}

func TestParseComponentDependencies_InvalidJSON(t *testing.T) {
	comp := workloadsv1alpha2.RoleInstanceComponent{
		Name: "bad",
		Annotations: map[string]string{
			ComponentDependsOnAnnotationKey: `{not-valid-json`,
		},
	}
	_, err := ParseComponentDependencies([]workloadsv1alpha2.RoleInstanceComponent{comp})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "bad")
}

func TestParseComponentDependencies_EmptyAnnotation(t *testing.T) {
	comp := workloadsv1alpha2.RoleInstanceComponent{
		Name: "x",
		Annotations: map[string]string{
			ComponentDependsOnAnnotationKey: "",
		},
	}
	deps, err := ParseComponentDependencies([]workloadsv1alpha2.RoleInstanceComponent{comp})
	require.NoError(t, err)
	assert.Nil(t, deps["x"])
}

// ---------------------------------------------------------------------------
// HasAnyDependency
// ---------------------------------------------------------------------------

func TestHasAnyDependency(t *testing.T) {
	t.Run("no deps", func(t *testing.T) {
		comps := []workloadsv1alpha2.RoleInstanceComponent{
			makeComponent("a", nil),
			makeComponent("b", nil),
		}
		assert.False(t, HasAnyDependency(comps))
	})

	t.Run("has dep", func(t *testing.T) {
		comps := []workloadsv1alpha2.RoleInstanceComponent{
			makeComponent("a", nil),
			makeComponent("b", []string{"a"}),
		}
		assert.True(t, HasAnyDependency(comps))
	})

	t.Run("empty annotation value", func(t *testing.T) {
		comp := workloadsv1alpha2.RoleInstanceComponent{
			Name: "x",
			Annotations: map[string]string{
				ComponentDependsOnAnnotationKey: "",
			},
		}
		assert.False(t, HasAnyDependency([]workloadsv1alpha2.RoleInstanceComponent{comp}))
	})
}

// ---------------------------------------------------------------------------
// DetectCycle
// ---------------------------------------------------------------------------

func TestDetectCycle_NoCycle(t *testing.T) {
	t.Run("empty graph", func(t *testing.T) {
		assert.False(t, DetectCycle(map[string][]string{}))
	})

	t.Run("linear chain router→leader→worker", func(t *testing.T) {
		deps := map[string][]string{
			"router": nil,
			"leader": {"router"},
			"worker": {"leader"},
		}
		assert.False(t, DetectCycle(deps))
	})

	t.Run("diamond: router→leader, router→worker, leader→engine, worker→engine", func(t *testing.T) {
		deps := map[string][]string{
			"router": nil,
			"leader": {"router"},
			"worker": {"router"},
			"engine": {"leader", "worker"},
		}
		assert.False(t, DetectCycle(deps))
	})

	t.Run("parallel independent components", func(t *testing.T) {
		deps := map[string][]string{
			"a": nil,
			"b": nil,
			"c": nil,
		}
		assert.False(t, DetectCycle(deps))
	})
}

func TestDetectCycle_HasCycle(t *testing.T) {
	t.Run("self-loop", func(t *testing.T) {
		deps := map[string][]string{
			"a": {"a"},
		}
		assert.True(t, DetectCycle(deps))
	})

	t.Run("two-node cycle: a→b, b→a", func(t *testing.T) {
		deps := map[string][]string{
			"a": {"b"},
			"b": {"a"},
		}
		assert.True(t, DetectCycle(deps))
	})

	t.Run("three-node cycle: a→b→c→a", func(t *testing.T) {
		deps := map[string][]string{
			"a": {"c"},
			"b": {"a"},
			"c": {"b"},
		}
		assert.True(t, DetectCycle(deps))
	})

	t.Run("cycle in one branch of a larger graph", func(t *testing.T) {
		// router has no deps; leader→router; worker→leader; engine→worker→engine (cycle)
		deps := map[string][]string{
			"router": nil,
			"leader": {"router"},
			"worker": {"engine"}, // worker depends on engine
			"engine": {"worker"}, // engine depends on worker → cycle
		}
		assert.True(t, DetectCycle(deps))
	})
}

// ---------------------------------------------------------------------------
// BuildReverseDependencies
// ---------------------------------------------------------------------------

func TestBuildReverseDependencies(t *testing.T) {
	t.Run("linear chain", func(t *testing.T) {
		// router → leader → worker  (startAfter direction)
		// reverse: router's dependent is leader; leader's dependent is worker
		deps := map[string][]string{
			"router": nil,
			"leader": {"router"},
			"worker": {"leader"},
		}
		rev := BuildReverseDependencies(deps)
		// every node must be present
		assert.Contains(t, rev, "router")
		assert.Contains(t, rev, "leader")
		assert.Contains(t, rev, "worker")
		// router has one dependent: leader
		assert.ElementsMatch(t, []string{"leader"}, rev["router"])
		// leader has one dependent: worker
		assert.ElementsMatch(t, []string{"worker"}, rev["leader"])
		// worker has no dependents
		assert.Empty(t, rev["worker"])
	})

	t.Run("multiple dependents on one node", func(t *testing.T) {
		// both leader and worker startAfter router
		deps := map[string][]string{
			"router": nil,
			"leader": {"router"},
			"worker": {"router"},
		}
		rev := BuildReverseDependencies(deps)
		assert.ElementsMatch(t, []string{"leader", "worker"}, rev["router"])
		assert.Empty(t, rev["leader"])
		assert.Empty(t, rev["worker"])
	})

	t.Run("no dependencies", func(t *testing.T) {
		deps := map[string][]string{"a": nil, "b": nil}
		rev := BuildReverseDependencies(deps)
		assert.Empty(t, rev["a"])
		assert.Empty(t, rev["b"])
	})
}

// ---------------------------------------------------------------------------
// BuildDeletionGates
// ---------------------------------------------------------------------------

func TestBuildDeletionGates_OnlyStartDeps(t *testing.T) {
	// Pure startAfter: router→leader→worker
	// Gates should equal BuildReverseDependencies(startDeps)
	startDeps := map[string][]string{
		"router": nil,
		"leader": {"router"},
		"worker": {"leader"},
	}
	deleteDeps := map[string][]string{"router": nil, "leader": nil, "worker": nil}

	gates := BuildDeletionGates(startDeps, deleteDeps)
	assert.ElementsMatch(t, []string{"leader"}, gates["router"])
	assert.ElementsMatch(t, []string{"worker"}, gates["leader"])
	assert.Empty(t, gates["worker"])
}

func TestBuildDeletionGates_OnlyDeleteDeps(t *testing.T) {
	// No startAfter; leader and worker both deleteAfter router
	startDeps := map[string][]string{"router": nil, "leader": nil, "worker": nil}
	deleteDeps := map[string][]string{
		"router": nil,
		"leader": {"router"},
		"worker": {"router"},
	}

	gates := BuildDeletionGates(startDeps, deleteDeps)
	assert.Empty(t, gates["router"])
	assert.ElementsMatch(t, []string{"router"}, gates["leader"])
	assert.ElementsMatch(t, []string{"router"}, gates["worker"])
}

func TestBuildDeletionGates_BothFieldsMerge(t *testing.T) {
	// router startAfter [leader, worker] → reverse: leader/worker gated on router
	// leader deleteAfter [router]         → already covered by reverse, no duplicate
	// worker deleteAfter [router]         → already covered by reverse, no duplicate
	startDeps := map[string][]string{
		"router": {"leader", "worker"},
		"leader": nil,
		"worker": nil,
	}
	deleteDeps := map[string][]string{
		"router": nil,
		"leader": {"router"},
		"worker": {"router"},
	}

	gates := BuildDeletionGates(startDeps, deleteDeps)
	// router: no reverse deps, no deleteAfter → deleted first
	assert.Empty(t, gates["router"])
	// leader: reverse gives [router], deleteAfter also [router] → deduplicated → [router]
	assert.ElementsMatch(t, []string{"router"}, gates["leader"])
	// worker: same
	assert.ElementsMatch(t, []string{"router"}, gates["worker"])
}

func TestBuildDeletionGates_AddsNewConstraint(t *testing.T) {
	// Start order: A → B → C  (normal chain)
	// Delete extra: C also deleteAfter A (C must wait for both B and A to be gone)
	startDeps := map[string][]string{
		"a": nil,
		"b": {"a"},
		"c": {"b"},
	}
	deleteDeps := map[string][]string{
		"a": nil,
		"b": nil,
		"c": {"a"}, // explicit extra constraint
	}

	gates := BuildDeletionGates(startDeps, deleteDeps)
	// a: reverse of b.startAfter → [b]
	assert.ElementsMatch(t, []string{"b"}, gates["a"])
	// b: reverse of c.startAfter → [c]
	assert.ElementsMatch(t, []string{"c"}, gates["b"])
	// c: no reverse-of-start dependents; deleteAfter a → [a]
	assert.ElementsMatch(t, []string{"a"}, gates["c"])
}
