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

package discovery

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/rbgs/api/workloads/constants"
	"sigs.k8s.io/rbgs/api/workloads/ordinals"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	"sigs.k8s.io/yaml"
)

// TestConfigBuilder_Build tests the Build method of ConfigBuilder
func TestConfigBuilder_Build(t *testing.T) {
	replicas3 := int32(3)
	replicas1 := int32(1)
	schema := runtime.NewScheme()
	_ = corev1.AddToScheme(schema)
	_ = workloadsv1alpha2.AddToScheme(schema)

	tests := []struct {
		name     string
		client   client.Client
		rbg      *workloadsv1alpha2.RoleBasedGroup
		role     *workloadsv1alpha2.RoleSpec
		expected string
		wantErr  bool
	}{
		{
			name:   "simple cluster config",
			client: fake.NewClientBuilder().WithScheme(schema).Build(),
			rbg: &workloadsv1alpha2.RoleBasedGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-cluster",
				},
				Spec: workloadsv1alpha2.RoleBasedGroupSpec{
					Roles: []workloadsv1alpha2.RoleSpec{
						{
							Name:     "worker",
							Replicas: &replicas3,
							ServicePorts: []corev1.ServicePort{
								{
									Name: "http",
									Port: 8080,
								},
							},
						},
						{
							Name:     "leader",
							Replicas: &replicas1,
							ServicePorts: []corev1.ServicePort{
								{
									Name: "api",
									Port: 6443,
								},
							},
						},
					},
				},
			},
			role: &workloadsv1alpha2.RoleSpec{
				Name:     "worker",
				Replicas: &replicas3,
			},
			expected: `group:
  name: test-cluster
  roles:
  - worker
  - leader
  size: 2
roles:
  leader:
    instances:
    - address: test-cluster-leader-0.s-test-cluster-leader
      ports:
        api: 6443
    size: 1
  worker:
    instances:
    - address: test-cluster-worker-0.s-test-cluster-worker
      ports:
        http: 8080
    - address: test-cluster-worker-1.s-test-cluster-worker
      ports:
        http: 8080
    - address: test-cluster-worker-2.s-test-cluster-worker
      ports:
        http: 8080
    size: 3
`,
			wantErr: false,
		},
		{
			name: "config with oldSvc",
			client: fake.NewClientBuilder().WithScheme(schema).WithRuntimeObjects(
				&corev1.Service{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-cluster-leader",
						Namespace: "default",
					},
				}, &corev1.Service{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-cluster-worker",
						Namespace: "default",
					},
				},
			).Build(),
			rbg: &workloadsv1alpha2.RoleBasedGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-cluster",
					Namespace: "default",
				},
				Spec: workloadsv1alpha2.RoleBasedGroupSpec{
					Roles: []workloadsv1alpha2.RoleSpec{
						{
							Name:     "worker",
							Replicas: &replicas3,
							ServicePorts: []corev1.ServicePort{
								{
									Name: "http",
									Port: 8080,
								},
							},
						},
						{
							Name:     "leader",
							Replicas: &replicas1,
							ServicePorts: []corev1.ServicePort{
								{
									Name: "api",
									Port: 6443,
								},
							},
						},
					},
				},
			},
			role: &workloadsv1alpha2.RoleSpec{
				Name:     "worker",
				Replicas: &replicas3,
			},
			expected: `group:
  name: test-cluster
  roles:
  - worker
  - leader
  size: 2
roles:
  leader:
    instances:
    - address: test-cluster-leader-0.test-cluster-leader
      ports:
        api: 6443
    size: 1
  worker:
    instances:
    - address: test-cluster-worker-0.test-cluster-worker
      ports:
        http: 8080
    - address: test-cluster-worker-1.test-cluster-worker
      ports:
        http: 8080
    - address: test-cluster-worker-2.test-cluster-worker
      ports:
        http: 8080
    size: 3
`,
			wantErr: false,
		},
		{
			name:   "role with unnamed ports",
			client: fake.NewClientBuilder().WithScheme(schema).Build(),
			rbg: &workloadsv1alpha2.RoleBasedGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-cluster",
					Namespace: "default",
				},
				Spec: workloadsv1alpha2.RoleBasedGroupSpec{
					Roles: []workloadsv1alpha2.RoleSpec{
						{
							Name:     "web",
							Replicas: &replicas1,
							ServicePorts: []corev1.ServicePort{
								{
									Port: 80,
								},
								{
									Port: 443,
								},
							},
						},
					},
				},
			},
			role: &workloadsv1alpha2.RoleSpec{
				Name:     "web",
				Replicas: &replicas1,
			},
			expected: `group:
  name: test-cluster
  roles:
  - web
  size: 1
roles:
  web:
    instances:
    - address: test-cluster-web-0.s-test-cluster-web
      ports:
        port80: 80
        port443: 443
    size: 1
`,
			wantErr: false,
		},
		{
			name:   "rbg name start with numeric",
			client: fake.NewClientBuilder().WithScheme(schema).Build(),
			rbg: &workloadsv1alpha2.RoleBasedGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name: "1-test-cluster",
				},
				Spec: workloadsv1alpha2.RoleBasedGroupSpec{
					Roles: []workloadsv1alpha2.RoleSpec{
						{
							Name:     "worker",
							Replicas: &replicas3,
							ServicePorts: []corev1.ServicePort{
								{
									Name: "http",
									Port: 8080,
								},
							},
						},
						{
							Name:     "leader",
							Replicas: &replicas1,
							ServicePorts: []corev1.ServicePort{
								{
									Name: "api",
									Port: 6443,
								},
							},
						},
					},
				},
			},
			role: &workloadsv1alpha2.RoleSpec{
				Name:     "worker",
				Replicas: &replicas3,
			},
			expected: `group:
  name: 1-test-cluster
  roles:
  - worker
  - leader
  size: 2
roles:
  leader:
    instances:
    - address: 1-test-cluster-leader-0.s-1-test-cluster-leader
      ports:
        api: 6443
    size: 1
  worker:
    instances:
    - address: 1-test-cluster-worker-0.s-1-test-cluster-worker
      ports:
        http: 8080
    - address: 1-test-cluster-worker-1.s-1-test-cluster-worker
      ports:
        http: 8080
    - address: 1-test-cluster-worker-2.s-1-test-cluster-worker
      ports:
        http: 8080
    size: 3
`,
			wantErr: false,
		},
		{
			name:   "custom components pattern with replicas=2",
			client: fake.NewClientBuilder().WithScheme(schema).Build(),
			rbg: &workloadsv1alpha2.RoleBasedGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-cluster",
				},
				Spec: workloadsv1alpha2.RoleBasedGroupSpec{
					Roles: []workloadsv1alpha2.RoleSpec{
						{
							Name:     "prefill",
							Replicas: ptr.To(int32(2)),
							Pattern: workloadsv1alpha2.Pattern{
								CustomComponentsPattern: &workloadsv1alpha2.CustomComponentsPattern{
									Components: []workloadsv1alpha2.InstanceComponent{
										{Name: "leader", Size: ptr.To(int32(1))},
										{Name: "worker", Size: ptr.To(int32(2))},
									},
								},
							},
						},
					},
				},
			},
			role: &workloadsv1alpha2.RoleSpec{
				Name:     "prefill",
				Replicas: ptr.To(int32(2)),
			},
			expected: `group:
  name: test-cluster
  roles:
  - prefill
  size: 1
roles:
  prefill:
    instances:
    - address: test-cluster-prefill-0-leader-0.s-test-cluster-prefill
    - address: test-cluster-prefill-0-worker-0.s-test-cluster-prefill
    - address: test-cluster-prefill-0-worker-1.s-test-cluster-prefill
    - address: test-cluster-prefill-1-leader-0.s-test-cluster-prefill
    - address: test-cluster-prefill-1-worker-0.s-test-cluster-prefill
    - address: test-cluster-prefill-1-worker-1.s-test-cluster-prefill
    size: 6
`,
			wantErr: false,
		},
		{
			name:   "custom components pattern with omitted size defaults to 1",
			client: fake.NewClientBuilder().WithScheme(schema).Build(),
			rbg: &workloadsv1alpha2.RoleBasedGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-cluster",
				},
				Spec: workloadsv1alpha2.RoleBasedGroupSpec{
					Roles: []workloadsv1alpha2.RoleSpec{
						{
							Name:     "decode",
							Replicas: ptr.To(int32(1)),
							Pattern: workloadsv1alpha2.Pattern{
								CustomComponentsPattern: &workloadsv1alpha2.CustomComponentsPattern{
									Components: []workloadsv1alpha2.InstanceComponent{
										{Name: "leader"},
										{Name: "worker", Size: ptr.To(int32(2))},
									},
								},
							},
						},
					},
				},
			},
			role: &workloadsv1alpha2.RoleSpec{
				Name:     "decode",
				Replicas: ptr.To(int32(1)),
			},
			expected: `group:
  name: test-cluster
  roles:
  - decode
  size: 1
roles:
  decode:
    instances:
    - address: test-cluster-decode-0-leader-0.s-test-cluster-decode
    - address: test-cluster-decode-0-worker-0.s-test-cluster-decode
    - address: test-cluster-decode-0-worker-1.s-test-cluster-decode
    size: 3
`,
			wantErr: false,
		},
		{
			name:   "custom components pattern with component serviceName override",
			client: fake.NewClientBuilder().WithScheme(schema).Build(),
			rbg: &workloadsv1alpha2.RoleBasedGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-cluster",
				},
				Spec: workloadsv1alpha2.RoleBasedGroupSpec{
					Roles: []workloadsv1alpha2.RoleSpec{
						{
							Name:     "decode",
							Replicas: ptr.To(int32(1)),
							Pattern: workloadsv1alpha2.Pattern{
								CustomComponentsPattern: &workloadsv1alpha2.CustomComponentsPattern{
									Components: []workloadsv1alpha2.InstanceComponent{
										{Name: "leader", Size: ptr.To(int32(1)), ServiceName: "leader-svc"},
										{Name: "worker", Size: ptr.To(int32(1))},
									},
								},
							},
						},
					},
				},
			},
			role: &workloadsv1alpha2.RoleSpec{
				Name:     "decode",
				Replicas: ptr.To(int32(1)),
			},
			expected: `group:
  name: test-cluster
  roles:
  - decode
  size: 1
roles:
  decode:
    instances:
    - address: test-cluster-decode-0-leader-0.leader-svc
    - address: test-cluster-decode-0-worker-0.s-test-cluster-decode
    size: 2
`,
			wantErr: false,
		},
		{
			name:   "leader worker pattern with size=2 on RoleInstanceSet",
			client: fake.NewClientBuilder().WithScheme(schema).Build(),
			rbg: &workloadsv1alpha2.RoleBasedGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-cluster",
				},
				Spec: workloadsv1alpha2.RoleBasedGroupSpec{
					Roles: []workloadsv1alpha2.RoleSpec{
						{
							Name:     "prefill",
							Replicas: ptr.To(int32(1)),
							Pattern: workloadsv1alpha2.Pattern{
								LeaderWorkerPattern: &workloadsv1alpha2.LeaderWorkerPattern{
									Size: ptr.To(int32(2)),
								},
							},
						},
					},
				},
			},
			role: &workloadsv1alpha2.RoleSpec{
				Name:     "prefill",
				Replicas: ptr.To(int32(1)),
			},
			expected: `group:
  name: test-cluster
  roles:
  - prefill
  size: 1
roles:
  prefill:
    instances:
    - address: test-cluster-prefill-0-0.s-test-cluster-prefill
    - address: test-cluster-prefill-0-1.s-test-cluster-prefill
    size: 2
`,
			wantErr: false,
		},
		{
			name:   "leader worker pattern with size=2 on LeaderWorkerSet",
			client: fake.NewClientBuilder().WithScheme(schema).Build(),
			rbg: &workloadsv1alpha2.RoleBasedGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-cluster",
				},
				Spec: workloadsv1alpha2.RoleBasedGroupSpec{
					Roles: []workloadsv1alpha2.RoleSpec{
						{
							Name:     "prefill",
							Replicas: ptr.To(int32(2)),
							Annotations: map[string]string{
								constants.RoleWorkloadTypeAnnotationKey: "leaderworkerset.x-k8s.io/v1/LeaderWorkerSet",
							},
							Pattern: workloadsv1alpha2.Pattern{
								LeaderWorkerPattern: &workloadsv1alpha2.LeaderWorkerPattern{
									Size: ptr.To(int32(2)),
								},
							},
						},
					},
				},
			},
			role: &workloadsv1alpha2.RoleSpec{
				Name:     "prefill",
				Replicas: ptr.To(int32(2)),
			},
			expected: `group:
  name: test-cluster
  roles:
  - prefill
  size: 1
roles:
  prefill:
    instances:
    - address: test-cluster-prefill-0.s-test-cluster-prefill
    - address: test-cluster-prefill-1.s-test-cluster-prefill
    - address: test-cluster-prefill-2.s-test-cluster-prefill
    - address: test-cluster-prefill-3.s-test-cluster-prefill
    size: 4
`,
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(
			tt.name, func(t *testing.T) {
				b := &ConfigBuilder{
					client: tt.client,
					rbg:    tt.rbg,
					role:   tt.role,
				}
				got, err := b.Build(context.Background())
				if (err != nil) != tt.wantErr {
					t.Errorf("ConfigBuilder.Build() error = %v, wantErr %v", err, tt.wantErr)
					return
				}

				if !tt.wantErr {
					if diff := cmp.Diff(string(got), tt.expected); diff != "" {
						t.Errorf("ConfigBuilder.Build() mismatch (-want +got):\n%s", diff)
					}
				}
			},
		)
	}
}

