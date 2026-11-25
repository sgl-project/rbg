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

package workloads

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/controller-runtime/pkg/event"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	coreapplyv1 "k8s.io/client-go/applyconfigurations/core/v1"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/rbgs/api/workloads/constants"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	"sigs.k8s.io/rbgs/pkg/discovery"
	"sigs.k8s.io/rbgs/pkg/reconciler"
	"sigs.k8s.io/rbgs/pkg/utils"
	wrappersv2 "sigs.k8s.io/rbgs/test/wrappers/v1alpha2"
	"sigs.k8s.io/yaml"
)

func discoveryTestReconciler(t *testing.T, objects ...runtime.Object) (*RoleBasedGroupReconciler, client.Client) {
	t.Helper()
	scheme := runtime.NewScheme()
	_ = workloadsv1alpha2.AddToScheme(scheme)
	_ = appsv1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	c := fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(objects...).Build()
	return &RoleBasedGroupReconciler{
		client:   c,
		scheme:   scheme,
		recorder: record.NewFakeRecorder(10),
	}, c
}

func TestReconcileDiscoveryConfigMap(t *testing.T) {
	t.Run("missing version annotation keeps v1alpha1 content", testDiscoveryConfigMissingVersion)
	t.Run("v1 annotation writes compact content", testDiscoveryConfigCompactContent)
	t.Run("v1 with a deprecated stateful role falls back to v1alpha1", testDiscoveryConfigDeprecatedFallback)
	t.Run("rebuilds after ConfigMap deletion", testDiscoveryConfigRebuild)
	t.Run("rejects an oversized v1alpha1 rollback", testDiscoveryConfigRejectsOversizedRollback)
	t.Run("allows fitting large legacy content", testDiscoveryConfigAllowsFittingLargeLegacyContent)
	t.Run("rejects an unsupported version annotation", testDiscoveryConfigRejectsUnknownVersion)
	t.Run("rejects an unsupported version annotation on stateless-only RBGs", testDiscoveryConfigRejectsUnknownVersionStatelessOnly)
	t.Run("rejects an oversized dense role", testDiscoveryConfigRejectsOversizedDenseRole)
	t.Run("skips stateless-only RBGs", testDiscoveryConfigSkipsStatelessOnly)
}

func testDiscoveryConfigMissingVersion(t *testing.T) {
	rbg := wrappersv2.BuildBasicRoleBasedGroup("test-rbg", "default").Obj()
	r, c := discoveryTestReconciler(t, rbg)

	if err := r.reconcileDiscoveryConfigMap(context.Background(), rbg); err != nil {
		t.Fatalf("reconcileDiscoveryConfigMap() error = %v", err)
	}

	cm := &corev1.ConfigMap{}
	if err := c.Get(context.Background(), clientKey(rbg), cm); err != nil {
		t.Fatalf("get configmap error: %v", err)
	}
	if strings.Contains(cm.Data["config.yaml"], "apiVersion") {
		t.Fatalf("v1alpha1 content must not carry apiVersion: %s", cm.Data["config.yaml"])
	}
	var cfg discovery.ClusterConfig
	if err := yaml.UnmarshalStrict([]byte(cm.Data["config.yaml"]), &cfg); err != nil {
		t.Fatalf("unmarshal v1alpha1 content error: %v", err)
	}
	if _, ok := cfg.Roles["test-role"]; !ok {
		t.Fatalf("test-role missing from v1alpha1 content")
	}
}

func testDiscoveryConfigCompactContent(t *testing.T) {
	rbg := wrappersv2.BuildBasicRoleBasedGroup("test-rbg", "default").Obj()
	rbg.SetDiscoveryConfigVersion(constants.DiscoveryConfigVersionV1)
	rbg.Spec.Roles = append(rbg.Spec.Roles, workloadsv1alpha2.RoleSpec{
		Name:     "router",
		Replicas: ptr.To(int32(1)),
		Annotations: map[string]string{
			constants.RoleWorkloadTypeAnnotationKey: "apps/v1/Deployment",
		},
	})
	r, c := discoveryTestReconciler(t, rbg)

	if err := r.reconcileDiscoveryConfigMap(context.Background(), rbg); err != nil {
		t.Fatalf("reconcileDiscoveryConfigMap() error = %v", err)
	}

	cm := &corev1.ConfigMap{}
	if err := c.Get(context.Background(), clientKey(rbg), cm); err != nil {
		t.Fatalf("get configmap error: %v", err)
	}
	var cfg discovery.CompactConfig
	if err := yaml.UnmarshalStrict([]byte(cm.Data["config.yaml"]), &cfg); err != nil {
		t.Fatalf("unmarshal compact content error: %v", err)
	}
	if cfg.APIVersion != "v1" || cfg.Group != rbg.Name {
		t.Fatalf("compact header = %+v", cfg)
	}
	if len(cfg.Roles) != 1 || cfg.Roles[0].Name != "test-role" {
		t.Fatalf("compact roles = %+v", cfg.Roles)
	}
	if cfg.Roles[0].Members != 1 || cfg.Roles[0].ExcludedOrdinals != "" {
		t.Fatalf("compact membership = %+v", cfg.Roles[0])
	}
}

