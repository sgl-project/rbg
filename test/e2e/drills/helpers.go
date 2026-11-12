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

package drills

import (
	"fmt"

	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"sigs.k8s.io/rbgs/api/workloads/constants"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	"sigs.k8s.io/rbgs/test/e2e/framework"
	"sigs.k8s.io/rbgs/test/utils"
	wrappersv2 "sigs.k8s.io/rbgs/test/wrappers/v1alpha2"
)

// cerpObject is an empty handle for a ClusterEngineRuntimeProfile, for Get and
// Delete calls that only need the name.
func cerpObject(name string) *workloadsv1alpha2.ClusterEngineRuntimeProfile {
	return &workloadsv1alpha2.ClusterEngineRuntimeProfile{
		ObjectMeta: metav1.ObjectMeta{Name: name},
	}
}

// buildProfile builds a ClusterEngineRuntimeProfile with one sidecar container
// and the given profile-side container patches. The sidecar is the fragment
// the render delivers in every compat assertion, and the patches are the
// dimension that only fires at role-declared targetContainers entries.
//
// updateStrategy is set explicitly because the field carries no omitempty and
// the apiserver rejects the empty string against the enum.
func buildProfile(
	name, sidecar string, patches ...workloadsv1alpha2.ContainerPatch,
) *workloadsv1alpha2.ClusterEngineRuntimeProfile {
	return &workloadsv1alpha2.ClusterEngineRuntimeProfile{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: workloadsv1alpha2.ClusterEngineRuntimeProfileSpec{
			UpdateStrategy: workloadsv1alpha2.NoUpdateStrategy,
			Containers: []corev1.Container{{
				Name:            sidecar,
				Image:           utils.DefaultImage,
				ImagePullPolicy: corev1.PullIfNotPresent,
			}},
			ContainerPatches: patches,
		},
	}
}

// envPatch builds one `add` /env/- entry, the shape both patch sides use for
// an observable render effect. jsonBytes marshals the value once, mirroring
// how the apiserver delivers authored YAML inside RawExtension.
func envPatch(env corev1.EnvVar) workloadsv1alpha2.ContainerPatch {
	return workloadsv1alpha2.ContainerPatch{
		JSONPatch: &workloadsv1alpha2.ContainerJSONPatch{
			Op:    "add",
			Path:  "/env/-",
			Value: runtime.RawExtension{Raw: jsonBytes(env)},
		},
	}
}

// lwpRbg builds a RoleBasedGroup with one leaderWorkerPattern role carrying
// the given engine runtime references. BuildLeaderWorkerRole declares an
// `nginx` container in the shared base template — the role-declared container
// the targetContainers fixtures address — and the leader/worker components
// carry the pattern's own names.
func lwpRbg(namespace string, name string, runtimes ...workloadsv1alpha2.EngineRuntime) *workloadsv1alpha2.RoleBasedGroup {
	role := wrappersv2.BuildLeaderWorkerRole(lwpRoleName).Obj()
	role.EngineRuntimes = runtimes
	return wrappersv2.BuildBasicRoleBasedGroup(name, namespace).
		WithRoles([]workloadsv1alpha2.RoleSpec{role}).
		Obj()
}

// standaloneRbg builds a RoleBasedGroup with one standalonePattern role
// carrying the given engine runtime references. The role's name doubles as
// the rendered component's name (KEP-173), which is what the standalone
// fixtures' component lookups key on.
func standaloneRbg(
	namespace, name, roleName string, runtimes ...workloadsv1alpha2.EngineRuntime,
) *workloadsv1alpha2.RoleBasedGroup {
	role := wrappersv2.BuildStandaloneRole(roleName).Obj()
	role.EngineRuntimes = runtimes
	return wrappersv2.BuildBasicRoleBasedGroup(name, namespace).
		WithRoles([]workloadsv1alpha2.RoleSpec{role}).
		Obj()
}

// getRBG reads one fixture RoleBasedGroup from the suite namespace. It
// asserts, so it is only for call sites outside gomega.Eventually bodies,
// where a missing object should abort the spec rather than be retried.
func getRBG(f *framework.Framework, name string) *workloadsv1alpha2.RoleBasedGroup {
	rbg, err := tryGetRBG(f, name)
	gomega.Expect(err).ToNot(gomega.HaveOccurred(), "could not read RoleBasedGroup %s: %v", name, err)
	return rbg
}

func tryGetRBG(f *framework.Framework, name string) (*workloadsv1alpha2.RoleBasedGroup, error) {
	rbg := &workloadsv1alpha2.RoleBasedGroup{}
	if err := f.Client.Get(f.Ctx, client.ObjectKey{
		Namespace: f.Namespace, Name: name,
	}, rbg); err != nil {
		return nil, err
	}
	return rbg, nil
}