func TestConfigBuilder_BuildCompact(t *testing.T) {
	replicas1 := int32(1)
	schema := runtime.NewScheme()
	_ = corev1.AddToScheme(schema)
	_ = workloadsv1alpha2.AddToScheme(schema)

	tests := []struct {
		name     string
		rbg      *workloadsv1alpha2.RoleBasedGroup
		expected string
		wantErr  bool
	}{
		{
			name: "standalone role with membership holes",
			rbg: &workloadsv1alpha2.RoleBasedGroup{
				ObjectMeta: metav1.ObjectMeta{Name: "pd-inference"},
				Spec: workloadsv1alpha2.RoleBasedGroupSpec{
					Roles: []workloadsv1alpha2.RoleSpec{{
						Name:     "prefill",
						Replicas: ptr.To(int32(3)),
						Annotations: map[string]string{
							constants.ExcludedOrdinalsAnnotationKey: "1,3",
						},
						ServicePorts: []corev1.ServicePort{
							{Name: "http-api", Port: 8080},
							{Port: 50000},
						},
					}},
				},
			},
			expected: `apiVersion: v1
group: pd-inference
roles:
- components:
  - base: pd-inference-prefill-{roleIndex}.s-pd-inference-prefill
    name: prefill
    size: 1
  excludedOrdinals: 1,3
  members: 3
  name: prefill
  ports:
    http_api: 8080
    port50000: 50000
`,
		},
		{
			name: "leader worker role",
			rbg: &workloadsv1alpha2.RoleBasedGroup{
				ObjectMeta: metav1.ObjectMeta{Name: "pd-inference"},
				Spec: workloadsv1alpha2.RoleBasedGroupSpec{
					Roles: []workloadsv1alpha2.RoleSpec{{
						Name:     "prefill",
						Replicas: &replicas1,
						Pattern: workloadsv1alpha2.Pattern{
							LeaderWorkerPattern: &workloadsv1alpha2.LeaderWorkerPattern{
								Size: ptr.To(int32(5)),
							},
						},
					}},
				},
			},
			expected: `apiVersion: v1
group: pd-inference
roles:
- components:
  - base: pd-inference-prefill-{roleIndex}-{componentIndex}.s-pd-inference-prefill
    name: leader
    size: 1
  - base: pd-inference-prefill-{roleIndex}-{componentIndex}.s-pd-inference-prefill
    name: worker
    offset: 1
    size: 4
  members: 1
  name: prefill
`,
		},
		{
			name: "custom components with service override",
			rbg: &workloadsv1alpha2.RoleBasedGroup{
				ObjectMeta: metav1.ObjectMeta{Name: "pd-inference"},
				Spec: workloadsv1alpha2.RoleBasedGroupSpec{
					Roles: []workloadsv1alpha2.RoleSpec{{
						Name:     "prefill",
						Replicas: &replicas1,
						Pattern: workloadsv1alpha2.Pattern{
							CustomComponentsPattern: &workloadsv1alpha2.CustomComponentsPattern{
								Components: []workloadsv1alpha2.InstanceComponent{
									{Name: "leader", Size: ptr.To(int32(1))},
									{Name: "worker", Size: ptr.To(int32(4)), ServiceName: "worker-svc"},
								},
							},
						},
					}},
				},
			},
			expected: `apiVersion: v1
group: pd-inference
roles:
- components:
  - base: pd-inference-prefill-{roleIndex}-leader-{componentIndex}.s-pd-inference-prefill
    name: leader
    size: 1
  - base: pd-inference-prefill-{roleIndex}-worker-{componentIndex}.worker-svc
    name: worker
    size: 4
  members: 1
  name: prefill
`,
		},
		{
			name: "rejects deprecated stateful workload types",
			rbg: &workloadsv1alpha2.RoleBasedGroup{
				ObjectMeta: metav1.ObjectMeta{Name: "old-rbg"},
				Spec: workloadsv1alpha2.RoleBasedGroupSpec{
					Roles: []workloadsv1alpha2.RoleSpec{{
						Name:     "prefill",
						Replicas: &replicas1,
						Annotations: map[string]string{
							constants.RoleWorkloadTypeAnnotationKey: constants.StatefulSetWorkloadType,
						},
					}},
				},
			},
			wantErr: true,
		},
		{
			name: "custom components clamps non-positive sizes to zero",
			rbg: &workloadsv1alpha2.RoleBasedGroup{
				ObjectMeta: metav1.ObjectMeta{Name: "pd-inference"},
				Spec: workloadsv1alpha2.RoleBasedGroupSpec{
					Roles: []workloadsv1alpha2.RoleSpec{{
						Name:     "prefill",
						Replicas: &replicas1,
						Pattern: workloadsv1alpha2.Pattern{
							CustomComponentsPattern: &workloadsv1alpha2.CustomComponentsPattern{
								Components: []workloadsv1alpha2.InstanceComponent{
									{Name: "leader", Size: ptr.To(int32(1))},
									{Name: "worker", Size: ptr.To(int32(-1))},
									{Name: "empty", Size: ptr.To(int32(0))},
								},
							},
						},
					}},
				},
			},
			expected: `apiVersion: v1
group: pd-inference
roles:
- components:
  - base: pd-inference-prefill-{roleIndex}-leader-{componentIndex}.s-pd-inference-prefill
    name: leader
    size: 1
  - base: pd-inference-prefill-{roleIndex}-worker-{componentIndex}.s-pd-inference-prefill
    name: worker
    size: 0
  - base: pd-inference-prefill-{roleIndex}-empty-{componentIndex}.s-pd-inference-prefill
    name: empty
    size: 0
  members: 1
  name: prefill
`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			builder := NewConfigBuilder(
				fake.NewClientBuilder().WithScheme(schema).Build(),
				tt.rbg,
				nil,
			)
			got, err := builder.BuildCompact(context.Background())
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.expected, string(got))
		})
	}
}

