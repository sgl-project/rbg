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
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestGenerateRBG(t *testing.T) {
	tests := []struct {
		name     string
		scenario *Scenario
		index    int
		wantName string
		checkFn  func(t *testing.T, rbg *unstructured.Unstructured)
	}{
		{
			name: "basic standalone",
			scenario: &Scenario{
				Namespace:    "test-ns",
				RolesPerRBG:  2,
				LWSRoles:     0,
				LWSSize:      4,
				UseKwokNodes: true,
			},
			index:    0,
			wantName: "stress-rbg-0000",
			checkFn: func(t *testing.T, rbg *unstructured.Unstructured) {
				roles, _, _ := unstructured.NestedSlice(rbg.Object, "spec", "roles")
				if len(roles) != 2 {
					t.Errorf("expected 2 roles, got %d", len(roles))
				}
				// Check standalone pattern exists
				role := roles[0].(map[string]interface{})
				if _, found := role["standalonePattern"]; !found {
					t.Error("expected standalonePattern on first role")
				}
				if _, found := role["leaderWorkerPattern"]; found {
					t.Error("unexpected leaderWorkerPattern on standalone role")
				}
			},
		},
		{
			name: "mixed lws and standalone",
			scenario: &Scenario{
				Namespace:    "test-ns",
				RolesPerRBG:  3,
				LWSRoles:     1,
				LWSSize:      4,
				UseKwokNodes: false,
			},
			index:    42,
			wantName: "stress-rbg-0042",
			checkFn: func(t *testing.T, rbg *unstructured.Unstructured) {
				roles, _, _ := unstructured.NestedSlice(rbg.Object, "spec", "roles")
				if len(roles) != 3 {
					t.Errorf("expected 3 roles, got %d", len(roles))
				}
				// First role should be LWS
				role0 := roles[0].(map[string]interface{})
				if _, found := role0["leaderWorkerPattern"]; !found {
					t.Error("expected leaderWorkerPattern on first role")
				}
				// Second role should be standalone
				role1 := roles[1].(map[string]interface{})
				if _, found := role1["standalonePattern"]; !found {
					t.Error("expected standalonePattern on second role")
				}
			},
		},
		{
			name: "in-place update strategy",
			scenario: &Scenario{
				Namespace:     "test-ns",
				RolesPerRBG:   1,
				LWSRoles:      0,
				LWSSize:       4,
				InPlaceUpdate: true,
				UseKwokNodes:  false,
			},
			index:    0,
			wantName: "stress-rbg-0000",
			checkFn: func(t *testing.T, rbg *unstructured.Unstructured) {
				roles, _, _ := unstructured.NestedSlice(rbg.Object, "spec", "roles")
				role := roles[0].(map[string]interface{})
				strategy, found, _ := unstructured.NestedMap(role, "rolloutStrategy")
				if !found {
					t.Fatal("expected rolloutStrategy on role when InPlaceUpdate is true")
				}
				if strategy["type"] != "RollingUpdate" {
					t.Errorf("expected RollingUpdate type, got %v", strategy["type"])
				}
			},
		},
		{
			name: "kwok scheduling",
			scenario: &Scenario{
				Namespace:    "test-ns",
				RolesPerRBG:  1,
				LWSRoles:     0,
				LWSSize:      4,
				UseKwokNodes: true,
			},
			index:    0,
			wantName: "stress-rbg-0000",
			checkFn: func(t *testing.T, rbg *unstructured.Unstructured) {
				roles, _, _ := unstructured.NestedSlice(rbg.Object, "spec", "roles")
				role := roles[0].(map[string]interface{})
				nodeSelector, found, _ := unstructured.NestedMap(role, "standalonePattern", "template", "spec", "nodeSelector")
				if !found {
					t.Fatal("expected nodeSelector when UseKwokNodes is true")
				}
				if nodeSelector["type"] != "kwok" {
					t.Errorf("expected nodeSelector type=kwok, got %v", nodeSelector["type"])
				}
			},
		},
		{
			name: "no kwok scheduling",
			scenario: &Scenario{
				Namespace:    "test-ns",
				RolesPerRBG:  1,
				LWSRoles:     0,
				LWSSize:      4,
				UseKwokNodes: false,
			},
			index:    0,
			wantName: "stress-rbg-0000",
			checkFn: func(t *testing.T, rbg *unstructured.Unstructured) {
				roles, _, _ := unstructured.NestedSlice(rbg.Object, "spec", "roles")
				role := roles[0].(map[string]interface{})
				_, found, _ := unstructured.NestedMap(role, "standalonePattern", "template", "spec", "nodeSelector")
				if found {
					t.Error("unexpected nodeSelector when UseKwokNodes is false")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rbg := GenerateRBG(tt.index, tt.scenario)
			if rbg.GetName() != tt.wantName {
				t.Errorf("name = %s, want %s", rbg.GetName(), tt.wantName)
			}
			if rbg.GetNamespace() != tt.scenario.Namespace {
				t.Errorf("namespace = %s, want %s", rbg.GetNamespace(), tt.scenario.Namespace)
			}
			if tt.checkFn != nil {
				tt.checkFn(t, rbg)
			}
		})
	}
}

func TestApplyUpdate(t *testing.T) {
	tests := []struct {
		name    string
		inPlace bool
		checkFn func(t *testing.T, rbg *unstructured.Unstructured)
	}{
		{
			name:    "in-place updates image",
			inPlace: true,
			checkFn: func(t *testing.T, rbg *unstructured.Unstructured) {
				roles, _, _ := unstructured.NestedSlice(rbg.Object, "spec", "roles")
				role := roles[0].(map[string]interface{})
				containers, _, _ := unstructured.NestedSlice(role, "standalonePattern", "template", "spec", "containers")
				container := containers[0].(map[string]interface{})
				if container["image"] != testImageUpdated {
					t.Errorf("image = %v, want %s", container["image"], testImageUpdated)
				}
			},
		},
		{
			name:    "rolling update adds annotation",
			inPlace: false,
			checkFn: func(t *testing.T, rbg *unstructured.Unstructured) {
				roles, _, _ := unstructured.NestedSlice(rbg.Object, "spec", "roles")
				role := roles[0].(map[string]interface{})
				ann, _, _ := unstructured.NestedStringMap(role, "standalonePattern", "template", "metadata", "annotations")
				if ann["stress-test-update"] != "true" {
					t.Errorf("annotation stress-test-update = %v, want true", ann["stress-test-update"])
				}
				// Image should NOT be changed for rolling update
				containers, _, _ := unstructured.NestedSlice(role, "standalonePattern", "template", "spec", "containers")
				container := containers[0].(map[string]interface{})
				if container["image"] != testImage {
					t.Errorf("image = %v, want %s (unchanged)", container["image"], testImage)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scenario := &Scenario{
				Namespace:    "test-ns",
				RolesPerRBG:  1,
				LWSRoles:     0,
				LWSSize:      4,
				UseKwokNodes: false,
			}
			rbg := GenerateRBG(0, scenario)
			ApplyUpdate(rbg, tt.inPlace)
			tt.checkFn(t, rbg)
		})
	}
}

func TestApplyUpdateLWS(t *testing.T) {
	scenario := &Scenario{
		Namespace:    "test-ns",
		RolesPerRBG:  1,
		LWSRoles:     1,
		LWSSize:      4,
		UseKwokNodes: false,
	}

	t.Run("in-place updates LWS image", func(t *testing.T) {
		rbg := GenerateRBG(0, scenario)
		ApplyUpdate(rbg, true)

		roles, _, _ := unstructured.NestedSlice(rbg.Object, "spec", "roles")
		role := roles[0].(map[string]interface{})
		containers, _, _ := unstructured.NestedSlice(role, "leaderWorkerPattern", "template", "spec", "containers")
		container := containers[0].(map[string]interface{})
		if container["image"] != testImageUpdated {
			t.Errorf("LWS image = %v, want %s", container["image"], testImageUpdated)
		}
	})

	t.Run("rolling update adds LWS annotation", func(t *testing.T) {
		rbg := GenerateRBG(0, scenario)
		ApplyUpdate(rbg, false)

		roles, _, _ := unstructured.NestedSlice(rbg.Object, "spec", "roles")
		role := roles[0].(map[string]interface{})
		ann, _, _ := unstructured.NestedStringMap(role, "leaderWorkerPattern", "template", "metadata", "annotations")
		if ann["stress-test-update"] != "true" {
			t.Errorf("LWS annotation = %v, want true", ann["stress-test-update"])
		}
	})
}