// updateFirstEngineRuntime rewrites the first engine runtime of the fixture's
// first role through the mutate callback and updates the object. It returns
// the apiserver's error rather than asserting, because the drills read both
// admissions and rejections off the same call.
func updateFirstEngineRuntime(
	f *framework.Framework, name string, mutate func(*workloadsv1alpha2.EngineRuntime),
) error {
	rbg := getRBG(f, name)
	if len(rbg.Spec.Roles) == 0 || len(rbg.Spec.Roles[0].EngineRuntimes) == 0 {
		return fmt.Errorf("fixture %s has no first engine runtime to update; the spec sequence is wrong", name)
	}
	mutate(&rbg.Spec.Roles[0].EngineRuntimes[0])
	return f.Client.Update(f.Ctx, rbg)
}

// appendEngineRuntime appends one engine runtime to the fixture's first role
// and updates the object, returning the apiserver's error for the same reason
// as updateFirstEngineRuntime.
func appendEngineRuntime(
	f *framework.Framework, name string, er workloadsv1alpha2.EngineRuntime,
) error {
	rbg := getRBG(f, name)
	if len(rbg.Spec.Roles) == 0 {
		return fmt.Errorf("fixture %s has no role to append an engine runtime to; the spec sequence is wrong", name)
	}
	rbg.Spec.Roles[0].EngineRuntimes = append(rbg.Spec.Roles[0].EngineRuntimes, er)
	return f.Client.Update(f.Ctx, rbg)
}

// updateProfileSpec mutates a profile's spec through the callback and updates
// the object. The error is returned, not asserted: the frozen-profile drill
// calls this both to prove a rejection (enforcement on) and an admission
// (enforcement off), off the exact same write.
func updateProfileSpec(
	f *framework.Framework, name string, mutate func(*workloadsv1alpha2.ClusterEngineRuntimeProfileSpec),
) error {
	profile := &workloadsv1alpha2.ClusterEngineRuntimeProfile{}
	gomega.Expect(f.Client.Get(f.Ctx, client.ObjectKey{Name: name}, profile)).To(gomega.Succeed())
	mutate(&profile.Spec)
	return f.Client.Update(f.Ctx, profile)
}

// storedEngineRuntime reads one fixture's first role's engine runtime as raw
// storage, unstructured, so the assertions see exactly what pruning left
// behind — a typed read cannot observe fields that are absent.
func storedEngineRuntime(f *framework.Framework, name string, idx int) (map[string]any, error) {
	runtimes, err := storedEngineRuntimes(f, name)
	if err != nil {
		return nil, err
	}
	if idx >= len(runtimes) {
		return nil, fmt.Errorf("fixture %s has fewer engine runtimes than the assertion reads", name)
	}
	return runtimes[idx], nil
}

func storedEngineRuntimes(f *framework.Framework, name string) ([]map[string]any, error) {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(schema.GroupVersionKind{
		Group: "workloads.x-k8s.io", Version: "v1alpha2", Kind: "RoleBasedGroup",
	})
	if err := f.Client.Get(f.Ctx, client.ObjectKey{
		Namespace: f.Namespace, Name: name,
	}, u); err != nil {
		return nil, err
	}
	roles, found, err := unstructured.NestedSlice(u.Object, "spec", "roles")
	if err != nil {
		return nil, err
	}
	if !found || len(roles) == 0 {
		return nil, fmt.Errorf("fixture %s stores no roles", name)
	}
	role, _ := roles[0].(map[string]any)
	runtimes, found, err := unstructured.NestedSlice(role, "engineRuntimes")
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, fmt.Errorf("fixture %s stores no engine runtimes", name)
	}
	out := make([]map[string]any, 0, len(runtimes))
	for _, rt := range runtimes {
		m, _ := rt.(map[string]any)
		out = append(out, m)
	}
	return out, nil
}

// waitRoleReady waits until every role of the fixture reports ready through
// the role status the controller maintains. The drill fixtures carry a single
// role each; the loop keeps the helper honest about where readiness is read.
func waitRoleReady(f *framework.Framework, name string) {
	gomega.Eventually(func(g gomega.Gomega) {
		rbg, err := tryGetRBG(f, name)
		g.Expect(err).ToNot(gomega.HaveOccurred())
		for i := range rbg.Spec.Roles {
			role := &rbg.Spec.Roles[i]
			status, found := rbg.GetRoleStatus(role.Name)
			g.Expect(found).To(gomega.BeTrue(), "role %s of %s has no status yet", role.Name, name)
			g.Expect(status.ReadyReplicas).To(gomega.Equal(*role.Replicas),
				"role %s of %s is not ready", role.Name, name)
		}
	}, renderTimeout, renderInterval).Should(gomega.Succeed(), "%s never became ready", name)
}