func testDiscoveryConfigDeprecatedFallback(t *testing.T) {
	rbg := wrappersv2.BuildBasicRoleBasedGroup("test-rbg", "default").Obj()
	rbg.Spec.Roles[0].Annotations = map[string]string{
		constants.RoleWorkloadTypeAnnotationKey: constants.StatefulSetWorkloadType,
	}
	rbg.SetDiscoveryConfigVersion(constants.DiscoveryConfigVersionV1)
	r, c := discoveryTestReconciler(t, rbg)

	if err := r.reconcileDiscoveryConfigMap(context.Background(), rbg); err != nil {
		t.Fatalf("reconcileDiscoveryConfigMap() error = %v", err)
	}

	cm := &corev1.ConfigMap{}
	if err := c.Get(context.Background(), clientKey(rbg), cm); err != nil {
		t.Fatalf("get configmap error: %v", err)
	}
	if strings.Contains(cm.Data["config.yaml"], "apiVersion") {
		t.Fatalf("deprecated stateful roles must keep v1alpha1 content: %s", cm.Data["config.yaml"])
	}
}

func testDiscoveryConfigRebuild(t *testing.T) {
	rbg := wrappersv2.BuildBasicRoleBasedGroup("test-rbg", "default").Obj()
	rbg.SetDiscoveryConfigVersion(constants.DiscoveryConfigVersionV1)
	r, c := discoveryTestReconciler(t, rbg)

	if err := r.reconcileDiscoveryConfigMap(context.Background(), rbg); err != nil {
		t.Fatalf("initial reconcile error: %v", err)
	}
	if err := c.Delete(context.Background(), &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: rbg.Name, Namespace: rbg.Namespace}}); err != nil {
		t.Fatalf("delete configmap error: %v", err)
	}
	if err := r.reconcileDiscoveryConfigMap(context.Background(), rbg); err != nil {
		t.Fatalf("rebuild reconcile error: %v", err)
	}

	cm := &corev1.ConfigMap{}
	if err := c.Get(context.Background(), clientKey(rbg), cm); err != nil {
		t.Fatalf("get rebuilt configmap error: %v", err)
	}
	if !strings.Contains(cm.Data["config.yaml"], "apiVersion: v1") {
		t.Fatalf("rebuilt content is not v1: %s", cm.Data["config.yaml"])
	}
}

func testDiscoveryConfigRejectsOversizedRollback(t *testing.T) {
	rbg := wrappersv2.BuildBasicRoleBasedGroup("test-rbg", "default").Obj()
	rbg.SetDiscoveryConfigVersion(constants.DiscoveryConfigVersionV1)
	r, c := discoveryTestReconciler(t, rbg)

	if err := r.reconcileDiscoveryConfigMap(context.Background(), rbg); err != nil {
		t.Fatalf("initial v1 reconcile error: %v", err)
	}

	rbg.Spec.Roles[0].Replicas = ptr.To(int32(25000))
	rbg.SetDiscoveryConfigVersion(constants.DiscoveryConfigVersionV1Alpha1)
	err := r.reconcileDiscoveryConfigMap(context.Background(), rbg)
	if err == nil {
		t.Fatalf("oversized v1alpha1 content must be rejected")
	}
	if !strings.Contains(err.Error(), "v1alpha1") {
		t.Fatalf("oversize error must report the built content version: %v", err)
	}

	cm := &corev1.ConfigMap{}
	if err := c.Get(context.Background(), clientKey(rbg), cm); err != nil {
		t.Fatalf("get configmap error: %v", err)
	}
	if !strings.Contains(cm.Data["config.yaml"], "apiVersion: v1") {
		t.Fatalf("rejected rollback must retain compact content: %s", cm.Data["config.yaml"])
	}
}

func testDiscoveryConfigAllowsFittingLargeLegacyContent(t *testing.T) {
	rbg := wrappersv2.BuildBasicRoleBasedGroup("test-rbg", "default").Obj()
	rbg.Spec.Roles[0].Replicas = ptr.To(int32(11300))
	rbg.Spec.Roles[0].ServicePorts = []corev1.ServicePort{{Name: "http", Port: 8080}}
	r, c := discoveryTestReconciler(t, rbg)

	upper, err := discovery.NewConfigBuilder(c, rbg, nil).LegacySizeUpperBound(context.Background())
	require.NoError(t, err)
	assert.Greater(t, upper, int64(maxDiscoveryConfigMapBytes),
		"the memory guard should remain deliberately conservative")

	err = r.reconcileDiscoveryConfigMap(context.Background(), rbg)
	require.NoError(t, err)

	cm := &corev1.ConfigMap{}
	require.NoError(t, c.Get(context.Background(), clientKey(rbg), cm))
	assert.NotEmpty(t, cm.Data[discoveryConfigKey])
}

