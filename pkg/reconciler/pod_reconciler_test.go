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

package reconciler

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	coreapplyv1 "k8s.io/client-go/applyconfigurations/core/v1"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/rbgs/api/workloads/constants"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	"sigs.k8s.io/rbgs/pkg/discovery"
	wrappersv2 "sigs.k8s.io/rbgs/test/wrappers/v1alpha2"
)

func Test_podSpecEqual(t *testing.T) {
	type args struct {
		spec1 corev1.PodSpec
		spec2 corev1.PodSpec
	}
	tests := []struct {
		name    string
		args    args
		want    bool
		wantErr bool
	}{
		{
			name: "equal pod specs with containers",
			args: args{
				spec1: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  "container1",
							Image: "nginx:1.20",
						},
					},
				},
				spec2: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  "container1",
							Image: "nginx:1.20",
						},
					},
				},
			},
			want:    true,
			wantErr: false,
		},
		{
			name: "unequal container count",
			args: args{
				spec1: corev1.PodSpec{
					Containers: []corev1.Container{
						{Name: "container1", Image: "nginx:1.20"},
					},
				},
				spec2: corev1.PodSpec{
					Containers: []corev1.Container{
						{Name: "container1", Image: "nginx:1.20"},
						{Name: "container2", Image: "redis:6.0"},
					},
				},
			},
			want:    false,
			wantErr: true,
		},
		{
			name: "equal pod specs with volumes",
			args: args{
				spec1: corev1.PodSpec{
					Containers: []corev1.Container{
						{Name: "container1", Image: "nginx:1.20"},
					},
					Volumes: []corev1.Volume{
						{Name: "vol1", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
					},
				},
				spec2: corev1.PodSpec{
					Containers: []corev1.Container{
						{Name: "container1", Image: "nginx:1.20"},
					},
					Volumes: []corev1.Volume{
						{Name: "vol1", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
					},
				},
			},
			want:    true,
			wantErr: false,
		},
		{
			name: "unequal pod specs with different volumes",
			args: args{
				spec1: corev1.PodSpec{
					Containers: []corev1.Container{
						{Name: "container1", Image: "nginx:1.20"},
					},
					Volumes: []corev1.Volume{
						{Name: "vol1", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
					},
				},
				spec2: corev1.PodSpec{
					Containers: []corev1.Container{
						{Name: "container1", Image: "nginx:1.20"},
					},
					Volumes: []corev1.Volume{
						{Name: "vol2", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
					},
				},
			},
			want:    false,
			wantErr: true,
		},
		{
			name: "empty schedulerName equals the apiserver default",
			args: args{
				spec1: corev1.PodSpec{
					SchedulerName: corev1.DefaultSchedulerName,
					Containers:    []corev1.Container{{Name: "container1", Image: "nginx:1.20"}},
				},
				spec2: corev1.PodSpec{
					Containers: []corev1.Container{{Name: "container1", Image: "nginx:1.20"}},
				},
			},
			want:    true,
			wantErr: false,
		},
		{
			name: "unequal schedulerName",
			args: args{
				spec1: corev1.PodSpec{
					SchedulerName: "volcano",
					Containers:    []corev1.Container{{Name: "container1", Image: "nginx:1.20"}},
				},
				spec2: corev1.PodSpec{
					Containers: []corev1.Container{{Name: "container1", Image: "nginx:1.20"}},
				},
			},
			want:    false,
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(
			tt.name, func(t *testing.T) {
				got, err := podSpecEqual(tt.args.spec1, tt.args.spec2)
				if (err != nil) != tt.wantErr {
					t.Errorf("podSpecEqual() error = %v, wantErr %v", err, tt.wantErr)
					return
				}
				if got != tt.want {
					t.Errorf("podSpecEqual() got = %v, want %v", got, tt.want)
				}
			},
		)
	}
}

func Test_mapsEqual(t *testing.T) {
	type args struct {
		map1 map[string]string
		map2 map[string]string
	}
	tests := []struct {
		name string
		args args
		want bool
	}{
		{
			name: "both nil maps",
			args: args{
				map1: nil,
				map2: nil,
			},
			want: true,
		},
		{
			name: "one nil one empty map",
			args: args{
				map1: nil,
				map2: map[string]string{},
			},
			want: true,
		},
		{
			name: "equal maps",
			args: args{
				map1: map[string]string{
					"key1": "value1",
					"key2": "value2",
				},
				map2: map[string]string{
					"key1": "value1",
					"key2": "value2",
				},
			},
			want: true,
		},
		{
			name: "different values",
			args: args{
				map1: map[string]string{
					"key1": "value1",
				},
				map2: map[string]string{
					"key1": "value2",
				},
			},
			want: false,
		},
		{
			name: "different keys",
			args: args{
				map1: map[string]string{
					"key1": "value1",
				},
				map2: map[string]string{
					"key2": "value1",
				},
			},
			want: false,
		},
		{
			name: "different map sizes",
			args: args{
				map1: map[string]string{
					"key1": "value1",
					"key2": "value2",
				},
				map2: map[string]string{
					"key1": "value1",
				},
			},
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(
			tt.name, func(t *testing.T) {
				if got := mapsEqual(tt.args.map1, tt.args.map2); got != tt.want {
					t.Errorf("mapsEqual() = %v, want %v", got, tt.want)
				}
			},
		)
	}
}

func Test_podTemplateSpecEqual(t *testing.T) {
	type args struct {
		template1 corev1.PodTemplateSpec
		template2 corev1.PodTemplateSpec
	}
	tests := []struct {
		name    string
		args    args
		want    bool
		wantErr bool
	}{
		{
			name: "equal pod template specs",
			args: args{
				template1: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Labels: map[string]string{
							"app": "test",
						},
					},
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name:  "container1",
								Image: "nginx:1.20",
							},
						},
					},
				},
				template2: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Labels: map[string]string{
							"app": "test",
						},
					},
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name:  "container1",
								Image: "nginx:1.20",
							},
						},
					},
				},
			},
			want:    true,
			wantErr: false,
		},
		{
			name: "different pod template specs metadata",
			args: args{
				template1: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Labels: map[string]string{
							"app": "test1",
						},
					},
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name:  "container1",
								Image: "nginx:1.20",
							},
						},
					},
				},
				template2: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Labels: map[string]string{
							"app": "test2",
						},
					},
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name:  "container1",
								Image: "nginx:1.20",
							},
						},
					},
				},
			},
			want:    false,
			wantErr: true,
		},
		{
			name: "different pod template specs containers",
			args: args{
				template1: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Labels: map[string]string{
							"app": "test",
						},
					},
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name:  "container1",
								Image: "nginx:1.20",
							},
						},
					},
				},
				template2: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Labels: map[string]string{
							"app": "test",
						},
					},
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name:  "container2",
								Image: "nginx:1.20",
							},
						},
					},
				},
			},
			want:    false,
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(
			tt.name, func(t *testing.T) {
				got, err := podTemplateSpecEqual(tt.args.template1, tt.args.template2)
				if (err != nil) != tt.wantErr {
					t.Errorf("podTemplateSpecEqual() error = %v, wantErr %v", err, tt.wantErr)
					return
				}
				if got != tt.want {
					t.Errorf("podTemplateSpecEqual() got = %v, want %v", got, tt.want)
				}
			},
		)
	}
}

