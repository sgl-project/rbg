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

package v1alpha2

import (
	"os"
	"testing"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"sigs.k8s.io/yaml"
)

// findCustomizedContainerSchemas returns the `containers` items schema for every
// customizedAction in the Warmup CRD (targetNodes and targetRoleBasedGroup.roles).
func findCustomizedContainerSchemas(schema *apiextensionsv1.JSONSchemaProps) []*apiextensionsv1.JSONSchemaProps {
	var out []*apiextensionsv1.JSONSchemaProps
	if ca, ok := schema.Properties["customizedAction"]; ok {
		if containers, ok := ca.Properties["containers"]; ok && containers.Items != nil && containers.Items.Schema != nil {
			out = append(out, containers.Items.Schema)
		}
	}
	for name := range schema.Properties {
		prop := schema.Properties[name]
		out = append(out, findCustomizedContainerSchemas(&prop)...)
	}
	if schema.AdditionalProperties != nil && schema.AdditionalProperties.Schema != nil {
		out = append(out, findCustomizedContainerSchemas(schema.AdditionalProperties.Schema)...)
	}
	if schema.Items != nil && schema.Items.Schema != nil {
		out = append(out, findCustomizedContainerSchemas(schema.Items.Schema)...)
	}
	return out
}

// Customized container images must be validated structurally (required + pattern), not by
// an item-scoped CEL rule. A CEL rule on list items is re-evaluated on every write,
// including status-only updates, which makes any already-stored object with a
// missing/blank container image unwritable on Kubernetes <= 1.32. A structural
// constraint is checked only when the field is written, so legacy objects stay
// writable while new writes are still rejected. hack/gen-warmup-crd enforces this.
func TestGeneratedWarmupCRDValidatesCustomizedContainerImages(t *testing.T) {
	manifest, err := os.ReadFile("../../../config/crd/bases/workloads.x-k8s.io_rolebasedgroupwarmups.yaml")
	if err != nil {
		t.Fatalf("read generated Warmup CRD: %v", err)
	}

	var crd apiextensionsv1.CustomResourceDefinition
	if err := yaml.Unmarshal(manifest, &crd); err != nil {
		t.Fatalf("decode generated Warmup CRD: %v", err)
	}
	if len(crd.Spec.Versions) != 1 || crd.Spec.Versions[0].Schema == nil || crd.Spec.Versions[0].Schema.OpenAPIV3Schema == nil {
		t.Fatalf("generated Warmup CRD has unexpected version schema: %#v", crd.Spec.Versions)
	}

	items := findCustomizedContainerSchemas(crd.Spec.Versions[0].Schema.OpenAPIV3Schema)
	if len(items) != 2 {
		t.Fatalf("expected customized container schemas in both target schemas, got %d", len(items))
	}

	for i, item := range items {
		if len(item.XValidations) != 0 {
			t.Errorf("schema %d: customized container must not carry a CEL rule (it would freeze pre-existing objects on k8s <=1.32); got %#v", i, item.XValidations)
		}

		required := map[string]bool{}
		for _, r := range item.Required {
			required[r] = true
		}
		if !required["image"] {
			t.Errorf("schema %d: customized container `image` must be required, got required=%v", i, item.Required)
		}
		if _, ok := item.Properties["image"]; !ok {
			t.Fatalf("schema %d: customized container has no image property", i)
		}
		if got := item.Properties["image"].Pattern; got != `.*\S.*` {
			t.Errorf("schema %d: customized container image pattern must reject blank values, got %q", i, got)
		}
	}
}