func TestDiscoveryConfigVersionMigrationRoundTrip(t *testing.T) {
	rbg := wrappersv2.BuildBasicRoleBasedGroup("test-rbg", "default").Obj()
	r, c := discoveryTestReconciler(t, rbg)

	require.NoError(t, r.reconcileDiscoveryConfigMap(context.Background(), rbg))
	cm := &corev1.ConfigMap{}
	require.NoError(t, c.Get(context.Background(), clientKey(rbg), cm))
	var legacy discovery.ClusterConfig
	require.NoError(t, yaml.UnmarshalStrict([]byte(cm.Data[discoveryConfigKey]), &legacy))
	require.Contains(t, legacy.Roles, "test-role")

	rbg.SetDiscoveryConfigVersion(constants.DiscoveryConfigVersionV1)
	require.NoError(t, r.reconcileDiscoveryConfigMap(context.Background(), rbg))
	require.NoError(t, c.Get(context.Background(), clientKey(rbg), cm))
	var compact discovery.CompactConfig
	require.NoError(t, yaml.UnmarshalStrict([]byte(cm.Data[discoveryConfigKey]), &compact))
	require.Equal(t, string(constants.DiscoveryConfigVersionV1), compact.APIVersion)

	rbg.SetDiscoveryConfigVersion(constants.DiscoveryConfigVersionV1Alpha1)
	require.NoError(t, r.reconcileDiscoveryConfigMap(context.Background(), rbg))
	require.NoError(t, c.Get(context.Background(), clientKey(rbg), cm))
	legacy = discovery.ClusterConfig{}
	require.NoError(t, yaml.UnmarshalStrict([]byte(cm.Data[discoveryConfigKey]), &legacy))
	require.Contains(t, legacy.Roles, "test-role")
}

func TestDiscoveryConfigTwoThousandMemberCompactReconcile(t *testing.T) {
	rbg := wrappersv2.BuildBasicRoleBasedGroup("test-rbg", "default").Obj()
	rbg.Spec.Roles[0].Replicas = ptr.To(int32(2000))
	rbg.SetDiscoveryConfigVersion(constants.DiscoveryConfigVersionV1)
	r, c := discoveryTestReconciler(t, rbg)

	require.NoError(t, r.reconcileDiscoveryConfigMap(context.Background(), rbg))

	cm := &corev1.ConfigMap{}
	require.NoError(t, c.Get(context.Background(), clientKey(rbg), cm))
	require.Less(t, len(cm.Data[discoveryConfigKey]), 1<<20,
		"2,000 compact members must stay below the ConfigMap limit")

	var config discovery.CompactConfig
	require.NoError(t, yaml.UnmarshalStrict([]byte(cm.Data[discoveryConfigKey]), &config))
	require.Equal(t, string(constants.DiscoveryConfigVersionV1), config.APIVersion)
	require.Len(t, config.Roles, 1)
	require.Equal(t, 2000, config.Roles[0].Members)
}

func testDiscoveryConfigRejectsUnknownVersion(t *testing.T) {
	rbg := wrappersv2.BuildBasicRoleBasedGroup("test-rbg", "default").Obj()
	rbg.SetDiscoveryConfigVersion(constants.DiscoveryConfigVersion("v2"))
	r, c := discoveryTestReconciler(t, rbg)

	err := r.reconcileDiscoveryConfigMap(context.Background(), rbg)
	if err == nil {
		t.Fatalf("unsupported discovery-config-version must fail the reconcile")
	}
	if !strings.Contains(err.Error(), "supported values are v1alpha1 and v1") {
		t.Fatalf("unexpected error: %v", err)
	}

	getErr := c.Get(context.Background(), clientKey(rbg), &corev1.ConfigMap{})
	if !apierrors.IsNotFound(getErr) {
		t.Fatalf("invalid version must not write a configmap, err = %v", getErr)
	}
}

func testDiscoveryConfigRejectsUnknownVersionStatelessOnly(t *testing.T) {
	rbg := wrappersv2.BuildBasicRoleBasedGroup("test-rbg", "default").
		WithRoles([]workloadsv1alpha2.RoleSpec{{
			Name:     "router",
			Replicas: ptr.To(int32(1)),
			Annotations: map[string]string{
				constants.RoleWorkloadTypeAnnotationKey: "apps/v1/Deployment",
			},
		}}).Obj()
	rbg.SetDiscoveryConfigVersion(constants.DiscoveryConfigVersion("v2"))
	r, c := discoveryTestReconciler(t, rbg)

	err := r.reconcileDiscoveryConfigMap(context.Background(), rbg)
	if err == nil {
		t.Fatalf("stateless-only RBGs must still reject an unsupported discovery-config-version")
	}
	if !strings.Contains(err.Error(), "supported values are v1alpha1 and v1") {
		t.Fatalf("unexpected error: %v", err)
	}

	getErr := c.Get(context.Background(), clientKey(rbg), &corev1.ConfigMap{})
	if !apierrors.IsNotFound(getErr) {
		t.Fatalf("invalid version must not write a configmap, err = %v", getErr)
	}
}

