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
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/log"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
)

func basePodSpec() *v1.PodTemplateSpec {
	return &v1.PodTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{Name: "tpl"},
		Spec: v1.PodSpec{
			Containers: []v1.Container{
				{Name: "engine", Image: "sglang:latest", Command: []string{"python"}, Args: []string{"-m", "sglang.launch_server"}},
			},
			Volumes: []v1.Volume{{Name: "role-vol", VolumeSource: v1.VolumeSource{EmptyDir: &v1.EmptyDirVolumeSource{}}}},
		},
	}
}

func roleContainers(names ...string) map[string]struct{} {
	m := make(map[string]struct{}, len(names))
	for _, n := range names {
		m[n] = struct{}{}
	}
	return m
}

// jp builds an `add` jsonPatch arm from a path and a Go value, mirroring how
// the apiserver delivers the authored YAML JSON inside RawExtension.
func jp(path string, value any) workloadsv1alpha2.ContainerPatch {
	raw, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return workloadsv1alpha2.ContainerPatch{JSONPatch: &workloadsv1alpha2.ContainerJSONPatch{
		Op: "add", Path: path, Value: runtime.RawExtension{Raw: raw},
	}}
}

// applyProfile runs the profile-side pass on one target list. The role's
// declared names are "engine" plus any initNames, and declared carries the
// reference's own profile declarations (the D8 profile-declared half).
func applyProfile(
	spec *v1.PodTemplateSpec, targets []string,
	rules []workloadsv1alpha2.ContainerPatch, profile string,
	declared map[string]struct{}, initNames ...string,
) error {
	return applyContainerPatch(spec, roleContainers("engine"), roleContainers(initNames...),
		"worker", targets, rules, profile, declared, nil, &envCreationTracker{}, nil)
}

// applyRole runs the role-side pass for one targetContainers entry whose
// extraPatches are rules. The returned recorder collects the events the
// pass emits (the capability form hazard).
func applyRole(
	ctx context.Context, spec *v1.PodTemplateSpec, target string,
	rules []workloadsv1alpha2.ContainerPatch, profile string,
	declared map[string]struct{},
) (*record.FakeRecorder, error) {
	recorder := record.NewFakeRecorder(16)
	rbg := &workloadsv1alpha2.RoleBasedGroup{}
	entry := EngineRuntimeRolePatch{
		ProfileName:        profile,
		Targets:            []workloadsv1alpha2.EngineRuntimeTargetContainer{{Name: target, ExtraPatches: rules}},
		DeclaredContainers: declared,
	}
	_, err := ApplyRoleEngineRuntimePatches(ctx, spec, roleContainers("engine"), nil,
		[]EngineRuntimeRolePatch{entry}, "worker", nil, recorder, rbg)
	return recorder, err
}

func drainEvents(recorder *record.FakeRecorder) []string {
	var events []string
	for {
		select {
		case e := <-recorder.Events:
			events = append(events, e)
		default:
			return events
		}
	}
}

func TestApplyContainerPatchTargeting(t *testing.T) {
	rules := []workloadsv1alpha2.ContainerPatch{jp("/env/-", v1.EnvVar{Name: "IK_SITE", Value: "/opt/p"})}

	t.Run("a name neither role-declared nor profile-declared errors", func(t *testing.T) {
		spec := basePodSpec()
		err := applyProfile(spec, []string{"ghost"}, rules, "p", nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "neither role-declared")
		assert.Contains(t, err.Error(), "nor declared by profile")
	})

	t.Run("a profile-declared target is addressable but takes no profile-side patches (D8 second half, consent)", func(t *testing.T) {
		spec := basePodSpec()
		spec.Spec.Containers = append(spec.Spec.Containers, v1.Container{Name: "runtime-sidecar", Image: "sidecar"})
		err := applyProfile(spec, []string{"runtime-sidecar"}, rules, "p",
			roleContainers("runtime-sidecar"))
		require.NoError(t, err)
		// Consent fires profile patches only at role-declared addresses; a
		// profile-declared entry takes its own extraPatches (stage 4), and
		// the profile-side pass skips it silently.
		assert.Empty(t, spec.Spec.Containers[1].Env)
	})

	t.Run("a role-declared init container is addressable", func(t *testing.T) {
		spec := basePodSpec()
		spec.Spec.InitContainers = []v1.Container{{Name: "setup", Image: "busybox"}}
		err := applyProfile(spec, []string{"setup"}, rules, "p", nil, "setup")
		require.NoError(t, err)
		require.Len(t, spec.Spec.InitContainers[0].Env, 1)
	})

	t.Run("a name injected only by a different profile is not addressable", func(t *testing.T) {
		spec := basePodSpec()
		// "other-sidecar" is rendered pod content, but neither role-declared
		// (the snapshot predates injection) nor declared by this reference's
		// own profile — the render error of the profile-injected target.
		spec.Spec.Containers = append(spec.Spec.Containers, v1.Container{Name: "other-sidecar", Image: "x"})
		err := applyProfile(spec, []string{"other-sidecar"}, rules, "p", nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "neither role-declared")
	})

	t.Run("empty rules or targets is a no-op", func(t *testing.T) {
		spec := basePodSpec()
		require.NoError(t, applyProfile(spec, []string{"engine"}, nil, "p", nil))
		require.NoError(t, applyProfile(spec, nil, rules, "p", nil))
		assert.Equal(t, basePodSpec().Spec, spec.Spec)
	})

	t.Run("element with no patch field set errors", func(t *testing.T) {
		spec := basePodSpec()
		err := applyProfile(spec, []string{"engine"}, []workloadsv1alpha2.ContainerPatch{{}}, "p", nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no patch field is set")
		assert.Contains(t, err.Error(), `profile "p"`)
		assert.Contains(t, err.Error(), "containerPatches[0]")
	})
}

func TestApplyContainerPatchGrammar(t *testing.T) {
	ctx := log.IntoContext(context.Background(), klog.NewKlogr())

	tests := []struct {
		name    string
		rule    workloadsv1alpha2.ContainerPatch
		wantErr string
	}{
		{name: "unsupported op", rule: workloadsv1alpha2.ContainerPatch{JSONPatch: &workloadsv1alpha2.ContainerJSONPatch{
			Op: "replace", Path: "/command/0", Value: runtime.RawExtension{Raw: []byte(`"x"`)}}}, wantErr: `op "replace" is not supported`},
		{name: "missing value", rule: workloadsv1alpha2.ContainerPatch{JSONPatch: &workloadsv1alpha2.ContainerJSONPatch{
			Op: "add", Path: "/command/0"}}, wantErr: "requires a value"},
		{name: "whole-list path", rule: jp("/command", "x"), wantErr: "not an element pointer"},
		{name: "path outside the allowlist", rule: jp("/image", "x"), wantErr: "not an element pointer"},
		{name: "deep pointer below the root", rule: jp("/env/0/value", "x"), wantErr: "not an element pointer"},
		{name: "segment is neither index nor dash", rule: jp("/command/x", "x"), wantErr: `neither an index nor "-"`},
		{name: "string value for an env slot", rule: jp("/env/-", "not-an-object"), wantErr: "value must match the target struct"},
		{name: "object value for a command slot", rule: jp("/command/0", map[string]string{"a": "b"}), wantErr: "value must be a JSON string"},
		{name: "unknown field in an object value", rule: jp("/env/-", map[string]any{"name": "X", "valuee": "typo"}),
			wantErr: "value must match the target struct"},
		{name: "env var without a name", rule: jp("/env/-", v1.EnvVar{Value: "x"}), wantErr: "requires a name"},
		{name: "volumeMount without a mountPath", rule: jp("/volumeMounts/-", v1.VolumeMount{Name: "role-vol"}), wantErr: "requires a mountPath"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			spec := basePodSpec()
			err := applyProfile(spec, []string{"engine"}, []workloadsv1alpha2.ContainerPatch{tc.rule}, "p", nil)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
			assert.Contains(t, err.Error(), `profile "p"`)
		})
	}

	roleGrammar := []struct {
		name    string
		rule    workloadsv1alpha2.ContainerPatch
		wantErr string
	}{
		{name: "index beyond the list length", rule: jp("/command/5", "x"), wantErr: "exceeds the list length"},
		{name: "index beyond int range", rule: jp("/command/9223372036854775808", "x"), wantErr: "invalid index"},
	}
	for _, tc := range roleGrammar {
		t.Run("role side: "+tc.name, func(t *testing.T) {
			spec := basePodSpec()
			_, err := applyRole(ctx, spec, "engine", []workloadsv1alpha2.ContainerPatch{tc.rule}, "p", nil)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
			assert.Contains(t, err.Error(), `role "worker"`)
		})
	}
}

