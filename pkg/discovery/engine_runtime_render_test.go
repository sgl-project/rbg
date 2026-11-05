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
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/rbgs/api/workloads/constants"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
)

// leaderPos and workerPos name the leaderWorkerPattern positions the same way
// the construct paths do.
func leaderPos() string { return string(constants.LeaderComponentType) }

func workerPos() string { return string(constants.WorkerComponentType) }

// renderSpec is the role-declared template one position's render passes
// mutate: a single engine container with a declared command, args and env.
func renderSpec() *v1.PodTemplateSpec {
	return &v1.PodTemplateSpec{Spec: v1.PodSpec{Containers: []v1.Container{{
		Name: "engine", Image: "sglang:latest",
		Command: []string{"python"}, Args: []string{"-m", "sglang.launch_server"},
		Env: []v1.EnvVar{{Name: "PYTHONPATH", Value: "/usr/lib/python"}},
	}}}}
}

// sidecarProfile builds a profile carrying one initContainer, one sidecar
// container (named name+"-sidecar") and the volume makeProfile provides.
func sidecarProfile(name string, patches []workloadsv1alpha2.ContainerPatch,
) *workloadsv1alpha2.ClusterEngineRuntimeProfile {
	profile := makeProfile(name, patches)
	profile.Spec.InitContainers = []v1.Container{{Name: name + "-init", Image: name + "-init:latest"}}
	profile.Spec.Containers = []v1.Container{{Name: name + "-sidecar", Image: name + "-sidecar:latest"}}
	return profile
}

func containerNames(spec *v1.PodTemplateSpec) []string {
	names := make([]string, 0, len(spec.Spec.Containers))
	for _, c := range spec.Spec.Containers {
		names = append(names, c.Name)
	}
	return names
}

// volumeNamesOf returns the rendered spec's volume names in order.
func volumeNamesOf(spec *v1.PodTemplateSpec) []string {
	names := make([]string, 0, len(spec.Spec.Volumes))
	for _, vol := range spec.Spec.Volumes {
		names = append(names, vol.Name)
	}
	return names
}

// envNames returns a container's env var names, in list order.
func envNames(c v1.Container) []string {
	names := make([]string, 0, len(c.Env))
	for _, e := range c.Env {
		names = append(names, e.Name)
	}
	return names
}

func TestInjectEngineRuntimeFragments(t *testing.T) {
	// The render passes pull their logger from the context; the package's
	// null logger keeps V(1) skip logs out of the test output.
	ctx := context.Background()

	tests := []struct {
		name      string
		podSpec   *v1.PodTemplateSpec
		fragments []EngineRuntimeFragment
		verify    func(t *testing.T, spec *v1.PodTemplateSpec)
	}{
		{
			name:    "appends initContainers, containers and volumes when names are absent",
			podSpec: renderSpec(),
			fragments: []EngineRuntimeFragment{{
				Profile: sidecarProfile("ik", nil),
				Runtime: workloadsv1alpha2.EngineRuntime{ProfileName: "ik"},
			}},
			verify: func(t *testing.T, spec *v1.PodTemplateSpec) {
				require.Len(t, spec.Spec.InitContainers, 1)
				assert.Equal(t, "ik-init", spec.Spec.InitContainers[0].Name)
				assert.Equal(t, []string{"engine", "ik-sidecar"}, containerNames(spec))
				require.Len(t, spec.Spec.Volumes, 1)
				assert.Equal(t, "ik-vol", spec.Spec.Volumes[0].Name)
			},
		},
		{
			name:    "is idempotent per name",
			podSpec: renderSpec(),
			// Overlapping fragment delivery is legal per the KEP: two
			// references at one position carrying the same profile.
			fragments: []EngineRuntimeFragment{
				{Profile: sidecarProfile("ik", nil), Runtime: workloadsv1alpha2.EngineRuntime{ProfileName: "ik"}},
				{Profile: sidecarProfile("ik", nil), Runtime: workloadsv1alpha2.EngineRuntime{ProfileName: "ik"}},
			},
			verify: func(t *testing.T, spec *v1.PodTemplateSpec) {
				require.Len(t, spec.Spec.InitContainers, 1)
				assert.Equal(t, []string{"engine", "ik-sidecar"}, containerNames(spec))
				require.Len(t, spec.Spec.Volumes, 1)
			},
		},
		{
			name: "applies the role-side Containers override to profile containers present in the spec",
			podSpec: &v1.PodTemplateSpec{Spec: v1.PodSpec{Containers: []v1.Container{
				{Name: "engine", Image: "sglang:latest"},
				// collides with the profile-declared sidecar name: the
				// append-if-missing keeps the role's own container, and the
				// override applies to it.
				{Name: "ik-sidecar", Image: "role-sidecar:latest"},
			}}},
			fragments: []EngineRuntimeFragment{{
				Profile: sidecarProfile("ik", nil),
				Runtime: workloadsv1alpha2.EngineRuntime{
					ProfileName: "ik",
					Containers: []v1.Container{{
						Name: "ik-sidecar",
						Env:  []v1.EnvVar{{Name: "IK_SITE", Value: "/opt/ik/site"}},
						Args: []string{"--ik-flag"},
					}},
				},
			}},
			verify: func(t *testing.T, spec *v1.PodTemplateSpec) {
				require.Len(t, spec.Spec.Containers, 2)
				sidecar := spec.Spec.Containers[1]
				assert.Equal(t, "role-sidecar:latest", sidecar.Image)
				require.Len(t, sidecar.Env, 1)
				assert.Equal(t, v1.EnvVar{Name: "IK_SITE", Value: "/opt/ik/site"}, sidecar.Env[0])
				assert.Equal(t, []string{"--ik-flag"}, sidecar.Args)
			},
		},
		{
			name: "applies the role-side Containers override to a profile-injected container",
			podSpec: &v1.PodTemplateSpec{Spec: v1.PodSpec{
				Containers: []v1.Container{{Name: "engine", Image: "sglang:latest"}},
			}},
			fragments: []EngineRuntimeFragment{{
				Profile: sidecarProfile("ik", nil),
				Runtime: workloadsv1alpha2.EngineRuntime{
					ProfileName: "ik",
					Containers: []v1.Container{{
						Name: "ik-sidecar",
						Env:  []v1.EnvVar{{Name: "IK_SITE", Value: "/opt/ik/site"}},
						Args: []string{"--ik-flag"},
					}},
				},
			}},
			verify: func(t *testing.T, spec *v1.PodTemplateSpec) {
				assert.Equal(t, []string{"engine", "ik-sidecar"}, containerNames(spec))
				sidecar := spec.Spec.Containers[1]
				require.Len(t, sidecar.Env, 1)
				assert.Equal(t, v1.EnvVar{Name: "IK_SITE", Value: "/opt/ik/site"}, sidecar.Env[0])
				assert.Equal(t, []string{"--ik-flag"}, sidecar.Args)
			},
		},
		{
			name:    "skips the override for names the profile does not declare",
			podSpec: renderSpec(),
			fragments: []EngineRuntimeFragment{{
				Profile: sidecarProfile("ik", nil),
				Runtime: workloadsv1alpha2.EngineRuntime{
					ProfileName: "ik",
					Containers: []v1.Container{{
						Name: "ghost",
						Env:  []v1.EnvVar{{Name: "GHOST_ENV", Value: "x"}},
						Args: []string{"--ghost"},
					}},
				},
			}},
			verify: func(t *testing.T, spec *v1.PodTemplateSpec) {
				// no ghost container appeared and no existing one gained its values
				assert.Equal(t, []string{"engine", "ik-sidecar"}, containerNames(spec))
				for _, c := range spec.Spec.Containers {
					for _, env := range c.Env {
						assert.NotEqual(t, "GHOST_ENV", env.Name,
							"container %q must not gain the skipped override env", c.Name)
					}
					for _, arg := range c.Args {
						assert.NotEqual(t, "--ghost", arg,
							"container %q must not gain the skipped override args", c.Name)
					}
				}
			},
		},
		{
			name:    "applies fragments of two profiles in reference order",
			podSpec: renderSpec(),
			fragments: []EngineRuntimeFragment{
				{Profile: sidecarProfile("a", nil), Runtime: workloadsv1alpha2.EngineRuntime{ProfileName: "a"}},
				{Profile: sidecarProfile("b", nil), Runtime: workloadsv1alpha2.EngineRuntime{ProfileName: "b"}},
			},
			verify: func(t *testing.T, spec *v1.PodTemplateSpec) {
				assert.Equal(t, []string{"engine", "a-sidecar", "b-sidecar"}, containerNames(spec))
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.NoError(t, InjectEngineRuntimeFragments(ctx, tt.podSpec, tt.fragments, "worker", nil, nil))
			tt.verify(t, tt.podSpec)
		})
	}
}

