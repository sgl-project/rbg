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

// These tests compare the schema embedded in the generated apply configurations with
// the one kube-apiserver uses for server-side apply on the RBG custom resources, which
// it builds from the CRDs. If the two track fields differently, Extract<Kind> returns
// the wrong fields from server-recorded managed fields, and fake.NewClientset merges
// differently from a real cluster.
package clientgo_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"

	"github.com/google/go-cmp/cmp"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apiextensions-apiserver/pkg/controller/openapi/builder"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/managedfields"
	"k8s.io/apimachinery/pkg/util/managedfields/managedfieldstest"
	"k8s.io/kube-openapi/pkg/spec3"
	"k8s.io/kube-openapi/pkg/validation/spec"
	"sigs.k8s.io/structured-merge-diff/v6/fieldpath"
	"sigs.k8s.io/structured-merge-diff/v6/typed"
	"sigs.k8s.io/yaml"

	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	"sigs.k8s.io/rbgs/client-go/applyconfiguration"
	applyv1alpha2 "sigs.k8s.io/rbgs/client-go/applyconfiguration/workloads/v1alpha2"
	"sigs.k8s.io/rbgs/client-go/clientset/versioned/fake"
)

// crdDir holds the CRDs kube-apiserver serves the RBG kinds from.
const crdDir = "../../config/crd/bases"

// applier is the field manager the tests apply objects with.
const applier = "applier"

// crdKind is a kind of the generated clientset with what kube-apiserver uses for
// server-side apply on it.
type crdKind struct {
	gvk        schema.GroupVersionKind
	namespaced bool
	schema     *apiextensionsv1.JSONSchemaProps
	server     managedfields.TypeConverter
}

func (k crdKind) name() string {
	return k.gvk.Version + "/" + k.gvk.Kind
}

// crdKinds returns the scheme of the generated clientset and, for every kind in it that
// has an apply configuration, the CRD schema and the type converter kube-apiserver
// builds for the CRD.
func crdKinds(t *testing.T) (*runtime.Scheme, []crdKind) {
	t.Helper()
	loaded, err := loadCRDKinds()
	if err != nil {
		t.Fatal(err)
	}
	return loaded.scheme, loaded.kinds
}

type loadedKinds struct {
	scheme *runtime.Scheme
	kinds  []crdKind
}

var loadCRDKinds = sync.OnceValues(func() (loadedKinds, error) {
	scheme := runtime.NewScheme()
	if err := fake.AddToScheme(scheme); err != nil {
		return loadedKinds{}, fmt.Errorf("failed to build scheme: %w", err)
	}
	files, err := filepath.Glob(filepath.Join(crdDir, "*.yaml"))
	if err != nil || len(files) == 0 {
		return loadedKinds{}, fmt.Errorf("found no CRDs in %s: %v", crdDir, err)
	}

	var kinds []crdKind
	covered := map[schema.GroupVersionKind]bool{}
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			return loadedKinds{}, err
		}
		crd := &apiextensionsv1.CustomResourceDefinition{}
		if err := yaml.Unmarshal(data, crd); err != nil {
			return loadedKinds{}, fmt.Errorf("%s: %w", file, err)
		}
		server, err := apiServerTypeConverter(crd)
		if err != nil {
			return loadedKinds{}, fmt.Errorf("CRD %s: %w", crd.Name, err)
		}
		for _, version := range crd.Spec.Versions {
			gvk := schema.GroupVersionKind{Group: crd.Spec.Group, Version: version.Name, Kind: crd.Spec.Names.Kind}
			if applyconfiguration.ForKind(gvk) == nil {
				continue // Not served by the generated clientset, e.g. RoleBasedGroupWarmup.
			}
			kinds = append(kinds, crdKind{
				gvk:        gvk,
				namespaced: crd.Spec.Scope == apiextensionsv1.NamespaceScoped,
				schema:     version.Schema.OpenAPIV3Schema,
				server:     server,
			})
			covered[gvk] = true
		}
	}
	for gvk := range scheme.AllKnownTypes() {
		if applyconfiguration.ForKind(gvk) != nil && !covered[gvk] {
			return loadedKinds{}, fmt.Errorf("no CRD in %s for %s", crdDir, gvk)
		}
	}
	return loadedKinds{scheme: scheme, kinds: kinds}, nil
})

func crdKindFor(t *testing.T, gvk schema.GroupVersionKind) crdKind {
	t.Helper()
	_, kinds := crdKinds(t)
	for _, k := range kinds {
		if k.gvk == gvk {
			return k
		}
	}
	t.Fatalf("no CRD for %s", gvk)
	return crdKind{}
}