// TestApplyContainerPatchRootAllowlist pins the side-asymmetric root
// allowlist (KEP-531 D11): the profile side may address /env,
// /volumeMounts and /command/0 only; /args at any element,
// /command/<i> with i > 0, and /securityContext/capabilities/add are
// role-side-only, and a profile-side entry addressing one is a render
// error attributed to the profile.
func TestApplyContainerPatchRootAllowlist(t *testing.T) {
	ctx := log.IntoContext(context.Background(), klog.NewKlogr())

	profileViolations := []struct {
		name string
		rule workloadsv1alpha2.ContainerPatch
	}{
		{name: "args append", rule: jp("/args/-", "--flag")},
		{name: "args head insert", rule: jp("/args/0", "--flag")},
		{name: "command mid-insert", rule: jp("/command/1", "--flag")},
		{name: "capability append", rule: jp("/securityContext/capabilities/add/-", "SYS_RESOURCE")},
	}
	for _, tc := range profileViolations {
		t.Run("profile side rejects "+tc.name, func(t *testing.T) {
			spec := basePodSpec()
			err := applyProfile(spec, []string{"engine"}, []workloadsv1alpha2.ContainerPatch{tc.rule}, "p", nil)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "role-side-only")
			assert.Contains(t, err.Error(), `profile "p"`)
			assert.Contains(t, err.Error(), "containerPatches[0]")
		})
	}

	t.Run("profile side /command/0 is the one legal argv vehicle", func(t *testing.T) {
		spec := basePodSpec()
		require.NoError(t, applyProfile(spec, []string{"engine"},
			[]workloadsv1alpha2.ContainerPatch{jp("/command/0", "wrap")}, "p", nil))
		assert.Equal(t, []string{"wrap", "python"}, spec.Spec.Containers[0].Command)
	})

	t.Run("role side keeps all five roots at any index", func(t *testing.T) {
		spec := basePodSpec()
		spec.Spec.Containers[0].SecurityContext = &v1.SecurityContext{}
		_, err := applyRole(ctx, spec, "engine", []workloadsv1alpha2.ContainerPatch{
			jp("/command/0", "wrap"),
			jp("/args/0", "--head"),
			jp("/args/-", "--tail"),
			jp("/securityContext/capabilities/add/-", "SYS_RESOURCE"),
			jp("/env/-", v1.EnvVar{Name: "X", Value: "1"}),
		}, "p", nil)
		require.NoError(t, err)
		c := spec.Spec.Containers[0]
		assert.Equal(t, []string{"wrap", "python"}, c.Command)
		assert.Equal(t, []string{"--head", "-m", "sglang.launch_server", "--tail"}, c.Args)
		assert.Equal(t, []v1.Capability{"SYS_RESOURCE"}, c.SecurityContext.Capabilities.Add)
		require.Len(t, c.Env, 1)
	})
}