func testDiscoveryConfigRejectsOversizedDenseRole(t *testing.T) {
	rbg := wrappersv2.BuildBasicRoleBasedGroup("test-rbg", "default").Obj()
	rbg.Spec.Roles[0].Replicas = ptr.To(int32(25000))
	r, c := discoveryTestReconciler(t, rbg)

	if err := r.reconcileDiscoveryConfigMap(context.Background(), rbg); err == nil {
		t.Fatalf("oversized v1alpha1 content must be rejected before enumeration")
	}

	cm := &corev1.ConfigMap{}
	err := c.Get(context.Background(), clientKey(rbg), cm)
	if !apierrors.IsNotFound(err) {
		t.Fatalf("rejected write must not create a configmap, err = %v", err)
	}
}

func testDiscoveryConfigSkipsStatelessOnly(t *testing.T) {
	rbg := wrappersv2.BuildBasicRoleBasedGroup("test-rbg", "default").
		WithRoles([]workloadsv1alpha2.RoleSpec{{
			Name:     "router",
			Replicas: ptr.To(int32(1)),
			Annotations: map[string]string{
				constants.RoleWorkloadTypeAnnotationKey: "apps/v1/Deployment",
			},
		}}).Obj()
	r, c := discoveryTestReconciler(t, rbg)

	if err := r.reconcileDiscoveryConfigMap(context.Background(), rbg); err != nil {
		t.Fatalf("reconcileDiscoveryConfigMap() error = %v", err)
	}
	err := c.Get(context.Background(), clientKey(rbg), &corev1.ConfigMap{})
	if !apierrors.IsNotFound(err) {
		t.Fatalf("stateless-only RBG must not get a configmap, err = %v", err)
	}
}

func TestRBGPredicateDiscoveryConfigVersion(t *testing.T) {
	key := constants.DiscoveryConfigVersionAnnotationKey
	for _, tt := range []struct {
		name string
		old  map[string]string
		new  map[string]string
		want bool
	}{
		{name: "added", new: map[string]string{key: "v1"}, want: true},
		{name: "changed", old: map[string]string{key: "v1alpha1"}, new: map[string]string{key: "v1"}, want: true},
		{name: "removed", old: map[string]string{key: "v1"}, want: true},
		{name: "unchanged", old: map[string]string{key: "v1"}, new: map[string]string{key: "v1"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			old := &workloadsv1alpha2.RoleBasedGroup{ObjectMeta: metav1.ObjectMeta{Annotations: tt.old}}
			new := &workloadsv1alpha2.RoleBasedGroup{ObjectMeta: metav1.ObjectMeta{Annotations: tt.new}}
			got := RBGPredicate().Update(event.UpdateEvent{ObjectOld: old, ObjectNew: new})
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestDiscoveryConfigValueLimit(t *testing.T) {
	exact := []byte(strings.Repeat("x", maxDiscoveryConfigMapBytes))
	assert.True(t, discoveryConfigValueWithinLimit(exact))

	over := append(exact, 'x')
	assert.False(t, discoveryConfigValueWithinLimit(over))
}

func TestDiscoveryConfigMapDoesNotAdoptForeignConfigMap(t *testing.T) {
	rbg := wrappersv2.BuildBasicRoleBasedGroup("test-rbg", "default").Obj()
	foreign := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: rbg.Name, Namespace: rbg.Namespace},
		Data:       map[string]string{"app.properties": "owned by someone else"},
	}
	r, c := discoveryTestReconciler(t, rbg, foreign)

	err := r.reconcileDiscoveryConfigMap(context.Background(), rbg)
	require.Error(t, err)
	require.Contains(t, err.Error(), "refusing to adopt")

	got := &corev1.ConfigMap{}
	require.NoError(t, c.Get(context.Background(), clientKey(rbg), got))
	assert.Nil(t, metav1.GetControllerOf(got))
	assert.NotContains(t, got.Data, discoveryConfigKey)
	assert.Contains(t, got.Data, "app.properties")
}

func TestDiscoveryConfigMapAdoptsOwnerlessDiscoveryConfigMap(t *testing.T) {
	rbg := wrappersv2.BuildBasicRoleBasedGroup("test-rbg", "default").Obj()
	legacyData, err := yaml.Marshal(discovery.ClusterConfig{
		Group: discovery.GroupInfo{
			Name:  rbg.Name,
			Size:  1,
			Roles: []string{"test-role"},
		},
		Roles: discovery.RolesInfo{
			"test-role": discovery.RoleInstances{
				Size: 1,
				Instances: []discovery.Instance{{
					Address: "test-rbg-test-role-0.s-test-rbg-test-role",
				}},
			},
		},
	})
	require.NoError(t, err)
	orphan := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: rbg.Name, Namespace: rbg.Namespace},
		Data:       map[string]string{discoveryConfigKey: string(legacyData)},
	}
	r, c := discoveryTestReconciler(t, rbg, orphan)

	require.NoError(t, r.reconcileDiscoveryConfigMap(context.Background(), rbg))

	got := &corev1.ConfigMap{}
	require.NoError(t, c.Get(context.Background(), clientKey(rbg), got))
	owner := metav1.GetControllerOf(got)
	require.NotNil(t, owner)
	assert.Equal(t, rbg.GetUID(), owner.UID)
	assert.Contains(t, got.Data, discoveryConfigKey)
}