func TestCompactMembershipDecoding(t *testing.T) {
	// The reference decoder jumps ranges instead of expanding them. The KEP's
	// extreme case must work without allocating billions of ordinals.
	members, err := decodeCompactMembers(1, "0-2147483645")
	require.NoError(t, err)
	assert.Equal(t, []int{2147483646}, members)

	members, err = decodeCompactMembers(3, "1,3")
	require.NoError(t, err)
	assert.Equal(t, []int{0, 2, 4}, members)

	members, err = decodeCompactMembers(0, "1,3")
	require.NoError(t, err)
	assert.Empty(t, members)
}

func decodeCompactMembers(count int, excluded string) ([]int, error) {
	ranges, err := ordinals.Parse(excluded)
	if err != nil {
		return nil, err
	}
	result := make([]int, 0, count)
	for next := 0; len(result) < count; {
		ordinal, err := ranges.Next(next)
		if err != nil {
			return nil, err
		}
		result = append(result, ordinal)
		next = ordinal + 1
	}
	return result, nil
}

func TestConfigBuilder_BuildCompactHugeReplicas(t *testing.T) {
	// A dense role at the ordinal limit must serialize without materializing
	// two billion member ordinals.
	schema := runtime.NewScheme()
	_ = corev1.AddToScheme(schema)
	_ = workloadsv1alpha2.AddToScheme(schema)

	rbg := &workloadsv1alpha2.RoleBasedGroup{
		ObjectMeta: metav1.ObjectMeta{Name: "huge"},
		Spec: workloadsv1alpha2.RoleBasedGroupSpec{
			Roles: []workloadsv1alpha2.RoleSpec{{
				Name:     "prefill",
				Replicas: ptr.To(int32(2147483647)),
			}},
		},
	}
	builder := NewConfigBuilder(fake.NewClientBuilder().WithScheme(schema).Build(), rbg, nil)

	data, err := builder.BuildCompact(context.Background())
	require.NoError(t, err)

	var cfg CompactConfig
	require.NoError(t, yaml.UnmarshalStrict(data, &cfg))
	require.Equal(t, 2147483647, cfg.Roles[0].Members)
	require.Empty(t, cfg.Roles[0].ExcludedOrdinals)
	require.Less(t, len(data), 4096, "compact content must not scale with replica count")
}