func TestApplyContainerPatchRFCAdd(t *testing.T) {
	ctx := log.IntoContext(context.Background(), klog.NewKlogr())

	t.Run("head insert: multi-element prepend is written in reverse order", func(t *testing.T) {
		spec := basePodSpec()
		require.NoError(t, applyProfile(spec, []string{"engine"}, []workloadsv1alpha2.ContainerPatch{
			jp("/command/0", "exec"), // [exec, python]
			jp("/command/0", "--"),   // [--, exec, python]
			jp("/command/0", "/opt/p/wrap"),
		}, "p", nil))
		assert.Equal(t, []string{"/opt/p/wrap", "--", "exec", "python"}, spec.Spec.Containers[0].Command)
	})

	t.Run("role side: tail insert appends", func(t *testing.T) {
		spec := basePodSpec()
		_, err := applyRole(ctx, spec, "engine", []workloadsv1alpha2.ContainerPatch{
			jp("/args/-", "--extra"),
		}, "p", nil)
		require.NoError(t, err)
		assert.Equal(t, []string{"-m", "sglang.launch_server", "--extra"}, spec.Spec.Containers[0].Args)
	})

	t.Run("role side: index == len behaves as append", func(t *testing.T) {
		spec := basePodSpec()
		_, err := applyRole(ctx, spec, "engine", []workloadsv1alpha2.ContainerPatch{
			jp("/args/2", "--extra"),
		}, "p", nil)
		require.NoError(t, err)
		assert.Equal(t, []string{"-m", "sglang.launch_server", "--extra"}, spec.Spec.Containers[0].Args)
	})

	t.Run("role side: middle index inserts in place", func(t *testing.T) {
		spec := basePodSpec()
		_, err := applyRole(ctx, spec, "engine", []workloadsv1alpha2.ContainerPatch{
			jp("/args/1", "--extra"),
		}, "p", nil)
		require.NoError(t, err)
		assert.Equal(t, []string{"-m", "--extra", "sglang.launch_server"}, spec.Spec.Containers[0].Args)
	})

	t.Run("creating command is always an error (ENTRYPOINT)", func(t *testing.T) {
		// the args-only shape: args are set but command is not —
		// B1 pins this exact target, because a /command/0 wrap onto it
		// silently discards the image ENTRYPOINT whatever args say.
		spec := basePodSpec()
		spec.Spec.Containers[0].Command = nil
		err := applyProfile(spec, []string{"engine"}, []workloadsv1alpha2.ContainerPatch{jp("/command/0", "x")}, "p", nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "declares no command")
		assert.Contains(t, err.Error(), "ENTRYPOINT")
		assert.Contains(t, err.Error(), `profile "p" containerPatches`,
			"the render error is attributed to the profile side")
		_, err = applyRole(ctx, spec, "engine", []workloadsv1alpha2.ContainerPatch{jp("/command/0", "x")}, "p", nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "ENTRYPOINT")
	})

	t.Run("a bare target (neither command nor args) is equally rejected on the profile side", func(t *testing.T) {
		spec := basePodSpec()
		spec.Spec.Containers[0].Command = nil
		spec.Spec.Containers[0].Args = nil
		err := applyProfile(spec, []string{"engine"}, []workloadsv1alpha2.ContainerPatch{jp("/command/0", "x")}, "p", nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "declares no command")
		assert.Contains(t, err.Error(), `profile "p" containerPatches`,
			"the rejection rides the source, not the argv shape")
	})

	t.Run("the args-only shape is fine when command is already declared (the wrap vehicle)", func(t *testing.T) {
		spec := basePodSpec()
		require.NoError(t, applyProfile(spec, []string{"engine"}, []workloadsv1alpha2.ContainerPatch{
			jp("/command/0", "env"),
		}, "p", nil))
		assert.Equal(t, []string{"env", "python"}, spec.Spec.Containers[0].Command)
	})

	t.Run("role side: creating args is allowed when command is set (the SGLang shape)", func(t *testing.T) {
		spec := basePodSpec()
		spec.Spec.Containers[0].Args = nil
		_, err := applyRole(ctx, spec, "engine", []workloadsv1alpha2.ContainerPatch{jp("/args/-", "--extra")}, "p", nil)
		require.NoError(t, err)
		assert.Equal(t, []string{"--extra"}, spec.Spec.Containers[0].Args)
	})

	t.Run("role side: creating args errors only when command and args are both absent (CMD hazard)", func(t *testing.T) {
		spec := basePodSpec()
		spec.Spec.Containers[0].Command = nil
		spec.Spec.Containers[0].Args = nil
		_, err := applyRole(ctx, spec, "engine", []workloadsv1alpha2.ContainerPatch{jp("/args/0", "x")}, "p", nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "neither command nor args")
		assert.Contains(t, err.Error(), "CMD")
	})

	t.Run("role side: an explicitly empty command counts as absent", func(t *testing.T) {
		spec := basePodSpec()
		spec.Spec.Containers[0].Command = []string{}
		spec.Spec.Containers[0].Args = nil
		_, err := applyRole(ctx, spec, "engine", []workloadsv1alpha2.ContainerPatch{jp("/args/0", "x")}, "p", nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "neither command nor args")
	})

	t.Run("later ops in the same list see earlier ops' mutations", func(t *testing.T) {
		spec := basePodSpec()
		require.NoError(t, applyProfile(spec, []string{"engine"}, []workloadsv1alpha2.ContainerPatch{
			jp("/env/-", v1.EnvVar{Name: "FIRST", Value: "1"}),
			jp("/env/0", v1.EnvVar{Name: "SECOND", Value: "2"}), // head-inserts before FIRST
		}, "p", nil))
		env := spec.Spec.Containers[0].Env
		require.Len(t, env, 2)
		assert.Equal(t, []string{"SECOND", "FIRST"}, []string{env[0].Name, env[1].Name})
	})
}

