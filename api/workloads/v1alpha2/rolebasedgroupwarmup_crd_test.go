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

func countCustomizedContainerImageRules(schema *apiextensionsv1.JSONSchemaProps, rule string) int {
	count := 0
	for _, validation := range schema.XValidations {
		if validation.Rule != rule {
			continue
		}
		if schema.Type != "object" {
			return -1
		}
		if _, ok := schema.Properties["image"]; !ok {
			return -1
		}
		count++
	}
	for _, property := range schema.Properties {
		nested := countCustomizedContainerImageRules(&property, rule)
		if nested < 0 {
			return -1
		}
		count += nested
	}
	if schema.AdditionalProperties != nil && schema.AdditionalProperties.Schema != nil {
		nested := countCustomizedContainerImageRules(schema.AdditionalProperties.Schema, rule)
		if nested < 0 {
			return -1
		}
		count += nested
	}
	if schema.Items != nil && schema.Items.Schema != nil {
		nested := countCustomizedContainerImageRules(schema.Items.Schema, rule)
		if nested < 0 {
			return -1
		}
		count += nested
	}
	return count
}

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

	validationRule := "has(self.image) && self.image != ''"
	if got := countCustomizedContainerImageRules(crd.Spec.Versions[0].Schema.OpenAPIV3Schema, validationRule); got != 2 {
		t.Fatalf("expected item-scoped customized container image validation in both target schemas, got %d", got)
	}
}