// TestInjectEngineRuntimeFragmentsNameCollision pins the KEP-531 duplicate
// delivery rule: a cross-profile container or volume name collision skips the
// later profile's same-named entry — the earlier reference wins — and emits an
// EngineRuntimeNameCollision warning attributed to the role, so a skipped
// fragment the operator believes present is never silent. A name the role's
// own template declares (or the same profile delivered earlier) is the
// sanctioned precedence: silent.
func TestInjectEngineRuntimeFragmentsNameCollision(t *testing.T) {
	ctx := context.Background()

	sharedContainerProfile := func(name string) *workloadsv1alpha2.ClusterEngineRuntimeProfile {
		profile := makeProfile(name, nil)
		profile.Spec.Containers = []v1.Container{{Name: "shared", Image: name + ":latest"}}
		return profile
	}

	t.Run("a cross-profile container name collision skips the later and warns", func(t *testing.T) {
		spec := renderSpec()
		recorder := record.NewFakeRecorder(16)
		require.NoError(t, InjectEngineRuntimeFragments(ctx, spec, []EngineRuntimeFragment{
			{Profile: sharedContainerProfile("a"), Runtime: workloadsv1alpha2.EngineRuntime{ProfileName: "a"}},
			{Profile: sharedContainerProfile("b"), Runtime: workloadsv1alpha2.EngineRuntime{ProfileName: "b"}},
		}, "worker", recorder, basicRBG()))
		// the earlier reference won
		assert.Equal(t, []string{"engine", "shared"}, containerNames(spec))
		assert.Equal(t, "a:latest", spec.Spec.Containers[1].Image)
		events := drainEvents(recorder)
		require.Len(t, events, 1)
		assert.Contains(t, events[0], ReasonEngineRuntimeNameCollision)
		assert.Contains(t, events[0], `role "worker"`)
		assert.Contains(t, events[0], `profile "b"`)
		assert.Contains(t, events[0], `profile "a"`)
		assert.Contains(t, events[0], "shared")
	})

	t.Run("a cross-profile volume name collision skips the later and warns", func(t *testing.T) {
		a := makeProfile("a", nil)
		b := makeProfile("b", nil)
		b.Spec.Volumes[0].Name = "a-vol"
		spec := renderSpec()
		recorder := record.NewFakeRecorder(16)
		require.NoError(t, InjectEngineRuntimeFragments(ctx, spec, []EngineRuntimeFragment{
			{Profile: a, Runtime: workloadsv1alpha2.EngineRuntime{ProfileName: "a"}},
			{Profile: b, Runtime: workloadsv1alpha2.EngineRuntime{ProfileName: "b"}},
		}, "worker", recorder, basicRBG()))
		assert.Equal(t, []string{"a-vol"}, volumeNamesOf(spec))
		events := drainEvents(recorder)
		require.Len(t, events, 1)
		assert.Contains(t, events[0], ReasonEngineRuntimeNameCollision)
		assert.Contains(t, events[0], "a-vol")
	})

	t.Run("names the role's own template declares win silently", func(t *testing.T) {
		// the role's same-named declaration is the sanctioned precedence —
		// never a cross-profile collision
		spec := renderSpec()
		spec.Spec.Containers = append(spec.Spec.Containers, v1.Container{Name: "shared", Image: "role:latest"})
		recorder := record.NewFakeRecorder(16)
		require.NoError(t, InjectEngineRuntimeFragments(ctx, spec, []EngineRuntimeFragment{
			{Profile: sharedContainerProfile("a"), Runtime: workloadsv1alpha2.EngineRuntime{ProfileName: "a"}},
			{Profile: sharedContainerProfile("b"), Runtime: workloadsv1alpha2.EngineRuntime{ProfileName: "b"}},
		}, "worker", recorder, basicRBG()))
		assert.Equal(t, "role:latest", spec.Spec.Containers[1].Image)
		assert.Empty(t, drainEvents(recorder))
	})

	t.Run("the same profile twice is the idempotent case, silent", func(t *testing.T) {
		profile := sharedContainerProfile("a")
		spec := renderSpec()
		recorder := record.NewFakeRecorder(16)
		require.NoError(t, InjectEngineRuntimeFragments(ctx, spec, []EngineRuntimeFragment{
			{Profile: profile, Runtime: workloadsv1alpha2.EngineRuntime{ProfileName: "a"}},
			{Profile: profile, Runtime: workloadsv1alpha2.EngineRuntime{ProfileName: "a"}},
		}, "worker", recorder, basicRBG()))
		assert.Equal(t, []string{"engine", "shared"}, containerNames(spec))
		assert.Empty(t, drainEvents(recorder))
	})
}

// TestInjectEngineRuntimeFragmentsLegacyEnvMerge pins the legacy containers
// merge on main's exact terms: repeated references append their overrides once
// per reference with no conflict check — a same-named entry appends a second
// time and wins positionally (the override gesture). KEP-531 Compatibility
// Guarantees keep this pass untouched; the patch grammar is where env
// semantics live.
func TestInjectEngineRuntimeFragmentsLegacyEnvMerge(t *testing.T) {
	ctx := context.Background()

	override := workloadsv1alpha2.EngineRuntime{
		ProfileName: "ik",
		Containers: []v1.Container{{
			Name: "ik-sidecar",
			Env:  []v1.EnvVar{{Name: "IK_SITE", Value: "/opt/ik/site"}},
		}},
	}

	t.Run("an override env name the role template already declares appends and wins positionally", func(t *testing.T) {
		// append-if-missing keeps the role's own same-named container, so the
		// override applies to it — and its env name appends a second entry,
		// exactly as on main
		spec := &v1.PodTemplateSpec{Spec: v1.PodSpec{Containers: []v1.Container{
			{Name: "engine", Image: "sglang:latest"},
			{Name: "ik-sidecar", Image: "role-sidecar:latest",
				Env: []v1.EnvVar{{Name: "IK_SITE", Value: "/role"}}},
		}}}
		require.NoError(t, InjectEngineRuntimeFragments(ctx, spec, []EngineRuntimeFragment{{
			Profile: sidecarProfile("ik", nil), Runtime: override,
		}}, "worker", nil, nil))
		sidecar := spec.Spec.Containers[1]
		require.Len(t, sidecar.Env, 2)
		assert.Equal(t, "/role", sidecar.Env[0].Value)
		assert.Equal(t, "/opt/ik/site", sidecar.Env[1].Value)
	})

	t.Run("an override env name the profile's own container declares appends", func(t *testing.T) {
		profile := sidecarProfile("ik", nil)
		profile.Spec.Containers[0].Env = []v1.EnvVar{{Name: "IK_SITE", Value: "/profile"}}
		spec := renderSpec()
		require.NoError(t, InjectEngineRuntimeFragments(ctx, spec, []EngineRuntimeFragment{{
			Profile: profile, Runtime: override,
		}}, "worker", nil, nil))
		require.Len(t, spec.Spec.Containers, 2)
		sidecar := spec.Spec.Containers[1]
		require.Len(t, sidecar.Env, 2)
		assert.Equal(t, "/profile", sidecar.Env[0].Value)
		assert.Equal(t, "/opt/ik/site", sidecar.Env[1].Value)
	})

	t.Run("two references of one profile overriding the same env name both append", func(t *testing.T) {
		// the duplicate-delivery case: each reference appends its override
		// once, exactly as on main — the second entry wins positionally
		spec := renderSpec()
		require.NoError(t, InjectEngineRuntimeFragments(ctx, spec, []EngineRuntimeFragment{
			{Profile: sidecarProfile("ik", nil), Runtime: override},
			{Profile: sidecarProfile("ik", nil), Runtime: override},
		}, "worker", nil, nil))
		require.Len(t, spec.Spec.Containers, 2)
		assert.Equal(t, []string{"IK_SITE", "IK_SITE"}, envNames(spec.Spec.Containers[1]))
	})

	t.Run("an override env name new to the container still appends, args untouched", func(t *testing.T) {
		argsOverride := override
		argsOverride.Containers[0].Args = []string{"--ik-flag"}
		spec := renderSpec()
		require.NoError(t, InjectEngineRuntimeFragments(ctx, spec, []EngineRuntimeFragment{{
			Profile: sidecarProfile("ik", nil), Runtime: argsOverride,
		}}, "worker", nil, nil))
		sidecar := spec.Spec.Containers[1]
		assert.Equal(t, []string{"IK_SITE"}, envNames(sidecar))
		assert.Equal(t, []string{"--ik-flag"}, sidecar.Args)
	})
}

