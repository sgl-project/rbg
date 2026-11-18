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

// Command models-schema prints the OpenAPI v2 document that hack/update-codegen.sh
// passes to applyconfiguration-gen through --applyconfig-openapi-schema. The
// generated apply configurations embed it, and Extract<Kind> and fake.NewClientset
// use it to track field ownership for server-side apply.
//
// kube-apiserver tracks field ownership of custom resources with a schema built from
// the CRD, not from the Go types, and the two differ: the CRDs keep some subtrees
// schemaless (x-kubernetes-preserve-unknown-fields), where the server tracks maps per
// key but each list as a whole, while the Go types would, for example, key pod
// template containers by name. Extracting server-recorded managed fields with a
// Go-derived schema loses fields.
//
// This command therefore builds the models from the CRD manifests with the same
// apiextensions-apiserver code the server uses (buildOpenAPIModelsForApply in
// k8s.io/apiextensions-apiserver/pkg/apiserver), and adapts them only where OpenAPI v2
// and applyconfiguration-gen require it:
//   - each kind definition is renamed to the REST-friendly name of its Go type, which
//     applyconfiguration-gen and runtime.Scheme.ToOpenAPIDefinitionName look up;
//   - references point to #/definitions, and a reference wrapped in a single-element
//     allOf becomes a plain reference;
//   - anyOf, oneOf, not and nullable are dropped: in a structural schema they only
//     carry value validation, and OpenAPI v2 has no equivalent.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apiextensions-apiserver/pkg/controller/openapi/builder"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/kube-openapi/pkg/spec3"
	"k8s.io/kube-openapi/pkg/util"
	"k8s.io/kube-openapi/pkg/validation/spec"
	"sigs.k8s.io/yaml"

	workloadsv1alpha1 "sigs.k8s.io/rbgs/api/workloads/v1alpha1"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
)

const (
	v3RefPrefix = "#/components/schemas/"
	v2RefPrefix = "#/definitions/"
)

func main() {
	crdDir := flag.String("crd-dir", "config/crd/bases", "directory with the CustomResourceDefinition manifests")
	flag.Parse()
	if err := run(*crdDir, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "Failed: %v\n", err)
		os.Exit(1)
	}
}

func run(crdDir string, out io.Writer) error {
	// Every API version with a CRD must be registered here, so that its kinds can be
	// renamed to their Go type names.
	scheme := runtime.NewScheme()
	for _, addToScheme := range []func(*runtime.Scheme) error{
		workloadsv1alpha1.AddToScheme,
		workloadsv1alpha2.AddToScheme,
	} {
		if err := addToScheme(scheme); err != nil {
			return err
		}
	}

	crds, err := loadCRDs(crdDir)
	if err != nil {
		return err
	}

	models := map[string]*spec.Schema{}
	renames := map[string]string{}
	for _, crd := range crds {
		crdModels, err := modelsForApply(crd)
		if err != nil {
			return fmt.Errorf("CRD %s: %w", crd.Name, err)
		}
		for name, model := range crdModels {
			if existing, ok := models[name]; ok {
				if !sameSchema(existing, model) {
					return fmt.Errorf("CRD %s: conflicting definitions of %s", crd.Name, name)
				}
				continue
			}
			models[name] = model
		}
		for _, version := range crd.Spec.Versions {
			for _, kind := range []string{crd.Spec.Names.Kind, crd.Spec.Names.ListKind} {
				gvk := schema.GroupVersionKind{Group: crd.Spec.Group, Version: version.Name, Kind: kind}
				goName, err := scheme.ToOpenAPIDefinitionName(gvk)
				if err != nil {
					return fmt.Errorf("CRD %s: no Go type registered for %s: %w", crd.Name, gvk, err)
				}
				crdName := util.ToRESTFriendlyName(fmt.Sprintf("%s/%s.%s", gvk.Group, gvk.Version, gvk.Kind))
				if _, ok := models[crdName]; !ok {
					return fmt.Errorf("CRD %s: no definition %s", crd.Name, crdName)
				}
				renames[crdName] = goName
			}
		}
	}

	definitions := make(spec.Definitions, len(models))
	for name, model := range models {
		if renamed, ok := renames[name]; ok {
			name = renamed
		}
		if _, ok := definitions[name]; ok {
			return fmt.Errorf("duplicate definition %s", name)
		}
		def, err := toV2(*model, renames)
		if err != nil {
			return fmt.Errorf("definition %s: %w", name, err)
		}
		definitions[name] = def
	}

	data, err := json.Marshal(&spec.Swagger{
		SwaggerProps: spec.SwaggerProps{
			Swagger: "2.0",
			Info: &spec.Info{
				InfoProps: spec.InfoProps{
					Title:   "RBG",
					Version: "unversioned",
				},
			},
			Definitions: definitions,
		},
	})
	if err != nil {
		return fmt.Errorf("error serializing api definitions: %w", err)
	}
	_, err = out.Write(data)
	return err
}

