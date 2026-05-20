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

package componentdiscovery

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
)

// ---------------------------------------------------------------------------
// ParseComponentDiscoveryConfig
// ---------------------------------------------------------------------------

func TestParseComponentDiscoveryConfig(t *testing.T) {
	t.Run("valid full config", func(t *testing.T) {
		raw := `{
			"addressRefs": [
				{"env": "LEADER_ADDR", "component": "leader", "index": 0}
			],
			"portRefs": [
				{"env": "LEADER_GRPC_PORT", "component": "leader", "portName": "grpc", "index": 0}
			]
		}`
		cfg, err := ParseComponentDiscoveryConfig(raw)
		require.NoError(t, err)
		require.Len(t, cfg.AddressRefs, 1)
		assert.Equal(t, "LEADER_ADDR", cfg.AddressRefs[0].Env)
		assert.Equal(t, "leader", cfg.AddressRefs[0].Component)
		assert.Equal(t, int32(0), cfg.AddressRefs[0].Index)

		require.Len(t, cfg.PortRefs, 1)
		assert.Equal(t, "LEADER_GRPC_PORT", cfg.PortRefs[0].Env)
		assert.Equal(t, "grpc", cfg.PortRefs[0].PortName)
	})

	t.Run("address refs only", func(t *testing.T) {
		raw := `{"addressRefs": [{"env": "ROUTER_ADDR", "component": "router"}]}`
		cfg, err := ParseComponentDiscoveryConfig(raw)
		require.NoError(t, err)
		require.Len(t, cfg.AddressRefs, 1)
		assert.Empty(t, cfg.PortRefs)
	})

	t.Run("empty config", func(t *testing.T) {
		cfg, err := ParseComponentDiscoveryConfig(`{}`)
		require.NoError(t, err)
		assert.Empty(t, cfg.AddressRefs)
		assert.Empty(t, cfg.PortRefs)
	})

	t.Run("invalid json", func(t *testing.T) {
		_, err := ParseComponentDiscoveryConfig(`{not-valid`)
		require.Error(t, err)
	})
}

// ---------------------------------------------------------------------------
// InjectComponentDiscovery — address injection
// ---------------------------------------------------------------------------

func makeTestInstance(name, namespace string, components []workloadsv1alpha2.RoleInstanceComponent, annotations map[string]string) *workloadsv1alpha2.RoleInstance {
	return &workloadsv1alpha2.RoleInstance{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Namespace:   namespace,
			Annotations: annotations,
		},
		Spec: workloadsv1alpha2.RoleInstanceSpec{
			Components: components,
		},
	}
}

func int32Ptr(v int32) *int32 { return &v }

func makePodWithAnnotation(name string, annotationValue string) *corev1.Pod {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
			Annotations: map[string]string{
				ComponentDiscoveryAnnotationKey: annotationValue,
			},
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{Name: "main"},
			},
		},
	}
	return pod
}

func TestInjectComponentDiscovery_AddressRef(t *testing.T) {
	instance := makeTestInstance(
		"llm-prefill-prefill-abc12",
		"default",
		[]workloadsv1alpha2.RoleInstanceComponent{
			{Name: "leader", ServiceName: "s-llm-prefill-prefill", Size: int32Ptr(1)},
			{Name: "worker", ServiceName: "s-llm-prefill-prefill", Size: int32Ptr(4)},
		},
		nil,
	)

	cfg := ComponentDiscoveryConfig{
		AddressRefs: []ComponentAddressRef{
			{Env: "LEADER_ADDR", Component: "leader", Index: 0},
		},
	}
	raw, _ := json.Marshal(cfg)
	pod := makePodWithAnnotation("llm-prefill-prefill-abc12-worker-0", string(raw))

	err := InjectComponentDiscovery(pod, instance)
	require.NoError(t, err)

	// Annotation should be removed
	_, hasAnnotation := pod.Annotations[ComponentDiscoveryAnnotationKey]
	assert.False(t, hasAnnotation, "annotation should be removed after injection")

	// Env var should be injected
	expectedFQDN := "llm-prefill-prefill-abc12-leader-0.s-llm-prefill-prefill.default.svc.cluster.local"
	require.NotEmpty(t, pod.Spec.Containers[0].Env)
	var found bool
	for _, env := range pod.Spec.Containers[0].Env {
		if env.Name == "LEADER_ADDR" {
			assert.Equal(t, expectedFQDN, env.Value)
			found = true
		}
	}
	assert.True(t, found, "LEADER_ADDR env var not injected")
}

func TestInjectComponentDiscovery_AddressRef_NonZeroIndex(t *testing.T) {
	instance := makeTestInstance(
		"inst-xyz",
		"mynamespace",
		[]workloadsv1alpha2.RoleInstanceComponent{
			{Name: "router", ServiceName: "svc-router", Size: int32Ptr(3)},
		},
		nil,
	)

	cfg := ComponentDiscoveryConfig{
		AddressRefs: []ComponentAddressRef{
			{Env: "ROUTER_2_ADDR", Component: "router", Index: 2},
		},
	}
	raw, _ := json.Marshal(cfg)
	pod := makePodWithAnnotation("inst-xyz-worker-0", string(raw))

	err := InjectComponentDiscovery(pod, instance)
	require.NoError(t, err)

	expected := "inst-xyz-router-2.svc-router.mynamespace.svc.cluster.local"
	var found bool
	for _, env := range pod.Spec.Containers[0].Env {
		if env.Name == "ROUTER_2_ADDR" {
			assert.Equal(t, expected, env.Value)
			found = true
		}
	}
	assert.True(t, found)
}