func TestApplyContainerPatchEnv(t *testing.T) {
	ctx := log.IntoContext(context.Background(), klog.NewKlogr())

	t.Run("adds a fresh var and leaves existing ones untouched", func(t *testing.T) {
		spec := basePodSpec()
		spec.Spec.Containers[0].Env = []v1.EnvVar{{Name: "PYTHONPATH", Value: "/usr/lib/python"}}
		require.NoError(t, applyProfile(spec, []string{"engine"}, []workloadsv1alpha2.ContainerPatch{
			jp("/env/-", v1.EnvVar{Name: "IK_SITE", Value: "/opt/p"}),
		}, "p", nil))
		env := spec.Spec.Containers[0].Env
		require.Len(t, env, 2)
		assert.Equal(t, v1.EnvVar{Name: "PYTHONPATH", Value: "/usr/lib/python"}, env[0])
		assert.Equal(t, v1.EnvVar{Name: "IK_SITE", Value: "/opt/p"}, env[1])
	})

	t.Run("profile-side append onto a role-owned name is a render error", func(t *testing.T) {
		spec := basePodSpec()
		spec.Spec.Containers[0].Env = []v1.EnvVar{{Name: "PYTHONPATH", Value: "/usr/lib/python"}}
		err := applyProfile(spec, []string{"engine"}, []workloadsv1alpha2.ContainerPatch{
			jp("/env/-", v1.EnvVar{Name: "PYTHONPATH", Value: "/opt/p"}),
		}, "p", nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "may only default a role-owned name")
		assert.Contains(t, err.Error(), `profile "p"`)
	})

	t.Run("profile-side /env/0 onto a role-owned name is the sanctioned default", func(t *testing.T) {
		spec := basePodSpec()
		spec.Spec.Containers[0].Env = []v1.EnvVar{{Name: "PYTHONPATH", Value: "/usr/lib/python"}}
		require.NoError(t, applyProfile(spec, []string{"engine"}, []workloadsv1alpha2.ContainerPatch{
			jp("/env/0", v1.EnvVar{Name: "PYTHONPATH", Value: "/opt/p"}),
		}, "p", nil))
		env := spec.Spec.Containers[0].Env
		require.Len(t, env, 2)
		// The default lands ahead of the role-owned entry, so the role's
		// later entry wins the positional contest — superseded-by-position
		// is what a default means.
		assert.Equal(t, "/opt/p", env[0].Value)
		assert.Equal(t, "/usr/lib/python", env[1].Value)
	})

	t.Run("the /env/0 default is effective when the template omits the name", func(t *testing.T) {
		spec := basePodSpec()
		require.NoError(t, applyProfile(spec, []string{"engine"}, []workloadsv1alpha2.ContainerPatch{
			jp("/env/0", v1.EnvVar{Name: "IK_ALL", Value: "1"}),
		}, "p", nil))
		require.Len(t, spec.Spec.Containers[0].Env, 1)
		assert.Equal(t, v1.EnvVar{Name: "IK_ALL", Value: "1"}, spec.Spec.Containers[0].Env[0])
	})

	// M5: envFrom names ride outside the positional rules. The env logic
	// reads container.Env only, so a name the target carries via envFrom
	// is neither an existing name (no render error, no collision warning)
	// nor a defense — declaring the name in direct env is what protects a
	// role-owned value; a same-name entry the envFrom would shadow is left
	// to kubelet's own direct-env-over-envFrom priority, unprotected.
	t.Run("an envFrom-carried name does not block a profile-side append", func(t *testing.T) {
		spec := basePodSpec()
		spec.Spec.Containers[0].EnvFrom = []v1.EnvFromSource{{
			ConfigMapRef: &v1.ConfigMapEnvSource{LocalObjectReference: v1.LocalObjectReference{Name: "site-cm"}},
		}}
		err := applyProfile(spec, []string{"engine"}, []workloadsv1alpha2.ContainerPatch{
			jp("/env/-", v1.EnvVar{Name: "IK_SITE", Value: "/opt/p"}),
		}, "p", nil)
		require.NoError(t, err, "envFrom names are outside the positional rules — the append renders")
		require.Len(t, spec.Spec.Containers[0].Env, 1)
		assert.Equal(t, "IK_SITE", spec.Spec.Containers[0].Env[0].Name)
	})

	t.Run("declaring the name in direct env is the role defense against a profile-side append", func(t *testing.T) {
		spec := basePodSpec()
		spec.Spec.Containers[0].EnvFrom = []v1.EnvFromSource{{
			ConfigMapRef: &v1.ConfigMapEnvSource{LocalObjectReference: v1.LocalObjectReference{Name: "site-cm"}},
		}}
		// the direct declaration is what the profile side must respect;
		// the envFrom alone defends nothing
		spec.Spec.Containers[0].Env = []v1.EnvVar{{Name: "IK_SITE", Value: "/opt/role"}}
		err := applyProfile(spec, []string{"engine"}, []workloadsv1alpha2.ContainerPatch{
			jp("/env/-", v1.EnvVar{Name: "IK_SITE", Value: "/opt/p"}),
		}, "p", nil)
		require.Error(t, err, "the direct env entry is a role-owned name; envFrom presence does not change that")
		assert.Contains(t, err.Error(), "may only default a role-owned name")
		assert.Contains(t, err.Error(), `profile "p"`)
	})

	t.Run("duplicate env name within one profile-side list errors", func(t *testing.T) {
		spec := basePodSpec()
		err := applyProfile(spec, []string{"engine"}, []workloadsv1alpha2.ContainerPatch{
			jp("/env/-", v1.EnvVar{Name: "IK_SITE", Value: "/opt/p"}),
			jp("/env/-", v1.EnvVar{Name: "IK_SITE", Value: "/other"}),
		}, "p", nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "duplicate env name")
		assert.Contains(t, err.Error(), "dead letter")
	})

	t.Run("duplicate env name within one role-side list errors", func(t *testing.T) {
		spec := basePodSpec()
		_, err := applyRole(ctx, spec, "engine", []workloadsv1alpha2.ContainerPatch{
			jp("/env/-", v1.EnvVar{Name: "IK_SITE", Value: "/opt/p"}),
			jp("/env/-", v1.EnvVar{Name: "IK_SITE", Value: "/other"}),
		}, "p", nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "duplicate env name")
		assert.Contains(t, err.Error(), `role "worker"`)
	})

	t.Run("role-side same-named append is the override gesture, never an error", func(t *testing.T) {
		spec := basePodSpec()
		spec.Spec.Containers[0].Env = []v1.EnvVar{{Name: "PYTHONPATH", Value: "/usr/lib/python"}}
		_, err := applyRole(ctx, spec, "engine", []workloadsv1alpha2.ContainerPatch{
			jp("/env/-", v1.EnvVar{Name: "PYTHONPATH", Value: "/opt/p"}),
		}, "p", nil)
		require.NoError(t, err)
		env := spec.Spec.Containers[0].Env
		require.Len(t, env, 2)
		// Role patches land last: the appended entry wins positionally.
		assert.Equal(t, "/opt/p", env[1].Value)
	})

	t.Run("RBG_ prefix is reserved on both sides", func(t *testing.T) {
		spec := basePodSpec()
		err := applyProfile(spec, []string{"engine"}, []workloadsv1alpha2.ContainerPatch{
			jp("/env/-", v1.EnvVar{Name: "RBG_FUTURE", Value: "x"}),
		}, "p", nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "RBG_ prefix is reserved")

		spec = basePodSpec()
		_, err = applyRole(ctx, spec, "engine", []workloadsv1alpha2.ContainerPatch{
			jp("/env/-", v1.EnvVar{Name: "RBG_FUTURE", Value: "x"}),
		}, "p", nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "RBG_ prefix is reserved")
	})
}