func TestApplyEngineRuntimePatches(t *testing.T) {
	ctx := context.Background()

	t.Run("applies each entry's patches to its targets in entry order", func(t *testing.T) {
		spec := renderSpec()
		entries := []EngineRuntimePatch{
			{
				ProfileName: "one", Targets: []string{"engine"},
				Patches: []workloadsv1alpha2.ContainerPatch{envPatch("IK_ONE", "1")},
			},
			{
				ProfileName: "two", Targets: []string{"engine"},
				Patches: []workloadsv1alpha2.ContainerPatch{envPatch("IK_TWO", "2")},
			},
		}
		_, err := ApplyEngineRuntimePatches(ctx, spec, roleContainers("engine"), nil, "worker", entries, nil, nil, nil)
		require.NoError(t, err)
		// both profiles' env vars are present, in the plan's entry order
		env := spec.Spec.Containers[0].Env
		require.Len(t, env, 3)
		assert.Equal(t, []string{"PYTHONPATH", "IK_ONE", "IK_TWO"},
			[]string{env[0].Name, env[1].Name, env[2].Name})
	})

	t.Run("cross-entry same-named appends resolve positionally — the later reference wins", func(t *testing.T) {
		// KEP-531 Patch Grammar: two covering references creating the same env
		// name appends both; the later-listed entry lands later and wins the
		// positional contest. No warning, no error.
		spec := renderSpec()
		entries := []EngineRuntimePatch{
			{
				ProfileName: "one", Targets: []string{"engine"},
				Patches: []workloadsv1alpha2.ContainerPatch{envPatch("IK_A", "1")},
			},
			{
				ProfileName: "two", Targets: []string{"engine"},
				Patches: []workloadsv1alpha2.ContainerPatch{envPatch("IK_A", "2")},
			},
		}
		_, err := ApplyEngineRuntimePatches(ctx, spec, roleContainers("engine"), nil, "worker", entries, nil, nil, nil)
		require.NoError(t, err)
		env := spec.Spec.Containers[0].Env
		require.Len(t, env, 3)
		assert.Equal(t, "1", env[1].Value)
		assert.Equal(t, "2", env[2].Value) // the later reference lands later, so it wins
	})

	t.Run("a target outside the reference's vocabulary is a render error", func(t *testing.T) {
		spec := renderSpec()
		entries := []EngineRuntimePatch{{
			ProfileName: "ik", Targets: []string{"sidecar"},
			Patches: []workloadsv1alpha2.ContainerPatch{envPatch("IK_SITE", "/opt/ik/site")},
		}}
		_, err := ApplyEngineRuntimePatches(ctx, spec, roleContainers("engine"), nil, "worker", entries, nil, nil, nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "neither role-declared")
		assert.Contains(t, err.Error(), `profile "ik"`)
		assert.Contains(t, err.Error(), `"sidecar"`)
	})

	t.Run("multi-profile /command/0 head-inserts stack outermost-last", func(t *testing.T) {
		// Re-homes the phase-1 multi-runtime ordering pin: two patch-carrying
		// references to different profiles at one position, both head-inserting
		// on /command/0 — the later-listed entry's insert lands outermost.
		spec := renderSpec() // engine declares command ["python"]
		entries := []EngineRuntimePatch{
			{
				ProfileName: "a", Targets: []string{"engine"},
				Patches: []workloadsv1alpha2.ContainerPatch{jp("/command/0", "wrap-a")},
			},
			{
				ProfileName: "b", Targets: []string{"engine"},
				Patches: []workloadsv1alpha2.ContainerPatch{jp("/command/0", "wrap-b")},
			},
		}
		positional, err := ApplyEngineRuntimePatches(ctx, spec, roleContainers("engine"), nil, "worker", entries, nil, nil, nil)
		require.NoError(t, err)
		// list order is outermost-last: a's insert lands first, b's wraps it
		assert.Equal(t, []string{"wrap-b", "wrap-a", "python"}, spec.Spec.Containers[0].Command)
		assert.Contains(t, positional, "engine",
			"the pass records the containers its positional inserts touched")
	})

	t.Run("an empty entry list is a no-op", func(t *testing.T) {
		spec := renderSpec()
		before := spec.DeepCopy()
		positional, err := ApplyEngineRuntimePatches(ctx, spec, roleContainers("engine"), nil, "worker", nil, nil, nil, nil)
		require.NoError(t, err)
		assert.Empty(t, positional)
		assert.True(t, reflect.DeepEqual(before.Spec, spec.Spec),
			"an empty entry list must not mutate the spec")
	})
}

// TestEngineRuntimeFullRender exercises the two plan-driven passes over the
// full vocabulary at the KEP-531 application order: fragments first (they
// provide the mounted volume), then the profile-side patch pass (D11 roots:
// /command/0, /env, /volumeMounts), then the role-side patch pass last (D10)
// with its role-only roots (/args, capabilities — verbatim, with the
// non-canonical CAP_ form drawing exactly one form-hazard warning).
func TestEngineRuntimeFullRender(t *testing.T) {
	ctx := context.Background()

	profile := makeProfile("ik", []workloadsv1alpha2.ContainerPatch{
		// Multi-element command prepends are written in reverse order: every
		// /command/0 head-insert lands before the previous op's element.
		jp("/command/0", "exec"),
		jp("/command/0", "--"),
		jp("/command/0", "/opt/ik/wrap"),
		jp("/env/-", v1.EnvVar{Name: "IK_SITE", Value: "/opt/ik/site"}),
		jp("/volumeMounts/-", v1.VolumeMount{Name: "ik-vol", MountPath: "/opt/ik", ReadOnly: true}),
	})
	fragments := []EngineRuntimeFragment{{
		Profile: profile,
		Runtime: workloadsv1alpha2.EngineRuntime{ProfileName: "ik"},
	}}
	entries := []EngineRuntimePatch{{
		ProfileName: "ik",
		Patches:     profile.Spec.ContainerPatches,
		Targets:     []string{"engine"},
	}}
	roleEntries := []EngineRuntimeRolePatch{{
		ProfileName:        "ik",
		DeclaredContainers: map[string]struct{}{},
		Targets: []workloadsv1alpha2.EngineRuntimeTargetContainer{{
			Name: "engine",
			ExtraPatches: []workloadsv1alpha2.ContainerPatch{
				jp("/args/-", "--ik-flag"),
				jp("/securityContext/capabilities/add/-", "CAP_SYS_RESOURCE"),
			},
		}},
	}}

	spec := renderSpec()
	roleContainerNames := roleContainers("engine") // captured before injection

	require.NoError(t, InjectEngineRuntimeFragments(ctx, spec, fragments, "worker", nil, nil))
	_, err := ApplyEngineRuntimePatches(ctx, spec, roleContainerNames, nil, "worker", entries, nil, nil, nil)
	require.NoError(t, err)
	recorder := record.NewFakeRecorder(16)
	_, err = ApplyRoleEngineRuntimePatches(
		ctx, spec, roleContainerNames, nil, roleEntries, "worker", nil, recorder, basicRBG())
	require.NoError(t, err)
	// the CAP_-prefixed capability lands verbatim with exactly one
	// form-hazard warning attributed to the role
	events := drainEvents(recorder)
	require.Len(t, events, 1)
	assert.Contains(t, events[0], ReasonEngineRuntimeCapabilityFormHazard)
	assert.Contains(t, events[0], `role "worker"`)
	assert.Contains(t, events[0], "CAP_SYS_RESOURCE")

	golden := &v1.PodTemplateSpec{Spec: v1.PodSpec{
		Containers: []v1.Container{{
			Name:    "engine",
			Image:   "sglang:latest",
			Command: []string{"/opt/ik/wrap", "--", "exec", "python"},
			Args:    []string{"-m", "sglang.launch_server", "--ik-flag"},
			Env: []v1.EnvVar{
				{Name: "PYTHONPATH", Value: "/usr/lib/python"},
				{Name: "IK_SITE", Value: "/opt/ik/site"},
			},
			VolumeMounts: []v1.VolumeMount{{Name: "ik-vol", MountPath: "/opt/ik", ReadOnly: true}},
			SecurityContext: &v1.SecurityContext{Capabilities: &v1.Capabilities{
				// verbatim — no normalization; the form hazard is a warning only
				Add: []v1.Capability{"CAP_SYS_RESOURCE"},
			}},
		}},
		// the fragment pass injected the profile volume the mount references
		Volumes: []v1.Volume{{Name: "ik-vol", VolumeSource: v1.VolumeSource{
			EmptyDir: &v1.EmptyDirVolumeSource{},
		}}},
	}}
	assert.True(t, reflect.DeepEqual(golden.Spec, spec.Spec),
		"golden mismatch:\n got: %+v\nwant: %+v", spec.Spec, golden.Spec)
}

// renderResolvedPosition runs the PodReconciler render flow for one position:
// the role's own container and init container names captured before any
// injection, then the fragment pass, then the profile-side patch pass, then
// the role-side patch pass — the KEP-531 application order, role-side last —
// over a copy of the position's role-declared template. Warning events are
// not collected here; tests that assert on render-time events call the passes
// directly with a recorder.
func renderResolvedPosition(
	t *testing.T,
	plan *EngineRuntimePlan,
	position string,
	template v1.PodTemplateSpec,
) (*v1.PodTemplateSpec, error) {
	t.Helper()
	spec := template.DeepCopy()
	roleContainerNames := make(map[string]struct{}, len(spec.Spec.Containers))
	for _, c := range spec.Spec.Containers {
		roleContainerNames[c.Name] = struct{}{}
	}
	roleInitContainerNames := make(map[string]struct{}, len(spec.Spec.InitContainers))
	for _, c := range spec.Spec.InitContainers {
		roleInitContainerNames[c.Name] = struct{}{}
	}
	ctx := context.Background()
	if err := InjectEngineRuntimeFragments(
		ctx, spec, plan.FragmentApplications[position], plan.RoleName, nil, nil); err != nil {
		return spec, err
	}
	profilePositionalInserts, err := ApplyEngineRuntimePatches(
		ctx, spec, roleContainerNames, roleInitContainerNames, plan.RoleName,
		plan.PatchApplications[position], plan, nil, nil)
	if err != nil {
		return spec, err
	}
	_, err = ApplyRoleEngineRuntimePatches(
		ctx, spec, roleContainerNames, roleInitContainerNames,
		plan.RolePatchApplications[position], plan.RoleName, profilePositionalInserts,
		nil, nil,
	)
	return spec, err
}

// TestEngineRuntimeCrossProfileVolumeVisibility pins the KEP test-plan row
// "cross-profile volume reference (any list position) → renders, all
// same-position fragments precede the patch pass": the earlier-listed
// reference's patch mounts a volume declared only by the later-listed
// reference's fragments. Under the same-position two-pass merge this must
// render successfully.
func TestEngineRuntimeCrossProfileVolumeVisibility(t *testing.T) {
	bProfile := makeProfile("b", nil) // declares b-vol, no patches
	aProfile := makeProfile("a", []workloadsv1alpha2.ContainerPatch{
		jp("/volumeMounts/-", v1.VolumeMount{Name: "b-vol", MountPath: "/opt/b", ReadOnly: true}),
	})
	role := leaderWorkerRole([]workloadsv1alpha2.EngineRuntime{
		// the patch-carrying reference consents at its own component
		{ProfileName: "a", Component: leaderPos(), TargetContainers: []workloadsv1alpha2.EngineRuntimeTargetContainer{{Name: "engine"}}},
		{ProfileName: "b"},
	})
	plan, recorder, err := resolverHarness(
		t, []*workloadsv1alpha2.ClusterEngineRuntimeProfile{aProfile, bProfile}, role,
	)
	require.NoError(t, err)
	assert.Empty(t, collectEvents(t, recorder))

	template := *role.GetLeaderWorkerPattern().Template
	spec, err := renderResolvedPosition(t, plan, leaderPos(), template)
	require.NoError(t, err, "the earlier reference's patch may mount the later reference's volume")

	// both references' fragments landed before the patch pass ran
	assert.Equal(t, []string{"a-vol", "b-vol"}, volumeNamesOf(spec))
	engine := spec.Spec.Containers[0]
	require.Len(t, engine.VolumeMounts, 1)
	assert.Equal(t, "b-vol", engine.VolumeMounts[0].Name)
	assert.Equal(t, "/opt/b", engine.VolumeMounts[0].MountPath)
}

