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

package clientgo_test

import (
	"context"
	"testing"

	"github.com/google/go-cmp/cmp"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"

	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	"sigs.k8s.io/rbgs/client-go/applyconfiguration"
	applyv1alpha2 "sigs.k8s.io/rbgs/client-go/applyconfiguration/workloads/v1alpha2"
	"sigs.k8s.io/rbgs/client-go/clientset/versioned/fake"
)

const testNamespace = "default"

// TestTypeConverterHandlesClientsetKinds fails when the schema embedded in the
// generated apply configurations rejects a Go object of a kind, for example because
// hack/update-codegen.sh stopped passing --applyconfig-openapi-schema.
func TestTypeConverterHandlesClientsetKinds(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := fake.AddToScheme(scheme); err != nil {
		t.Fatalf("failed to build scheme: %v", err)
	}
	converter := applyconfiguration.NewTypeConverter(scheme)
	checked := 0
	for gvk := range scheme.AllKnownTypes() {
		if applyconfiguration.ForKind(gvk) == nil {
			continue
		}
		obj, err := scheme.New(gvk)
		if err != nil {
			t.Fatalf("failed to create %s: %v", gvk, err)
		}
		obj.GetObjectKind().SetGroupVersionKind(gvk)
		if _, err := converter.ObjectToTyped(obj); err != nil {
			t.Errorf("failed to convert %s: %v", gvk, err)
		}
		checked++
	}
	if checked == 0 {
		t.Fatal("found no kinds with an apply configuration")
	}
}

// TestFakeClientsetWritesRoleBasedGroups covers the writes that fail with
// "no type found matching" when the embedded schema is missing. Seeding and Get work
// either way because they bypass field management.
func TestFakeClientsetWritesRoleBasedGroups(t *testing.T) {
	ctx := context.Background()
	seeded := &workloadsv1alpha2.RoleBasedGroup{
		ObjectMeta: metav1.ObjectMeta{Name: "seeded", Namespace: testNamespace},
		Spec: workloadsv1alpha2.RoleBasedGroupSpec{
			Roles: []workloadsv1alpha2.RoleSpec{{Name: "prefill", Replicas: ptr.To[int32](1)}},
		},
	}
	client := fake.NewClientset(seeded).WorkloadsV1alpha2().RoleBasedGroups(testNamespace)

	got, err := client.Get(ctx, "seeded", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	got.Spec.Roles[0].Replicas = ptr.To[int32](2)
	if _, err := client.Update(ctx, got, metav1.UpdateOptions{FieldManager: "updater"}); err != nil {
		t.Fatalf("Update: %v", err)
	}

	created, err := client.Create(ctx, &workloadsv1alpha2.RoleBasedGroup{
		ObjectMeta: metav1.ObjectMeta{Name: "created"},
		Spec: workloadsv1alpha2.RoleBasedGroupSpec{
			Roles: []workloadsv1alpha2.RoleSpec{{Name: "decode", Replicas: ptr.To[int32](1)}},
		},
	}, metav1.CreateOptions{FieldManager: "creator"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if len(created.ManagedFields) == 0 {
		t.Error("Create recorded no managed fields")
	}
}

// TestFakeClientsetApplyAndExtract checks that Apply creates a missing object, as
// the API server does, and that the extracted configuration matches the applied one.
func TestFakeClientsetApplyAndExtract(t *testing.T) {
	ctx := context.Background()
	client := fake.NewClientset().WorkloadsV1alpha2().RoleBasedGroups(testNamespace)

	applyConfig := applyv1alpha2.RoleBasedGroup("applied", testNamespace).
		WithSpec(applyv1alpha2.RoleBasedGroupSpec().
			WithRoles(applyv1alpha2.RoleSpec().WithName("decode").WithReplicas(2)))
	applied, err := client.Apply(ctx, applyConfig, metav1.ApplyOptions{FieldManager: applier})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	extracted, err := applyv1alpha2.ExtractRoleBasedGroup(applied, applier)
	if err != nil {
		t.Fatalf("ExtractRoleBasedGroup: %v", err)
	}
	if diff := cmp.Diff(applyConfig, extracted); diff != "" {
		t.Errorf("extracted configuration differs from the applied one (-applied +extracted):\n%s", diff)
	}
}

// TestFakeClientsetMergesRoleInstanceComponents checks the list-map semantics of
// RoleInstanceSpec.Components: components applied by two field managers are
// merged by name instead of conflicting, and each manager owns only its own.
func TestFakeClientsetMergesRoleInstanceComponents(t *testing.T) {
	ctx := context.Background()
	client := fake.NewClientset().WorkloadsV1alpha2().RoleInstances(testNamespace)

	componentSpec := func(name string) *applyv1alpha2.RoleInstanceSpecApplyConfiguration {
		return applyv1alpha2.RoleInstanceSpec().
			WithComponents(applyv1alpha2.RoleInstanceComponent().WithName(name).WithSize(1))
	}
	for _, component := range []string{"prefill", "decode"} {
		applyConfig := applyv1alpha2.RoleInstance("demo", testNamespace).WithSpec(componentSpec(component))
		if _, err := client.Apply(ctx, applyConfig, metav1.ApplyOptions{FieldManager: component + "-manager"}); err != nil {
			t.Fatalf("Apply %s: %v", component, err)
		}
	}

	got, err := client.Get(ctx, "demo", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	names := make([]string, 0, len(got.Spec.Components))
	for _, component := range got.Spec.Components {
		names = append(names, component.Name)
	}
	if diff := cmp.Diff([]string{"prefill", "decode"}, names); diff != "" {
		t.Errorf("unexpected components (-want +got):\n%s", diff)
	}

	extracted, err := applyv1alpha2.ExtractRoleInstance(got, "decode-manager")
	if err != nil {
		t.Fatalf("ExtractRoleInstance: %v", err)
	}
	if diff := cmp.Diff(componentSpec("decode"), extracted.Spec); diff != "" {
		t.Errorf("decode-manager owns unexpected fields (-want +got):\n%s", diff)
	}
}