func TestDiscoveryConfigMapPreservesNonControllerOwnersAndAddsCacheLabel(t *testing.T) {
	rbg := wrappersv2.BuildBasicRoleBasedGroup("test-rbg", "default").Obj()
	controller := *metav1.NewControllerRef(
		rbg, workloadsv1alpha2.GroupVersion.WithKind("RoleBasedGroup"),
	)
	otherOwner := metav1.OwnerReference{
		APIVersion: "example.com/v1",
		Kind:       "Other",
		Name:       "other",
		UID:        "other-uid",
	}
	existing := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:            rbg.Name,
			Namespace:       rbg.Namespace,
			OwnerReferences: []metav1.OwnerReference{controller, otherOwner},
		},
		Data: map[string]string{discoveryConfigKey: "group:\n  name: test-rbg\n"},
	}
	r, c := discoveryTestReconciler(t, rbg, existing)

	require.NoError(t, r.reconcileDiscoveryConfigMap(context.Background(), rbg))

	got := &corev1.ConfigMap{}
	require.NoError(t, c.Get(context.Background(), clientKey(rbg), got))
	assert.Equal(t, rbg.Name, got.Labels[constants.GroupNameLabelKey])
	assert.Contains(t, got.OwnerReferences, otherOwner)
	assert.Equal(t, rbg.GetUID(), metav1.GetControllerOf(got).UID)
}

func TestDiscoveryConfigMapAddsGroupLabelOnCreate(t *testing.T) {
	rbg := wrappersv2.BuildBasicRoleBasedGroup("test-rbg", "default").Obj()
	r, c := discoveryTestReconciler(t, rbg)

	require.NoError(t, r.reconcileDiscoveryConfigMap(context.Background(), rbg))

	got := &corev1.ConfigMap{}
	require.NoError(t, c.Get(context.Background(), clientKey(rbg), got))
	assert.Equal(t, rbg.Name, got.Labels[constants.GroupNameLabelKey])
}

func TestDiscoveryConfigMapWritePathSettlesWithoutRequests(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, workloadsv1alpha2.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))

	var gets, creates, patches, applies int
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithInterceptorFuncs(interceptor.Funcs{
			Get: func(ctx context.Context, cl client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				if err := cl.Get(ctx, key, obj, opts...); err != nil {
					return err
				}
				if cm, ok := obj.(*corev1.ConfigMap); ok {
					gets++
					// The fake client does not preserve managed fields. After the
					// controller creates the object, seed the Apply ownership that a
					// real API server records for its field manager.
					cm.ManagedFields = []metav1.ManagedFieldsEntry{{
						Manager:    utils.FieldManager,
						APIVersion: "v1",
						Operation:  metav1.ManagedFieldsOperationApply,
						FieldsType: "FieldsV1",
						FieldsV1:   &metav1.FieldsV1{Raw: []byte(`{"f:data":{}}`)},
					}}
				}
				return nil
			},
			Create: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
				if _, ok := obj.(*corev1.ConfigMap); ok {
					creates++
				}
				return cl.Create(ctx, obj, opts...)
			},
			Patch: func(ctx context.Context, cl client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
				if _, ok := obj.(*corev1.ConfigMap); ok {
					patches++
				}
				return cl.Patch(ctx, obj, patch, opts...)
			},
			Apply: func(ctx context.Context, cl client.WithWatch, obj runtime.ApplyConfiguration, opts ...client.ApplyOption) error {
				if _, ok := obj.(*coreapplyv1.ConfigMapApplyConfiguration); ok {
					applies++
				}
				return cl.Apply(ctx, obj, opts...)
			},
		}).Build()
	r := &RoleBasedGroupReconciler{
		client:    c,
		apiReader: c,
		scheme:    scheme,
		recorder:  record.NewFakeRecorder(10),
	}
	rbg := wrappersv2.BuildBasicRoleBasedGroup("test-rbg", "default").Obj()

	require.NoError(t, r.reconcileDiscoveryConfigMap(context.Background(), rbg))
	first := struct{ gets, creates, patches, applies int }{gets, creates, patches, applies}

	require.NoError(t, r.reconcileDiscoveryConfigMap(context.Background(), rbg))

	assert.Equal(t, first.gets+1, gets, "steady state should perform only the cached read")
	assert.Equal(t, first.creates, creates)
	assert.Equal(t, first.patches, patches)
	assert.Equal(t, first.applies, applies)
}