func Test_slicesEqualByName(t *testing.T) {
	type testStruct struct {
		Name  string
		Value string
	}

	type args struct {
		a        []testStruct
		b        []testStruct
		name     func(testStruct) string
		itemType string
	}
	tests := []struct {
		name    string
		args    args
		want    bool
		wantErr bool
	}{
		{
			name: "equal slices",
			args: args{
				a: []testStruct{
					{Name: "item1", Value: "value1"},
					{Name: "item2", Value: "value2"},
				},
				b: []testStruct{
					{Name: "item1", Value: "value1"},
					{Name: "item2", Value: "value2"},
				},
				name:     func(ts testStruct) string { return ts.Name },
				itemType: "testStruct",
			},
			want:    true,
			wantErr: false,
		},
		{
			name: "equal slices different order",
			args: args{
				a: []testStruct{
					{Name: "item2", Value: "value2"},
					{Name: "item1", Value: "value1"},
				},
				b: []testStruct{
					{Name: "item1", Value: "value1"},
					{Name: "item2", Value: "value2"},
				},
				name:     func(ts testStruct) string { return ts.Name },
				itemType: "testStruct",
			},
			want:    true,
			wantErr: false,
		},
		{
			name: "different slice lengths",
			args: args{
				a: []testStruct{
					{Name: "item1", Value: "value1"},
				},
				b: []testStruct{
					{Name: "item1", Value: "value1"},
					{Name: "item2", Value: "value2"},
				},
				name:     func(ts testStruct) string { return ts.Name },
				itemType: "testStruct",
			},
			want:    false,
			wantErr: true,
		},
		{
			name: "different item names",
			args: args{
				a: []testStruct{
					{Name: "item1", Value: "value1"},
				},
				b: []testStruct{
					{Name: "item2", Value: "value1"},
				},
				name:     func(ts testStruct) string { return ts.Name },
				itemType: "testStruct",
			},
			want:    false,
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(
			tt.name, func(t *testing.T) {
				got, err := slicesEqualByName(tt.args.a, tt.args.b, tt.args.name, tt.args.itemType)
				if (err != nil) != tt.wantErr {
					t.Errorf("slicesEqualByName() error = %v, wantErr %v", err, tt.wantErr)
					return
				}
				if got != tt.want {
					t.Errorf("slicesEqualByName() got = %v, want %v", got, tt.want)
				}
			},
		)
	}
}

func TestPodReconciler_SetInjectors(t *testing.T) {
	scheme := runtime.NewScheme()
	client := fake.NewClientBuilder().WithScheme(scheme).Build()
	reconciler := NewPodReconciler(scheme, client)

	injectors := []string{configInjector, commonEnvInjector}
	reconciler.SetInjectors(injectors)

	assert.Equal(t, injectors, reconciler.injectObjects)
}

func TestPodReconciler_ConstructPodTemplateSpecApplyConfiguration(t *testing.T) {
	// Setup
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = workloadsv1alpha2.AddToScheme(scheme)

	client := fake.NewClientBuilder().WithScheme(scheme).Build()
	reconciler := NewPodReconciler(scheme, client)

	// Test data
	rbg := wrappersv2.BuildBasicRoleBasedGroup("test-rbg", "test-ns").Obj()
	role := &rbg.Spec.Roles[0]

	tests := []struct {
		name        string
		podLabels   map[string]string
		podTmpls    []corev1.PodTemplateSpec
		expectError bool
		setupFunc   func(*PodReconciler)
	}{
		{
			name:        "basic pod template construction",
			podLabels:   map[string]string{"role": "worker"},
			expectError: false,
		},
		{
			name: "with custom pod template",
			podLabels: map[string]string{
				"custom-label": "custom-value",
			},
			podTmpls: []corev1.PodTemplateSpec{
				{
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name:  "custom-container",
								Image: "redis:latest",
							},
						},
					},
				},
			},
			expectError: false,
		},
		{
			name: "with injectors disabled",
			podLabels: map[string]string{
				"test-label": "test-value",
			},
			setupFunc: func(pr *PodReconciler) {
				pr.SetInjectors([]string{}) // Disable all injectors
			},
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(
			tt.name, func(t *testing.T) {
				// Run setup function if provided
				if tt.setupFunc != nil {
					tt.setupFunc(reconciler)
				}

				// Execute the method under test
				result, err := reconciler.ConstructPodTemplateSpecApplyConfiguration(
					context.Background(),
					rbg,
					role,
					tt.podLabels,
					tt.podTmpls...,
				)

				// Check error expectations
				if tt.expectError {
					assert.Error(t, err)
					assert.Nil(t, result)
				} else {
					assert.NoError(t, err)
					assert.NotNil(t, result)

					// Check that labels are properly applied
					if tt.podLabels != nil {
						for k, v := range tt.podLabels {
							assert.Equal(t, v, result.Labels[k])
						}
					}
				}
			},
		)
	}
}