func TestValidateCompactPlaceholders(t *testing.T) {
	standalone := &workloadsv1alpha2.RoleSpec{Name: "prefill"}
	leaderWorker := &workloadsv1alpha2.RoleSpec{
		Name:    "prefill",
		Pattern: workloadsv1alpha2.Pattern{LeaderWorkerPattern: &workloadsv1alpha2.LeaderWorkerPattern{}},
	}

	tests := []struct {
		name       string
		role       *workloadsv1alpha2.RoleSpec
		components []CompactComponent
		wantErr    string
	}{
		{
			name: "standalone requires roleIndex",
			role: standalone,
			components: []CompactComponent{{
				Name: "prefill",
				Base: "prefill.s-prefill",
			}},
			wantErr: "missing placeholder {roleIndex}",
		},
		{
			name: "leader worker requires componentIndex",
			role: leaderWorker,
			components: []CompactComponent{{
				Name: "leader",
				Base: "prefill-{roleIndex}.s-prefill",
			}},
			wantErr: "missing placeholder {componentIndex}",
		},
		{
			name: "unknown placeholders are rejected",
			role: standalone,
			components: []CompactComponent{{
				Name: "prefill",
				Base: "prefill-{roleIndex}-{memberIndex}.s-prefill",
			}},
			wantErr: "unknown placeholder {memberIndex}",
		},
		{
			name: "valid templates pass",
			role: leaderWorker,
			components: []CompactComponent{{
				Name: "leader",
				Base: "prefill-{roleIndex}-{componentIndex}.s-prefill",
			}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateCompactPlaceholders(tt.role, tt.components)
			if tt.wantErr == "" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, tt.wantErr)
			}
		})
	}
}

func TestCompactContentVersionMarker(t *testing.T) {
	// Consumer contract: missing apiVersion means v1alpha1, apiVersion: v1
	// means compact, and any other value is rejected.
	type marker struct {
		APIVersion string `json:"apiVersion"`
	}

	var legacy marker
	require.NoError(t, yaml.Unmarshal([]byte("group: demo\n"), &legacy))
	require.Empty(t, legacy.APIVersion)

	var compact marker
	require.NoError(t, yaml.Unmarshal([]byte("apiVersion: v1\ngroup: demo\n"), &compact))
	require.Equal(t, "v1", compact.APIVersion)

	var unknown marker
	require.NoError(t, yaml.Unmarshal([]byte("apiVersion: v2\ngroup: demo\n"), &unknown))
	require.Equal(t, "v2", unknown.APIVersion)
	require.NotEqual(t, "v1", unknown.APIVersion, "consumers must reject unknown versions")
}

func TestConfigBuilder_MemberSummaryMatchesMemberOrdinals(t *testing.T) {
	replicas := int32(5)
	for _, record := range []string{"", "1,3", "1-3", "0,2,4"} {
		t.Run(record, func(t *testing.T) {
			schema := runtime.NewScheme()
			_ = corev1.AddToScheme(schema)
			_ = workloadsv1alpha2.AddToScheme(schema)
			rbg := &workloadsv1alpha2.RoleBasedGroup{
				ObjectMeta: metav1.ObjectMeta{Name: "test-cluster", Namespace: "default"},
				Spec: workloadsv1alpha2.RoleBasedGroupSpec{
					Roles: []workloadsv1alpha2.RoleSpec{{
						Name:     "worker",
						Replicas: &replicas,
						Annotations: map[string]string{
							constants.ExcludedOrdinalsAnnotationKey: record,
						},
					}},
				},
			}
			builder := NewConfigBuilder(fake.NewClientBuilder().WithScheme(schema).Build(), rbg, nil)
			role := &rbg.Spec.Roles[0]

			members, err := builder.memberOrdinals(context.Background(), role)
			require.NoError(t, err)
			count, excluded, err := builder.memberSummary(context.Background(), role)
			require.NoError(t, err)

			require.Equal(t, len(members), count)
			require.Equal(t, ordinals.Record(members), excluded)
		})
	}
}

func TestConfigBuilder_MemberSummaryMatchesCommittedChild(t *testing.T) {
	replicas := int32(5)
	schema := runtime.NewScheme()
	_ = corev1.AddToScheme(schema)
	_ = workloadsv1alpha2.AddToScheme(schema)

	for _, record := range []string{"", "1,3", "1-3"} {
		t.Run("record="+record, func(t *testing.T) {
			rbg := &workloadsv1alpha2.RoleBasedGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-cluster", Namespace: "default", UID: "rbg-uid",
				},
				Spec: workloadsv1alpha2.RoleBasedGroupSpec{
					Roles: []workloadsv1alpha2.RoleSpec{{
						Name:     "worker",
						Replicas: &replicas,
					}},
				},
			}
			set := &workloadsv1alpha2.RoleInstanceSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      rbg.GetWorkloadName(&rbg.Spec.Roles[0]),
					Namespace: rbg.Namespace,
					Labels:    map[string]string{constants.GroupNameLabelKey: rbg.Name},
					Annotations: map[string]string{
						constants.CommittedSkipOrdinalsAnnotationKey: record,
					},
					OwnerReferences: []metav1.OwnerReference{
						*metav1.NewControllerRef(rbg, workloadsv1alpha2.GroupVersion.WithKind("RoleBasedGroup")),
					},
				},
				Spec: workloadsv1alpha2.RoleInstanceSetSpec{Replicas: ptr.To(int32(3))},
			}
			builder := NewConfigBuilder(
				fake.NewClientBuilder().WithScheme(schema).WithObjects(set).Build(), rbg, nil)
			role := &rbg.Spec.Roles[0]

			members, err := builder.memberOrdinals(context.Background(), role)
			require.NoError(t, err)
			count, excluded, err := builder.memberSummary(context.Background(), role)
			require.NoError(t, err)

			require.Equal(t, len(members), count)
			require.Equal(t, ordinals.Record(members), excluded)
		})
	}
}