// TestEngineRuntimePatchPassOrder pins the KEP-531 D10 pass order at the two
// points the discovery layer can observe it. With both patch passes failing,
// the error of the pass that runs first surfaces — the profile-side pass
// (stage 3) before the role-side pass (stage 4, last). And with both passes
// succeeding on the same role-declared container — the direct overlap D8's
// target vocabulary admits — the role-side mutations land last.
// renderResolvedPosition mirrors the PodReconciler pipeline's pass order, so
// the surfaced attribution and the landed order both pin that order directly.
func TestEngineRuntimePatchPassOrder(t *testing.T) {
	t.Run("with both patch passes failing, the profile-side error surfaces first", func(t *testing.T) {
		// The profile-side patch appends a name the legacy merge (stage 2)
		// already landed on the role-owned engine — role-owned, so a "-"
		// append is a render error. The role-side patch carries an env entry
		// without a name — legal at the resolver's list validation, a render
		// error in the role-side pass. The profile-side pass runs first, so
		// its error surfaces.
		profile := makeProfile("ik", []workloadsv1alpha2.ContainerPatch{
			envPatch("ROLE_ENV", "profile-side"),
		})
		profile.Spec.Containers = []v1.Container{
			{Name: "runtime-sidecar", Env: []v1.EnvVar{{Name: "ROLE_X", Value: "profile"}}},
			{Name: "engine"}, // declared so the legacy merge reaches the role-owned engine
		}
		role := leaderWorkerRole([]workloadsv1alpha2.EngineRuntime{{
			ProfileName: "ik",
			Component:   leaderPos(),
			TargetContainers: []workloadsv1alpha2.EngineRuntimeTargetContainer{
				{Name: "engine"},
				{
					Name: "runtime-sidecar",
					ExtraPatches: []workloadsv1alpha2.ContainerPatch{
						jp("/env/-", v1.EnvVar{Value: "no-name"}),
					},
				},
			},
			Containers: []v1.Container{{
				Name: "engine",
				Env:  []v1.EnvVar{{Name: "ROLE_ENV", Value: "role"}},
			}},
		}})
		plan, recorder, err := resolverHarness(
			t, []*workloadsv1alpha2.ClusterEngineRuntimeProfile{profile}, role,
		)
		require.NoError(t, err)
		assert.Empty(t, collectEvents(t, recorder))

		template := *role.GetLeaderWorkerPattern().Template
		_, err = renderResolvedPosition(t, plan, leaderPos(), template)
		require.Error(t, err)
		// the profile-side pass ran first: its role-owned env append error
		// surfaces, not the role-side one
		assert.Contains(t, err.Error(), `profile "ik" containerPatches[0]`)
		assert.Contains(t, err.Error(), "may only default a role-owned name")
		assert.NotContains(t, err.Error(), `role "worker" engineRuntimes`)
	})

	t.Run("both patch passes on one role-declared container: the role-side result lands last", func(t *testing.T) {
		// The direct overlap the failing-pass pin above cannot express: D8
		// lets the role-side pass target the same role-declared container the
		// profile-side pass patches, and D10 decides the outcome — the
		// role-side pass runs last, so its /command/0 insert lands ahead of
		// (wrapping) the profile's and its env append lands after it.
		profile := makeProfile("ik", []workloadsv1alpha2.ContainerPatch{
			jp("/command/0", "profile-wrap"),
			envPatch("PROF_ENV", "profile-side"),
		})
		role := leaderWorkerRole([]workloadsv1alpha2.EngineRuntime{{
			ProfileName: "ik",
			Component:   leaderPos(),
			TargetContainers: []workloadsv1alpha2.EngineRuntimeTargetContainer{
				{
					Name: "engine",
					ExtraPatches: []workloadsv1alpha2.ContainerPatch{
						jp("/command/0", "role-wrap"),
						jp("/env/-", v1.EnvVar{Name: "ROLE_ENV", Value: "role-side"}),
					},
				},
			},
		}})
		plan, recorder, err := resolverHarness(
			t, []*workloadsv1alpha2.ClusterEngineRuntimeProfile{profile}, role,
		)
		require.NoError(t, err)
		assert.Empty(t, collectEvents(t, recorder))

		template := *role.GetLeaderWorkerPattern().Template
		template.Spec.Containers[0].Command = []string{"python"}
		spec, err := renderResolvedPosition(t, plan, leaderPos(), template)
		require.NoError(t, err)

		engine := spec.Spec.Containers[0]
		// the role-side insert is outermost: it ran after the profile-side
		// insert and wrapped it
		assert.Equal(t, []string{"role-wrap", "profile-wrap", "python"}, engine.Command)
		// the role-side env append landed after the profile's
		assert.Equal(t, []string{"PROF_ENV", "ROLE_ENV"}, envNames(engine))
	})
}

// TestEngineRuntimePositionalShadowing pins the KEP-531 D10 second clause:
// a role-side positional insert landing ahead of a profile-side command wrap
// on the same container is reported, never failed — the role-side pass runs
// last, so its insert lands at the written index and shifts profile-side
// content at or after it. The wrap-shadowing report is command-root-only: the
// profile-side pass records the containers its /command/0 inserts touched
// (EngineRuntimeWrapShadowed is about the command wrap being displaced), and
// the role-side pass reports the subset its own positional inserts shadow.
// Env-root overlaps are the EnvCollision family instead and are never
// adjudicated here. The render succeeds either way — the shadowing must
// never be silent.
func TestEngineRuntimePositionalShadowing(t *testing.T) {
	ctx := context.Background()

	t.Run("a role-side /command/0 ahead of a profile-side /command/0 reports the container", func(t *testing.T) {
		spec := renderSpec() // engine's command is [python]
		roleContainerNames := roleContainers("engine")

		profileEntries := []EngineRuntimePatch{{
			ProfileName: "ik",
			Targets:     []string{"engine"},
			Patches:     []workloadsv1alpha2.ContainerPatch{jp("/command/0", "profile-wrap")},
		}}
		roleEntries := []EngineRuntimeRolePatch{{
			ProfileName:        "ik",
			DeclaredContainers: map[string]struct{}{},
			Targets:            roleTargets(roleJp("engine", "/command/0", "role-wrap")),
		}}

		profilePositionalInserts, err := ApplyEngineRuntimePatches(
			ctx, spec, roleContainerNames, nil, "worker", profileEntries, nil, nil, nil)
		require.NoError(t, err)
		assert.Contains(t, profilePositionalInserts, "engine",
			"the profile-side pass records the container its command wrap touched")

		shadowed, err := ApplyRoleEngineRuntimePatches(
			ctx, spec, roleContainerNames, nil, roleEntries, "worker", profilePositionalInserts, nil, nil)
		require.NoError(t, err, "wrap shadowing emits a render-time event, never a failure")
		assert.Contains(t, shadowed, "engine",
			"the shadow report names the container whose profile-side wrap was landed ahead of")

		// the render succeeded: the role-side wrap is the rendered command's
		// index 0, ahead of the profile-side wrap it shifted
		assert.Equal(t, []string{"role-wrap", "profile-wrap", "python"}, spec.Spec.Containers[0].Command)
	})

	t.Run("an env head insert records nothing — env overlap is EnvCollision territory", func(t *testing.T) {
		spec := renderSpec() // engine carries env [PYTHONPATH]
		roleContainerNames := roleContainers("engine")

		profileEntries := []EngineRuntimePatch{{
			ProfileName: "ik",
			Targets:     []string{"engine"},
			Patches:     []workloadsv1alpha2.ContainerPatch{jp("/env/0", v1.EnvVar{Name: "PROFILE_X", Value: "p"})},
		}}

		profilePositionalInserts, err := ApplyEngineRuntimePatches(
			ctx, spec, roleContainerNames, nil, "worker", profileEntries, nil, nil, nil)
		require.NoError(t, err)
		assert.Empty(t, profilePositionalInserts,
			"the recorder is command-root-only; env head inserts are adjudicated by EnvCollision, not WrapShadowed")
	})

	t.Run("a profile-side append records nothing, so nothing can shadow", func(t *testing.T) {
		// appends land after all existing content and shadow nothing; only
		// when both sides claim absolute positions on the same container's
		// command root can the role-side insert land ahead of the profile-side one
		spec := renderSpec()
		roleContainerNames := roleContainers("engine")
		profileEntries := []EngineRuntimePatch{{
			ProfileName: "ik",
			Targets:     []string{"engine"},
			Patches:     []workloadsv1alpha2.ContainerPatch{jp("/env/-", v1.EnvVar{Name: "PROFILE_X", Value: "p"})},
		}}
		roleEntries := []EngineRuntimeRolePatch{{
			ProfileName:        "ik",
			DeclaredContainers: map[string]struct{}{},
			Targets:            roleTargets(roleJp("engine", "/command/0", "role-wrap")),
		}}

		profilePositionalInserts, err := ApplyEngineRuntimePatches(
			ctx, spec, roleContainerNames, nil, "worker", profileEntries, nil, nil, nil)
		require.NoError(t, err)
		assert.Empty(t, profilePositionalInserts, "an append is not a positional insert")

		shadowed, err := ApplyRoleEngineRuntimePatches(
			ctx, spec, roleContainerNames, nil, roleEntries, "worker", profilePositionalInserts, nil, nil)
		require.NoError(t, err)
		assert.Empty(t, shadowed)
	})

	t.Run("a role-side append reports nothing even over a profile-side wrap", func(t *testing.T) {
		spec := renderSpec()
		roleContainerNames := roleContainers("engine")
		profileEntries := []EngineRuntimePatch{{
			ProfileName: "ik",
			Targets:     []string{"engine"},
			Patches:     []workloadsv1alpha2.ContainerPatch{jp("/command/0", "profile-wrap")},
		}}
		roleEntries := []EngineRuntimeRolePatch{{
			ProfileName:        "ik",
			DeclaredContainers: map[string]struct{}{},
			Targets:            roleTargets(roleJp("engine", "/env/-", v1.EnvVar{Name: "ROLE_Y", Value: "r"})),
		}}

		profilePositionalInserts, err := ApplyEngineRuntimePatches(
			ctx, spec, roleContainerNames, nil, "worker", profileEntries, nil, nil, nil)
		require.NoError(t, err)
		assert.Contains(t, profilePositionalInserts, "engine")

		shadowed, err := ApplyRoleEngineRuntimePatches(
			ctx, spec, roleContainerNames, nil, roleEntries, "worker", profilePositionalInserts, nil, nil)
		require.NoError(t, err)
		assert.Empty(t, shadowed, "a role-side append lands after everything, ahead of nothing")
	})

	t.Run("command inserts on different containers report nothing", func(t *testing.T) {
		spec := renderSpec()
		spec.Spec.Containers = append(spec.Spec.Containers, v1.Container{
			Name: "engine2", Image: "sglang:latest", Command: []string{"python"},
		})
		roleContainerNames := roleContainers("engine", "engine2")

		profileEntries := []EngineRuntimePatch{{
			ProfileName: "ik",
			Targets:     []string{"engine"},
			Patches:     []workloadsv1alpha2.ContainerPatch{jp("/command/0", "profile-wrap")},
		}}
		roleEntries := []EngineRuntimeRolePatch{{
			ProfileName:        "ik",
			DeclaredContainers: map[string]struct{}{},
			Targets:            roleTargets(roleJp("engine2", "/command/0", "role-wrap")),
		}}

		profilePositionalInserts, err := ApplyEngineRuntimePatches(
			ctx, spec, roleContainerNames, nil, "worker", profileEntries, nil, nil, nil)
		require.NoError(t, err)
		shadowed, err := ApplyRoleEngineRuntimePatches(
			ctx, spec, roleContainerNames, nil, roleEntries, "worker", profilePositionalInserts, nil, nil)
		require.NoError(t, err)
		assert.Empty(t, shadowed,
			"the role-side insert landed on a container the profile-side pass never touched positionally")
	})
}