func TestPodReconciler_ConstructPodTemplateSpecApplyConfiguration_WithInjectors(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = workloadsv1alpha2.AddToScheme(scheme)

	client := fake.NewClientBuilder().WithScheme(scheme).Build()
	reconciler := NewPodReconciler(scheme, client)

	rbg := wrappersv2.BuildBasicRoleBasedGroup("test-rbg", "default").Obj()
	role := &rbg.Spec.Roles[0]

	t.Run(
		"with config injector enabled", func(t *testing.T) {
			reconciler.SetInjectors([]string{"config"})

			result, err := reconciler.ConstructPodTemplateSpecApplyConfiguration(
				context.Background(),
				rbg,
				role,
				map[string]string{"test": "label"},
			)

			// Note: Since we're using a fake client without actual config objects,
			// the injector might not do anything, but we're mainly testing that
			// the code path executes without panicking
			assert.NoError(t, err)
			assert.NotNil(t, result)
		},
	)

	t.Run(
		"with an engine runtime plan set", func(t *testing.T) {
			reconciler := NewPodReconciler(scheme, client)
			reconciler.SetInjectors([]string{configInjector})
			reconciler.SetEngineRuntimePlan(role.Name, &discovery.EngineRuntimePlan{
				FragmentApplications: map[string][]discovery.EngineRuntimeFragment{
					role.Name: {{
						Profile: &workloadsv1alpha2.ClusterEngineRuntimeProfile{
							ObjectMeta: metav1.ObjectMeta{Name: "rt"},
							Spec: workloadsv1alpha2.ClusterEngineRuntimeProfileSpec{
								Volumes: []corev1.Volume{{Name: "rt-vol", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}}},
							},
						},
					}},
				},
			})

			result, err := reconciler.ConstructPodTemplateSpecApplyConfiguration(
				context.Background(), rbg, role, map[string]string{"test": "label"},
			)
			assert.NoError(t, err)
			assert.NotNil(t, result)
			volumeFound := false
			for _, vol := range result.Spec.Volumes {
				if vol.Name != nil && *vol.Name == "rt-vol" {
					volumeFound = true
				}
			}
			assert.True(t, volumeFound, "expected the profile volume in the rendered template")
		},
	)

	t.Run(
		"with a plan set but no position", func(t *testing.T) {
			reconciler := NewPodReconciler(scheme, client)
			reconciler.SetEngineRuntimePlan("", &discovery.EngineRuntimePlan{})
			_, err := reconciler.ConstructPodTemplateSpecApplyConfiguration(
				context.Background(), rbg, role, map[string]string{"test": "label"},
			)
			assert.Error(t, err)
		},
	)

	t.Run(
		"with env injector enabled", func(t *testing.T) {
			reconciler.SetInjectors([]string{"common_env"})

			result, err := reconciler.ConstructPodTemplateSpecApplyConfiguration(
				context.Background(),
				rbg,
				role,
				map[string]string{"test": "label"},
			)

			// Note: Since we're using a fake client without actual env configurations,
			// the injector might not do anything, but we're mainly testing that
			// the code path executes without panicking
			assert.NoError(t, err)
			assert.NotNil(t, result)
		},
	)
}

