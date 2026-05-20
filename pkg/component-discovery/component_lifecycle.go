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
	"fmt"

	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
)

const (
	// ComponentDependsOnAnnotationKey is set on an InstanceComponent's pod template to declare
	// start and/or delete ordering constraints relative to sibling components in the same role.
	//
	// startAfter: this component's pods are created only after all listed components have
	//   ReadyReplicas == Size in the instance's componentStatuses. By default (when deleteAfter is absent),
	//   scale-down order is the reverse: this component is deleted before any component it listed
	//   in startAfter.
	//
	// deleteAfter: this component's pods are deleted only after all listed components have been
	//   fully deleted (ToDeleteIDs == 0). This field is independent of startAfter and can be
	//   used to express delete ordering that differs from the reverse of the start order.
	//   When both fields are set, both constraints are applied (union).
	//
	// Default behavior (annotation absent): no ordering constraints — all components are
	// created and deleted concurrently (same as existing behavior).
	//
	// Cycle detection: if the start or deletion dependency graph contains a cycle the controller
	// logs an error and falls back to the default parallel mode to avoid a deadlock.
	//
	// Value format: JSON object with optional startAfter and/or deleteAfter arrays.
	//
	// Examples:
	//   # worker starts after leader:
	//   rolebasedgroup.workloads.x-k8s.io/component-depends-on: |
	//     {"startAfter": ["leader"]}
	//
	//   # leader/worker start in parallel; leader is deleted after router is gone:
	//   rolebasedgroup.workloads.x-k8s.io/component-depends-on: |
	//     {"deleteAfter": ["router"]}
	//
	//   # both constraints combined:
	//   rolebasedgroup.workloads.x-k8s.io/component-depends-on: |
	//     {"startAfter": ["router"], "deleteAfter": ["router"]}
	//
	// Constraint: only meaningful within CustomComponentsPattern roles.
	ComponentDependsOnAnnotationKey = "rolebasedgroup.workloads.x-k8s.io/component-depends-on"
)

// ComponentDependsOnConfig is the JSON-encoded value of ComponentDependsOnAnnotationKey.
type ComponentDependsOnConfig struct {
	// StartAfter lists components that must have ReadyReplicas == Size in the instance's
	// componentStatuses before this component's pods are created.
	// When deleteAfter is absent, scale-down order is the reverse: this component
	// is deleted before any component in StartAfter.
	// +optional
	StartAfter []string `json:"startAfter,omitempty"`

	// DeleteAfter lists components that must be fully deleted (no excess pods remaining)
	// before this component's pods are deleted. Independent of StartAfter — use this to
	// express delete ordering that differs from (or complements) the reverse start order.
	// When both StartAfter and DeleteAfter are set, both constraints are applied (union).
	// +optional
	DeleteAfter []string `json:"deleteAfter,omitempty"`
}

// ParseAllComponentDependencies parses both startAfter and deleteAfter from every component's
// annotation in a single pass and returns two forward dependency maps:
//
//	startDeps[X]  = list of components X must start after (from startAfter)
//	deleteDeps[X] = list of components X must be deleted after (from deleteAfter)
//
// Every component appears as a key in both maps, even when its annotation is absent.
// The annotation is read from the component's top-level Annotations field first;
// if absent there it falls back to Template.Metadata.Annotations for backward compatibility.
func ParseAllComponentDependencies(components []workloadsv1alpha2.RoleInstanceComponent) (startDeps, deleteDeps map[string][]string, err error) {
	startDeps = make(map[string][]string, len(components))
	deleteDeps = make(map[string][]string, len(components))
	for _, comp := range components {
		startDeps[comp.Name] = nil  // ensure all nodes appear in the graph
		deleteDeps[comp.Name] = nil // ensure all nodes appear in the graph
		raw := componentAnnotation(comp, ComponentDependsOnAnnotationKey)
		if raw == "" {
			continue
		}
		cfg := &ComponentDependsOnConfig{}
		if err = json.Unmarshal([]byte(raw), cfg); err != nil {
			return nil, nil, fmt.Errorf("component %q: invalid %s annotation JSON: %w",
				comp.Name, ComponentDependsOnAnnotationKey, err)
		}
		startDeps[comp.Name] = cfg.StartAfter
		deleteDeps[comp.Name] = cfg.DeleteAfter
	}
	return startDeps, deleteDeps, nil
}

// ParseComponentDependencies is a convenience wrapper that returns only the startAfter
// dependency graph. Use ParseAllComponentDependencies when deleteAfter is also needed.
func ParseComponentDependencies(components []workloadsv1alpha2.RoleInstanceComponent) (map[string][]string, error) {
	startDeps, _, err := ParseAllComponentDependencies(components)
	return startDeps, err
}

