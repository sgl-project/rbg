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

// Command models-schema prints the OpenAPI definitions from pkg/generated/openapi
// as one OpenAPI v2 document on stdout.
//
// hack/update-codegen.sh passes the output to applyconfiguration-gen through
// --applyconfig-openapi-schema. Without it, the generated apply configurations
// carry no schema for RBG types, so the field-managed fake clientset
// (fake.NewClientset) cannot handle them and no Extract<Kind> helpers are
// generated.
//
// Adapted from k8s.io/kubernetes/pkg/generated/openapi/cmd/models-schema.
package main

import (
	"encoding/json"
	"fmt"
	"os"

	"k8s.io/kube-openapi/pkg/common"
	"k8s.io/kube-openapi/pkg/util"
	"k8s.io/kube-openapi/pkg/validation/spec"

	"sigs.k8s.io/rbgs/pkg/generated/openapi"
)

func main() {
	if err := output(); err != nil {
		fmt.Fprintf(os.Stderr, "Failed: %v\n", err)
		os.Exit(1)
	}
}

func output() error {
	// Definitions are keyed by the same REST-friendly names that applyconfiguration-gen
	// and runtime.Scheme.ToOpenAPIDefinitionName derive from Go type names, e.g.
	// io.k8s.sigs.rbgs.api.workloads.v1alpha2.RoleBasedGroup.
	refFunc := func(name string) spec.Ref {
		return spec.MustCreateRef("#/definitions/" + util.ToRESTFriendlyName(name))
	}
	defs := openapi.GetOpenAPIDefinitions(refFunc)
	schemaDefs := make(map[string]spec.Schema, len(defs))
	for name, def := range defs {
		// Prefer the embedded v2 schema, if any, so the output is always valid OpenAPI v2.
		if schema, ok := def.Schema.Extensions[common.ExtensionV2Schema]; ok {
			if v2Schema, isOpenAPISchema := schema.(spec.Schema); isOpenAPISchema {
				schemaDefs[util.ToRESTFriendlyName(name)] = v2Schema
				continue
			}
		}
		schemaDefs[util.ToRESTFriendlyName(name)] = def.Schema
	}

	data, err := json.Marshal(&spec.Swagger{
		SwaggerProps: spec.SwaggerProps{
			Definitions: schemaDefs,
			Info: &spec.Info{
				InfoProps: spec.InfoProps{
					Title:   "RBG",
					Version: "unversioned",
				},
			},
			Swagger: "2.0",
		},
	})
	if err != nil {
		return fmt.Errorf("error serializing api definitions: %w", err)
	}
	_, err = os.Stdout.Write(data)
	return err
}