func TestConfigBuilder_LegacySizeUpperBound(t *testing.T) {
	replicas := int32(3)
	schema := runtime.NewScheme()
	_ = corev1.AddToScheme(schema)
	_ = workloadsv1alpha2.AddToScheme(schema)

	for _, tt := range []struct {
		name string
		rbg  *workloadsv1alpha2.RoleBasedGroup
	}{
		{
			name: "standalone role",
			rbg: &workloadsv1alpha2.RoleBasedGroup{
				ObjectMeta: metav1.ObjectMeta{Name: "test"},
				Spec: workloadsv1alpha2.RoleBasedGroupSpec{
					Roles: []workloadsv1alpha2.RoleSpec{{
						Name: "worker", Replicas: &replicas,
						ServicePorts: []corev1.ServicePort{{Name: "http", Port: 8080}},
					}},
				},
			},
		},
		{
			name: "leader worker role",
			rbg: &workloadsv1alpha2.RoleBasedGroup{
				ObjectMeta: metav1.ObjectMeta{Name: "test"},
				Spec: workloadsv1alpha2.RoleBasedGroupSpec{
					Roles: []workloadsv1alpha2.RoleSpec{{
						Name: "worker", Replicas: &replicas,
						Pattern: workloadsv1alpha2.Pattern{
							LeaderWorkerPattern: &workloadsv1alpha2.LeaderWorkerPattern{Size: ptr.To(int32(3))},
						},
					}},
				},
			},
		},
		{
			name: "custom components with service override",
			rbg: &workloadsv1alpha2.RoleBasedGroup{
				ObjectMeta: metav1.ObjectMeta{Name: "test"},
				Spec: workloadsv1alpha2.RoleBasedGroupSpec{
					Roles: []workloadsv1alpha2.RoleSpec{{
						Name: "worker", Replicas: &replicas,
						Pattern: workloadsv1alpha2.Pattern{
							CustomComponentsPattern: &workloadsv1alpha2.CustomComponentsPattern{
								Components: []workloadsv1alpha2.InstanceComponent{
									{Name: "leader", Size: ptr.To(int32(1)), ServiceName: "leader-svc"},
									{Name: "worker", Size: ptr.To(int32(2))},
								},
							},
						},
					}},
				},
			},
		},
		{
			name: "non-positive component sizes emit no entries",
			rbg: &workloadsv1alpha2.RoleBasedGroup{
				ObjectMeta: metav1.ObjectMeta{Name: "test"},
				Spec: workloadsv1alpha2.RoleBasedGroupSpec{
					Roles: []workloadsv1alpha2.RoleSpec{{
						Name: "worker", Replicas: &replicas,
						Pattern: workloadsv1alpha2.Pattern{
							CustomComponentsPattern: &workloadsv1alpha2.CustomComponentsPattern{
								Components: []workloadsv1alpha2.InstanceComponent{
									{Name: "leader", Size: ptr.To(int32(0))},
									{Name: "worker", Size: ptr.To(int32(-1))},
								},
							},
						},
					}},
				},
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			builder := NewConfigBuilder(fake.NewClientBuilder().WithScheme(schema).Build(), tt.rbg, nil)
			upper, err := builder.LegacySizeUpperBound(context.Background())
			require.NoError(t, err)

			built, err := builder.Build(context.Background())
			require.NoError(t, err)
			require.GreaterOrEqual(t, upper, int64(len(built)),
				"the estimator must stay an upper bound of the serialized content")
		})
	}

	t.Run("huge component sizes reject before enumeration", func(t *testing.T) {
		rbg := &workloadsv1alpha2.RoleBasedGroup{
			ObjectMeta: metav1.ObjectMeta{Name: "test"},
			Spec: workloadsv1alpha2.RoleBasedGroupSpec{
				Roles: []workloadsv1alpha2.RoleSpec{{
					Name:     "worker",
					Replicas: ptr.To(int32(1)),
					Pattern: workloadsv1alpha2.Pattern{
						CustomComponentsPattern: &workloadsv1alpha2.CustomComponentsPattern{
							Components: []workloadsv1alpha2.InstanceComponent{
								{Name: "worker", Size: ptr.To(int32(2147483647))},
							},
						},
					},
				}},
			},
		}
		builder := NewConfigBuilder(fake.NewClientBuilder().WithScheme(schema).Build(), rbg, nil)
		upper, err := builder.LegacySizeUpperBound(context.Background())
		require.NoError(t, err)
		require.Greater(t, upper, int64(1<<20))
	})

	t.Run("large ordinals remain an upper bound", func(t *testing.T) {
		rbg := &workloadsv1alpha2.RoleBasedGroup{
			ObjectMeta: metav1.ObjectMeta{Name: "test"},
			Spec: workloadsv1alpha2.RoleBasedGroupSpec{
				Roles: []workloadsv1alpha2.RoleSpec{{
					Name:     "worker",
					Replicas: ptr.To(int32(1000)),
				}},
			},
		}
		builder := NewConfigBuilder(fake.NewClientBuilder().WithScheme(schema).Build(), rbg, nil)

		upper, err := builder.LegacySizeUpperBound(context.Background())
		require.NoError(t, err)
		built, err := builder.Build(context.Background())
		require.NoError(t, err)
		require.GreaterOrEqual(t, upper, int64(len(built)))
	})

	t.Run("maximum int32 replicas and leader-worker size saturate", func(t *testing.T) {
		rbg := &workloadsv1alpha2.RoleBasedGroup{
			ObjectMeta: metav1.ObjectMeta{Name: "test"},
			Spec: workloadsv1alpha2.RoleBasedGroupSpec{
				Roles: []workloadsv1alpha2.RoleSpec{{
					Name:     "worker",
					Replicas: ptr.To(int32(math.MaxInt32)),
					Pattern: workloadsv1alpha2.Pattern{
						LeaderWorkerPattern: &workloadsv1alpha2.LeaderWorkerPattern{
							Size: ptr.To(int32(math.MaxInt32)),
						},
					},
				}},
			},
		}
		builder := NewConfigBuilder(fake.NewClientBuilder().WithScheme(schema).Build(), rbg, nil)
		upper, err := builder.LegacySizeUpperBound(context.Background())
		require.NoError(t, err)
		require.Equal(t, int64(math.MaxInt64), upper)
	})

	t.Run("port-heavy role exceeds the budget before enumeration", func(t *testing.T) {
		ports := make([]corev1.ServicePort, 60000)
		for i := range ports {
			ports[i] = corev1.ServicePort{Name: fmt.Sprintf("port-%06d", i), Port: 8080}
		}
		rbg := &workloadsv1alpha2.RoleBasedGroup{
			ObjectMeta: metav1.ObjectMeta{Name: "test"},
			Spec: workloadsv1alpha2.RoleBasedGroupSpec{
				Roles: []workloadsv1alpha2.RoleSpec{{
					Name: "worker", Replicas: ptr.To(int32(1)), ServicePorts: ports,
				}},
			},
		}
		builder := NewConfigBuilder(fake.NewClientBuilder().WithScheme(schema).Build(), rbg, nil)
		upper, err := builder.LegacySizeUpperBound(context.Background())
		require.NoError(t, err)
		require.Greater(t, upper, int64(1<<20))
	})

	t.Run("huge replicas with zero-address role skip enumeration", func(t *testing.T) {
		rbg := &workloadsv1alpha2.RoleBasedGroup{
			ObjectMeta: metav1.ObjectMeta{Name: "test"},
			Spec: workloadsv1alpha2.RoleBasedGroupSpec{
				Roles: []workloadsv1alpha2.RoleSpec{{
					Name:     "worker",
					Replicas: ptr.To(int32(2147483647)),
					Pattern: workloadsv1alpha2.Pattern{
						CustomComponentsPattern: &workloadsv1alpha2.CustomComponentsPattern{
							Components: []workloadsv1alpha2.InstanceComponent{
								{Name: "worker", Size: ptr.To(int32(0))},
							},
						},
					},
				}},
			},
		}
		builder := NewConfigBuilder(fake.NewClientBuilder().WithScheme(schema).Build(), rbg, nil)
		built, err := builder.Build(context.Background())
		require.NoError(t, err)
		require.NotContains(t, string(built), "address:", "a zero-address role must not enumerate instances")
	})
}