func TestDiscoveryConfigMapApplyPreservesOtherOwners(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, workloadsv1alpha2.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))

	rbg := wrappersv2.BuildBasicRoleBasedGroup("test-rbg", "default").Obj()
	controller := *metav1.NewControllerRef(
		rbg, workloadsv1alpha2.GroupVersion.WithKind("RoleBasedGroup"),
	)
	otherOwner := metav1.OwnerReference{
		APIVersion: "example.com/v1",
		Kind:       "Other",
		Name:       "other",
		UID:        "other-uid",
	}
	existing := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:            rbg.Name,
			Namespace:       rbg.Namespace,
			Labels:          map[string]string{constants.GroupNameLabelKey: rbg.Name},
			OwnerReferences: []metav1.OwnerReference{controller, otherOwner},
		},
		Data: map[string]string{discoveryConfigKey: "stale"},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(existing).Build()
	r := &RoleBasedGroupReconciler{
		client:    c,
		apiReader: c,
		scheme:    scheme,
		recorder:  record.NewFakeRecorder(10),
	}

	require.NoError(t, r.applyDiscoveryConfigMap(context.Background(), rbg, []byte("current")))

	got := &corev1.ConfigMap{}
	require.NoError(t, c.Get(context.Background(), clientKey(rbg), got))
	assert.Equal(t, "current", got.Data[discoveryConfigKey])
	assert.Contains(t, got.OwnerReferences, otherOwner)
	assert.Equal(t, rbg.GetUID(), metav1.GetControllerOf(got).UID)
}

func TestDiscoveryConfigMapRetriesOwnershipHandoff(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, workloadsv1alpha2.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))

	rbg := wrappersv2.BuildBasicRoleBasedGroup("test-rbg", "default").Obj()
	controller := *metav1.NewControllerRef(
		rbg, workloadsv1alpha2.GroupVersion.WithKind("RoleBasedGroup"),
	)
	existing := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:            rbg.Name,
			Namespace:       rbg.Namespace,
			UID:             "cm-uid",
			Labels:          map[string]string{constants.GroupNameLabelKey: rbg.Name},
			OwnerReferences: []metav1.OwnerReference{controller},
			ManagedFields: []metav1.ManagedFieldsEntry{{
				Manager:    utils.FieldManager,
				APIVersion: "v1",
				Operation:  metav1.ManagedFieldsOperationUpdate,
				FieldsType: "FieldsV1",
				FieldsV1:   &metav1.FieldsV1{Raw: []byte(`{"f:data":{}}`)},
			}},
		},
		Data: map[string]string{discoveryConfigKey: "current"},
	}
	var patches int
	var applies int
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(existing).
		WithInterceptorFuncs(interceptor.Funcs{
			Get: func(ctx context.Context, cl client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				if err := cl.Get(ctx, key, obj, opts...); err != nil {
					return err
				}
				// The fake client does not preserve managed fields, so restore the
				// seeded Update ownership before the ownership migration runs.
				if cm, ok := obj.(*corev1.ConfigMap); ok {
					cm.ManagedFields = existing.ManagedFields
				}
				return nil
			},
			Apply: func(ctx context.Context, cl client.WithWatch, obj runtime.ApplyConfiguration, opts ...client.ApplyOption) error {
				if _, ok := obj.(*coreapplyv1.ConfigMapApplyConfiguration); ok {
					applies++
				}
				return cl.Apply(ctx, obj, opts...)
			},
			Patch: func(ctx context.Context, cl client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
				if _, ok := obj.(*corev1.ConfigMap); ok {
					patches++
				}
				return cl.Patch(ctx, obj, patch, opts...)
			},
		}).Build()
	r := &RoleBasedGroupReconciler{
		client:    c,
		apiReader: c,
		scheme:    scheme,
		recorder:  record.NewFakeRecorder(10),
	}

	require.NoError(t, r.applyDiscoveryConfigMap(context.Background(), rbg, []byte("current")))

	assert.Equal(t, 1, patches,
		"current data must retry the CSA-to-SSA ownership handoff")
	assert.Zero(t, applies, "the data is current, so no content apply is needed")
}

func TestIsAdoptableDiscoveryConfigMap(t *testing.T) {
	rbg := wrappersv2.BuildBasicRoleBasedGroup("test-rbg", "default").Obj()
	validLegacy := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: rbg.Name, Namespace: rbg.Namespace},
		Data:       map[string]string{discoveryConfigKey: "group:\n  name: test-rbg\n"},
	}
	validCompact := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: rbg.Name, Namespace: rbg.Namespace},
		Data:       map[string]string{discoveryConfigKey: "apiVersion: v1\ngroup: test-rbg\n"},
	}

	tests := []struct {
		name string
		cm   *corev1.ConfigMap
		want bool
	}{
		{name: "valid legacy content", cm: validLegacy, want: true},
		{name: "valid compact content", cm: validCompact, want: true},
		{
			name: "owner reference with a different UID",
			cm: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      rbg.Name,
					Namespace: rbg.Namespace,
					OwnerReferences: []metav1.OwnerReference{{
						APIVersion: "workloads.x-k8s.io/v1alpha2",
						Kind:       "RoleBasedGroup",
						Name:       "other",
						UID:        "other-uid",
						Controller: ptr.To(true),
					}},
				},
				Data: validLegacy.Data,
			},
			want: false,
		},
		{
			name: "valid content naming a different group",
			cm: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{Name: rbg.Name, Namespace: rbg.Namespace},
				Data:       map[string]string{discoveryConfigKey: "group:\n  name: other-rbg\n"},
			},
			want: false,
		},
		{
			name: "BinaryData present",
			cm: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{Name: rbg.Name, Namespace: rbg.Namespace},
				Data:       validLegacy.Data,
				BinaryData: map[string][]byte{"other": []byte("value")},
			},
			want: false,
		},
		{
			name: "unparsable content",
			cm: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{Name: rbg.Name, Namespace: rbg.Namespace},
				Data:       map[string]string{discoveryConfigKey: "not: [valid: yaml"},
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, isAdoptableDiscoveryConfigMap(tt.cm, rbg))
		})
	}
}