// apiServerTypeConverter builds the type converter kube-apiserver uses for server-side
// apply on crd, as buildOpenAPIModelsForApply and the managedfields.NewTypeConverter
// call in k8s.io/apiextensions-apiserver/pkg/apiserver/customresource_handler.go do.
func apiServerTypeConverter(crd *apiextensionsv1.CustomResourceDefinition) (managedfields.TypeConverter, error) {
	// The server passes its built-in definitions as the first spec. MergeSpecsV3 skips
	// specs without paths, so their content does not matter.
	specs := []*spec3.OpenAPI{{
		Version:    "3.0.0",
		Info:       &spec.Info{InfoProps: spec.InfoProps{Title: "Kubernetes CRD Swagger", Version: "v0.1.0"}},
		Components: &spec3.Components{Schemas: map[string]*spec.Schema{}},
	}}
	for _, version := range crd.Spec.Versions {
		s, err := builder.BuildOpenAPIV3(crd, version.Name, builder.Options{})
		if err != nil {
			return nil, fmt.Errorf("failed to build OpenAPI for %s: %w", version.Name, err)
		}
		specs = append(specs, s)
	}
	merged, err := builder.MergeSpecsV3(specs...)
	if err != nil {
		return nil, fmt.Errorf("failed to merge OpenAPI: %w", err)
	}
	return managedfields.NewTypeConverter(merged.Components.Schemas, crd.Spec.PreserveUnknownFields)
}

// sampleObject returns an object of k that sets every property of its CRD schema, so
// that comparing field sets covers every path. Lists and maps get one element, which is
// enough to tell keyed, set and atomic lists, and granular and atomic maps apart.
func sampleObject(t *testing.T, k crdKind) *unstructured.Unstructured {
	t.Helper()
	content, ok := sampleValue(t, *k.schema).(map[string]interface{})
	if !ok {
		t.Fatalf("%s: the root of the CRD schema is not an object", k.name())
	}
	content["apiVersion"] = k.gvk.GroupVersion().String()
	content["kind"] = k.gvk.Kind
	metadata := map[string]interface{}{
		"name":        "sample",
		"labels":      map[string]interface{}{"app": "sample"},
		"annotations": map[string]interface{}{"note": "sample"},
		"finalizers":  []interface{}{"example.com/finalizer"},
		"ownerReferences": []interface{}{map[string]interface{}{
			"apiVersion": "v1",
			"kind":       "ConfigMap",
			"name":       "owner",
			"uid":        "00000000-0000-0000-0000-000000000001",
			"controller": true,
		}},
	}
	if k.namespaced {
		metadata["namespace"] = "default"
	}
	content["metadata"] = metadata
	return &unstructured.Unstructured{Object: content}
}

func sampleValue(t *testing.T, s apiextensionsv1.JSONSchemaProps) interface{} {
	t.Helper()
	if s.XIntOrString {
		return int64(1)
	}
	switch s.Type {
	case "object":
		object := map[string]interface{}{}
		for name, property := range s.Properties {
			object[name] = sampleValue(t, property)
		}
		if s.AdditionalProperties != nil && s.AdditionalProperties.Schema != nil {
			object["key"] = sampleValue(t, *s.AdditionalProperties.Schema)
		}
		if s.XPreserveUnknownFields != nil && *s.XPreserveUnknownFields {
			object["unknownField"] = schemalessValue()
		}
		return object
	case "array":
		return []interface{}{sampleValue(t, *s.Items.Schema)}
	case "string":
		if s.Format == "date-time" {
			return "2024-01-01T00:00:00Z"
		}
		return "value"
	case "integer":
		return int64(1)
	case "number":
		return 1.5
	case "boolean":
		return true
	case "":
		if s.XPreserveUnknownFields != nil && *s.XPreserveUnknownFields {
			return schemalessValue()
		}
	}
	t.Fatalf("no sample for schema type %q", s.Type)
	return nil
}

// schemalessValue fills a schemaless subtree like the pod templates and template
// patches the CRDs keep schemaless. kube-apiserver tracks its maps per key and its
// lists as a whole.
func schemalessValue() map[string]interface{} {
	return map[string]interface{}{
		"metadata": map[string]interface{}{"labels": map[string]interface{}{"app": "sample"}},
		"spec": map[string]interface{}{
			"containers": []interface{}{map[string]interface{}{"name": "main", "image": "main:v1"}},
		},
	}
}

func fieldSet(t *testing.T, converter managedfields.TypeConverter, obj runtime.Object) *fieldpath.Set {
	t.Helper()
	value, err := converter.ObjectToTyped(obj)
	if err != nil {
		t.Fatalf("failed to convert to a typed value: %v", err)
	}
	set, err := value.ToFieldSet()
	if err != nil {
		t.Fatalf("failed to compute field set: %v", err)
	}
	return set
}