func TestConfigBuilder_BuildCompactMatchesLegacyAddresses(t *testing.T) {
	replicas := int32(3)
	schema := runtime.NewScheme()
	_ = corev1.AddToScheme(schema)
	_ = workloadsv1alpha2.AddToScheme(schema)

	rbg := &workloadsv1alpha2.RoleBasedGroup{
		ObjectMeta: metav1.ObjectMeta{Name: "test"},
		Spec: workloadsv1alpha2.RoleBasedGroupSpec{
			Roles: []workloadsv1alpha2.RoleSpec{
				{
					Name: "standalone", Replicas: &replicas,
					Annotations:  map[string]string{constants.ExcludedOrdinalsAnnotationKey: "1"},
					ServicePorts: []corev1.ServicePort{{Name: "http-api", Port: 8080}, {Port: 50000}},
				},
				{
					Name: "leader-worker", Replicas: ptr.To(int32(1)),
					Pattern: workloadsv1alpha2.Pattern{
						LeaderWorkerPattern: &workloadsv1alpha2.LeaderWorkerPattern{Size: ptr.To(int32(3))},
					},
				},
				{
					Name: "custom", Replicas: ptr.To(int32(2)),
					Pattern: workloadsv1alpha2.Pattern{
						CustomComponentsPattern: &workloadsv1alpha2.CustomComponentsPattern{
							Components: []workloadsv1alpha2.InstanceComponent{
								{Name: "leader", Size: ptr.To(int32(1))},
								{Name: "worker", Size: ptr.To(int32(2))},
							},
						},
					},
				},
			},
		},
	}
	builder := NewConfigBuilder(fake.NewClientBuilder().WithScheme(schema).Build(), rbg, nil)

	legacyData, err := builder.Build(context.Background())
	require.NoError(t, err)
	compactData, err := builder.BuildCompact(context.Background())
	require.NoError(t, err)

	var legacy ClusterConfig
	require.NoError(t, yaml.UnmarshalStrict(legacyData, &legacy))
	var compact CompactConfig
	require.NoError(t, yaml.UnmarshalStrict(compactData, &compact))

	for _, role := range compact.Roles {
		legacyInstances := legacy.Roles[role.Name].Instances
		require.Equal(t, legacyInstances, expandCompactRole(t, role))
	}
}

func TestBuildCompactSkipsStatelessRoles(t *testing.T) {
	schema := runtime.NewScheme()
	_ = corev1.AddToScheme(schema)
	_ = workloadsv1alpha2.AddToScheme(schema)

	rbg := &workloadsv1alpha2.RoleBasedGroup{
		ObjectMeta: metav1.ObjectMeta{Name: "test"},
		Spec: workloadsv1alpha2.RoleBasedGroupSpec{
			Roles: []workloadsv1alpha2.RoleSpec{
				{Name: "stateful", Replicas: ptr.To(int32(1))},
				{
					Name:     "stateless",
					Replicas: ptr.To(int32(1)),
					Annotations: map[string]string{
						constants.RoleWorkloadTypeAnnotationKey: constants.DeploymentWorkloadType,
					},
				},
			},
		},
	}
	builder := NewConfigBuilder(fake.NewClientBuilder().WithScheme(schema).Build(), rbg, nil)

	data, err := builder.BuildCompact(context.Background())
	require.NoError(t, err)

	var config CompactConfig
	require.NoError(t, yaml.UnmarshalStrict(data, &config))
	require.Len(t, config.Roles, 1)
	assert.Equal(t, "stateful", config.Roles[0].Name)
}

func expandCompactRole(t *testing.T, role CompactRole) []Instance {
	members, err := decodeCompactMembers(role.Members, role.ExcludedOrdinals)
	require.NoError(t, err)

	instances := make([]Instance, 0, role.Members)
	for _, member := range members {
		for _, component := range role.Components {
			for i := 0; i < component.Size; i++ {
				address := component.Base
				address = strings.ReplaceAll(address, "{roleIndex}", strconv.Itoa(member))
				address = strings.ReplaceAll(address, "{componentIndex}", strconv.Itoa(component.Offset+i))
				instances = append(instances, Instance{
					Address: address,
					Ports:   role.Ports,
				})
			}
		}
	}
	return instances
}