// TestEngineRuntimeSplitDeliveryDanglingMount pins the KEP test-plan row
// "split delivery: a /volumeMounts patch qualified to a position whose
// fragments (and volume) go elsewhere → dangling-mount render error". The
// reworked reference cannot split its own fragments and patches across
// positions — component scopes both — so the split rides two references:
// the patch-carrying reference fires on the leader while the volume-declaring
// profile's fragments go to the worker, and the leader's patch mount
// references a volume the leader never received.
func TestEngineRuntimeSplitDeliveryDanglingMount(t *testing.T) {
	aProfile := makeProfile("a", []workloadsv1alpha2.ContainerPatch{
		jp("/volumeMounts/-", v1.VolumeMount{Name: "b-vol", MountPath: "/opt/b", ReadOnly: true}),
	})
	bProfile := makeProfile("b", nil) // declares b-vol
	role := leaderWorkerRole([]workloadsv1alpha2.EngineRuntime{
		{ProfileName: "a", Component: leaderPos(), TargetContainers: []workloadsv1alpha2.EngineRuntimeTargetContainer{{Name: "engine"}}},
		{ProfileName: "b", Component: workerPos()},
	})
	plan, recorder, err := resolverHarness(
		t, []*workloadsv1alpha2.ClusterEngineRuntimeProfile{aProfile, bProfile}, role,
	)
	require.NoError(t, err)
	assert.Empty(t, collectEvents(t, recorder))

	// the worker received the volume and carries no patch: it renders clean
	template := *role.GetLeaderWorkerPattern().Template
	workerSpec, err := renderResolvedPosition(t, plan, workerPos(), template)
	require.NoError(t, err)
	assert.Equal(t, []string{"b-vol"}, volumeNamesOf(workerSpec))
	assert.Empty(t, plan.PatchApplications[workerPos()])

	// the leader carries the patch but not the volume: dangling-mount error
	_, err = renderResolvedPosition(t, plan, leaderPos(), template)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "b-vol")
	assert.Contains(t, err.Error(), "declared by neither the rendered spec nor the profile")
}

// heterogeneousComponentsRole builds a customComponents role whose
// engine-comp template declares an engine container and whose aux-comp
// template does not — the shape the "target presence scoped to addressed
// positions" test-plan row requires.
func heterogeneousComponentsRole(runtimes []workloadsv1alpha2.EngineRuntime) *workloadsv1alpha2.RoleSpec {
	return &workloadsv1alpha2.RoleSpec{
		Name: "worker",
		Pattern: workloadsv1alpha2.Pattern{
			CustomComponentsPattern: &workloadsv1alpha2.CustomComponentsPattern{
				Components: []workloadsv1alpha2.InstanceComponent{
					{
						Name: "engine-comp", Size: ptr.To(int32(1)),
						Template: v1.PodTemplateSpec{Spec: v1.PodSpec{
							Containers: []v1.Container{{Name: "engine", Image: "sglang:latest"}},
						}},
					},
					{
						Name: "aux-comp", Size: ptr.To(int32(1)),
						Template: v1.PodTemplateSpec{Spec: v1.PodSpec{
							Containers: []v1.Container{{Name: "aux", Image: "aux:latest"}},
						}},
					},
				},
			},
		},
		EngineRuntimes: runtimes,
	}
}

// TestEngineRuntimeTargetPresenceScopedToAddressedPositions pins the KEP
// test-plan row "target presence scoped to addressed positions" — the
// phase-2 fix for the KEP's customComponents defect 2, reworked for the
// named-component reference: only the component a reference addresses sees
// its patch applications, and a component lacking the named container only
// errors when the reference addresses it.
func TestEngineRuntimeTargetPresenceScopedToAddressedPositions(t *testing.T) {
	profile := makeProfile("ik", []workloadsv1alpha2.ContainerPatch{envPatch("IK_SITE", "/opt/ik/site")})

	t.Run("a reference addressing the component lacking the container is a render error", func(t *testing.T) {
		role := heterogeneousComponentsRole([]workloadsv1alpha2.EngineRuntime{
			{ProfileName: "ik", Component: "aux-comp", TargetContainers: []workloadsv1alpha2.EngineRuntimeTargetContainer{{Name: "engine"}}},
		})
		plan, recorder, err := resolverHarness(t, []*workloadsv1alpha2.ClusterEngineRuntimeProfile{profile}, role)
		require.NoError(t, err)
		assert.Empty(t, collectEvents(t, recorder))
		// the patch routed to the addressed component only
		require.Len(t, plan.PatchApplications["aux-comp"], 1)
		assert.NotContains(t, plan.PatchApplications, "engine-comp")

		components := role.GetCustomComponentsPattern().Components

		// the addressed component that lacks the container is a render error
		_, err = renderResolvedPosition(t, plan, "aux-comp", components[1].Template)
		require.Error(t, err)
		assert.Contains(t, err.Error(), `"engine"`)
		assert.Contains(t, err.Error(), "neither role-declared")
	})

	t.Run("a reference addressing only the component that has the container renders with no error", func(t *testing.T) {
		role := heterogeneousComponentsRole([]workloadsv1alpha2.EngineRuntime{
			{ProfileName: "ik", Component: "engine-comp", TargetContainers: []workloadsv1alpha2.EngineRuntimeTargetContainer{{Name: "engine"}}},
		})
		plan, recorder, err := resolverHarness(t, []*workloadsv1alpha2.ClusterEngineRuntimeProfile{profile}, role)
		require.NoError(t, err)
		assert.Empty(t, collectEvents(t, recorder))
		// the patch routes to the addressed component only
		require.Len(t, plan.PatchApplications, 1)
		require.Len(t, plan.PatchApplications["engine-comp"], 1)
		assert.NotContains(t, plan.PatchApplications, "aux-comp")

		components := role.GetCustomComponentsPattern().Components

		// the unaddressed component renders with no error (narrowed scope)
		spec, err := renderResolvedPosition(t, plan, "aux-comp", components[1].Template)
		require.NoError(t, err)
		assert.Equal(t, []string{"aux"}, containerNames(spec))
		assert.Empty(t, spec.Spec.Containers[0].Env)

		// the addressed one renders the patch
		spec, err = renderResolvedPosition(t, plan, "engine-comp", components[0].Template)
		require.NoError(t, err)
		require.Len(t, spec.Spec.Containers[0].Env, 1)
		assert.Equal(t, "IK_SITE", spec.Spec.Containers[0].Env[0].Name)
	})
}