// TestEngineRuntimeCrossProfileEnvResolution pins the cross-profile
// positional rules (KEP-531 Patch Grammar): appends resolve last-wins by
// reference order; /env/0 defaults invert it — head-insert geometry puts the
// later reference's entry ahead of the earlier one, so the earlier
// reference's default wins.
func TestEngineRuntimeCrossProfileEnvResolution(t *testing.T) {
	ctx := log.IntoContext(context.Background(), klog.NewKlogr())

	t.Run("same profile twice: append gestures resolve last-wins by reference order", func(t *testing.T) {
		spec := basePodSpec()
		entries := []EngineRuntimePatch{
			{ProfileName: "p", Targets: []string{"engine"},
				Patches: []workloadsv1alpha2.ContainerPatch{jp("/env/-", v1.EnvVar{Name: "X", Value: "first"})}},
			{ProfileName: "p", Targets: []string{"engine"},
				Patches: []workloadsv1alpha2.ContainerPatch{jp("/env/-", v1.EnvVar{Name: "X", Value: "second"})}},
		}
		_, err := ApplyEngineRuntimePatches(ctx, spec, roleContainers("engine"), nil, "worker", entries, nil, nil, nil)
		require.NoError(t, err)
		env := spec.Spec.Containers[0].Env
		require.Len(t, env, 2)
		assert.Equal(t, "first", env[0].Value)
		assert.Equal(t, "second", env[1].Value) // the later reference lands later, so it wins
	})

	t.Run("two profiles: /env/0 defaults invert — the earlier reference wins", func(t *testing.T) {
		spec := basePodSpec()
		entries := []EngineRuntimePatch{
			{ProfileName: "accelerator", Targets: []string{"engine"},
				Patches: []workloadsv1alpha2.ContainerPatch{jp("/env/0", v1.EnvVar{Name: "X", Value: "accelerator"})}},
			{ProfileName: "telemetry", Targets: []string{"engine"},
				Patches: []workloadsv1alpha2.ContainerPatch{jp("/env/0", v1.EnvVar{Name: "X", Value: "telemetry"})}},
		}
		_, err := ApplyEngineRuntimePatches(ctx, spec, roleContainers("engine"), nil, "worker", entries, nil, nil, nil)
		require.NoError(t, err)
		env := spec.Spec.Containers[0].Env
		require.Len(t, env, 2)
		// [X_telemetry, X_accelerator]: the later reference's insert landed
		// ahead, so the last same-named entry — the winner — is the earlier
		// reference's.
		assert.Equal(t, "telemetry", env[0].Value)
		assert.Equal(t, "accelerator", env[1].Value)
	})
}

func TestApplyContainerPatchVolumeMounts(t *testing.T) {
	ctx := log.IntoContext(context.Background(), klog.NewKlogr())
	ikMount := v1.VolumeMount{Name: "role-vol", MountPath: "/opt/p", ReadOnly: true}

	profileMount := func(spec *v1.PodTemplateSpec, m v1.VolumeMount, path string) error {
		return applyProfile(spec, []string{"engine"}, []workloadsv1alpha2.ContainerPatch{jp(path, m)}, "p", nil)
	}

	t.Run("appends a mount referencing a role volume", func(t *testing.T) {
		spec := basePodSpec()
		require.NoError(t, profileMount(spec, ikMount, "/volumeMounts/-"))
		require.Len(t, spec.Spec.Containers[0].VolumeMounts, 1)
		assert.Equal(t, ikMount, spec.Spec.Containers[0].VolumeMounts[0])
	})

	t.Run("full-field equality is an idempotent no-op, absent equal to explicit zero", func(t *testing.T) {
		spec := basePodSpec()
		// The existing entry carries explicit readOnly: false; the patch
		// omits it. Key equal, all other fields equal — a no-op.
		spec.Spec.Containers[0].VolumeMounts = []v1.VolumeMount{
			{Name: "role-vol", MountPath: "/opt/p", ReadOnly: false},
		}
		require.NoError(t, profileMount(spec, v1.VolumeMount{Name: "role-vol", MountPath: "/opt/p"}, "/volumeMounts/-"))
		assert.Len(t, spec.Spec.Containers[0].VolumeMounts, 1)

		// The mirror case: existing omits readOnly, patch sets false — still equal.
		spec = basePodSpec()
		spec.Spec.Containers[0].VolumeMounts = []v1.VolumeMount{{Name: "role-vol", MountPath: "/opt/p"}}
		require.NoError(t, profileMount(spec, v1.VolumeMount{Name: "role-vol", MountPath: "/opt/p", ReadOnly: false}, "/volumeMounts/-"))
		assert.Len(t, spec.Spec.Containers[0].VolumeMounts, 1)
	})

	t.Run("partial match — key equal, readOnly differing — is a render error", func(t *testing.T) {
		spec := basePodSpec()
		spec.Spec.Containers[0].VolumeMounts = []v1.VolumeMount{
			{Name: "role-vol", MountPath: "/opt/p", ReadOnly: true},
		}
		err := profileMount(spec, v1.VolumeMount{Name: "role-vol", MountPath: "/opt/p", ReadOnly: false}, "/volumeMounts/-")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "differing fields")
		assert.Contains(t, err.Error(), `profile "p"`)
	})

	t.Run("partial match — key equal, subPath differing — is a render error", func(t *testing.T) {
		spec := basePodSpec()
		spec.Spec.Containers[0].VolumeMounts = []v1.VolumeMount{
			{Name: "role-vol", MountPath: "/opt/p"},
		}
		err := profileMount(spec, v1.VolumeMount{Name: "role-vol", MountPath: "/opt/p", SubPath: "sub"}, "/volumeMounts/-")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "differing fields")
	})

	t.Run("role side partial match is attributed to the role", func(t *testing.T) {
		spec := basePodSpec()
		spec.Spec.Containers[0].VolumeMounts = []v1.VolumeMount{
			{Name: "role-vol", MountPath: "/opt/p", ReadOnly: true},
		}
		_, err := applyRole(ctx, spec, "engine", []workloadsv1alpha2.ContainerPatch{
			jp("/volumeMounts/-", v1.VolumeMount{Name: "role-vol", MountPath: "/opt/p", ReadOnly: false}),
		}, "p", nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "differing fields")
		assert.Contains(t, err.Error(), `role "worker"`)
	})

	t.Run("same volume at a different mountPath is a legal second mount", func(t *testing.T) {
		spec := basePodSpec()
		spec.Spec.Containers[0].VolumeMounts = []v1.VolumeMount{{Name: "role-vol", MountPath: "/first"}}
		require.NoError(t, profileMount(spec, v1.VolumeMount{Name: "role-vol", MountPath: "/second"}, "/volumeMounts/-"))
		assert.Len(t, spec.Spec.Containers[0].VolumeMounts, 2)
	})

	t.Run("mount path already used by a different volume errors", func(t *testing.T) {
		spec := basePodSpec()
		// The conflicting volume must exist in the spec, otherwise the dangling
		// reference check fires before the mountPath conflict check.
		spec.Spec.Volumes = append(spec.Spec.Volumes,
			v1.Volume{Name: "other-vol", VolumeSource: v1.VolumeSource{EmptyDir: &v1.EmptyDirVolumeSource{}}})
		spec.Spec.Containers[0].VolumeMounts = []v1.VolumeMount{{Name: "role-vol", MountPath: "/opt/p"}}
		err := profileMount(spec, v1.VolumeMount{Name: "other-vol", MountPath: "/opt/p"}, "/volumeMounts/-")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "/opt/p")
		assert.Contains(t, err.Error(), "last-wins")
	})

	t.Run("duplicate volumeMount key within one patch list errors", func(t *testing.T) {
		spec := basePodSpec()
		err := applyProfile(spec, []string{"engine"}, []workloadsv1alpha2.ContainerPatch{
			jp("/volumeMounts/-", v1.VolumeMount{Name: "role-vol", MountPath: "/a"}),
			jp("/volumeMounts/-", v1.VolumeMount{Name: "role-vol", MountPath: "/a"}),
		}, "p", nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "duplicate volumeMount")
		assert.Contains(t, err.Error(), "dead letter")
	})

	t.Run("the rbg-cluster-config volume is reserved on both sides", func(t *testing.T) {
		spec := basePodSpec()
		spec.Spec.Volumes = append(spec.Spec.Volumes, v1.Volume{Name: "rbg-cluster-config",
			VolumeSource: v1.VolumeSource{ConfigMap: &v1.ConfigMapVolumeSource{}}})
		err := profileMount(spec, v1.VolumeMount{Name: "rbg-cluster-config", MountPath: "/etc/rbg"}, "/volumeMounts/-")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "rbg-cluster-config")
		assert.Contains(t, err.Error(), "reserved")

		spec = basePodSpec()
		spec.Spec.Volumes = append(spec.Spec.Volumes, v1.Volume{Name: "rbg-cluster-config",
			VolumeSource: v1.VolumeSource{ConfigMap: &v1.ConfigMapVolumeSource{}}})
		_, err = applyRole(ctx, spec, "engine", []workloadsv1alpha2.ContainerPatch{
			jp("/volumeMounts/-", v1.VolumeMount{Name: "rbg-cluster-config", MountPath: "/etc/rbg"}),
		}, "p", nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "reserved")
	})

	t.Run("dangling volume reference errors naming profile and volume", func(t *testing.T) {
		spec := basePodSpec()
		err := profileMount(spec, v1.VolumeMount{Name: "no-such-vol", MountPath: "/opt/p"}, "/volumeMounts/-")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "profile \"p\"")
		assert.Contains(t, err.Error(), "no-such-vol")
	})

	t.Run("head insert is available for mounts too", func(t *testing.T) {
		spec := basePodSpec()
		spec.Spec.Containers[0].VolumeMounts = []v1.VolumeMount{ikMount}
		require.NoError(t, profileMount(spec, v1.VolumeMount{Name: "role-vol", MountPath: "/first"}, "/volumeMounts/0"))
		assert.Equal(t, "/first", spec.Spec.Containers[0].VolumeMounts[0].MountPath)
	})

	t.Run("role side: mounting a role-declared volume into a profile-declared container is legal (D9)", func(t *testing.T) {
		spec := basePodSpec()
		spec.Spec.Containers = append(spec.Spec.Containers, v1.Container{Name: "runtime-sidecar", Image: "sidecar"})
		_, err := applyRole(ctx, spec, "runtime-sidecar", []workloadsv1alpha2.ContainerPatch{
			jp("/volumeMounts/-", v1.VolumeMount{Name: "role-vol", MountPath: "/cache", ReadOnly: true}),
		}, "p", roleContainers("runtime-sidecar"))
		require.NoError(t, err)
		require.Len(t, spec.Spec.Containers[1].VolumeMounts, 1)
		assert.Equal(t, "role-vol", spec.Spec.Containers[1].VolumeMounts[0].Name)
	})

	t.Run("role side: a secretKeyRef env value into a profile-declared container is legal (D9)", func(t *testing.T) {
		spec := basePodSpec()
		spec.Spec.Containers = append(spec.Spec.Containers, v1.Container{Name: "runtime-sidecar", Image: "sidecar"})
		_, err := applyRole(ctx, spec, "runtime-sidecar", []workloadsv1alpha2.ContainerPatch{
			jp("/env/-", v1.EnvVar{Name: "HF_TOKEN", ValueFrom: &v1.EnvVarSource{
				SecretKeyRef: &v1.SecretKeySelector{LocalObjectReference: v1.LocalObjectReference{Name: "role-secret"}, Key: "token"},
			}}),
		}, "p", roleContainers("runtime-sidecar"))
		require.NoError(t, err)
		require.Len(t, spec.Spec.Containers[1].Env, 1)
	})
}