// TestConfigBuilder_getRoleNames tests the getRoleNames method of ConfigBuilder
func TestConfigBuilder_getRoleNames(t *testing.T) {
	replicas := int32(1)

	rbg := &workloadsv1alpha2.RoleBasedGroup{
		Spec: workloadsv1alpha2.RoleBasedGroupSpec{
			Roles: []workloadsv1alpha2.RoleSpec{
				{
					Name:     "role1",
					Replicas: &replicas,
				},
				{
					Name:     "role2",
					Replicas: &replicas,
				},
				{
					Name:     "role3",
					Replicas: &replicas,
				},
			},
		},
	}

	b := &ConfigBuilder{
		rbg: rbg,
	}

	expected := []string{"role1", "role2", "role3"}
	got := b.getRoleNames()

	if diff := cmp.Diff(got, expected); diff != "" {
		t.Errorf("ConfigBuilder.getRoleNames() mismatch (-want +got):\n%s", diff)
	}
}

// TestConfigBuilder_buildRolesInfo tests the buildRolesInfo method of ConfigBuilder
func TestConfigBuilder_buildRolesInfo(t *testing.T) {
	replicas3 := int32(3)
	replicas1 := int32(1)

	rbg := &workloadsv1alpha2.RoleBasedGroup{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-cluster",
		},
		Spec: workloadsv1alpha2.RoleBasedGroupSpec{
			Roles: []workloadsv1alpha2.RoleSpec{
				{
					Name:     "worker",
					Replicas: &replicas3,
					ServicePorts: []corev1.ServicePort{
						{
							Name: "http",
							Port: 8080,
						},
					},
				},
				{
					Name:     "master",
					Replicas: &replicas1,
					ServicePorts: []corev1.ServicePort{
						{
							Name: "api",
							Port: 6443,
						},
					},
				},
			},
		},
	}
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = workloadsv1alpha2.AddToScheme(scheme)

	b := &ConfigBuilder{
		client: fake.NewClientBuilder().WithScheme(scheme).Build(),
		rbg:    rbg,
	}

	rolesInfo, err := b.buildRolesInfo(context.Background())
	if err != nil {
		t.Errorf("buildRolesInfo() error = %v", err)
	}

	// Verify number of roles
	if len(rolesInfo) != 2 {
		t.Errorf("Expected 2 roles, got %d", len(rolesInfo))
	}

	// Verify worker role
	workerRole, exists := rolesInfo["worker"]
	if !exists {
		t.Error("Expected 'worker' role not found")
	} else {
		if workerRole.Size != 3 {
			t.Errorf("Expected worker size 3, got %d", workerRole.Size)
		}
		if len(workerRole.Instances) != 3 {
			t.Errorf("Expected 3 worker instances, got %d", len(workerRole.Instances))
		}
	}

	// Verify master role
	masterRole, exists := rolesInfo["master"]
	if !exists {
		t.Error("Expected 'master' role not found")
	} else {
		if masterRole.Size != 1 {
			t.Errorf("Expected master size 1, got %d", masterRole.Size)
		}
		if len(masterRole.Instances) != 1 {
			t.Errorf("Expected 1 master instance, got %d", len(masterRole.Instances))
		}
	}
}

// TestGeneratePortKey tests the generatePortKey function
func TestGeneratePortKey(t *testing.T) {
	tests := []struct {
		name     string
		port     corev1.ServicePort
		expected string
	}{
		{
			name: "named port",
			port: corev1.ServicePort{
				Name: "http",
				Port: 8080,
			},
			expected: "http",
		},
		{
			name: "named port with dash",
			port: corev1.ServicePort{
				Name: "http-api",
				Port: 8080,
			},
			expected: "http_api",
		},
		{
			name: "unnamed port",
			port: corev1.ServicePort{
				Port: 80,
			},
			expected: "port80",
		},
	}

	for _, tt := range tests {
		t.Run(
			tt.name, func(t *testing.T) {
				got := generatePortKey(tt.port)
				if got != tt.expected {
					t.Errorf("generatePortKey() = %v, want %v", got, tt.expected)
				}
			},
		)
	}
}

// TestSemanticallyEqualConfigmap tests the semanticallyEqualConfigmap function
func TestSemanticallyEqualConfigmap(t *testing.T) {
	baseConfigMap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "test-config",
			Namespace:       "default",
			ResourceVersion: "1",
			UID:             "uid-1",
		},
		Data: map[string]string{
			"config.yaml": "test: data",
		},
	}

	tests := []struct {
		name     string
		old      *corev1.ConfigMap
		new      *corev1.ConfigMap
		expected bool
	}{
		{
			name:     "both nil",
			old:      nil,
			new:      nil,
			expected: true,
		},
		{
			name:     "one nil",
			old:      nil,
			new:      baseConfigMap,
			expected: false,
		},
		{
			name:     "equal ignoring metadata",
			old:      baseConfigMap,
			new:      baseConfigMap.DeepCopy(),
			expected: true,
		},
		{
			name: "different data",
			old:  baseConfigMap,
			new: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-config",
					Namespace: "default",
				},
				Data: map[string]string{
					"config.yaml": "different: data",
				},
			},
			expected: false,
		},
		{
			name: "same data, different metadata",
			old:  baseConfigMap,
			new: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:            "test-config",
					Namespace:       "default",
					ResourceVersion: "2",
					UID:             "uid-2",
				},
				Data: map[string]string{
					"config.yaml": "test: data",
				},
			},
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(
			tt.name, func(t *testing.T) {
				got, _ := semanticallyEqualConfigmap(tt.old, tt.new)
				if got != tt.expected {
					t.Errorf("semanticallyEqualConfigmap() = %v, want %v", got, tt.expected)
				}
			},
		)
	}
}

// TestConfigBuilder_buildInstances tests the buildInstances method of ConfigBuilder
func TestConfigBuilder_buildInstances(t *testing.T) {
	replicas := int32(2)

	rbg := &workloadsv1alpha2.RoleBasedGroup{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-cluster",
		},
	}

	role := &workloadsv1alpha2.RoleSpec{
		Name:     "server",
		Replicas: &replicas,
		ServicePorts: []corev1.ServicePort{
			{
				Name: "http",
				Port: 80,
			},
			{
				Name: "https",
				Port: 443,
			},
		},
	}

	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = workloadsv1alpha2.AddToScheme(scheme)

	b := &ConfigBuilder{
		client: fake.NewClientBuilder().WithScheme(scheme).Build(),
		rbg:    rbg,
		role:   role,
	}

	instances, err := b.buildInstances(context.Background(), role)
	if err != nil {
		t.Errorf("buildInstances() error = %v", err)
	}

	// Verify number of instances
	if len(instances) != int(replicas) {
		t.Errorf("Expected %d instances, got %d", replicas, len(instances))
	}

	// Verify first instance
	if instances[0].Address != "test-cluster-server-0.s-test-cluster-server" {
		t.Errorf("Expected address 'test-cluster-server-0.test-cluster-server', got '%s'", instances[0].Address)
	}

	if len(instances[0].Ports) != 2 {
		t.Errorf("Expected 2 ports, got %d", len(instances[0].Ports))
	}

	if port, exists := instances[0].Ports["http"]; !exists || port != 80 {
		t.Errorf("Expected http port 80, got %v", instances[0].Ports["http"])
	}

	if port, exists := instances[0].Ports["https"]; !exists || port != 443 {
		t.Errorf("Expected https port 443, got %v", instances[0].Ports["https"])
	}
}