func TestDiscoveryConfigMapPredicate(t *testing.T) {
	predicate := discoveryConfigMapPredicate()
	obj := &corev1.ConfigMap{}

	assert.False(t, predicate.CreateFunc(event.CreateEvent{Object: obj}))
	assert.False(t, predicate.UpdateFunc(event.UpdateEvent{ObjectOld: obj, ObjectNew: obj}))
	assert.True(t, predicate.DeleteFunc(event.DeleteEvent{Object: obj}))
	assert.False(t, predicate.GenericFunc(event.GenericEvent{Object: obj}))
}

func TestDiscoveryFailureStopsRoleRendering(t *testing.T) {
	tests := []struct {
		name string
		objs []client.Object
	}{
		{
			name: "foreign ConfigMap",
			objs: []client.Object{
				wrappersv2.BuildBasicRoleBasedGroup("test-rbg", "default").Obj(),
				&corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-rbg",
						Namespace: "default",
					},
					Data: map[string]string{"app.properties": "foreign"},
				},
			},
		},
		{
			name: "ConfigMap create failure",
			objs: []client.Object{
				wrappersv2.BuildBasicRoleBasedGroup("test-rbg", "default").Obj(),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			require.NoError(t, clientgoscheme.AddToScheme(scheme))
			require.NoError(t, workloadsv1alpha2.AddToScheme(scheme))
			require.NoError(t, corev1.AddToScheme(scheme))

			builder := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(tt.objs...).
				WithStatusSubresource(
					&workloadsv1alpha2.RoleBasedGroup{},
					&workloadsv1alpha2.RoleInstanceSet{},
				)
			if tt.name == "ConfigMap create failure" {
				builder = builder.WithInterceptorFuncs(interceptor.Funcs{
					Create: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
						if _, ok := obj.(*corev1.ConfigMap); ok {
							return apierrors.NewServiceUnavailable("ConfigMap creation failed")
						}
						return cl.Create(ctx, obj, opts...)
					},
				})
			}
			c := builder.Build()
			r := &RoleBasedGroupReconciler{
				client:             c,
				apiReader:          c,
				scheme:             scheme,
				recorder:           record.NewFakeRecorder(10),
				workloadReconciler: make(map[string]reconciler.WorkloadReconciler),
			}
			req := reconcile.Request{
				NamespacedName: types.NamespacedName{Name: "test-rbg", Namespace: "default"},
			}

			_, err := r.Reconcile(context.Background(), req)
			require.Error(t, err)

			ris := &workloadsv1alpha2.RoleInstanceSet{}
			getErr := c.Get(context.Background(), types.NamespacedName{
				Name:      "test-rbg-test-role",
				Namespace: "default",
			}, ris)
			assert.True(t, apierrors.IsNotFound(getErr), "role rendering must stop before creating a workload")
		})
	}
}

func TestOversizedLegacyScaleUpStopsRoleRendering(t *testing.T) {
	rbg := wrappersv2.BuildBasicRoleBasedGroup("test-rbg", "default").Obj()
	rbg.Spec.Roles[0].Replicas = ptr.To(int32(25000))
	rbg.SetDiscoveryConfigVersion(constants.DiscoveryConfigVersionV1Alpha1)
	legacy := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      rbg.Name,
			Namespace: rbg.Namespace,
			OwnerReferences: []metav1.OwnerReference{
				*metav1.NewControllerRef(rbg, workloadsv1alpha2.GroupVersion.WithKind("RoleBasedGroup")),
			},
		},
		Data: map[string]string{discoveryConfigKey: "group:\n  name: test-rbg\n"},
	}
	r, c := discoveryTestReconciler(t, rbg, legacy)
	r.apiReader = c
	r.workloadReconciler = make(map[string]reconciler.WorkloadReconciler)

	_, err := r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Name: rbg.Name, Namespace: rbg.Namespace},
	})
	require.ErrorContains(t, err, "exceeding")

	getErr := c.Get(context.Background(), types.NamespacedName{
		Name:      rbg.GetWorkloadName(&rbg.Spec.Roles[0]),
		Namespace: rbg.Namespace,
	}, &workloadsv1alpha2.RoleInstanceSet{})
	assert.True(t, apierrors.IsNotFound(getErr),
		"an owned legacy ConfigMap is not a recoverable rollback snapshot")
}