// tryRoleInstanceSet reads the fixture's rendered child, returning the error
// so gomega.Eventually bodies can retry on it instead of aborting.
func tryRoleInstanceSet(f *framework.Framework, rbgName string) (*workloadsv1alpha2.RoleInstanceSet, error) {
	rbg := getRBG(f, rbgName)
	if len(rbg.Spec.Roles) != 1 {
		return nil, fmt.Errorf("the drill fixtures carry exactly one role; %s carries %d", rbgName, len(rbg.Spec.Roles))
	}
	ris := &workloadsv1alpha2.RoleInstanceSet{}
	key := client.ObjectKey{
		Namespace: f.Namespace, Name: rbg.GetWorkloadName(&rbg.Spec.Roles[0]),
	}
	if err := f.Client.Get(f.Ctx, key, ris); err != nil {
		return nil, err
	}
	return ris, nil
}

// roleInstanceSet reads the fixture's rendered child and asserts existence.
// For use outside gomega.Eventually bodies; inside one, use
// tryComponentTemplate.
func roleInstanceSet(f *framework.Framework, rbgName string) *workloadsv1alpha2.RoleInstanceSet {
	ris, err := tryRoleInstanceSet(f, rbgName)
	gomega.Expect(err).ToNot(gomega.HaveOccurred(),
		"could not read the RoleInstanceSet of %s: %v", rbgName, err)
	return ris
}

// waitRoleRendered waits until the fixture's rendered child exists.
func waitRoleRendered(f *framework.Framework, name string) *workloadsv1alpha2.RoleInstanceSet {
	var ris *workloadsv1alpha2.RoleInstanceSet
	gomega.Eventually(func(g gomega.Gomega) {
		var err error
		ris, err = tryRoleInstanceSet(f, name)
		g.Expect(err).ToNot(gomega.HaveOccurred())
	}, renderTimeout, renderInterval).Should(gomega.Succeed(), "%s was never rendered", name)
	return ris
}

// componentTemplate returns the named component's rendered pod template from
// the RoleInstanceSet, or nil when the component is not rendered.
func componentTemplate(
	ris *workloadsv1alpha2.RoleInstanceSet, component string,
) *corev1.PodTemplateSpec {
	for i := range ris.Spec.RoleInstanceTemplate.Components {
		c := &ris.Spec.RoleInstanceTemplate.Components[i]
		if c.Name == component {
			return &c.Template
		}
	}
	return nil
}

// tryComponentTemplate is the Eventually-safe read: it returns an error while
// the child or the component is not there yet, instead of failing the spec on
// the first poll.
func tryComponentTemplate(
	f *framework.Framework, rbgName, component string,
) (*corev1.PodTemplateSpec, error) {
	ris, err := tryRoleInstanceSet(f, rbgName)
	if err != nil {
		return nil, err
	}
	tpl := componentTemplate(ris, component)
	if tpl == nil {
		return nil, fmt.Errorf("%s has no %s component rendered yet", rbgName, component)
	}
	return tpl, nil
}

func findContainer(tpl *corev1.PodTemplateSpec, name string) *corev1.Container {
	if tpl == nil {
		return nil
	}
	for i := range tpl.Spec.Containers {
		if tpl.Spec.Containers[i].Name == name {
			return &tpl.Spec.Containers[i]
		}
	}
	return nil
}

func hasContainer(tpl *corev1.PodTemplateSpec, name string) bool {
	return findContainer(tpl, name) != nil
}

func envValue(c *corev1.Container, name string) (string, bool) {
	if c == nil {
		return "", false
	}
	for _, ev := range c.Env {
		if ev.Name == name {
			return ev.Value, true
		}
	}
	return "", false
}

// sampleLWPRenders reads both leader-worker fixtures' rendered leader and
// worker templates, keyed <rbg>/<component>: the byte-for-byte phases compare
// two of these maps against each other and against the pre-upgrade render.
func sampleLWPRenders(f *framework.Framework) map[string]corev1.PodTemplateSpec {
	out := map[string]corev1.PodTemplateSpec{}
	for _, name := range []string{fxPruned, fxStrippedWorker} {
		ris := roleInstanceSet(f, name)
		for _, component := range []string{
			string(constants.LeaderComponentType), string(constants.WorkerComponentType),
		} {
			tpl := componentTemplate(ris, component)
			gomega.Expect(tpl).ToNot(gomega.BeNil(), "%s has no %s component", name, component)
			out[name+"/"+component] = *tpl.DeepCopy()
		}
	}
	return out
}

// engineRuntimeEvent reports whether a warning event with the given reason was
// recorded for the fixture, and its message. Events repeat on every reconcile
// by design, so presence — never a count — is the assertion surface.
func engineRuntimeEvent(f *framework.Framework, objName, reason string) (bool, string) {
	events := &corev1.EventList{}
	if err := f.Client.List(f.Ctx, events, client.InNamespace(f.Namespace)); err != nil {
		return false, ""
	}
	for i := range events.Items {
		ev := &events.Items[i]
		if ev.InvolvedObject.Name == objName && ev.Reason == reason {
			return true, ev.Message
		}
	}
	return false, ""
}