// TestPodReconciler_EngineRuntimePositionVocabularyGuard pins the
// fail-loud position guard: the resolver keys the plan by the role pattern's
// position vocabulary, while some construct paths hardcode their position
// regardless of the pattern. A position outside the plan's vocabulary means
// a pattern/workload-type mismatch that would otherwise silently drop all
// engine runtime content — the render must error instead. The guard is
// silent for every legitimate shape: RIS leader/worker and component
// positions, and the standalone sentinel for deploy/sts and RIS standalone.
func TestPodReconciler_EngineRuntimePositionVocabularyGuard(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = workloadsv1alpha2.AddToScheme(scheme)
	client := fake.NewClientBuilder().WithScheme(scheme).Build()

	rbg := wrappersv2.BuildBasicRoleBasedGroup("guard-rbg", "default").Obj()
	role := &rbg.Spec.Roles[0]

	// render runs one render pass with the given plan wired for position.
	// Injectors are disabled so the assertion observes only the guard, and
	// the gate is armed — the guard is the RoleInstanceSet-path gate, so the
	// deprecated renders leave it off and keep main's silent-skip behavior.
	render := func(t *testing.T, position string, plan *discovery.EngineRuntimePlan) error {
		t.Helper()
		reconciler := NewPodReconciler(scheme, client)
		reconciler.SetInjectors([]string{})
		reconciler.SetEngineRuntimePlan(position, plan)
		reconciler.EnableEngineRuntimePositionGate()
		_, err := reconciler.ConstructPodTemplateSpecApplyConfiguration(
			context.Background(), rbg, role, nil,
		)
		return err
	}

	fragmentVolume := func(position string) map[string][]discovery.EngineRuntimeFragment {
		return map[string][]discovery.EngineRuntimeFragment{position: {{
			Profile: &workloadsv1alpha2.ClusterEngineRuntimeProfile{
				ObjectMeta: metav1.ObjectMeta{Name: "rt"},
				Spec: workloadsv1alpha2.ClusterEngineRuntimeProfileSpec{
					Volumes: []corev1.Volume{{Name: "rt-vol", VolumeSource: corev1.VolumeSource{
						EmptyDir: &corev1.EmptyDirVolumeSource{},
					}}},
				},
			},
		}}}
	}

	t.Run("guard fires when the position is outside the plan's vocabulary", func(t *testing.T) {
		// a leaderWorker plan rendered through a construct path that asks for
		// the role-named standalone position the pattern does not define
		plan := &discovery.EngineRuntimePlan{
			Vocabulary: []string{
				string(constants.LeaderComponentType), string(constants.WorkerComponentType),
			},
			FragmentApplications: fragmentVolume(string(constants.LeaderComponentType)),
		}
		err := render(t, role.Name, plan)
		require.Error(t, err)
		assert.Contains(t, err.Error(), fmt.Sprintf("%q", role.Name))
		assert.Contains(t, err.Error(), "not defined by role")
		assert.Contains(t, err.Error(),
			fmt.Sprintf("[%s %s]", constants.LeaderComponentType, constants.WorkerComponentType))
	})

	t.Run("guard is silent for a leaderWorker plan asked for leader", func(t *testing.T) {
		plan := &discovery.EngineRuntimePlan{
			Vocabulary: []string{
				string(constants.LeaderComponentType), string(constants.WorkerComponentType),
			},
			FragmentApplications: fragmentVolume(string(constants.LeaderComponentType)),
		}
		require.NoError(t, render(t, string(constants.LeaderComponentType), plan))
	})

	t.Run("guard is silent for a leaderWorker plan asked for worker", func(t *testing.T) {
		plan := &discovery.EngineRuntimePlan{
			Vocabulary: []string{
				string(constants.LeaderComponentType), string(constants.WorkerComponentType),
			},
			FragmentApplications: fragmentVolume(string(constants.WorkerComponentType)),
		}
		require.NoError(t, render(t, string(constants.WorkerComponentType), plan))
	})

	t.Run("guard is silent for a customComponents plan asked for a component name", func(t *testing.T) {
		plan := &discovery.EngineRuntimePlan{
			Vocabulary:           []string{"engine-comp", "aux-comp"},
			FragmentApplications: fragmentVolume("engine-comp"),
		}
		require.NoError(t, render(t, "engine-comp", plan))
	})

	t.Run("guard is silent for a standalone plan asked for the role's own name", func(t *testing.T) {
		// the standalone component is named after the role (KEP-173), so the
		// plan keys by role.Name
		plan := &discovery.EngineRuntimePlan{
			Vocabulary:           []string{role.Name},
			FragmentApplications: fragmentVolume(role.Name),
		}
		require.NoError(t, render(t, role.Name, plan))
	})

	t.Run("guard is silent for a plan with no engine runtime content", func(t *testing.T) {
		// a role referencing no engine runtimes resolves to an empty plan with
		// no vocabulary: nothing is routed, so nothing can be mis-keyed
		require.NoError(t, render(t, role.Name, &discovery.EngineRuntimePlan{}))
	})

	t.Run("the gate stays off for the deprecated render paths", func(t *testing.T) {
		// the gate is the RoleInstanceSet-path gate: an unmatched position on
		// a deprecated LWS/Deployment/StatefulSet render silently skips the
		// plan's content for that template, exactly as main always did
		plan := &discovery.EngineRuntimePlan{
			Vocabulary: []string{
				string(constants.LeaderComponentType), string(constants.WorkerComponentType),
			},
			FragmentApplications: fragmentVolume(string(constants.LeaderComponentType)),
		}
		reconciler := NewPodReconciler(scheme, client)
		reconciler.SetInjectors([]string{})
		reconciler.SetEngineRuntimePlan(role.Name, plan)
		_, err := reconciler.ConstructPodTemplateSpecApplyConfiguration(
			context.Background(), rbg, role, nil,
		)
		require.NoError(t, err, "the deprecated paths never arm the vocabulary gate")
	})
}

