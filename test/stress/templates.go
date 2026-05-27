/*
Copyright 2025 The RBG Authors.

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

package main

import (
	"fmt"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

const (
	testImage        = "registry.k8s.io/pause:3.9"
	testImageUpdated = "registry.k8s.io/pause:3.10"
)

// GenerateRBG creates an unstructured RBG object for stress testing.
func GenerateRBG(index int, scenario *Scenario) *unstructured.Unstructured {
	name := fmt.Sprintf("stress-rbg-%04d", index)

	roles := make([]interface{}, 0, scenario.RolesPerRBG)

	for i := 0; i < scenario.RolesPerRBG; i++ {
		roleName := fmt.Sprintf("role-%d", i)

		if i < scenario.LWSRoles {
			// LeaderWorkerPattern role
			role := buildLWSRole(roleName, scenario)
			roles = append(roles, role)
		} else {
			// Standalone pattern role
			role := buildStandaloneRole(roleName, scenario)
			roles = append(roles, role)
		}
	}

	rbg := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "workloads.x-k8s.io/v1alpha2",
			"kind":       "RoleBasedGroup",
			"metadata": map[string]interface{}{
				"name":      name,
				"namespace": scenario.Namespace,
				"labels": map[string]interface{}{
					"stress-test": "true",
					"test-index":  fmt.Sprintf("%d", index),
				},
			},
			"spec": map[string]interface{}{
				"roles": roles,
			},
		},
	}

	// Add rollout strategy if in-place update is enabled
	if scenario.InPlaceUpdate {
		spec := rbg.Object["spec"].(map[string]interface{})
		spec["rolloutStrategy"] = map[string]interface{}{
			"type": "RollingUpdate",
			"rollingUpdate": map[string]interface{}{
				"type": "InPlaceIfPossible",
			},
		}
	}

	return rbg
}

func buildStandaloneRole(name string, scenario *Scenario) map[string]interface{} {
	return map[string]interface{}{
		"name":     name,
		"replicas": int64(1),
		"annotations": map[string]interface{}{
			"rbg.workloads.x-k8s.io/role-workload-type": "apps/v1/Deployment",
		},
		"standalonePattern": map[string]interface{}{
			"template": map[string]interface{}{
				"metadata": map[string]interface{}{
					"labels": map[string]interface{}{
						"stress-test": "true",
						"role":        name,
					},
				},
				"spec": podSpec(scenario),
			},
		},
	}
}

func buildLWSRole(name string, scenario *Scenario) map[string]interface{} {
	return map[string]interface{}{
		"name":     name,
		"replicas": int64(1),
		"annotations": map[string]interface{}{
			"rbg.workloads.x-k8s.io/role-workload-type": "leaderworkerset.x-k8s.io/v1/LeaderWorkerSet",
		},
		"leaderWorkerPattern": map[string]interface{}{
			"size": int64(scenario.LWSSize),
			"template": map[string]interface{}{
				"metadata": map[string]interface{}{
					"labels": map[string]interface{}{
						"stress-test": "true",
						"role":        name,
					},
				},
				"spec": podSpec(scenario),
			},
		},
	}
}

// podSpec builds the pod spec with optional kwok scheduling fields.
func podSpec(scenario *Scenario) map[string]interface{} {
	spec := map[string]interface{}{
		"containers": []interface{}{
			map[string]interface{}{
				"name":  "main",
				"image": testImage,
			},
		},
	}

	if scenario.UseKwokNodes {
		spec["nodeSelector"] = map[string]interface{}{
			"type": "kwok",
		}
		spec["tolerations"] = []interface{}{
			map[string]interface{}{
				"key":      "kwok.x-k8s.io/node",
				"operator": "Exists",
				"effect":   "NoSchedule",
			},
		}
	}

	return spec
}

// ApplyUpdate modifies an RBG for the update phase.
// For in-place update: changes container image.
// For rolling update: changes a label to trigger recreation.
func ApplyUpdate(rbg *unstructured.Unstructured, inPlace bool) {
	roles, _, _ := unstructured.NestedSlice(rbg.Object, "spec", "roles")
	for i, r := range roles {
		role, ok := r.(map[string]interface{})
		if !ok {
			continue
		}

		if inPlace {
			// Update image to trigger in-place update
			updateImageInRole(role)
		} else {
			// Update annotation to trigger rolling update
			updateAnnotationInRole(role)
		}
		roles[i] = role
	}
	_ = unstructured.SetNestedSlice(rbg.Object, roles, "spec", "roles")
}

func updateImageInRole(role map[string]interface{}) {
	// Try standalonePattern
	containers, found, _ := unstructured.NestedSlice(role, "standalonePattern", "template", "spec", "containers")
	if found {
		for i, c := range containers {
			container, ok := c.(map[string]interface{})
			if !ok {
				continue
			}
			container["image"] = testImageUpdated
			containers[i] = container
		}
		_ = unstructured.SetNestedSlice(role, containers, "standalonePattern", "template", "spec", "containers")
		return
	}

	// Try leaderWorkerPattern
	containers, found, _ = unstructured.NestedSlice(role, "leaderWorkerPattern", "template", "spec", "containers")
	if found {
		for i, c := range containers {
			container, ok := c.(map[string]interface{})
			if !ok {
				continue
			}
			container["image"] = testImageUpdated
			containers[i] = container
		}
		_ = unstructured.SetNestedSlice(role, containers, "leaderWorkerPattern", "template", "spec", "containers")
	}
}

func updateAnnotationInRole(role map[string]interface{}) {
	// Only update the pattern that actually exists in this role
	if _, found, _ := unstructured.NestedMap(role, "standalonePattern"); found {
		ann, _, _ := unstructured.NestedStringMap(role, "standalonePattern", "template", "metadata", "annotations")
		if ann == nil {
			ann = map[string]string{}
		}
		ann["stress-test-update"] = "true"
		_ = unstructured.SetNestedStringMap(role, ann, "standalonePattern", "template", "metadata", "annotations")
	}

	if _, found, _ := unstructured.NestedMap(role, "leaderWorkerPattern"); found {
		ann, _, _ := unstructured.NestedStringMap(role, "leaderWorkerPattern", "template", "metadata", "annotations")
		if ann == nil {
			ann = map[string]string{}
		}
		ann["stress-test-update"] = "true"
		_ = unstructured.SetNestedStringMap(role, ann, "leaderWorkerPattern", "template", "metadata", "annotations")
	}
}