func TestConfigBuilderBuildCommittedSparseMembership(t *testing.T) {
	sparseAddresses := []string{
		"test-cluster-worker-0.s-test-cluster-worker",
		"test-cluster-worker-2.s-test-cluster-worker",
		"test-cluster-worker-4.s-test-cluster-worker",
	}
	denseAddresses := []string{
		"test-cluster-worker-0.s-test-cluster-worker",
		"test-cluster-worker-1.s-test-cluster-worker",
		"test-cluster-worker-2.s-test-cluster-worker",
	}
	for _, tc := range []struct {
		name          string
		excluded      *string
		missingChild  bool
		record, owner string
		groupLabel    *string
		wantAddresses []string
		wantError     string
	}{
		{name: "invalid exclusions preserve committed members", excluded: ptr.To("2-1"), record: "1,3", wantAddresses: sparseAddresses},
		{name: "invalid exclusions preserve dense members", excluded: ptr.To("2-1"), wantAddresses: denseAddresses},
		{name: "invalid exclusions without a child publish no guessed members", excluded: ptr.To("2-1"), missingChild: true},
		{name: "committed sparse members", excluded: ptr.To("1,3"), record: "1,3", wantAddresses: sparseAddresses},
		{name: "changed exclusions at fixed replicas", excluded: ptr.To("0,2,4"), record: "1,3", wantAddresses: sparseAddresses},
		{name: "removed exclusions at fixed replicas", record: "1,3", wantAddresses: sparseAddresses},
		{name: "empty exclusions at fixed replicas", excluded: ptr.To(""), record: "1,3", wantAddresses: sparseAddresses},
		{name: "missing child plans initial sparse members", missingChild: true, excluded: ptr.To("1,3"), wantAddresses: sparseAddresses},
		{name: "missing child without exclusions stays dense", missingChild: true, wantAddresses: denseAddresses},
		{name: "invalid child record", record: "3-1", wantError: "invalid " + constants.CommittedSkipOrdinalsAnnotationKey},
		{name: "mismatched owner with exclusions", excluded: ptr.To("1,3"), record: "1,3", owner: "different", wantError: "has a different controller owner"},
		{name: "mismatched owner after exclusions removed", record: "1,3", owner: "different", wantError: "has a different controller owner"},
		{name: "missing controller owner", record: "1,3", owner: "missing", wantError: "has a different controller owner"},
		{name: "claimable dense orphan follows current members", excluded: ptr.To("1,3"), owner: "missing", wantAddresses: denseAddresses},
		{name: "dense orphan with mismatched group label", excluded: ptr.To("1,3"), owner: "missing", groupLabel: ptr.To("other"), wantError: "has a different controller owner"},
		{name: "dense orphan without group label", excluded: ptr.To("1,3"), owner: "missing", groupLabel: ptr.To(""), wantError: "has a different controller owner"},
		{name: "dense child with foreign controller", excluded: ptr.To("1,3"), owner: "different", wantError: "has a different controller owner"},
		{name: "sparse orphan with matching label", excluded: ptr.To("1,3"), record: "1,3", owner: "missing", wantError: "has a different controller owner"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			require.NoError(t, corev1.AddToScheme(scheme))
			require.NoError(t, workloadsv1alpha2.AddToScheme(scheme))
			role := workloadsv1alpha2.RoleSpec{
				Name: "worker", Replicas: ptr.To[int32](3),
				Annotations:  map[string]string{constants.RoleInstancePatternKey: string(constants.StatefulPattern)},
				ServicePorts: []corev1.ServicePort{{Name: "http", Port: 8080}},
			}
			if tc.excluded != nil {
				role.Annotations[constants.ExcludedOrdinalsAnnotationKey] = *tc.excluded
			}
			rbg := &workloadsv1alpha2.RoleBasedGroup{
				ObjectMeta: metav1.ObjectMeta{Name: "test-cluster", Namespace: "default", UID: "group-uid"},
				Spec:       workloadsv1alpha2.RoleBasedGroupSpec{Roles: []workloadsv1alpha2.RoleSpec{role}},
			}
			builder := fake.NewClientBuilder().WithScheme(scheme)
			if !tc.missingChild {
				owner := *metav1.NewControllerRef(rbg, workloadsv1alpha2.GroupVersion.WithKind("RoleBasedGroup"))
				if tc.owner == "different" {
					owner.UID = "different-group-uid"
				}
				set := &workloadsv1alpha2.RoleInstanceSet{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-cluster-worker", Namespace: rbg.Namespace,
						Labels:          map[string]string{constants.GroupNameLabelKey: ptr.Deref(tc.groupLabel, rbg.Name)},
						Annotations:     map[string]string{constants.CommittedSkipOrdinalsAnnotationKey: tc.record},
						OwnerReferences: []metav1.OwnerReference{owner},
					},
					Spec: workloadsv1alpha2.RoleInstanceSetSpec{Replicas: ptr.To[int32](3)},
				}
				if tc.owner == "missing" {
					set.OwnerReferences = nil
				}
				builder.WithObjects(set)
			}
			// No Pods or RoleInstances are needed: discovery follows the persisted target.
			data, err := NewConfigBuilder(builder.Build(), rbg, &rbg.Spec.Roles[0]).Build(context.Background())
			if tc.wantError != "" {
				require.ErrorContains(t, err, tc.wantError)
				require.Nil(t, data, "invalid membership must not produce a discovery payload")
				return
			}
			require.NoError(t, err)
			var config ClusterConfig
			require.NoError(t, yaml.UnmarshalStrict(data, &config))
			require.Equal(t, GroupInfo{Name: rbg.Name, Size: 1, Roles: []string{"worker"}}, config.Group)
			require.Len(t, config.Roles, 1)
			published := config.Roles["worker"]
			require.Equal(t, len(tc.wantAddresses), published.Size)
			require.Len(t, published.Instances, len(tc.wantAddresses))
			for i, address := range tc.wantAddresses {
				require.Equal(t, Instance{Address: address, Ports: map[string]int32{"http": 8080}}, published.Instances[i])
			}
		})
	}
}