// TestEngineRuntimeTargetingBoundary re-homes the phase-1 cross-profile
// targeting pins for the D8 target vocabulary: a targetContainers entry may
// name a container this reference's own profile declares — profile-injected
// included — while a name only a different reference or profile injected is
// not addressable, and a profile-declared name colliding with a role-declared
// container resolves to the role's own container.
func TestEngineRuntimeTargetingBoundary(t *testing.T) {
	ctx := context.Background()

	t.Run("a profile-declared container is addressable, but profile-side consent does not fire there", func(t *testing.T) {
		spec := renderSpec()
		roleContainerNames := roleContainers("engine") // captured before injection
		ikProfile := sidecarProfile("ik", nil)
		require.NoError(t, InjectEngineRuntimeFragments(ctx, spec, []EngineRuntimeFragment{{
			Profile: ikProfile,
			Runtime: workloadsv1alpha2.EngineRuntime{ProfileName: "ik"},
		}}, "worker", nil, nil))
		// the sidecar the profile itself injected is present in the rendered spec...
		assert.Equal(t, []string{"engine", "ik-sidecar"}, containerNames(spec))

		// ...addressable in the D8 sense — the pass resolves the name without
		// error — but consent fires profile patches only at role-declared
		// addresses, so the profile's own containerPatches skip it; its
		// extraPatches (stage 4, delivered by the role-side pass below) are
		// the entry's only delivery vehicle.
		entries := []EngineRuntimePatch{{
			ProfileName: "ik", Targets: []string{"ik-sidecar"},
			Patches: []workloadsv1alpha2.ContainerPatch{envPatch("IK_SITE", "/opt/ik/site")},
			// DeclaredContainersOf mirrors the resolver's capture of the
			// profile-declared half of the target vocabulary.
			DeclaredContainers: declaredContainersOf(ikProfile),
		}}
		_, err := ApplyEngineRuntimePatches(ctx, spec, roleContainerNames, nil, "worker", entries, nil, nil, nil)
		require.NoError(t, err)
		assert.Empty(t, spec.Spec.Containers[1].Env,
			"profile-side patches never fire on a profile-declared target (consent)")

		// the role-side pass delivers the entry's own extraPatches to it
		roleEntries := []EngineRuntimeRolePatch{{
			ProfileName:        "ik",
			DeclaredContainers: declaredContainersOf(ikProfile),
			Targets: []workloadsv1alpha2.EngineRuntimeTargetContainer{{
				Name: "ik-sidecar",
				ExtraPatches: []workloadsv1alpha2.ContainerPatch{
					envPatch("IK_SITE", "/opt/ik/site"),
				},
			}},
		}}
		_, err = ApplyRoleEngineRuntimePatches(
			ctx, spec, roleContainerNames, nil, roleEntries, "worker", nil, nil, nil)
		require.NoError(t, err)
		require.Len(t, spec.Spec.Containers[1].Env, 1)
		assert.Equal(t, "IK_SITE", spec.Spec.Containers[1].Env[0].Name)
	})

	t.Run("a patch aimed at a container another profile injected is a render error", func(t *testing.T) {
		spec := renderSpec()
		roleContainerNames := roleContainers("engine") // captured before injection
		ikProfile := sidecarProfile("ik", nil)
		require.NoError(t, InjectEngineRuntimeFragments(ctx, spec, []EngineRuntimeFragment{{
			Profile: ikProfile,
			Runtime: workloadsv1alpha2.EngineRuntime{ProfileName: "ik"},
		}}, "worker", nil, nil))
		other := makeProfile("other", nil)
		other.Spec.Containers = []v1.Container{{Name: "other-sidecar", Image: "other:latest"}}
		require.NoError(t, InjectEngineRuntimeFragments(ctx, spec, []EngineRuntimeFragment{{
			Profile: other,
			Runtime: workloadsv1alpha2.EngineRuntime{ProfileName: "other"},
		}}, "worker", nil, nil))
		// the other profile's sidecar is rendered into the position...
		require.NotNil(t, findRenderedContainer(spec, "other-sidecar"))

		// ...but is not addressable by the ik reference's patches
		entries := []EngineRuntimePatch{{
			ProfileName: "ik", Targets: []string{"other-sidecar"},
			Patches:            []workloadsv1alpha2.ContainerPatch{envPatch("IK_SITE", "/opt/ik/site")},
			DeclaredContainers: declaredContainersOf(ikProfile),
		}}
		_, err := ApplyEngineRuntimePatches(ctx, spec, roleContainerNames, nil, "worker", entries, nil, nil, nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), `"other-sidecar"`)
		assert.Contains(t, err.Error(), "neither role-declared")
		assert.Contains(t, err.Error(), `profile "ik"`)
		// the other profile's sidecar gained nothing
		assert.Empty(t, findRenderedContainer(spec, "other-sidecar").Env)
	})

	t.Run("a profile-declared name colliding with a role-declared container resolves to the role's own", func(t *testing.T) {
		spec := &v1.PodTemplateSpec{Spec: v1.PodSpec{Containers: []v1.Container{
			{Name: "engine", Image: "sglang:latest"},
			// the role declares its own ik-sidecar, colliding with the
			// profile-declared sidecar name
			{Name: "ik-sidecar", Image: "role-sidecar:latest"},
		}}}
		roleContainerNames := roleContainers("engine", "ik-sidecar") // captured before injection
		ikProfile := sidecarProfile("ik", nil)
		require.NoError(t, InjectEngineRuntimeFragments(ctx, spec, []EngineRuntimeFragment{{
			Profile: ikProfile,
			Runtime: workloadsv1alpha2.EngineRuntime{ProfileName: "ik"},
		}}, "worker", nil, nil))
		// append-if-missing kept the role's own container
		require.Len(t, spec.Spec.Containers, 2)
		assert.Equal(t, "role-sidecar:latest", spec.Spec.Containers[1].Image)

		// the address resolves to the role's own container and applies legally
		entries := []EngineRuntimePatch{{
			ProfileName: "ik", Targets: []string{"ik-sidecar"},
			Patches:            []workloadsv1alpha2.ContainerPatch{envPatch("IK_SITE", "/opt/ik/site")},
			DeclaredContainers: declaredContainersOf(ikProfile),
		}}
		_, err := ApplyEngineRuntimePatches(ctx, spec, roleContainerNames, nil, "worker", entries, nil, nil, nil)
		require.NoError(t, err)
		sidecar := spec.Spec.Containers[1]
		assert.Equal(t, "role-sidecar:latest", sidecar.Image)
		require.Len(t, sidecar.Env, 1)
		assert.Equal(t, "IK_SITE", sidecar.Env[0].Name)
	})
}

// roleJp builds one role-side targetContainers entry — a named target plus
// an add-op jsonPatch — from a path and a Go value.
func roleJp(container, path string, value any) workloadsv1alpha2.EngineRuntimeTargetContainer {
	patch := jp(path, value)
	return workloadsv1alpha2.EngineRuntimeTargetContainer{
		Name:         container,
		ExtraPatches: []workloadsv1alpha2.ContainerPatch{{JSONPatch: patch.JSONPatch}},
	}
}

// roleTargets merges same-named roleJp entries into one targetContainers
// entry — extraPatches append in argument order, so rule order is preserved.
func roleTargets(entries ...workloadsv1alpha2.EngineRuntimeTargetContainer) []workloadsv1alpha2.EngineRuntimeTargetContainer {
	merged := make([]workloadsv1alpha2.EngineRuntimeTargetContainer, 0, len(entries))
	for _, entry := range entries {
		placed := false
		for i := range merged {
			if merged[i].Name == entry.Name {
				merged[i].ExtraPatches = append(merged[i].ExtraPatches, entry.ExtraPatches...)
				placed = true
				break
			}
		}
		if !placed {
			merged = append(merged, entry)
		}
	}
	return merged
}

// declaredContainersOf mirrors the resolver's capture of one profile's
// declared container names (init containers and sidecars) — the
// profile-declared half of a patch entry's target vocabulary.
func declaredContainersOf(profile *workloadsv1alpha2.ClusterEngineRuntimeProfile) map[string]struct{} {
	declared := make(map[string]struct{},
		len(profile.Spec.InitContainers)+len(profile.Spec.Containers))
	for _, c := range profile.Spec.InitContainers {
		declared[c.Name] = struct{}{}
	}
	for _, c := range profile.Spec.Containers {
		declared[c.Name] = struct{}{}
	}
	return declared
}