func TestTransientDiscoveryErrorIsNotInvalidSpec(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, clientgoscheme.AddToScheme(scheme))
	require.NoError(t, workloadsv1alpha2.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))

	rbg := wrappersv2.BuildBasicRoleBasedGroup("test-rbg", "default").Obj()
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(rbg).
		WithStatusSubresource(
			&workloadsv1alpha2.RoleBasedGroup{},
			&workloadsv1alpha2.RoleInstanceSet{},
		).
		WithInterceptorFuncs(interceptor.Funcs{
			Get: func(ctx context.Context, cl client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				if _, ok := obj.(*corev1.ConfigMap); ok {
					return apierrors.NewServiceUnavailable("etcd unavailable")
				}
				return cl.Get(ctx, key, obj, opts...)
			},
		}).Build()
	r := &RoleBasedGroupReconciler{
		client:             c,
		apiReader:          c,
		scheme:             scheme,
		recorder:           record.NewFakeRecorder(10),
		workloadReconciler: make(map[string]reconciler.WorkloadReconciler),
	}

	_, err := r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Name: "test-rbg", Namespace: "default"},
	})
	require.Error(t, err)

	updated := &workloadsv1alpha2.RoleBasedGroup{}
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{
		Name:      "test-rbg",
		Namespace: "default",
	}, updated))
	condition := apimeta.FindStatusCondition(
		updated.Status.Conditions, string(workloadsv1alpha2.RoleBasedGroupProgressing),
	)
	require.NotNil(t, condition)
	assert.NotEqual(t, constants.ProgressingReasonInvalidSpec, condition.Reason)
}

func TestReconcileContinuesAfterOversizedDiscoveryRollback(t *testing.T) {
	rbg := wrappersv2.BuildBasicRoleBasedGroup("test-rbg", "default").Obj()
	rbg.Spec.Roles[0].Replicas = ptr.To(int32(0))
	rbg.Spec.Roles[0].Annotations = map[string]string{
		constants.ExcludedOrdinalsAnnotationKey: "1",
	}
	rbg.SetDiscoveryConfigVersion(constants.DiscoveryConfigVersionV1Alpha1)

	compactData, err := yaml.Marshal(discovery.CompactConfig{
		APIVersion: string(constants.DiscoveryConfigVersionV1),
		Group:      rbg.Name,
		Roles: []discovery.CompactRole{{
			Name:    rbg.Spec.Roles[0].Name,
			Members: 100,
			Components: []discovery.CompactComponent{{
				Name: rbg.Spec.Roles[0].Name,
				Size: 1,
				Base: fmt.Sprintf("%s-{roleIndex}.s-%s", rbg.GetWorkloadName(&rbg.Spec.Roles[0]), rbg.GetWorkloadName(&rbg.Spec.Roles[0])),
			}},
		}},
	})
	require.NoError(t, err)
	compact := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      rbg.Name,
			Namespace: rbg.Namespace,
			OwnerReferences: []metav1.OwnerReference{
				*metav1.NewControllerRef(rbg, workloadsv1alpha2.GroupVersion.WithKind("RoleBasedGroup")),
			},
		},
		Data: map[string]string{discoveryConfigKey: string(compactData)},
	}
	role := &rbg.Spec.Roles[0]
	ris := &workloadsv1alpha2.RoleInstanceSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      rbg.GetWorkloadName(role),
			Namespace: rbg.Namespace,
			Annotations: map[string]string{
				constants.CommittedSkipOrdinalsAnnotationKey: "1",
			},
			OwnerReferences: []metav1.OwnerReference{
				*metav1.NewControllerRef(rbg, workloadsv1alpha2.GroupVersion.WithKind("RoleBasedGroup")),
			},
		},
		Spec: workloadsv1alpha2.RoleInstanceSetSpec{
			Replicas: ptr.To(int32(25000)),
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "test"}},
		},
	}

	r, c := discoveryTestReconciler(t, rbg, compact, ris)
	r.apiReader = c
	r.workloadReconciler = make(map[string]reconciler.WorkloadReconciler)

	_, err = r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Name: rbg.Name, Namespace: rbg.Namespace},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "exceeding")

	updated := &workloadsv1alpha2.RoleInstanceSet{}
	require.NoError(t, c.Get(context.Background(), client.ObjectKeyFromObject(ris), updated))
	assert.Equal(t, int32(0), ptr.Deref(updated.Spec.Replicas, -1))

	updatedRBG := &workloadsv1alpha2.RoleBasedGroup{}
	require.NoError(t, c.Get(context.Background(), clientKey(rbg), updatedRBG))
	foundBlocker := false
	for _, condition := range updatedRBG.Status.Conditions {
		if condition.Type == string(workloadsv1alpha2.RoleBasedGroupProgressing) &&
			condition.Reason == constants.ProgressingReasonInvalidSpec {
			foundBlocker = true
			break
		}
	}
	assert.True(t, foundBlocker, "oversized rollback must be reported as an InvalidSpec blocker")

	got := &corev1.ConfigMap{}
	require.NoError(t, c.Get(context.Background(), clientKey(rbg), got))
	assert.Contains(t, got.Data[discoveryConfigKey], "apiVersion: v1")
}

func clientKey(rbg *workloadsv1alpha2.RoleBasedGroup) types.NamespacedName {
	return types.NamespacedName{Name: rbg.Name, Namespace: rbg.Namespace}
}
