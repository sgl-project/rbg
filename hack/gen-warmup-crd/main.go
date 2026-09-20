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

// Command gen-warmup-crd rewrites the generated RoleBasedGroupWarmup CRD so that a
// customizedAction container image is validated *structurally* (required + pattern)
// instead of with a CEL rule on list items.
//
// Why: the item-scoped CEL rule
//
//	+kubebuilder:validation:items:XValidation:rule="has(self.image) && self.image != ''"
//
// is re-evaluated on every write, including status-only updates. That makes any
// already-stored object whose customized container has a missing/blank image
// unwritable on Kubernetes <= 1.32: status updates are rejected with the CEL message,
// so the controller can no longer record phase or conditions and the object is stuck.
// A structural constraint is only checked when the field is written, so existing
// objects stay updateable while new writes are still rejected at admission time.
//
// controller-gen cannot express "required, and non-blank" on a slice item (the
// `items:Required` marker is not generated, and an array-level CEL rule exceeds the
// API server's rule-cost budget even with a small maxItems), so the generated CRD is
// patched here. This runs from `make manifests`, right after controller-gen.
//
// The generated file is a build artifact: re-run "make manifests" after changing the
// Warmup API markers.
package main

import (
	"errors"
	"fmt"
	"os"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"sigs.k8s.io/yaml"
)

const (
	// targetPath is the CRD emitted by controller-gen before this step.
	targetPath = "config/crd/bases/workloads.x-k8s.io_rolebasedgroupwarmups.yaml"

	// celReasonToReplace marks the rule this program is responsible for.
	celReasonToReplace = "customized action container image must not be empty"

	// imagePattern rejects empty and whitespace-only images, matching the
	// controller's strings.TrimSpace check. It allows any other value, including
	// references that contain internal spaces.
	imagePattern = `.*\S.*`
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "gen-warmup-crd: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	raw, err := os.ReadFile(targetPath)
	if err != nil {
		return fmt.Errorf("read %s: %w", targetPath, err)
	}

	var crd apiextensionsv1.CustomResourceDefinition
	if err := yaml.Unmarshal(raw, &crd); err != nil {
		return fmt.Errorf("%s: decode CRD: %w", targetPath, err)
	}

	schemas, err := customizedContainerItemSchemas(&crd)
	if err != nil {
		return err
	}

	patched := 0
	for _, schema := range schemas {
		// Drop the item-scoped CEL rule so stored objects remain writable.
		kept := schema.XValidations[:0]
		for _, validation := range schema.XValidations {
			if validation.Message == celReasonToReplace {
				patched++
				continue
			}
			kept = append(kept, validation)
		}
		if len(kept) == 0 {
			schema.XValidations = nil
		} else {
			schema.XValidations = kept
		}

		if err := requireNonBlankImage(schema); err != nil {
			return err
		}
	}

	if patched == 0 {
		return errors.New("no customizedAction container CEL rule found to replace; " +
			"if the API marker changed, update gen-warmup-crd")
	}

	out, err := yaml.Marshal(&crd)
	if err != nil {
		return fmt.Errorf("encode CRD: %w", err)
	}
	if err := os.WriteFile(targetPath, out, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", targetPath, err)
	}
	fmt.Printf("gen-warmup-crd: replaced %d customized-container CEL rule(s) with structural validation in %s\n",
		patched, targetPath)
	return nil
}

// customizedContainerItemSchemas walks the CRD and returns the `containers` item
// schema of every customizedAction, so both the targetNodes and
// targetRoleBasedGroup.roles paths are covered.
func customizedContainerItemSchemas(crd *apiextensionsv1.CustomResourceDefinition) ([]*apiextensionsv1.JSONSchemaProps, error) {
	if len(crd.Spec.Versions) == 0 || crd.Spec.Versions[0].Schema == nil || crd.Spec.Versions[0].Schema.OpenAPIV3Schema == nil {
		return nil, fmt.Errorf("%s: unexpected version schema", targetPath)
	}

	var (
		out  []*apiextensionsv1.JSONSchemaProps
		walk func(schema *apiextensionsv1.JSONSchemaProps)
	)
	walk = func(schema *apiextensionsv1.JSONSchemaProps) {
		if ca, ok := schema.Properties["customizedAction"]; ok {
			if containers, ok := ca.Properties["containers"]; ok && containers.Items != nil && containers.Items.Schema != nil {
				out = append(out, containers.Items.Schema)
			}
		}
		for name := range schema.Properties {
			prop := schema.Properties[name]
			walk(&prop)
			schema.Properties[name] = prop
		}
		if schema.AdditionalProperties != nil && schema.AdditionalProperties.Schema != nil {
			walk(schema.AdditionalProperties.Schema)
		}
		if schema.Items != nil && schema.Items.Schema != nil {
			walk(schema.Items.Schema)
		}
	}
	walk(crd.Spec.Versions[0].Schema.OpenAPIV3Schema)

	if len(out) == 0 {
		return nil, fmt.Errorf("%s: no customizedAction.containers schema found", targetPath)
	}
	return out, nil
}

// requireNonBlankImage makes the container image required and non-blank without CEL.
func requireNonBlankImage(schema *apiextensionsv1.JSONSchemaProps) error {
	image, ok := schema.Properties["image"]
	if !ok {
		return errors.New("customizedAction container has no image property")
	}
	if image.Type != "string" {
		return fmt.Errorf("customizedAction container image has unexpected type %q", image.Type)
	}

	required := schema.Required
	if !contains(required, "image") {
		required = append(append([]string{}, required...), "image")
	}
	schema.Required = required

	image.Pattern = imagePattern
	schema.Properties["image"] = image
	return nil
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
