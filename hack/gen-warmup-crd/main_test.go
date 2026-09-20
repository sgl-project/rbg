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

package main

import (
	"testing"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
)

func itemSchema(required []string, xValidations apiextensionsv1.ValidationRules) apiextensionsv1.JSONSchemaProps {
	return apiextensionsv1.JSONSchemaProps{
		Type:     "object",
		Required: required,
		Properties: map[string]apiextensionsv1.JSONSchemaProps{
			"name":  {Type: "string"},
			"image": {Type: "string"},
		},
		XValidations: xValidations,
	}
}

func warmupCRD(targetNodes, roles *apiextensionsv1.JSONSchemaProps) *apiextensionsv1.CustomResourceDefinition {
	spec := apiextensionsv1.JSONSchemaProps{
		Type:       "object",
		Properties: map[string]apiextensionsv1.JSONSchemaProps{},
	}
	if targetNodes != nil {
		spec.Properties["targetNodes"] = *targetNodes
	}
	if roles != nil {
		spec.Properties["targetRoleBasedGroup"] = apiextensionsv1.JSONSchemaProps{
			Type: "object",
			Properties: map[string]apiextensionsv1.JSONSchemaProps{
				"roles": {
					Type: "object",
					AdditionalProperties: &apiextensionsv1.JSONSchemaPropsOrBool{
						Schema: roles,
					},
				},
			},
		}
	}
	return &apiextensionsv1.CustomResourceDefinition{
		Spec: apiextensionsv1.CustomResourceDefinitionSpec{
			Versions: []apiextensionsv1.CustomResourceDefinitionVersion{{
				Schema: &apiextensionsv1.CustomResourceValidation{
					OpenAPIV3Schema: &apiextensionsv1.JSONSchemaProps{
						Type:       "object",
						Properties: map[string]apiextensionsv1.JSONSchemaProps{"spec": spec},
					},
				},
			}},
		},
	}
}

func customizedAction(items apiextensionsv1.JSONSchemaProps) apiextensionsv1.JSONSchemaProps {
	return apiextensionsv1.JSONSchemaProps{
		Type: "object",
		Properties: map[string]apiextensionsv1.JSONSchemaProps{
			"customizedAction": {
				Type: "object",
				Properties: map[string]apiextensionsv1.JSONSchemaProps{
					"containers": {
						Type:  "array",
						Items: &apiextensionsv1.JSONSchemaPropsOrArray{Schema: &items},
					},
				},
			},
		},
	}
}

func celRule() apiextensionsv1.ValidationRules {
	return apiextensionsv1.ValidationRules{{Rule: "has(self.image) && self.image != ''", Message: celReasonToReplace}}
}

func TestReplacesCELItemRuleInBothTargets(t *testing.T) {
	crd := warmupCRD(
		func() *apiextensionsv1.JSONSchemaProps {
			s := customizedAction(itemSchema([]string{"name"}, celRule()))
			return &s
		}(),
		func() *apiextensionsv1.JSONSchemaProps {
			s := customizedAction(itemSchema([]string{"name"}, celRule()))
			return &s
		}(),
	)

	schemas, err := customizedContainerItemSchemas(crd)
	if err != nil {
		t.Fatalf("collect schemas: %v", err)
	}
	if len(schemas) != 2 {
		t.Fatalf("expected 2 customized container schemas, got %d", len(schemas))
	}
	for _, schema := range schemas {
		schema.XValidations = nil
		if err := requireNonBlankImage(schema); err != nil {
			t.Fatalf("requireNonBlankImage: %v", err)
		}
	}

	for i, schema := range schemas {
		if !contains(schema.Required, "image") {
			t.Errorf("schema %d: image must be required, got %v", i, schema.Required)
		}
		if !contains(schema.Required, "name") {
			t.Errorf("schema %d: name must stay required, got %v", i, schema.Required)
		}
		if got := schema.Properties["image"].Pattern; got != imagePattern {
			t.Errorf("schema %d: image pattern = %q, want %q", i, got, imagePattern)
		}
		if len(schema.XValidations) != 0 {
			t.Errorf("schema %d: CEL rule must be gone, got %#v", i, schema.XValidations)
		}
	}
}

func TestRequireNonBlankImageIsIdempotent(t *testing.T) {
	schema := itemSchema([]string{"name", "image"}, nil)
	for i := 0; i < 3; i++ {
		if err := requireNonBlankImage(&schema); err != nil {
			t.Fatalf("iteration %d: %v", i, err)
		}
	}
	want := []string{"name", "image"}
	if len(schema.Required) != len(want) {
		t.Fatalf("required duplicated: %v", schema.Required)
	}
	for i, r := range want {
		if schema.Required[i] != r {
			t.Fatalf("required = %v, want %v", schema.Required, want)
		}
	}
}

func TestMissingImagePropertyIsAnError(t *testing.T) {
	schema := apiextensionsv1.JSONSchemaProps{
		Type:       "object",
		Properties: map[string]apiextensionsv1.JSONSchemaProps{"name": {Type: "string"}},
	}
	if err := requireNonBlankImage(&schema); err == nil {
		t.Fatal("expected an error when the image property is absent")
	}
}

func TestNoCustomizedActionIsAnError(t *testing.T) {
	crd := warmupCRD(nil, nil)
	if _, err := customizedContainerItemSchemas(crd); err == nil {
		t.Fatal("expected an error when no customizedAction schema exists")
	}
}