func TestApplyContainerPatchCapabilities(t *testing.T) {
	ctx := log.IntoContext(context.Background(), klog.NewKlogr())

	cap := func(spec *v1.PodTemplateSpec, name string) (*record.FakeRecorder, error) {
		return applyRole(ctx, spec, "engine",
			[]workloadsv1alpha2.ContainerPatch{jp("/securityContext/capabilities/add/-", name)}, "p", nil)
	}

	t.Run("creates securityContext.capabilities when absent", func(t *testing.T) {
		spec := basePodSpec()
		recorder, err := cap(spec, "SYS_RESOURCE")
		require.NoError(t, err)
		assert.Empty(t, drainEvents(recorder))
		require.NotNil(t, spec.Spec.Containers[0].SecurityContext)
		assert.Equal(t, []v1.Capability{"SYS_RESOURCE"}, spec.Spec.Containers[0].SecurityContext.Capabilities.Add)
	})

	t.Run("appends to an existing add list", func(t *testing.T) {
		spec := basePodSpec()
		spec.Spec.Containers[0].SecurityContext = &v1.SecurityContext{Capabilities: &v1.Capabilities{
			Add: []v1.Capability{"SYS_PTRACE"},
		}}
		recorder, err := cap(spec, "SYS_RESOURCE")
		require.NoError(t, err)
		assert.Empty(t, drainEvents(recorder))
		assert.Equal(t, []v1.Capability{"SYS_PTRACE", "SYS_RESOURCE"},
			spec.Spec.Containers[0].SecurityContext.Capabilities.Add)
	})

	t.Run("names land verbatim — no normalization", func(t *testing.T) {
		spec := basePodSpec()
		recorder, err := cap(spec, "CAP_SYS_RESOURCE")
		require.NoError(t, err)
		require.Len(t, drainEvents(recorder), 1) // the form hazard warning, not an error
		assert.Equal(t, []v1.Capability{"CAP_SYS_RESOURCE"}, spec.Spec.Containers[0].SecurityContext.Capabilities.Add)

		spec = basePodSpec()
		recorder, err = cap(spec, "sys_resource")
		require.NoError(t, err)
		require.Len(t, drainEvents(recorder), 1)
		assert.Equal(t, []v1.Capability{"sys_resource"}, spec.Spec.Containers[0].SecurityContext.Capabilities.Add)
	})

	t.Run("duplicate is a verbatim no-op; a case variant is a second entry", func(t *testing.T) {
		spec := basePodSpec()
		_, err := cap(spec, "SYS_RESOURCE")
		require.NoError(t, err)
		_, err = cap(spec, "SYS_RESOURCE")
		require.NoError(t, err)
		assert.Equal(t, []v1.Capability{"SYS_RESOURCE"}, spec.Spec.Containers[0].SecurityContext.Capabilities.Add)

		// No normalization means the case variant is a distinct name —
		// containerd matches case-insensitively, CRI-O does not; the
		// renderer is no stricter than the target surface.
		_, err = cap(spec, "sys_resource")
		require.NoError(t, err)
		assert.Equal(t, []v1.Capability{"SYS_RESOURCE", "sys_resource"},
			spec.Spec.Containers[0].SecurityContext.Capabilities.Add)
	})

	t.Run("well-formed unknown names pass silently — only the runtime can judge", func(t *testing.T) {
		spec := basePodSpec()
		recorder, err := cap(spec, "NOT_A_CAPABILITY")
		require.NoError(t, err)
		assert.Empty(t, drainEvents(recorder))
		assert.Equal(t, []v1.Capability{"NOT_A_CAPABILITY"},
			spec.Spec.Containers[0].SecurityContext.Capabilities.Add)
	})

	t.Run("the form hazard warning is attributed to the role", func(t *testing.T) {
		spec := basePodSpec()
		recorder, err := cap(spec, "cap_sys_resource")
		require.NoError(t, err)
		events := drainEvents(recorder)
		require.Len(t, events, 1)
		assert.Contains(t, events[0], ReasonEngineRuntimeCapabilityFormHazard)
		assert.Contains(t, events[0], `role "worker"`)
		assert.Contains(t, events[0], "cap_sys_resource")
	})

	t.Run("the drop list is untouched when a capability is added", func(t *testing.T) {
		spec := basePodSpec()
		spec.Spec.Containers[0].SecurityContext = &v1.SecurityContext{Capabilities: &v1.Capabilities{
			Drop: []v1.Capability{"ALL"},
		}}
		_, err := cap(spec, "SYS_RESOURCE")
		require.NoError(t, err)
		assert.Equal(t, []v1.Capability{"ALL"}, spec.Spec.Containers[0].SecurityContext.Capabilities.Drop)
	})

	t.Run("non-string capability value errors", func(t *testing.T) {
		spec := basePodSpec()
		_, err := applyRole(ctx, spec, "engine",
			[]workloadsv1alpha2.ContainerPatch{jp("/securityContext/capabilities/add/-", map[string]string{"a": "b"})}, "p", nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "value must be a JSON string")
	})
}