// TestPodReconciler_ReservedConfigVolumeMountIsRenderError pins the
// rbg-cluster-config reservation at the reconciler: the controller-injected
// config volume (appended by InjectConfig for stateful roles) is a third
// ownership class — neither role nor profile content — so a role-side patch
// mounting it into any container is a render error, even though the volume
// is present in the rendered spec when the patch pass runs (the patch passes
// run after the built-in injector chain). The D9 one-way guard this test
// originally pinned is gone: role content into profile-declared containers is
// legal, but no side steers the controller's own config surface.
func TestPodReconciler_ReservedConfigVolumeMountIsRenderError(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = workloadsv1alpha2.AddToScheme(scheme)
	client := fake.NewClientBuilder().WithScheme(scheme).Build()

	// the default workload type is stateful, so InjectConfig appends the
	// rbg-cluster-config volume to the role's own (volume-less) template
	rbg := wrappersv2.BuildBasicRoleBasedGroup("reserved-rbg", "default").Obj()
	role := &rbg.Spec.Roles[0]

	mountRaw, err := json.Marshal(corev1.VolumeMount{
		Name: "rbg-cluster-config", MountPath: "/etc/rbg", ReadOnly: true,
	})
	require.NoError(t, err)

	reconciler := NewPodReconciler(scheme, client)
	reconciler.SetInjectors([]string{configInjector})
	reconciler.SetEngineRuntimePlan(role.Name, &discovery.EngineRuntimePlan{
		FragmentApplications: map[string][]discovery.EngineRuntimeFragment{
			role.Name: {{
				Profile: &workloadsv1alpha2.ClusterEngineRuntimeProfile{
					ObjectMeta: metav1.ObjectMeta{Name: "rt"},
					Spec: workloadsv1alpha2.ClusterEngineRuntimeProfileSpec{
						Containers: []corev1.Container{{Name: "runtime-sidecar", Image: "rt-sidecar:latest"}},
					},
				},
			}},
		},
		RolePatchApplications: map[string][]discovery.EngineRuntimeRolePatch{
			role.Name: {{
				ProfileName:        "rt",
				DeclaredContainers: map[string]struct{}{"runtime-sidecar": {}},
				Targets: []workloadsv1alpha2.EngineRuntimeTargetContainer{{
					Name: "runtime-sidecar",
					ExtraPatches: []workloadsv1alpha2.ContainerPatch{{
						JSONPatch: &workloadsv1alpha2.ContainerJSONPatch{
							Op: "add", Path: "/volumeMounts/-",
							Value: runtime.RawExtension{Raw: mountRaw},
						},
					}},
				}},
			}},
		},
		RoleName: role.Name,
	})

	result, err := reconciler.ConstructPodTemplateSpecApplyConfiguration(
		context.Background(), rbg, role, nil,
	)
	require.Error(t, err,
		"the controller's own config volume is reserved — no patch entry on either side may mount it")
	assert.Contains(t, err.Error(), "rbg-cluster-config")
	assert.Contains(t, err.Error(), "reserved")
	assert.Nil(t, result)
}