func loadCRDs(dir string) ([]*apiextensionsv1.CustomResourceDefinition, error) {
	files, err := filepath.Glob(filepath.Join(dir, "*.yaml"))
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("no CRD manifests in %s", dir)
	}
	crds := make([]*apiextensionsv1.CustomResourceDefinition, 0, len(files))
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			return nil, err
		}
		crd := &apiextensionsv1.CustomResourceDefinition{}
		if err := yaml.Unmarshal(data, crd); err != nil {
			return nil, fmt.Errorf("%s: %w", file, err)
		}
		if crd.Kind != "CustomResourceDefinition" {
			return nil, fmt.Errorf("%s: not a CustomResourceDefinition", file)
		}
		crds = append(crds, crd)
	}
	return crds, nil
}

// modelsForApply returns the models kube-apiserver uses for server-side apply on crd,
// following buildOpenAPIModelsForApply in
// k8s.io/apiextensions-apiserver/pkg/apiserver/customresource_handler.go. The server
// also passes its built-in definitions as the first spec, but MergeSpecsV3 skips specs
// without paths, so the result consists of the per-version specs, which include the
// meta/v1 definitions they reference.
func modelsForApply(crd *apiextensionsv1.CustomResourceDefinition) (map[string]*spec.Schema, error) {
	specs := make([]*spec3.OpenAPI, 0, len(crd.Spec.Versions))
	for _, version := range crd.Spec.Versions {
		s, err := builder.BuildOpenAPIV3(crd, version.Name, builder.Options{})
		if err != nil {
			return nil, err
		}
		specs = append(specs, s)
	}
	merged, err := builder.MergeSpecsV3(specs...)
	if err != nil {
		return nil, err
	}
	return merged.Components.Schemas, nil
}

func sameSchema(a, b *spec.Schema) bool {
	aJSON, aErr := json.Marshal(a)
	bJSON, bErr := json.Marshal(b)
	return aErr == nil && bErr == nil && bytes.Equal(aJSON, bJSON)
}

// toV2 converts a schema from the CRD models to OpenAPI v2 without changing how
// structured-merge-diff interprets it.
func toV2(s spec.Schema, renames map[string]string) (spec.Schema, error) {
	if len(s.PatternProperties) > 0 || len(s.Dependencies) > 0 || s.AdditionalItems != nil ||
		len(s.Definitions) > 0 || len(s.ExtraProps) > 0 {
		return spec.Schema{}, errors.New("unsupported OpenAPI construct")
	}

	ref := s.Ref.String()
	if ref == "" && len(s.AllOf) == 1 {
		ref = s.AllOf[0].Ref.String()
	}
	if ref != "" {
		// Like the server, keep only what structured-merge-diff reads next to a reference.
		name, ok := strings.CutPrefix(ref, v3RefPrefix)
		if !ok {
			return spec.Schema{}, fmt.Errorf("unexpected reference %s", ref)
		}
		if renamed, ok := renames[name]; ok {
			name = renamed
		}
		refSchema := spec.RefSchema(v2RefPrefix + name)
		refSchema.Description = s.Description
		refSchema.Default = s.Default
		refSchema.Extensions = s.Extensions
		return *refSchema, nil
	}

	s.AllOf, s.AnyOf, s.OneOf, s.Not = nil, nil, nil, nil
	s.Nullable = false
	if s.Properties != nil {
		properties := make(map[string]spec.Schema, len(s.Properties))
		for name, property := range s.Properties {
			converted, err := toV2(property, renames)
			if err != nil {
				return spec.Schema{}, fmt.Errorf("%s: %w", name, err)
			}
			properties[name] = converted
		}
		s.Properties = properties
	}
	if s.AdditionalProperties != nil && s.AdditionalProperties.Schema != nil {
		converted, err := toV2(*s.AdditionalProperties.Schema, renames)
		if err != nil {
			return spec.Schema{}, err
		}
		s.AdditionalProperties = &spec.SchemaOrBool{Allows: true, Schema: &converted}
	}
	if s.Items != nil {
		items := &spec.SchemaOrArray{}
		if s.Items.Schema != nil {
			converted, err := toV2(*s.Items.Schema, renames)
			if err != nil {
				return spec.Schema{}, err
			}
			items.Schema = &converted
		}
		for _, item := range s.Items.Schemas {
			converted, err := toV2(item, renames)
			if err != nil {
				return spec.Schema{}, err
			}
			items.Schemas = append(items.Schemas, converted)
		}
		s.Items = items
	}
	return s, nil
}