// ---------------------------------------------------------------------------
// InjectComponentDiscovery — port injection
// ---------------------------------------------------------------------------

func TestInjectComponentDiscovery_PortRef_RoleScoped(t *testing.T) {
	// RoleScoped key: <component>.<portName>
	instance := makeTestInstance(
		"inst-abc",
		"default",
		[]workloadsv1alpha2.RoleInstanceComponent{
			{Name: "leader", ServiceName: "svc", Size: int32Ptr(1)},
		},
		map[string]string{
			"leader.grpc": "50051",
		},
	)

	cfg := ComponentDiscoveryConfig{
		PortRefs: []ComponentPortRef{
			{Env: "LEADER_GRPC_PORT", Component: "leader", PortName: "grpc"},
		},
	}
	raw, _ := json.Marshal(cfg)
	pod := makePodWithAnnotation("inst-abc-worker-0", string(raw))

	err := InjectComponentDiscovery(pod, instance)
	require.NoError(t, err)

	var found bool
	for _, env := range pod.Spec.Containers[0].Env {
		if env.Name == "LEADER_GRPC_PORT" {
			assert.Equal(t, "50051", env.Value)
			found = true
		}
	}
	assert.True(t, found, "LEADER_GRPC_PORT not injected")
}

func TestInjectComponentDiscovery_PortRef_PodScoped(t *testing.T) {
	// PodScoped key: <pod-name>.<portName>
	// pod name: <instanceName>-<componentName>-<index>
	instance := makeTestInstance(
		"inst-abc",
		"default",
		[]workloadsv1alpha2.RoleInstanceComponent{
			{Name: "leader", ServiceName: "svc", Size: int32Ptr(1)},
		},
		map[string]string{
			// PodScoped key: inst-abc-leader-0.grpc
			"inst-abc-leader-0.grpc": "50052",
		},
	)

	cfg := ComponentDiscoveryConfig{
		PortRefs: []ComponentPortRef{
			{Env: "LEADER_GRPC_PORT", Component: "leader", PortName: "grpc", Index: 0},
		},
	}
	raw, _ := json.Marshal(cfg)
	pod := makePodWithAnnotation("inst-abc-worker-0", string(raw))

	err := InjectComponentDiscovery(pod, instance)
	require.NoError(t, err)

	var found bool
	for _, env := range pod.Spec.Containers[0].Env {
		if env.Name == "LEADER_GRPC_PORT" {
			assert.Equal(t, "50052", env.Value)
			found = true
		}
	}
	assert.True(t, found)
}

// ---------------------------------------------------------------------------
// InjectComponentDiscovery — error cases
// ---------------------------------------------------------------------------

func TestInjectComponentDiscovery_UnknownComponent(t *testing.T) {
	instance := makeTestInstance("inst", "default",
		[]workloadsv1alpha2.RoleInstanceComponent{
			{Name: "leader", ServiceName: "svc", Size: int32Ptr(1)},
		},
		nil,
	)

	cfg := ComponentDiscoveryConfig{
		AddressRefs: []ComponentAddressRef{
			{Env: "UNKNOWN_ADDR", Component: "does-not-exist", Index: 0},
		},
	}
	raw, _ := json.Marshal(cfg)
	pod := makePodWithAnnotation("inst-worker-0", string(raw))

	err := InjectComponentDiscovery(pod, instance)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does-not-exist")
}

func TestInjectComponentDiscovery_PortNotFound(t *testing.T) {
	instance := makeTestInstance("inst", "default",
		[]workloadsv1alpha2.RoleInstanceComponent{
			{Name: "leader", ServiceName: "svc", Size: int32Ptr(1)},
		},
		map[string]string{
			"leader.grpc": "50051",
		},
	)

	cfg := ComponentDiscoveryConfig{
		PortRefs: []ComponentPortRef{
			{Env: "MISSING_PORT", Component: "leader", PortName: "non-existent-port"},
		},
	}
	raw, _ := json.Marshal(cfg)
	pod := makePodWithAnnotation("inst-worker-0", string(raw))

	err := InjectComponentDiscovery(pod, instance)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "non-existent-port")
}

func TestInjectComponentDiscovery_NoAnnotation(t *testing.T) {
	instance := makeTestInstance("inst", "default", nil, nil)
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "inst-worker-0"},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "main"}}},
	}
	err := InjectComponentDiscovery(pod, instance)
	require.NoError(t, err)
	assert.Empty(t, pod.Spec.Containers[0].Env)
}

// ---------------------------------------------------------------------------
// HasComponentDiscoveryConfig
// ---------------------------------------------------------------------------

func TestHasComponentDiscoveryConfig(t *testing.T) {
	tmpl := &corev1.PodTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{
				ComponentDiscoveryAnnotationKey: `{}`,
			},
		},
	}
	assert.True(t, HasComponentDiscoveryConfig(tmpl))

	tmplNoAnno := &corev1.PodTemplateSpec{}
	assert.False(t, HasComponentDiscoveryConfig(tmplNoAnno))
	assert.False(t, HasComponentDiscoveryConfig(nil))
}