// TestPodReconciler_WrapShadowedEvent pins the KEP-531 D10 second clause at
// the reconciler: a role-side /command/0 insert landing ahead of a
// profile-side command wrap on the same container emits exactly one
// EngineRuntimeWrapShadowed warning event naming the container, and the
// render still succeeds. The report is command-root-only: env-root overlaps
// are the EnvCollision family and never this event. The profile-side pass
// records its command positional inserts, the role-side pass reports the
// shadowed containers, and the reconciler emits the event — shadowing never
// fails the render and is never silent.
func TestPodReconciler_WrapShadowedEvent(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = workloadsv1alpha2.AddToScheme(scheme)
	client := fake.NewClientBuilder().WithScheme(scheme).Build()

	rbg := wrappersv2.BuildBasicRoleBasedGroup("shadow-rbg", "default").Obj()
	role := &rbg.Spec.Roles[0]

	// the role's own template declares the engine container and its command —
	// both /command/0 wraps below address it (D11: the profile side may
	// address /command/0 too, so both sides can wrap).
	template := corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{
		Name: "engine", Image: "sglang:latest", Command: []string{"python"},
	}}}}

	strPatch := func(t *testing.T, path, value string) *workloadsv1alpha2.ContainerJSONPatch {
		t.Helper()
		raw, err := json.Marshal(value)
		require.NoError(t, err)
		return &workloadsv1alpha2.ContainerJSONPatch{
			Op: "add", Path: path, Value: runtime.RawExtension{Raw: raw},
		}
	}

	// render runs one render pass with the given plan wired for the
	// role-named standalone position; injectors are disabled so the assertion
	// observes only the patch passes.
	render := func(t *testing.T, plan *discovery.EngineRuntimePlan) (
		*coreapplyv1.PodTemplateSpecApplyConfiguration, *record.FakeRecorder, error,
	) {
		t.Helper()
		recorder := record.NewFakeRecorder(16)
		reconciler := NewPodReconciler(scheme, client)
		reconciler.SetInjectors([]string{})
		reconciler.SetRecorder(recorder)
		reconciler.SetEngineRuntimePlan(role.Name, plan)
		result, err := reconciler.ConstructPodTemplateSpecApplyConfiguration(
			context.Background(), rbg, role, nil, template,
		)
		return result, recorder, err
	}

	drainEvents := func(recorder *record.FakeRecorder) []string {
		var events []string
		for {
			select {
			case ev := <-recorder.Events:
				events = append(events, ev)
			default:
				return events
			}
		}
	}

	t.Run("a role-side wrap ahead of a profile-side one emits the warning and renders", func(t *testing.T) {
		plan := &discovery.EngineRuntimePlan{
			PatchApplications: map[string][]discovery.EngineRuntimePatch{
				role.Name: {{
					ProfileName: "rt",
					Targets:     []string{"engine"},
					Patches: []workloadsv1alpha2.ContainerPatch{
						{JSONPatch: strPatch(t, "/command/0", "profile-wrap")},
					},
				}},
			},
			RolePatchApplications: map[string][]discovery.EngineRuntimeRolePatch{
				role.Name: {{
					ProfileName:        "rt",
					DeclaredContainers: map[string]struct{}{},
					Targets: []workloadsv1alpha2.EngineRuntimeTargetContainer{{
						Name: "engine",
						ExtraPatches: []workloadsv1alpha2.ContainerPatch{
							{JSONPatch: strPatch(t, "/command/0", "role-wrap")},
						},
					}},
				}},
			},
			RoleName: role.Name,
		}

		result, recorder, err := render(t, plan)
		require.NoError(t, err, "wrap shadowing warns, it never fails the render")

		// the render succeeded: the role-side wrap is the rendered command's
		// index 0, ahead of the profile-side wrap it displaced
		require.Len(t, result.Spec.Containers, 1)
		require.Len(t, result.Spec.Containers[0].Command, 3)
		assert.Equal(t, "role-wrap", result.Spec.Containers[0].Command[0])
		assert.Equal(t, "profile-wrap", result.Spec.Containers[0].Command[1])
		assert.Equal(t, "python", result.Spec.Containers[0].Command[2])

		events := drainEvents(recorder)
		require.Len(t, events, 1)
		assert.Contains(t, events[0], discovery.ReasonEngineRuntimeWrapShadowed)
		assert.Contains(t, events[0], "engine")
	})

	t.Run("a role-side command append over a profile-side wrap emits no event", func(t *testing.T) {
		plan := &discovery.EngineRuntimePlan{
			PatchApplications: map[string][]discovery.EngineRuntimePatch{
				role.Name: {{
					ProfileName: "rt",
					Targets:     []string{"engine"},
					Patches: []workloadsv1alpha2.ContainerPatch{
						{JSONPatch: strPatch(t, "/command/0", "profile-wrap")},
					},
				}},
			},
			RolePatchApplications: map[string][]discovery.EngineRuntimeRolePatch{
				role.Name: {{
					ProfileName:        "rt",
					DeclaredContainers: map[string]struct{}{},
					Targets: []workloadsv1alpha2.EngineRuntimeTargetContainer{{
						Name: "engine",
						ExtraPatches: []workloadsv1alpha2.ContainerPatch{
							{JSONPatch: strPatch(t, "/command/-", "role-append")},
						},
					}},
				}},
			},
			RoleName: role.Name,
		}

		result, recorder, err := render(t, plan)
		require.NoError(t, err)
		require.NotNil(t, result)
		assert.Empty(t, drainEvents(recorder), "an append lands after everything and displaces no wrap")
	})
}