// TestEngineRuntimeEnvCollision pins the EngineRuntimeEnvCollision warning
// event (KEP-531 Duplicate Delivery): a legal insert creating an env name a
// cross-source earlier entry already carries fires a warning naming the
// container, the env name and the winning side, never an error — kubelet
// resolves same-name entries by last occurrence. The legacy containers merge
// never passes through applyJSONPatch, so it is exempt by construction.
func TestEngineRuntimeEnvCollision(t *testing.T) {
	ctx := log.IntoContext(context.Background(), klog.NewKlogr())
	rbg := &workloadsv1alpha2.RoleBasedGroup{}

	profilePass := func(spec *v1.PodTemplateSpec, entries []EngineRuntimePatch, recorder record.EventRecorder) error {
		_, err := ApplyEngineRuntimePatches(ctx, spec, roleContainers("engine"), nil, "worker",
			entries, nil, recorder, rbg)
		return err
	}

	t.Run("a profile insert colliding with a role-template env warns", func(t *testing.T) {
		spec := basePodSpec()
		// PYTHONPATH rides the role template's engine container
		spec.Spec.Containers[0].Env = []v1.EnvVar{{Name: "PYTHONPATH", Value: "/opt/py"}}
		recorder := record.NewFakeRecorder(8)
		err := profilePass(spec, []EngineRuntimePatch{{
			ProfileName: "p", Targets: []string{"engine"},
			// /env/0 head insert — the sanctioned default gesture for a
			// role-owned name; a "-" append would be a render error instead
			Patches: []workloadsv1alpha2.ContainerPatch{jp("/env/0", v1.EnvVar{Name: "PYTHONPATH", Value: "/opt/ik"})},
		}}, recorder)
		require.NoError(t, err, "the collision is a warning, never a failure")
		events := drainEvents(recorder)
		require.Len(t, events, 1)
		assert.Contains(t, events[0], ReasonEngineRuntimeEnvCollision)
		assert.Contains(t, events[0], `"PYTHONPATH"`)
	})

	t.Run("a profile insert colliding with another profile's insert of this render warns", func(t *testing.T) {
		spec := basePodSpec()
		recorder := record.NewFakeRecorder(8)
		err := profilePass(spec, []EngineRuntimePatch{
			{ProfileName: "a", Targets: []string{"engine"},
				Patches: []workloadsv1alpha2.ContainerPatch{jp("/env/-", v1.EnvVar{Name: "X", Value: "a"})}},
			{ProfileName: "b", Targets: []string{"engine"},
				Patches: []workloadsv1alpha2.ContainerPatch{jp("/env/-", v1.EnvVar{Name: "X", Value: "b"})}},
		}, recorder)
		require.NoError(t, err)
		events := drainEvents(recorder)
		require.Len(t, events, 1, "only the colliding second insert warns; the first creates the name")
		assert.Contains(t, events[0], ReasonEngineRuntimeEnvCollision)
		assert.Contains(t, events[0], "profile-side insert of this render")
	})

	t.Run("a fresh profile insert creating no existing name stays silent", func(t *testing.T) {
		spec := basePodSpec()
		recorder := record.NewFakeRecorder(8)
		err := profilePass(spec, []EngineRuntimePatch{{
			ProfileName: "p", Targets: []string{"engine"},
			Patches: []workloadsv1alpha2.ContainerPatch{jp("/env/-", v1.EnvVar{Name: "IK_SITE", Value: "/opt/ik"})},
		}}, recorder)
		require.NoError(t, err)
		assert.Empty(t, drainEvents(recorder))
	})

	t.Run("envFrom-carried names stay outside the collision warning (M5)", func(t *testing.T) {
		spec := basePodSpec()
		// the same name rides a configMap envFrom — direct env wins by
		// Kubernetes' own priority, so the render stays silent and the
		// priority question is left to kubelet, not re-litigated here
		spec.Spec.Containers[0].EnvFrom = []v1.EnvFromSource{{
			ConfigMapRef: &v1.ConfigMapEnvSource{LocalObjectReference: v1.LocalObjectReference{Name: "site-cm"}},
		}}
		recorder := record.NewFakeRecorder(8)
		err := profilePass(spec, []EngineRuntimePatch{{
			ProfileName: "p", Targets: []string{"engine"},
			Patches: []workloadsv1alpha2.ContainerPatch{jp("/env/-", v1.EnvVar{Name: "IK_SITE", Value: "/opt/ik"})},
		}}, recorder)
		require.NoError(t, err)
		assert.Empty(t, drainEvents(recorder),
			"the collision rules read direct env only — envFrom names are not tracked")
	})

	t.Run("a role-side extraPatches insert colliding with the role template warns", func(t *testing.T) {
		spec := basePodSpec()
		spec.Spec.Containers[0].Env = []v1.EnvVar{{Name: "PYTHONPATH", Value: "/opt/py"}}
		recorder := record.NewFakeRecorder(8)
		entry := EngineRuntimeRolePatch{
			ProfileName:        "p",
			DeclaredContainers: map[string]struct{}{},
			Targets: []workloadsv1alpha2.EngineRuntimeTargetContainer{{
				Name: "engine",
				ExtraPatches: []workloadsv1alpha2.ContainerPatch{
					jp("/env/-", v1.EnvVar{Name: "PYTHONPATH", Value: "/opt/override"}),
				},
			}},
		}
		_, err := ApplyRoleEngineRuntimePatches(ctx, spec, roleContainers("engine"), nil,
			[]EngineRuntimeRolePatch{entry}, "worker", nil, recorder, rbg)
		require.NoError(t, err, "the role-side override gesture is legal; the warning names the collision")
		events := drainEvents(recorder)
		require.Len(t, events, 1)
		assert.Contains(t, events[0], ReasonEngineRuntimeEnvCollision)
		assert.Contains(t, events[0], `"PYTHONPATH"`)
	})

	t.Run("the legacy containers merge is exempt — it never rides applyJSONPatch", func(t *testing.T) {
		spec := basePodSpec()
		recorder := record.NewFakeRecorder(8)
		profile := &workloadsv1alpha2.ClusterEngineRuntimeProfile{
			ObjectMeta: metav1.ObjectMeta{Name: "legacy"},
			Spec: workloadsv1alpha2.ClusterEngineRuntimeProfileSpec{
				Containers: []v1.Container{{
					Name:  "engine",
					Image: "legacy:latest",
					Env:   []v1.EnvVar{{Name: "PYTHONPATH", Value: "/opt/legacy"}},
				}},
			},
		}
		err := InjectEngineRuntimeFragments(ctx, spec, []EngineRuntimeFragment{{
			Profile: profile,
			Runtime: workloadsv1alpha2.EngineRuntime{ProfileName: "legacy"},
		}}, "worker", recorder, rbg)
		require.NoError(t, err)
		events := drainEvents(recorder)
		assert.Empty(t, events,
			"the legacy merge is main's behavior verbatim and never passes the collision check")
	})
}