// TestApplyRoleEngineRuntimePatches pins the KEP-531 role-side patch pass:
// its place in the application order (after the profile-side pass, last) and
// the D8 target resolution — a target may be role-declared (a spec.containers
// or spec.initContainers member of the role's own template, no consent
// involved) or declared by this reference's own profile, and a name the
// role's own template also declares resolves to the role-owned container —
// the same precedence injection applies. Errors attribute to the role's
// reference, never the profile.
func TestApplyRoleEngineRuntimePatches(t *testing.T) {
	ctx := context.Background()

	// renderProfile declares the container vocabulary role-side patches
	// address: an init container runtime-init (args declared so /args inserts
	// are legal) and a sidecar runtime-sidecar.
	renderProfile := func(sidecarEnv ...v1.EnvVar) *workloadsv1alpha2.ClusterEngineRuntimeProfile {
		profile := declaringProfile("ik")
		profile.Spec.InitContainers[0].Args = []string{"--base"}
		profile.Spec.Containers[0].Env = sidecarEnv
		return profile
	}

	applyRoleEntries := func(
		spec *v1.PodTemplateSpec, roleContainerNames map[string]struct{},
		roleInitContainerNames map[string]struct{},
		entries []EngineRuntimeRolePatch, profilePositionalInserts map[string]struct{},
	) (map[string]struct{}, error) {
		return ApplyRoleEngineRuntimePatches(
			ctx, spec, roleContainerNames, roleInitContainerNames, entries,
			"worker", profilePositionalInserts, nil, nil)
	}

	t.Run("role-side patches fire on profile-declared sidecars and init containers", func(t *testing.T) {
		spec := renderSpec()
		roleContainerNames := roleContainers("engine") // captured before injection
		profile := renderProfile()
		require.NoError(t, InjectEngineRuntimeFragments(ctx, spec, []EngineRuntimeFragment{{
			Profile: profile,
			Runtime: workloadsv1alpha2.EngineRuntime{ProfileName: "ik"},
		}}, "worker", nil, nil))

		_, err := applyRoleEntries(spec, roleContainerNames, nil, []EngineRuntimeRolePatch{{
			ProfileName:        "ik",
			DeclaredContainers: declaredContainersOf(profile),
			Targets: roleTargets(
				roleJp("runtime-sidecar", "/env/-", v1.EnvVar{Name: "A", Value: "b"}),
				roleJp("runtime-init", "/args/-", "--flag"),
			),
		}}, nil)
		require.NoError(t, err)

		// the sidecar gains env A
		sidecar := findRenderedContainer(spec, "runtime-sidecar")
		require.NotNil(t, sidecar)
		require.Len(t, sidecar.Env, 1)
		assert.Equal(t, v1.EnvVar{Name: "A", Value: "b"}, sidecar.Env[0])
		// the init container gains the arg
		require.Len(t, spec.Spec.InitContainers, 1)
		assert.Equal(t, []string{"--base", "--flag"}, spec.Spec.InitContainers[0].Args)
	})

	t.Run("a role-declared container target applies with no consent involved", func(t *testing.T) {
		// D8: the reference is the single toggle for all profile-coupled
		// modification, so its patches may couple to the role's own
		// containers; targetContainers consent gates the profile's rules only.
		spec := renderSpec()
		roleContainerNames := roleContainers("engine") // captured before injection

		_, err := applyRoleEntries(spec, roleContainerNames, nil, []EngineRuntimeRolePatch{{
			ProfileName:        "ik",
			DeclaredContainers: map[string]struct{}{}, // the profile declares nothing
			Targets:            roleTargets(roleJp("engine", "/env/-", v1.EnvVar{Name: "ROLE_X", Value: "b"})),
		}}, nil)
		require.NoError(t, err)

		require.Len(t, spec.Spec.Containers, 1)
		assert.Equal(t, []string{"PYTHONPATH", "ROLE_X"}, envNames(spec.Spec.Containers[0]))
	})

	t.Run("a role-declared init container target applies", func(t *testing.T) {
		// the legacy containers merge cannot reach init containers — the
		// patch grammar closes that gap
		spec := renderSpec()
		spec.Spec.InitContainers = []v1.Container{{
			Name: "role-init", Image: "role-init:latest", Args: []string{"--base"},
		}}
		roleContainerNames := roleContainers("engine")
		roleInitContainerNames := roleContainers("role-init")

		_, err := applyRoleEntries(spec, roleContainerNames, roleInitContainerNames, []EngineRuntimeRolePatch{{
			ProfileName:        "ik",
			DeclaredContainers: map[string]struct{}{},
			Targets:            roleTargets(roleJp("role-init", "/args/-", "--flag")),
		}}, nil)
		require.NoError(t, err)

		require.Len(t, spec.Spec.InitContainers, 1)
		assert.Equal(t, []string{"--base", "--flag"}, spec.Spec.InitContainers[0].Args)
	})

	t.Run("a name declared by both the role template and the profile resolves to the role-owned container", func(t *testing.T) {
		profile := renderProfile()
		// the profile also declares a sidecar colliding with the role's engine
		profile.Spec.Containers = append(profile.Spec.Containers, v1.Container{Name: "engine", Image: "profile-engine:latest"})
		spec := renderSpec()
		roleContainerNames := roleContainers("engine") // captured before injection
		require.NoError(t, InjectEngineRuntimeFragments(ctx, spec, []EngineRuntimeFragment{{
			Profile: profile,
			Runtime: workloadsv1alpha2.EngineRuntime{ProfileName: "ik"},
		}}, "worker", nil, nil))
		// append-if-missing kept the role's own engine: the rendered
		// same-named container is role-owned
		require.Len(t, spec.Spec.Containers, 2)
		assert.Equal(t, "sglang:latest", spec.Spec.Containers[0].Image)

		_, err := applyRoleEntries(spec, roleContainerNames, nil, []EngineRuntimeRolePatch{{
			ProfileName:        "ik",
			DeclaredContainers: declaredContainersOf(profile),
			Targets:            roleTargets(roleJp("engine", "/env/-", v1.EnvVar{Name: "A", Value: "b"})),
		}}, nil)
		require.NoError(t, err)

		// the patch landed on the role-owned engine, not a profile-declared
		// same-named container
		engine := spec.Spec.Containers[0]
		assert.Equal(t, "sglang:latest", engine.Image)
		assert.Equal(t, []string{"PYTHONPATH", "A"}, envNames(engine))
	})

	t.Run("an init container name declared by both the role template and the profile resolves to the role-owned init container", func(t *testing.T) {
		profile := renderProfile()
		spec := renderSpec()
		// the role's own template declares an init container colliding with
		// the profile's runtime-init (declaring args so the patch would be
		// legal on either owner)
		spec.Spec.InitContainers = []v1.Container{{
			Name: "runtime-init", Image: "role-init:latest", Args: []string{"--base"},
		}}
		roleContainerNames := roleContainers("engine") // captured before injection
		roleInitContainerNames := roleContainers("runtime-init")
		require.NoError(t, InjectEngineRuntimeFragments(ctx, spec, []EngineRuntimeFragment{{
			Profile: profile,
			Runtime: workloadsv1alpha2.EngineRuntime{ProfileName: "ik"},
		}}, "worker", nil, nil))
		// append-if-missing kept the role's own runtime-init: the rendered
		// same-named init container is role-owned
		require.Len(t, spec.Spec.InitContainers, 1)
		assert.Equal(t, "role-init:latest", spec.Spec.InitContainers[0].Image)

		_, err := applyRoleEntries(spec, roleContainerNames, roleInitContainerNames, []EngineRuntimeRolePatch{{
			ProfileName:        "ik",
			DeclaredContainers: declaredContainersOf(profile),
			Targets:            roleTargets(roleJp("runtime-init", "/args/-", "--flag")),
		}}, nil)
		require.NoError(t, err)

		// the patch landed on the role-owned init container
		assert.Equal(t, "role-init:latest", spec.Spec.InitContainers[0].Image)
		assert.Equal(t, []string{"--base", "--flag"}, spec.Spec.InitContainers[0].Args)
	})

	t.Run("a role-side patch re-adding a legacy-merged env name is the override gesture", func(t *testing.T) {
		// The role's legacy containers entry appends env X to the profile
		// sidecar — a name new to it, so the merge lands. The role-side
		// patch then appends the same X: role patches land last, so its
		// entry wins the positional contest. Never an error — the role-side
		// same-named append is the override gesture (KEP-531 Patch Grammar).
		spec := renderSpec()
		profile := renderProfile()
		require.NoError(t, InjectEngineRuntimeFragments(ctx, spec, []EngineRuntimeFragment{{
			Profile: profile,
			Runtime: workloadsv1alpha2.EngineRuntime{
				ProfileName: "ik",
				Containers: []v1.Container{{
					Name: "runtime-sidecar",
					Env:  []v1.EnvVar{{Name: "X", Value: "role"}},
				}},
			},
		}}, "worker", nil, nil))
		require.Len(t, spec.Spec.Containers[1].Env, 1) // the legacy merge landed

		_, err := applyRoleEntries(spec, roleContainers("engine"), nil, []EngineRuntimeRolePatch{{
			ProfileName:        "ik",
			DeclaredContainers: declaredContainersOf(profile),
			Targets:            roleTargets(roleJp("runtime-sidecar", "/env/-", v1.EnvVar{Name: "X", Value: "patch"})),
		}}, nil)
		require.NoError(t, err)
		sidecar := spec.Spec.Containers[1]
		require.Len(t, sidecar.Env, 2)
		assert.Equal(t, "role", sidecar.Env[0].Value)
		assert.Equal(t, "patch", sidecar.Env[1].Value) // the role-side entry lands last, so it wins
	})

	t.Run("a profile-declared target absent from the rendered spec is a render error", func(t *testing.T) {
		// the name is inside this reference's declared vocabulary, but the
		// position never rendered it (here: no fragment pass ran)
		spec := renderSpec()
		_, err := applyRoleEntries(spec, roleContainers("engine"), nil, []EngineRuntimeRolePatch{{
			ProfileName:        "ik",
			DeclaredContainers: declaredContainersOf(renderProfile()),
			Targets:            roleTargets(roleJp("runtime-sidecar", "/env/-", v1.EnvVar{Name: "A", Value: "b"})),
		}}, nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "neither role-declared nor declared by profile")
		assert.Contains(t, err.Error(), `role "worker" engineRuntimes["ik"].targetContainers["runtime-sidecar"].extraPatches[0]`)
	})

	t.Run("a target declared only by a different reference's profile is a render error", func(t *testing.T) {
		// a name injected only by a different reference or profile is
		// outside this reference's target vocabulary
		spec := renderSpec()
		profile := renderProfile()
		require.NoError(t, InjectEngineRuntimeFragments(ctx, spec, []EngineRuntimeFragment{{
			Profile: profile,
			Runtime: workloadsv1alpha2.EngineRuntime{ProfileName: "ik"},
		}}, "worker", nil, nil))
		other := makeProfile("other", nil)
		other.Spec.Containers = []v1.Container{{Name: "other-sidecar", Image: "other:latest"}}
		require.NoError(t, InjectEngineRuntimeFragments(ctx, spec, []EngineRuntimeFragment{{
			Profile: other,
			Runtime: workloadsv1alpha2.EngineRuntime{ProfileName: "other"},
		}}, "worker", nil, nil))
		// the other profile's sidecar is rendered into the position...
		require.NotNil(t, findRenderedContainer(spec, "other-sidecar"))

		// ...but is not addressable by this reference's patches
		_, err := applyRoleEntries(spec, roleContainers("engine"), nil, []EngineRuntimeRolePatch{{
			ProfileName:        "ik",
			DeclaredContainers: declaredContainersOf(profile),
			Targets:            roleTargets(roleJp("other-sidecar", "/env/-", v1.EnvVar{Name: "A", Value: "b"})),
		}}, nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(),
			`role "worker" engineRuntimes["ik"].targetContainers["other-sidecar"].extraPatches[0] targets container "other-sidecar", `+
				`which is neither role-declared nor declared by profile`)
		// the other profile's sidecar gained nothing
		assert.Empty(t, findRenderedContainer(spec, "other-sidecar").Env)
	})

	t.Run("profile-side and role-side patches both fire in application order", func(t *testing.T) {
		// KEP-531 D10: the profile-side pass applies before the role-side
		// pass — what the role writes lands where it is written. The
		// profile declares a sidecar so the role-side pass can address it,
		// and the role's legacy containers entry merges its env into the
		// role-owned engine ahead of the profile-side patch. The
		// profile-declared sidecar carries the profile's command and env, and
		// the role-side pass — last — renders its /command/0 insert as the
		// rendered command's index 0, ahead of anything the profile
		// inserted, and appends env entries that may $(VAR)-reference
		// profile-appended ones (the forward direction only).
		profile := renderProfile(v1.EnvVar{Name: "PROF_X", Value: "profile"})
		profile.Spec.Containers[0].Command = []string{"--sidecar-base"}
		profile.Spec.Containers = append(profile.Spec.Containers, v1.Container{Name: "engine"})
		fragment := EngineRuntimeFragment{
			Profile: profile,
			Runtime: workloadsv1alpha2.EngineRuntime{
				ProfileName: "ik",
				Containers: []v1.Container{{
					Name: "engine",
					Env:  []v1.EnvVar{{Name: "ROLE_ENV", Value: "role"}},
				}},
			},
		}
		profileEntries := []EngineRuntimePatch{{
			ProfileName: "ik",
			Targets:     []string{"engine"},
			Patches: []workloadsv1alpha2.ContainerPatch{
				jp("/command/0", "profile-wrap"),
				envPatch("PROF_ENV", "profile-side"),
			},
		}}
		roleEntries := []EngineRuntimeRolePatch{{
			ProfileName:        "ik",
			DeclaredContainers: declaredContainersOf(profile),
			Targets: roleTargets(
				roleJp("runtime-sidecar", "/command/0", "role-wrap"),
				roleJp("runtime-sidecar", "/env/-", v1.EnvVar{Name: "ROLE_X", Value: "$(PROF_X)"}),
			),
		}}

		spec := renderSpec()
		roleContainerNames := roleContainers("engine") // captured before injection
		require.NoError(t, InjectEngineRuntimeFragments(ctx, spec, []EngineRuntimeFragment{fragment}, "worker", nil, nil))
		profileInserts, err := ApplyEngineRuntimePatches(ctx, spec, roleContainerNames, nil, "worker", profileEntries, nil, nil, nil)
		require.NoError(t, err)
		shadowed, err := applyRoleEntries(spec, roleContainerNames, nil, roleEntries, profileInserts)
		require.NoError(t, err)
		// the two passes inserted positionally on different containers, so
		// nothing shadowed
		assert.Empty(t, shadowed)

		// the profile-side pass fired on the role-declared engine: its
		// command insert landed and its env appends land behind the env the
		// legacy merge (role-side authored) appended
		engine := spec.Spec.Containers[0]
		assert.Equal(t, []string{"profile-wrap", "python"}, engine.Command)
		assert.Equal(t, []string{"PYTHONPATH", "ROLE_ENV", "PROF_ENV"}, envNames(engine))

		// the role-side pass fired on the profile-declared sidecar, last:
		// its /command/0 insert is the rendered command's index 0, ahead of
		// anything the profile inserted, and its env appends may
		// $(VAR)-reference profile-appended entries — the forward direction
		sidecar := spec.Spec.Containers[1]
		assert.Equal(t, []string{"role-wrap", "--sidecar-base"}, sidecar.Command)
		require.Len(t, sidecar.Env, 2)
		assert.Equal(t, []string{"PROF_X", "ROLE_X"}, envNames(sidecar))
		assert.Equal(t, "$(PROF_X)", sidecar.Env[1].Value)

		// a profile-side patch re-adding the role-side-merged env name
		// errors with profile attribution: the name is role-owned (the
		// legacy merge authored it), and a "-" append is not the sanctioned
		// /env/0 default
		_, err = ApplyEngineRuntimePatches(ctx, spec, roleContainerNames, nil, "worker", []EngineRuntimePatch{{
			ProfileName: "ik",
			Targets:     []string{"engine"},
			Patches:     []workloadsv1alpha2.ContainerPatch{envPatch("ROLE_ENV", "again")},
		}}, nil, nil, nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), `profile "ik" containerPatches[0]`)
		assert.Contains(t, err.Error(), "may only default a role-owned name")
	})
}