func Test_setExclusiveAffinities(t *testing.T) {
	tests := []struct {
		name                                   string
		pod                                    *corev1.PodTemplateSpec
		uniqueKey, topologyKey, podAffinityKey string
		want                                   *corev1.PodTemplateSpec
		wantErr                                bool
	}{
		{
			name:           "empty pod: create affinity/anti-affinity from scratch",
			pod:            &corev1.PodTemplateSpec{},
			uniqueKey:      "abcd1234",
			topologyKey:    "kubernetes.io/hostname",
			podAffinityKey: constants.GroupUniqueHashLabelKey,
			wantErr:        false,
			want: &corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Affinity: &corev1.Affinity{
						PodAffinity: &corev1.PodAffinity{
							RequiredDuringSchedulingIgnoredDuringExecution: []corev1.PodAffinityTerm{
								{
									TopologyKey: "kubernetes.io/hostname",
									LabelSelector: &metav1.LabelSelector{
										MatchExpressions: []metav1.LabelSelectorRequirement{
											{
												Key:      constants.GroupUniqueHashLabelKey,
												Operator: metav1.LabelSelectorOpIn,
												Values:   []string{"abcd1234"},
											},
										},
									},
								},
							},
						},
						PodAntiAffinity: &corev1.PodAntiAffinity{
							RequiredDuringSchedulingIgnoredDuringExecution: []corev1.PodAffinityTerm{
								{
									TopologyKey: "kubernetes.io/hostname",
									LabelSelector: &metav1.LabelSelector{
										MatchExpressions: []metav1.LabelSelectorRequirement{
											{
												Key:      constants.GroupUniqueHashLabelKey,
												Operator: metav1.LabelSelectorOpExists,
											},
											{
												Key:      constants.GroupUniqueHashLabelKey,
												Operator: metav1.LabelSelectorOpNotIn,
												Values:   []string{"abcd1234"},
											},
										},
									},
								},
							},
						},
					},
				},
			},
		},
		{
			name: "pod with pre-existing terms: append new ones",
			pod: &corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Affinity: &corev1.Affinity{
						PodAffinity: &corev1.PodAffinity{
							RequiredDuringSchedulingIgnoredDuringExecution: []corev1.PodAffinityTerm{
								{TopologyKey: "zone"},
							},
						},
						PodAntiAffinity: &corev1.PodAntiAffinity{
							RequiredDuringSchedulingIgnoredDuringExecution: []corev1.PodAffinityTerm{
								{TopologyKey: "zone"},
							},
						},
					},
				},
			},
			uniqueKey:      "xyz5678",
			topologyKey:    "node",
			podAffinityKey: constants.GroupUniqueHashLabelKey,
			wantErr:        false,
			want: &corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Affinity: &corev1.Affinity{
						PodAffinity: &corev1.PodAffinity{
							RequiredDuringSchedulingIgnoredDuringExecution: []corev1.PodAffinityTerm{
								{TopologyKey: "zone"},
								{
									TopologyKey: "node",
									LabelSelector: &metav1.LabelSelector{
										MatchExpressions: []metav1.LabelSelectorRequirement{
											{
												Key:      constants.GroupUniqueHashLabelKey,
												Operator: metav1.LabelSelectorOpIn,
												Values:   []string{"xyz5678"},
											},
										},
									},
								},
							},
						},
						PodAntiAffinity: &corev1.PodAntiAffinity{
							RequiredDuringSchedulingIgnoredDuringExecution: []corev1.PodAffinityTerm{
								{TopologyKey: "zone"},
								{
									TopologyKey: "node",
									LabelSelector: &metav1.LabelSelector{
										MatchExpressions: []metav1.LabelSelectorRequirement{
											{
												Key:      constants.GroupUniqueHashLabelKey,
												Operator: metav1.LabelSelectorOpExists,
											},
											{
												Key:      constants.GroupUniqueHashLabelKey,
												Operator: metav1.LabelSelectorOpNotIn,
												Values:   []string{"xyz5678"},
											},
										},
									},
								},
							},
						},
					},
				},
			},
		},
		{
			name: "topology already exists: expect no change",
			pod: &corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Affinity: &corev1.Affinity{
						PodAffinity: &corev1.PodAffinity{
							RequiredDuringSchedulingIgnoredDuringExecution: []corev1.PodAffinityTerm{
								{
									TopologyKey: "rack",
									LabelSelector: &metav1.LabelSelector{
										MatchExpressions: []metav1.LabelSelectorRequirement{
											{
												Key:      constants.GroupUniqueHashLabelKey,
												Operator: metav1.LabelSelectorOpIn,
												Values:   []string{"old"},
											},
										},
									},
								},
							},
						},
						PodAntiAffinity: &corev1.PodAntiAffinity{
							RequiredDuringSchedulingIgnoredDuringExecution: []corev1.PodAffinityTerm{
								{TopologyKey: "rack"},
							},
						},
					},
				},
			},
			uniqueKey:      "newkey",
			topologyKey:    "rack",
			podAffinityKey: constants.GroupUniqueHashLabelKey,
			wantErr:        false,
			want: &corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Affinity: &corev1.Affinity{
						PodAffinity: &corev1.PodAffinity{
							RequiredDuringSchedulingIgnoredDuringExecution: []corev1.PodAffinityTerm{
								{
									TopologyKey: "rack",
									LabelSelector: &metav1.LabelSelector{
										MatchExpressions: []metav1.LabelSelectorRequirement{
											{
												Key:      constants.GroupUniqueHashLabelKey,
												Operator: metav1.LabelSelectorOpIn,
												Values:   []string{"old"},
											},
										},
									},
								},
							},
						},
						PodAntiAffinity: &corev1.PodAntiAffinity{
							RequiredDuringSchedulingIgnoredDuringExecution: []corev1.PodAffinityTerm{
								{TopologyKey: "rack"},
							},
						},
					},
				},
			},
		}, {
			name:           "empty topology key should return error",
			pod:            &corev1.PodTemplateSpec{},
			uniqueKey:      "key",
			topologyKey:    "", // illegal
			podAffinityKey: constants.GroupUniqueHashLabelKey,
			want:           &corev1.PodTemplateSpec{}, // No change
			wantErr:        true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := setExclusiveAffinities(tt.pod, tt.uniqueKey, tt.topologyKey, tt.podAffinityKey)
			if tt.wantErr {
				assert.Error(t, err, "expected error but got nil")
				return
			}
			assert.NoError(t, err)
			assert.Equal(t, tt.want, tt.pod, "unexpected PodTemplateSpec after injection")
		})
	}
}