// applyAsAPIServer applies obj as applier with kube-apiserver's field manager and
// returns the resulting object with the managed fields the server records.
func applyAsAPIServer(t *testing.T, k crdKind, obj *unstructured.Unstructured) *unstructured.Unstructured {
	t.Helper()
	manager := managedfieldstest.NewTestFieldManager(k.server, k.gvk)
	if err := manager.Apply(obj.DeepCopy(), applier, false); err != nil {
		t.Fatalf("kube-apiserver field manager failed to apply: %v", err)
	}
	live, ok := manager.Live().(*unstructured.Unstructured)
	if !ok {
		t.Fatalf("unexpected live object %T", manager.Live())
	}
	return live
}

// jsonValue normalizes v to what encoding/json produces, e.g. float64 numbers.
func jsonValue(t *testing.T, v interface{}) interface{} {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var out interface{}
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// TestFieldSetsMatchAPIServer fails when the generated client tracks the fields of an
// RBG object differently from kube-apiserver.
func TestFieldSetsMatchAPIServer(t *testing.T) {
	scheme, kinds := crdKinds(t)
	client := applyconfiguration.NewTypeConverter(scheme)
	for _, k := range kinds {
		t.Run(k.name(), func(t *testing.T) {
			obj := sampleObject(t, k)
			serverFields := fieldSet(t, k.server, obj)
			clientFields := fieldSet(t, client, obj)
			if !serverFields.Equals(clientFields) {
				t.Errorf("field sets differ\nonly kube-apiserver tracks:\n%s\nonly the generated client tracks:\n%s",
					serverFields.Difference(clientFields), clientFields.Difference(serverFields))
			}
		})
	}
}

// TestExtractRoundTripsAPIServerManagedFields applies an object with kube-apiserver's
// field manager, then extracts the applier's fields with the schema embedded in the
// generated apply configurations, as Extract<Kind> does. The result must equal the
// extraction with kube-apiserver's schema, and hold everything that was applied.
func TestExtractRoundTripsAPIServerManagedFields(t *testing.T) {
	scheme, kinds := crdKinds(t)
	client := applyconfiguration.NewTypeConverter(scheme)
	for _, k := range kinds {
		t.Run(k.name(), func(t *testing.T) {
			applied := sampleObject(t, k)
			live := applyAsAPIServer(t, k, applied)

			extracted := extract(t, client, live)
			if diff := cmp.Diff(extract(t, k.server, live), extracted); diff != "" {
				t.Errorf("extraction differs from the one with kube-apiserver's schema (-server +client):\n%s", diff)
			}

			// kube-apiserver does not track the name and namespace.
			want := applied.DeepCopy()
			unstructured.RemoveNestedField(want.Object, "metadata", "name")
			unstructured.RemoveNestedField(want.Object, "metadata", "namespace")
			if diff := cmp.Diff(withoutEmpty(jsonValue(t, want.Object)), withoutEmpty(jsonValue(t, extracted))); diff != "" {
				t.Errorf("extracted fields differ from the applied ones (-applied +extracted):\n%s", diff)
			}
		})
	}
}

// extract returns the fields applier owns in live as Extract<Kind> computes them, but
// with the schema of converter.
func extract(t *testing.T, converter managedfields.TypeConverter, live *unstructured.Unstructured) map[string]interface{} {
	t.Helper()
	value, err := converter.ObjectToTyped(live)
	if err != nil {
		t.Fatalf("failed to convert the live object: %v", err)
	}
	objectType := typed.ParseableType{Schema: value.Schema(), TypeRef: value.TypeRef()}
	extracted := map[string]interface{}{}
	if err := managedfields.ExtractInto(live, objectType, applier, &extracted, ""); err != nil {
		t.Fatalf("failed to extract: %v", err)
	}
	return extracted
}

// withoutEmpty drops nulls and empty maps: extraction returns an empty map as null,
// whatever the schema.
func withoutEmpty(v interface{}) interface{} {
	switch typedValue := v.(type) {
	case map[string]interface{}:
		out := map[string]interface{}{}
		for key, value := range typedValue {
			value = withoutEmpty(value)
			if nested, ok := value.(map[string]interface{}); value == nil || ok && len(nested) == 0 {
				continue
			}
			out[key] = value
		}
		return out
	case []interface{}:
		out := make([]interface{}, len(typedValue))
		for i := range typedValue {
			out[i] = withoutEmpty(typedValue[i])
		}
		return out
	}
	return v
}

func objectFromYAML(t *testing.T, manifest string) *unstructured.Unstructured {
	t.Helper()
	data, err := yaml.YAMLToJSON([]byte(manifest))
	if err != nil {
		t.Fatal(err)
	}
	obj := &unstructured.Unstructured{}
	if err := obj.UnmarshalJSON(data); err != nil {
		t.Fatal(err)
	}
	return obj
}

// assertKeeps fails unless extracted sets every field of applied to the same value.
// Extracted may set more: decoding into Go types adds empty values such as
// "resources: {}" inside lists that kube-apiserver tracks as a whole.
func assertKeeps(t *testing.T, applied, extracted interface{}) {
	t.Helper()
	want, got := jsonValue(t, applied), jsonValue(t, extracted)
	if !keeps(want, got) {
		t.Errorf("extracted configuration lost applied fields\napplied:   %s\nextracted: %s", mustJSON(t, want), mustJSON(t, got))
	}
}

func keeps(want, got interface{}) bool {
	switch w := want.(type) {
	case map[string]interface{}:
		g, ok := got.(map[string]interface{})
		if !ok {
			return false
		}
		for key, value := range w {
			if !keeps(value, g[key]) {
				return false
			}
		}
		return true
	case []interface{}:
		g, ok := got.([]interface{})
		if !ok || len(g) != len(w) {
			return false
		}
		for i := range w {
			if !keeps(w[i], g[i]) {
				return false
			}
		}
		return true
	default:
		return reflect.DeepEqual(want, got)
	}
}

func mustJSON(t *testing.T, v interface{}) string {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// TestExtractRoleInstanceKeepsComponentTemplates covers RoleInstance component
// templates, which the CRD keeps schemaless.
func TestExtractRoleInstanceKeepsComponentTemplates(t *testing.T) {
	k := crdKindFor(t, workloadsv1alpha2.SchemeGroupVersion.WithKind("RoleInstance"))
	applied := objectFromYAML(t, `
apiVersion: workloads.x-k8s.io/v1alpha2
kind: RoleInstance
metadata:
  name: demo
  namespace: default
spec:
  components:
  - name: main
    size: 1
    template:
      metadata:
        labels:
          app: demo
      spec:
        containers:
        - name: main
          image: main:v1
          ports:
          - containerPort: 8080
`)
	live := applyAsAPIServer(t, k, applied)

	roleInstance := &workloadsv1alpha2.RoleInstance{}
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(live.Object, roleInstance); err != nil {
		t.Fatal(err)
	}
	extracted, err := applyv1alpha2.ExtractRoleInstance(roleInstance, applier)
	if err != nil {
		t.Fatalf("ExtractRoleInstance: %v", err)
	}
	assertKeeps(t, applied.Object["spec"], extracted.Spec)
}

// TestExtractRoleBasedGroupKeepsTemplatesAndPatches covers the RoleBasedGroup fields the
// CRD keeps schemaless (custom component templates and template patches) next to the
// typed role templates.
func TestExtractRoleBasedGroupKeepsTemplatesAndPatches(t *testing.T) {
	k := crdKindFor(t, workloadsv1alpha2.SchemeGroupVersion.WithKind("RoleBasedGroup"))
	applied := objectFromYAML(t, `
apiVersion: workloads.x-k8s.io/v1alpha2
kind: RoleBasedGroup
metadata:
  name: demo
  namespace: default
spec:
  roleTemplates:
  - name: base
    template:
      spec:
        containers:
        - name: main
          image: base:v1
  roles:
  - name: router
    replicas: 1
    standalonePattern:
      templateRef:
        name: base
        patch:
          spec:
            containers:
            - name: main
              image: router:v1
  - name: prefill
    replicas: 1
    leaderWorkerPattern:
      size: 2
      template:
        spec:
          containers:
          - name: main
            image: prefill:v1
      leaderTemplatePatch:
        spec:
          containers:
          - name: main
            env:
            - name: ROLE
              value: leader
  - name: decode
    replicas: 1
    customComponentsPattern:
      components:
      - name: main
        size: 1
        template:
          spec:
            containers:
            - name: main
              image: decode:v1
`)
	live := applyAsAPIServer(t, k, applied)

	group := &workloadsv1alpha2.RoleBasedGroup{}
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(live.Object, group); err != nil {
		t.Fatal(err)
	}
	extracted, err := applyv1alpha2.ExtractRoleBasedGroup(group, applier)
	if err != nil {
		t.Fatalf("ExtractRoleBasedGroup: %v", err)
	}
	assertKeeps(t, applied.Object["spec"], extracted.Spec)
}