// TestEngineRuntimeUnaimedRenderHalf pins the render-side half of the
// unaimed-patch warning (KEP-531 Per-Dimension Gates): a patch-carrying
// profile covered by a reference whose targetContainers name only
// profile-declared containers fires the warning once per reference, even
// across multiple rendered templates sharing one plan.
func TestEngineRuntimeUnaimedRenderHalf(t *testing.T) {
	ctx := log.IntoContext(context.Background(), klog.NewKlogr())
	rbg := &workloadsv1alpha2.RoleBasedGroup{}

	entries := []EngineRuntimePatch{{
		ProfileName:        "ik",
		Targets:            []string{"ik-sidecar"},
		Patches:            []workloadsv1alpha2.ContainerPatch{jp("/env/-", v1.EnvVar{Name: "IK_SITE", Value: "/opt/ik/site"})},
		DeclaredContainers: map[string]struct{}{"ik-sidecar": {}},
	}}

	t.Run("entries naming only profile-declared containers warn once per reference", func(t *testing.T) {
		plan := &EngineRuntimePlan{}
		recorder := record.NewFakeRecorder(8)
		// two rendered templates share one plan — the dedupe set rides it
		for i := 0; i < 2; i++ {
			spec := basePodSpec()
			spec.Spec.Containers = append(spec.Spec.Containers, v1.Container{Name: "ik-sidecar", Image: "ik:latest"})
			require.NoError(t, func() error {
				_, err := ApplyEngineRuntimePatches(ctx, spec, roleContainers("engine"), nil, "worker", entries, plan, recorder, rbg)
				return err
			}())
		}
		events := drainEvents(recorder)
		require.Len(t, events, 1, "the warning is deduped per reference across templates")
		assert.Contains(t, events[0], ReasonUnaimedContainerPatch)
		assert.Contains(t, events[0], `profile "ik" declares containerPatches`)
	})

	t.Run("a role-declared consent address in the mix silences the warning", func(t *testing.T) {
		plan := &EngineRuntimePlan{}
		recorder := record.NewFakeRecorder(8)
		mixed := []EngineRuntimePatch{{
			ProfileName:        "ik",
			Targets:            []string{"engine", "ik-sidecar"},
			Patches:            entries[0].Patches,
			DeclaredContainers: entries[0].DeclaredContainers,
		}}
		spec := basePodSpec()
		spec.Spec.Containers = append(spec.Spec.Containers, v1.Container{Name: "ik-sidecar", Image: "ik:latest"})
		_, err := ApplyEngineRuntimePatches(ctx, spec, roleContainers("engine"), nil,
			"worker", mixed, plan, recorder, rbg)
		require.NoError(t, err)
		assert.Empty(t, drainEvents(recorder))
	})

	t.Run("a nil plan skips the render-half warning (plan-less callers)", func(t *testing.T) {
		recorder := record.NewFakeRecorder(8)
		spec := basePodSpec()
		spec.Spec.Containers = append(spec.Spec.Containers, v1.Container{Name: "ik-sidecar", Image: "ik:latest"})
		_, err := ApplyEngineRuntimePatches(ctx, spec, roleContainers("engine"), nil,
			"worker", entries, nil, recorder, rbg)
		require.NoError(t, err)
		assert.Empty(t, drainEvents(recorder))
	})
}