func Test_exclusiveAffinityApplied(t *testing.T) {
	tests := []struct {
		name string // description of this test case
		// Named input parameters for target function.
		podTemplateSpec corev1.PodTemplateSpec
		topologyKey     string
		want            bool
	}{
		{
			name:            "empty affinity: should return false",
			podTemplateSpec: corev1.PodTemplateSpec{},
			topologyKey:     "kubernetes.io/hostname",
			want:            false,
		},
		{
			name: "both affinity and anti-affinity contain the required topology key: should return true",
			podTemplateSpec: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Affinity: &corev1.Affinity{
						PodAffinity: &corev1.PodAffinity{
							RequiredDuringSchedulingIgnoredDuringExecution: []corev1.PodAffinityTerm{
								{TopologyKey: "kubernetes.io/hostname"},
							},
						},
						PodAntiAffinity: &corev1.PodAntiAffinity{
							RequiredDuringSchedulingIgnoredDuringExecution: []corev1.PodAffinityTerm{
								{TopologyKey: "kubernetes.io/hostname"},
							},
						},
					},
				},
			},
			topologyKey: "kubernetes.io/hostname",
			want:        true,
		},
		{
			name: "only affinity contains the topology key: should return false",
			podTemplateSpec: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Affinity: &corev1.Affinity{
						PodAffinity: &corev1.PodAffinity{
							RequiredDuringSchedulingIgnoredDuringExecution: []corev1.PodAffinityTerm{
								{TopologyKey: "kubernetes.io/hostname"},
							},
						},
						PodAntiAffinity: &corev1.PodAntiAffinity{
							RequiredDuringSchedulingIgnoredDuringExecution: []corev1.PodAffinityTerm{
								{TopologyKey: "zone"},
							},
						},
					},
				},
			},
			topologyKey: "kubernetes.io/hostname",
			want:        false,
		},
		{
			name: "only anti-affinity contains the topology key: should return false",
			podTemplateSpec: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Affinity: &corev1.Affinity{
						PodAffinity: &corev1.PodAffinity{
							RequiredDuringSchedulingIgnoredDuringExecution: []corev1.PodAffinityTerm{
								{TopologyKey: "zone"},
							},
						},
						PodAntiAffinity: &corev1.PodAntiAffinity{
							RequiredDuringSchedulingIgnoredDuringExecution: []corev1.PodAffinityTerm{
								{TopologyKey: "kubernetes.io/hostname"},
							},
						},
					},
				},
			},
			topologyKey: "kubernetes.io/hostname",
			want:        false,
		},
		{
			name: "topology key does not match: should return false",
			podTemplateSpec: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Affinity: &corev1.Affinity{
						PodAffinity: &corev1.PodAffinity{
							RequiredDuringSchedulingIgnoredDuringExecution: []corev1.PodAffinityTerm{
								{TopologyKey: "zone"},
							},
						},
						PodAntiAffinity: &corev1.PodAntiAffinity{
							RequiredDuringSchedulingIgnoredDuringExecution: []corev1.PodAffinityTerm{
								{TopologyKey: "zone"},
							},
						},
					},
				},
			},
			topologyKey: "kubernetes.io/hostname",
			want:        false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := exclusiveAffinityApplied(tt.podTemplateSpec, tt.topologyKey)
			if got != tt.want {
				t.Errorf("exclusiveAffinityApplied() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_applyStrategicMergePatch(t *testing.T) {
	type args struct {
		base  corev1.PodTemplateSpec
		patch runtime.RawExtension
	}
	tests := []struct {
		name    string
		args    args
		want    corev1.PodTemplateSpec
		wantErr bool
	}{
		{
			name: "nil or empty patch returns base unchanged",
			args: args{
				base: corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{Name: "app", Image: "nginx:1.20"},
						},
					},
				},
				patch: runtime.RawExtension{Raw: nil},
			},
			want: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{Name: "app", Image: "nginx:1.20"},
					},
				},
			},
			wantErr: false,
		},
		{
			name: "patch adds new field",
			args: args{
				base: corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{Name: "app", Image: "nginx:1.20"},
						},
					},
				},
				patch: runtime.RawExtension{
					Raw: []byte(`{"spec":{"restartPolicy":"Always"}}`),
				},
			},
			want: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{Name: "app", Image: "nginx:1.20"},
					},
					RestartPolicy: corev1.RestartPolicyAlways,
				},
			},
			wantErr: false,
		},
		{
			name: "patch overrides existing field",
			args: args{
				base: corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{Name: "app", Image: "nginx:1.20"},
						},
						RestartPolicy: corev1.RestartPolicyNever,
					},
				},
				patch: runtime.RawExtension{
					Raw: []byte(`{"spec":{"restartPolicy":"Always"}}`),
				},
			},
			want: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{Name: "app", Image: "nginx:1.20"},
					},
					RestartPolicy: corev1.RestartPolicyAlways,
				},
			},
			wantErr: false,
		},
		{
			name: "patch modifies container",
			args: args{
				base: corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{Name: "app", Image: "nginx:1.20"},
						},
					},
				},
				patch: runtime.RawExtension{
					Raw: []byte(`{"spec":{"containers":[{"name":"app","command":["sh","-c","echo hello"]}]}}`),
				},
			},
			want: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{Name: "app", Image: "nginx:1.20", Command: []string{"sh", "-c", "echo hello"}},
					},
				},
			},
			wantErr: false,
		},
		{
			name: "invalid JSON patch returns error",
			args: args{
				base: corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{Name: "app", Image: "nginx:1.20"},
						},
					},
				},
				patch: runtime.RawExtension{
					Raw: []byte(`{invalid json}`),
				},
			},
			want:    corev1.PodTemplateSpec{},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := applyStrategicMergePatch(tt.args.base, tt.args.patch)
			if (err != nil) != tt.wantErr {
				t.Errorf("applyStrategicMergePatch() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr && !reflect.DeepEqual(got, tt.want) {
				t.Errorf("applyStrategicMergePatch() got = %v, want %v", got, tt.want)
			}
		})
	}
}