// HasAnyDependency returns true when at least one component carries a non-empty
// ComponentDependsOnAnnotationKey annotation with at least one startAfter or deleteAfter entry.
func HasAnyDependency(components []workloadsv1alpha2.RoleInstanceComponent) bool {
	for _, comp := range components {
		raw := componentAnnotation(comp, ComponentDependsOnAnnotationKey)
		if raw == "" {
			continue
		}
		cfg := &ComponentDependsOnConfig{}
		if err := json.Unmarshal([]byte(raw), cfg); err != nil {
			continue
		}
		if len(cfg.StartAfter) > 0 || len(cfg.DeleteAfter) > 0 {
			return true
		}
	}
	return false
}

// componentAnnotation returns the value of key from the component's top-level Annotations
// map.  If not found there it falls back to Template.Metadata.Annotations for backward
// compatibility with components created before the top-level Annotations field existed.
func componentAnnotation(comp workloadsv1alpha2.RoleInstanceComponent, key string) string {
	if v, ok := comp.Annotations[key]; ok {
		return v
	}
	return comp.Template.Annotations[key]
}

// DetectCycle reports whether the directed dependency graph contains a cycle.
//
// The graph is represented as: deps[node] = list of nodes that node depends on
// (i.e. directed edges point from a component to its dependencies).
// A cycle means at least two components transitively depend on each other,
// which would cause a deadlock in ordered lifecycle management.
//
// The implementation uses recursive DFS with a three-colour scheme:
//
//	white (0) — not yet visited
//	gray  (1) — currently on the DFS stack
//	black (2) — fully processed
//
// A back-edge to a gray node indicates a cycle.
func DetectCycle(deps map[string][]string) bool {
	const (
		white = 0
		gray  = 1
		black = 2
	)
	color := make(map[string]int, len(deps))

	var dfs func(node string) bool
	dfs = func(node string) bool {
		color[node] = gray
		for _, dep := range deps[node] {
			switch color[dep] {
			case gray:
				return true // back-edge → cycle detected
			case white:
				if dfs(dep) {
					return true
				}
			}
		}
		color[node] = black
		return false
	}

	for node := range deps {
		if color[node] == white {
			if dfs(node) {
				return true
			}
		}
	}
	return false
}

// BuildReverseDependencies returns the transposed graph of deps.
//
// If deps["worker"] = ["leader"] (worker startAfter leader), then
// reverse["leader"] = ["worker"] (leader has a dependent: worker must be removed first).
//
// The reverse graph is used during scale-down: a component can be deleted only after
// all components that list it in their startAfter have been fully removed.
func BuildReverseDependencies(deps map[string][]string) map[string][]string {
	rev := make(map[string][]string, len(deps))
	// Ensure every node appears, even if it has no dependents.
	for node := range deps {
		if _, ok := rev[node]; !ok {
			rev[node] = nil
		}
	}
	for node, nodeDeps := range deps {
		for _, dep := range nodeDeps {
			rev[dep] = append(rev[dep], node)
		}
	}
	return rev
}

// BuildDeletionGates returns the final deletion constraint graph for each component.
//
//	gates[X] = set of components that must be fully deleted before X can be deleted
//
// The gates are built from two sources (union of both):
//  1. Reverse of startDeps: if X.startAfter contains Y, then Y's deletion is gated on X
//     (X was started after Y, so X must be torn down before Y).
//  2. Explicit deleteDeps[X]: components that X declared in deleteAfter. These are merged
//     with the auto-derived constraints so that either field alone is sufficient.
//
// Example (the scenario where router starts last but is deleted first):
//
//	router.startAfter = ["leader", "worker"]  → reverseOf: leader/worker gated on router
//	leader.deleteAfter = ["router"]            → leader also explicitly waits for router
//	worker.deleteAfter = ["router"]            → worker also explicitly waits for router
//
//	Result:
//	  gates["router"]  = []          (nothing to wait for → deleted first)
//	  gates["leader"]  = ["router"]  (waits for router → deleted after router)
//	  gates["worker"]  = ["router"]  (waits for router → deleted after router)
func BuildDeletionGates(startDeps, deleteDeps map[string][]string) map[string][]string {
	// Start with the reverse-of-start graph.
	gates := BuildReverseDependencies(startDeps)

	// Merge in explicit deleteAfter entries, deduplicating as we go.
	for node, deps := range deleteDeps {
		if len(deps) == 0 {
			continue
		}
		existing := make(map[string]struct{}, len(gates[node]))
		for _, d := range gates[node] {
			existing[d] = struct{}{}
		}
		for _, d := range deps {
			if _, dup := existing[d]; !dup {
				gates[node] = append(gates[node], d)
				existing[d] = struct{}{}
			}
		}
	}
	return gates
}