// TestApplyRoleEngineRuntimePatchesD9 pins the KEP-531 D9 content-flow rule
// as reworked: the one-way guard is gone — role content flowing into a
// profile-declared container is the role author's explicit decision, reviewable
// in the role spec, so role volumes, secret refs and config mounts all land
// legally. The owning-side boundaries that remain live in the patch grammar
// (profile-side env defaulting, the reserved volume name) and are pinned in
// container_patch_test.go.
func TestApplyRoleEngineRuntimePatchesD9(t *testing.T) {
	ctx := context.Background()

	roleVol := v1.Volume{
		Name:         "role-vol",
		VolumeSource: v1.VolumeSource{EmptyDir: &v1.EmptyDirVolumeSource{}},
	}
	secretRefEnv := v1.EnvVar{Name: "TOKEN", ValueFrom: &v1.EnvVarSource{
		SecretKeyRef: &v1.SecretKeySelector{
			LocalObjectReference: v1.LocalObjectReference{Name: "role-secret"}, Key: "token",
		},
	}}

	// renderedWithProfile returns a fresh spec carrying the role-declared
	// volume plus the ik profile's declared init container and sidecar.
	renderedWithProfile := func() *v1.PodTemplateSpec {
		spec := renderSpec()
		spec.Spec.Volumes = []v1.Volume{roleVol}
		profile := declaringProfile("ik")
		require.NoError(t, InjectEngineRuntimeFragments(ctx, spec, []EngineRuntimeFragment{{
			Profile: profile,
			Runtime: workloadsv1alpha2.EngineRuntime{ProfileName: "ik"},
		}}, "worker", nil, nil))
		return spec
	}

	t.Run("a role-declared volume mounted into a profile-declared container is legal", func(t *testing.T) {
		spec := renderedWithProfile()
		_, err := ApplyRoleEngineRuntimePatches(ctx, spec, roleContainers("engine"), nil, []EngineRuntimeRolePatch{{
			ProfileName:        "ik",
			DeclaredContainers: declaredContainersOf(declaringProfile("ik")),
			Targets:            roleTargets(roleJp("runtime-sidecar", "/volumeMounts/-", v1.VolumeMount{Name: "role-vol", MountPath: "/data"})),
		}}, "worker", nil, nil, nil)
		require.NoError(t, err)
		sidecar := findRenderedContainer(spec, "runtime-sidecar")
		require.NotNil(t, sidecar)
		require.Len(t, sidecar.VolumeMounts, 1)
		assert.Equal(t, "role-vol", sidecar.VolumeMounts[0].Name)
	})

	t.Run("a secretKeyRef env value into a profile-declared container is legal", func(t *testing.T) {
		spec := renderedWithProfile()
		_, err := ApplyRoleEngineRuntimePatches(ctx, spec, roleContainers("engine"), nil, []EngineRuntimeRolePatch{{
			ProfileName:        "ik",
			DeclaredContainers: declaredContainersOf(declaringProfile("ik")),
			Targets:            roleTargets(roleJp("runtime-sidecar", "/env/-", secretRefEnv)),
		}}, "worker", nil, nil, nil)
		require.NoError(t, err)
		sidecar := findRenderedContainer(spec, "runtime-sidecar")
		require.NotNil(t, sidecar)
		require.Len(t, sidecar.Env, 1)
		assert.Equal(t, "TOKEN", sidecar.Env[0].Name)
	})

	t.Run("the same content into a role-declared container is legal too", func(t *testing.T) {
		// the converse needs no pin of its own: role-declared containers may
		// consume both sides' content — the role's own volumes and secrets
		// included
		spec := renderedWithProfile()
		_, err := ApplyRoleEngineRuntimePatches(ctx, spec, roleContainers("engine"), nil, []EngineRuntimeRolePatch{{
			ProfileName:        "ik",
			DeclaredContainers: map[string]struct{}{}, // the target is role-declared
			Targets: roleTargets(
				roleJp("engine", "/volumeMounts/-", v1.VolumeMount{Name: "role-vol", MountPath: "/data"}),
				roleJp("engine", "/env/-", secretRefEnv),
			),
		}}, "worker", nil, nil, nil)
		require.NoError(t, err)
		engine := spec.Spec.Containers[0]
		require.Len(t, engine.VolumeMounts, 1)
		assert.Equal(t, "role-vol", engine.VolumeMounts[0].Name)
		assert.Equal(t, []string{"PYTHONPATH", "TOKEN"}, envNames(engine))
	})
}
