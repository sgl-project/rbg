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

package upgrade

import (
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	lwsv1 "sigs.k8s.io/lws/api/leaderworkerset/v1"

	"sigs.k8s.io/rbgs/api/workloads/constants"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	"sigs.k8s.io/rbgs/test/e2e/framework"
)

// PodFacts is everything about a pod that must survive an upgrade untouched.
//
// resourceVersion is deliberately absent: any status write bumps it, and the new
// controller legitimately rewrites status. It appears only in the debug dump.
type PodFacts struct {
	UID               types.UID
	CreationTimestamp metav1.Time
	NodeName          string
	// RestartCounts is per container name. Compared for equality, not >=: a
	// container restarted in place keeps the pod identity, so this is the only
	// signal that catches it.
	RestartCounts map[string]int32
	// OwnerUIDs is the sorted ownerReferences of the pod. A pod re-parented onto
	// another owner keeps every field above, so this is the only place it shows.
	OwnerUIDs []types.UID
	// Phase is compared, not required to be Running: one fixture is deliberately
	// Pending, and the question is whether the upgrade moved a pod out of its phase.
	Phase corev1.PodPhase
	// Labels and Annotations are compared in full because the drifts this suite has
	// actually found did not move any of the fields above. An instance whose pod
	// template carried another ordinal's identity labels kept its pod UID, its node
	// and a restart count of zero: the labels were the only place it showed.
	//
	// Compared, not validated. Whether a label holds the right value is a question
	// for the correctness suites; the only question here is whether the upgrade
	// changed it.
	Labels      map[string]string
	Annotations map[string]string
	// ReadinessGates is the sorted condition types of spec.readinessGates. The
	// in-place update path injects a gate, so an upgrade that quietly triggers an
	// in-place update shows up here first -- again with the pod identity intact.
	ReadinessGates []string
	// ResourceVersion is recorded for the debug dump only, never asserted on.
	ResourceVersion string
}

// ownerFacts tracks the object that owns a role's pods. Generation matters as much
// as UID: a spec-level rewrite bumps generation before any pod is touched, which
// makes it the earliest visible symptom of a revision hash change.
type ownerFacts struct {
	UID        types.UID
	Generation int64
	// Spec is the object's whole spec as stored, normalized through a JSON round
	// trip. A generation bump says the spec was rewritten; keeping the spec itself
	// is what lets the report show what the rewrite changed.
	Spec map[string]any
}

// serviceFacts is one Service of an RBG, plus what is behind it.
//
// Endpoints is the field that matters most and the only one in this suite that speaks
// about traffic rather than about objects: a Service whose own spec is untouched still
// stops serving if its selector no longer matches the pods, and a selector change and a
// pod label change are two ways to arrive there. Recorded from the Service's
// EndpointSlices, which is where the apiserver publishes the result of that match.
type serviceFacts struct {
	UID       types.UID
	ClusterIP string
	// Ports is rendered as sorted strings because a reordered port list is not a
	// change and comparing the structs would report it as one.
	Ports     []string
	Selector  map[string]string
	Endpoints []string
}

// The two kinds a snapshot can describe. Spelled out because controller-runtime
// clears TypeMeta on typed results, so the kind cannot be read off the object.
const (
	kindRBG    = "RoleBasedGroup"
	kindRBGSet = "RoleBasedGroupSet"
)

// RBGSnapshot is one RoleBasedGroup's observable state at a point in time, or -- with
// only Kind, Name, RBGUID and Generation set -- one RoleBasedGroupSet root's.
//
// The set root shares this type rather than getting its own so that every detector
// covers it for free. The ones that need pods, owners or Services find empty maps and
// say nothing, while identity and generation are compared exactly as for an RBG.
type RBGSnapshot struct {
	// Kind is "RoleBasedGroup" or "RoleBasedGroupSet". It is what the generation-bump
	// lookup and the failure messages are keyed off, so the two are never conflated.
	Kind       string
	Name       string
	RBGUID     types.UID
	Generation int64
	// Roles maps role name -> pod name -> facts.
	Roles map[string]map[string]PodFacts
	// Owners maps "Kind/name" -> facts, over every workload object the RBG owns.
	Owners map[string]ownerFacts
	// Services maps service name -> facts, over every Service the RBG owns.
	Services map[string]serviceFacts
	// RevisionNames are the sorted ControllerRevision names for this RBG. A new
	// name appearing is the fingerprint of a changed revision hash.
	RevisionNames []string
	// Spec is the root object's whole spec as stored, kept for the same reason as
	// ownerFacts.Spec: a generation bump on the root is reported with the diff.
	Spec        map[string]any
	ReadyByRole map[string]int32
	// RBGReady is the RBG's Ready condition being True.
	RBGReady bool
}

// ownerSource pairs a list type with its kind name. The kind is spelled out because
// controller-runtime clears TypeMeta on typed list results, so it cannot be read
// back off the items.
type ownerSource struct {
	kind string
	list func() client.ObjectList
}

// ownerSources are the kinds an RBG owns. Mostly the workload objects behind its
// roles, plus the scaling adapter: that one is not a workload, but it is created,
// relabelled and spec-patched by the same controller, and it is the object an
// autoscaler holds on to. All of them label their objects with GroupNameLabelKey, on
// v0.7.0 as well as now, so one label query per kind covers every pattern the fixtures
// use.
func ownerSources() []ownerSource {
	return []ownerSource{
		{"RoleInstanceSet", func() client.ObjectList { return &workloadsv1alpha2.RoleInstanceSetList{} }},
		{"RoleInstance", func() client.ObjectList { return &workloadsv1alpha2.RoleInstanceList{} }},
		{"Deployment", func() client.ObjectList { return &appsv1.DeploymentList{} }},
		{"StatefulSet", func() client.ObjectList { return &appsv1.StatefulSetList{} }},
		{"LeaderWorkerSet", func() client.ObjectList { return &lwsv1.LeaderWorkerSetList{} }},
		{"RoleBasedGroupScalingAdapter", func() client.ObjectList {
			return &workloadsv1alpha2.RoleBasedGroupScalingAdapterList{}
		}},
	}
}

// captureAll snapshots every RoleBasedGroup and every RoleBasedGroupSet in the test
// namespace, keyed by object name.
//
// It lists rather than taking a fixture list so that RBGs created indirectly are
// covered too: the children of the RoleBasedGroupSet fixture, and the RBG created
// through v1alpha1. Anything appearing or disappearing across the upgrade is itself
// churn, and comparing the key sets catches it.
//
// The set roots are captured because a child RBG can be intact while the object that
// owns it was recreated or had its spec rewritten -- and the set is what would then
// restamp the children. Names cannot collide: a set stamps its children out as
// <set>-<ordinal>, so "up-set" and "up-set-0" are distinct keys.
//
// g carries the assertion target. Callers inside an Eventually body must pass the
// injected gomega.Gomega so a transient List error is retried rather than failing
// the spec outright; callers outside one pass gomega.Default.
func captureAll(g gomega.Gomega, f *framework.Framework) map[string]RBGSnapshot {
	rbgList := &workloadsv1alpha2.RoleBasedGroupList{}
	g.Expect(f.Client.List(f.Ctx, rbgList, client.InNamespace(f.Namespace))).To(gomega.Succeed())
	setList := &workloadsv1alpha2.RoleBasedGroupSetList{}
	g.Expect(f.Client.List(f.Ctx, setList, client.InNamespace(f.Namespace))).To(gomega.Succeed())

	out := make(map[string]RBGSnapshot, len(rbgList.Items)+len(setList.Items))
	for i := range rbgList.Items {
		rbg := &rbgList.Items[i]
		out[rbg.Name] = captureRBG(g, f, rbg)
	}
	for i := range setList.Items {
		set := &setList.Items[i]
		out[set.Name] = RBGSnapshot{
			Kind:       kindRBGSet,
			Name:       set.Name,
			RBGUID:     set.UID,
			Generation: set.Generation,
			Spec:       specJSONMap(set.Spec),
		}
	}
	return out
}

func captureRBG(g gomega.Gomega, f *framework.Framework, rbg *workloadsv1alpha2.RoleBasedGroup) RBGSnapshot {
	snap := RBGSnapshot{
		Kind:          kindRBG,
		Name:          rbg.Name,
		RBGUID:        rbg.UID,
		Generation:    rbg.Generation,
		Spec:          specJSONMap(rbg.Spec),
		Roles:         make(map[string]map[string]PodFacts, len(rbg.Spec.Roles)),
		Owners:        listOwnersForRBG(g, f, rbg),
		Services:      listServicesForRBG(g, f, rbg),
		RevisionNames: listRevisionsForRBG(g, f, rbg),
		ReadyByRole:   make(map[string]int32, len(rbg.Spec.Roles)),
	}

	for _, role := range rbg.Spec.Roles {
		snap.Roles[role.Name] = listPodFactsForRole(g, f, rbg, role.Name)
	}
	for _, rs := range rbg.Status.RoleStatuses {
		snap.ReadyByRole[rs.Name] = rs.ReadyReplicas
	}
	for _, cond := range rbg.Status.Conditions {
		if cond.Type == string(workloadsv1alpha2.RoleBasedGroupReady) {
			snap.RBGReady = cond.Status == metav1.ConditionTrue
		}
	}
	return snap
}

// listPodFactsForRole returns facts for the live pods of one role. Terminating pods
// are skipped: they are already on their way out and would make the comparison
// depend on GC timing rather than on the controller's behavior.
func listPodFactsForRole(
	g gomega.Gomega,
	f *framework.Framework,
	rbg *workloadsv1alpha2.RoleBasedGroup,
	roleName string,
) map[string]PodFacts {
	podList := &corev1.PodList{}
	g.Expect(f.Client.List(f.Ctx, podList,
		client.InNamespace(rbg.Namespace),
		client.MatchingLabels{
			constants.GroupNameLabelKey: rbg.Name,
			constants.RoleNameLabelKey:  roleName,
		},
	)).To(gomega.Succeed())

	out := make(map[string]PodFacts, len(podList.Items))
	for i := range podList.Items {
		pod := &podList.Items[i]
		if pod.DeletionTimestamp != nil {
			continue
		}

		restarts := make(map[string]int32, len(pod.Status.ContainerStatuses))
		for _, cs := range pod.Status.ContainerStatuses {
			restarts[cs.Name] = cs.RestartCount
		}

		owners := make([]types.UID, 0, len(pod.OwnerReferences))
		for _, ref := range pod.OwnerReferences {
			owners = append(owners, ref.UID)
		}
		sort.Slice(owners, func(a, b int) bool { return owners[a] < owners[b] })

		gates := make([]string, 0, len(pod.Spec.ReadinessGates))
		for _, gate := range pod.Spec.ReadinessGates {
			gates = append(gates, string(gate.ConditionType))
		}
		sort.Strings(gates)

		out[pod.Name] = PodFacts{
			UID:               pod.UID,
			CreationTimestamp: pod.CreationTimestamp,
			NodeName:          pod.Spec.NodeName,
			RestartCounts:     restarts,
			OwnerUIDs:         owners,
			Phase:             pod.Status.Phase,
			Labels:            copyStringMap(pod.Labels),
			Annotations:       copyStringMap(pod.Annotations),
			ReadinessGates:    gates,
			ResourceVersion:   pod.ResourceVersion,
		}
	}
	return out
}

// copyStringMap detaches a map from the object it was read off, so that the snapshot
// keeps what was observed even though the caller reuses the list buffer.
func copyStringMap(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func listOwnersForRBG(
	g gomega.Gomega,
	f *framework.Framework,
	rbg *workloadsv1alpha2.RoleBasedGroup,
) map[string]ownerFacts {
	out := map[string]ownerFacts{}
	for _, source := range ownerSources() {
		list := source.list()
		err := f.Client.List(f.Ctx, list,
			client.InNamespace(rbg.Namespace),
			client.MatchingLabels{constants.GroupNameLabelKey: rbg.Name},
		)
		if apimeta.IsNoMatchError(err) {
			// An unregistered kind (LeaderWorkerSet on a cluster without the CRD)
			// must not abort the snapshot; the fixtures that need it will fail to
			// become ready and report that instead. Any other error is not tolerated:
			// a kind missing from both snapshots would make checkOwnersStable pass
			// without ever comparing it.
			ginkgo.GinkgoWriter.Printf("[snapshot] kind %s is not registered, skipping\n", source.kind)
			continue
		}
		g.Expect(err).ToNot(gomega.HaveOccurred(), "listing %s owners of %s failed", source.kind, rbg.Name)

		err = apimeta.EachListItem(list, func(obj runtime.Object) error {
			accessor, err := apimeta.Accessor(obj)
			if err != nil {
				return err
			}
			raw, err := runtime.DefaultUnstructuredConverter.ToUnstructured(obj)
			if err != nil {
				return err
			}
			// A missing spec is not an error: NestedMap returns a nil map, which
			// diffs cleanly against another nil map.
			spec, _, err := unstructured.NestedMap(raw, "spec")
			if err != nil {
				return err
			}
			out[source.kind+"/"+accessor.GetName()] = ownerFacts{
				UID:        accessor.GetUID(),
				Generation: accessor.GetGeneration(),
				Spec:       spec,
			}
			return nil
		})
		g.Expect(err).ToNot(gomega.HaveOccurred())
	}
	return out
}

// listServicesForRBG returns the Services an RBG owns, keyed by name. The controller
// labels them with the group name like every other object it creates, so one label
// query covers all roles.
//
// A list failure is fatal for the same reason as in listRevisionsForRBG: two empty
// results compare equal, so degrading here would turn this detector into one that
// always passes.
func listServicesForRBG(
	g gomega.Gomega,
	f *framework.Framework,
	rbg *workloadsv1alpha2.RoleBasedGroup,
) map[string]serviceFacts {
	svcList := &corev1.ServiceList{}
	g.Expect(f.Client.List(f.Ctx, svcList,
		client.InNamespace(rbg.Namespace),
		client.MatchingLabels{constants.GroupNameLabelKey: rbg.Name},
	)).To(gomega.Succeed(), "listing services of %s failed", rbg.Name)

	out := make(map[string]serviceFacts, len(svcList.Items))
	for i := range svcList.Items {
		svc := &svcList.Items[i]

		ports := make([]string, 0, len(svc.Spec.Ports))
		for _, port := range svc.Spec.Ports {
			ports = append(ports, fmt.Sprintf("%s/%s:%d->%s",
				port.Name, port.Protocol, port.Port, port.TargetPort.String()))
		}
		sort.Strings(ports)

		out[svc.Name] = serviceFacts{
			UID:       svc.UID,
			ClusterIP: svc.Spec.ClusterIP,
			Ports:     ports,
			Selector:  copyStringMap(svc.Spec.Selector),
			Endpoints: listEndpointsForService(g, f, svc),
		}
	}
	return out
}

// listEndpointsForService returns the sorted addresses backing a Service, as published
// in its EndpointSlices.
//
// The readiness of each address is part of the string rather than a filter: an endpoint
// that went from ready to not-ready is a traffic disruption, and dropping it from the
// list would report that as a removed endpoint without saying why.
func listEndpointsForService(
	g gomega.Gomega,
	f *framework.Framework,
	svc *corev1.Service,
) []string {
	sliceList := &discoveryv1.EndpointSliceList{}
	g.Expect(f.Client.List(f.Ctx, sliceList,
		client.InNamespace(svc.Namespace),
		client.MatchingLabels{discoveryv1.LabelServiceName: svc.Name},
	)).To(gomega.Succeed(), "listing endpoint slices of service %s failed", svc.Name)

	var out []string
	for i := range sliceList.Items {
		for _, endpoint := range sliceList.Items[i].Endpoints {
			target := "<no targetRef>"
			if endpoint.TargetRef != nil {
				target = endpoint.TargetRef.Name
			}
			ready := "unset"
			if endpoint.Conditions.Ready != nil {
				ready = fmt.Sprintf("%t", *endpoint.Conditions.Ready)
			}
			for _, address := range endpoint.Addresses {
				out = append(out, fmt.Sprintf("%s=%s ready=%s", target, address, ready))
			}
		}
	}
	sort.Strings(out)
	return out
}

// listRevisionsForRBG returns the sorted ControllerRevision names for an RBG. A list
// failure is fatal rather than an empty result: two empty results compare equal, so
// degrading here would make checkNoRevisionExplosion report success for the signal
// this suite exists to watch.
func listRevisionsForRBG(
	g gomega.Gomega,
	f *framework.Framework,
	rbg *workloadsv1alpha2.RoleBasedGroup,
) []string {
	revList := &appsv1.ControllerRevisionList{}
	g.Expect(f.Client.List(f.Ctx, revList,
		client.InNamespace(rbg.Namespace),
		client.MatchingLabels{constants.GroupNameLabelKey: rbg.Name},
	)).To(gomega.Succeed(), "listing revisions of %s failed", rbg.Name)

	names := make([]string, 0, len(revList.Items))
	for i := range revList.Items {
		names = append(names, revList.Items[i].Name)
	}
	sort.Strings(names)
	return names
}

// roleSnapshot wraps one role's pod facts in the shape the expect* helpers consume,
// so a caller holding a single role can reuse checkNoPodChurn and get its per-pod
// diff instead of an opaque map equality failure.
func roleSnapshot(rbgName, roleName string, pods map[string]PodFacts) map[string]RBGSnapshot {
	return map[string]RBGSnapshot{
		rbgName: {
			Kind:  kindRBG,
			Name:  rbgName,
			Roles: map[string]map[string]PodFacts{roleName: pods},
		},
	}
}

// missingFrom returns the elements of names that do not appear in other.
func missingFrom(names, other []string) []string {
	present := make(map[string]struct{}, len(other))
	for _, name := range other {
		present[name] = struct{}{}
	}
	var out []string
	for _, name := range names {
		if _, found := present[name]; !found {
			out = append(out, name)
		}
	}
	return out
}

// runDetectors runs every before/after check this suite has, so that each spec claiming
// "this changed nothing" makes the same claim rather than a weaker one that drifts apart
// as detectors are added.
//
// rec is what the compared interval is known to change: upgradeRewrites when the two
// snapshots span the v0.7.0 -> current upgrade, the zero value for an action that must
// change nothing at all. skip names the RBGs a spec deliberately disturbed; they are
// dropped from both snapshots and from the event search.
//
// It fills fs rather than reporting, so a caller can add its own comparisons to the same
// report. Every detector answers a different question and they are all worth seeing.
func runDetectors(
	fs *findings,
	f *framework.Framework,
	before, after map[string]RBGSnapshot,
	since metav1.Time,
	rec recordedRewrites,
	skip []string,
) {
	before, after = exclude(before, skip...), exclude(after, skip...)

	checkSnapshotDiff(fs, f, before, after, rec)
	checkNoKillingEvents(fs, f, since, skip)
}

// quiesceTimeout bounds the wait below. It is generous on purpose: `helm upgrade --wait`
// returns once the new controller pod is available, which is before it takes over the
// leader-election lease and before its first reconcile of every fixture has landed.
const quiesceTimeout = 5 * time.Minute

// waitQuiesced samples the namespace twice, settleDuration apart, until the two samples
// agree, and returns the later one. skip names the fixtures a spec is deliberately
// keeping in motion: they are left out of the comparison but kept in the returned
// sample, which the caller still needs whole.
//
// Two agreeing samples is the precondition of every before/after comparison here. While
// the controller is still converging, a difference against the pre-upgrade snapshot
// cannot be attributed to the upgrade rather than to the moment the sample was taken --
// so this says "the controller is still moving" instead of blaming the hop.
//
// It waits rather than measuring one window after helm returns, because that boundary is
// a claim about `helm --wait`, not about the upgrade: the run this replaced reported the
// new controller's very first reconcile as churn.
//
// Nothing is folded. The interval between the two samples contains no action of this
// suite's and no controller start, and everything the controller did before the samples
// agreed still has to be accounted for by the caller's comparison against the
// pre-upgrade snapshot, where only the recorded rewrites are allowed.
func waitQuiesced(f *framework.Framework, skip ...string) map[string]RBGSnapshot {
	var latest map[string]RBGSnapshot
	gomega.Eventually(
		func(g gomega.Gomega) {
			first := captureAll(g, f)
			time.Sleep(settleDuration)
			latest = captureAll(g, f)

			fs := &findings{}
			checkSnapshotDiff(fs, f, exclude(first, skip...), exclude(latest, skip...), recordedRewrites{})
			g.Expect(fs.sections).To(gomega.BeEmpty(), strings.Join(fs.sections, "\n\n"))
		}, quiesceTimeout, time.Second,
	).Should(
		gomega.Succeed(),
		"the controller was still changing things %s after the upgrade, so nothing can be attributed to "+
			"the upgrade itself", quiesceTimeout,
	)
	return latest
}

// checkSnapshotDiff runs every check that needs nothing but the two snapshots. It is
// separate from runDetectors so that a caller with two snapshots and no event mark --
// waitQuiesced, comparing two post-upgrade samples -- runs the same set rather than
// picking a few detectors by hand and quietly falling behind as more are added.
func checkSnapshotDiff(fs *findings, f *framework.Framework, before, after map[string]RBGSnapshot, rec recordedRewrites) {
	checkSameRBGSet(fs, before, after)
	checkNoPodChurn(fs, before, after)
	checkNoRestarts(fs, before, after)
	checkPodMetadataStable(fs, before, after)
	checkServicesStable(fs, before, after, rec.leaderOnlyServices)
	checkOwnersStable(fs, before, after, rec.specRewrites)
	checkNoRevisionExplosion(fs, f, before, after, rec.revisionAdds)
	checkStillReady(fs, before, after)
}

// checkSameRBGSet fails when an RBG or a RoleBasedGroupSet appeared or disappeared
// across the upgrade.
func checkSameRBGSet(fs *findings, before, after map[string]RBGSnapshot) {
	var problems []string
	for name, snap := range before {
		if _, ok := after[name]; !ok {
			problems = append(problems, fmt.Sprintf("%s %q disappeared", snap.Kind, name))
		}
	}
	for name, snap := range after {
		if _, ok := before[name]; !ok {
			problems = append(problems, fmt.Sprintf("%s %q appeared", snap.Kind, name))
		}
	}
	fs.add("the set of RoleBasedGroups changed across the upgrade", problems)
}

// checkNoPodChurn is the primary assertion of this suite. It compares pod name ->
// UID per role, which catches both ways a pod can be replaced: a new generated name,
// and a reused stable name (StatefulSet style) carrying a new UID.
//
// It also compares ownerReferences. A pod adopted by a different owner object keeps
// its own UID, node and creation timestamp, so re-parenting is invisible to every
// other comparison here -- and it means the object that will next roll this pod is not
// the one that created it.
func checkNoPodChurn(fs *findings, before, after map[string]RBGSnapshot) {
	var problems []string
	for rbgName, beforeSnap := range before {
		afterSnap, ok := after[rbgName]
		if !ok {
			continue // reported by checkSameRBGSet
		}
		for role, beforePods := range beforeSnap.Roles {
			afterPods := afterSnap.Roles[role]

			for podName, beforeFacts := range beforePods {
				afterFacts, found := afterPods[podName]
				if !found {
					problems = append(problems, fmt.Sprintf(
						"%s/%s: pod %s is gone (was UID %s, node %s)",
						rbgName, role, podName, beforeFacts.UID, beforeFacts.NodeName))
					continue
				}
				if afterFacts.UID != beforeFacts.UID {
					problems = append(problems, fmt.Sprintf(
						"%s/%s: pod %s was recreated under the same name (UID %s -> %s)",
						rbgName, role, podName, beforeFacts.UID, afterFacts.UID))
				}
				if !afterFacts.CreationTimestamp.Equal(&beforeFacts.CreationTimestamp) {
					problems = append(problems, fmt.Sprintf(
						"%s/%s: pod %s creationTimestamp changed (%s -> %s)",
						rbgName, role, podName,
						beforeFacts.CreationTimestamp, afterFacts.CreationTimestamp))
				}
				if afterFacts.NodeName != beforeFacts.NodeName {
					problems = append(problems, fmt.Sprintf(
						"%s/%s: pod %s moved node (%s -> %s)",
						rbgName, role, podName, beforeFacts.NodeName, afterFacts.NodeName))
				}
				if !slices.Equal(beforeFacts.OwnerUIDs, afterFacts.OwnerUIDs) {
					problems = append(problems, fmt.Sprintf(
						"%s/%s: pod %s is owned by different objects (%v -> %v)",
						rbgName, role, podName, beforeFacts.OwnerUIDs, afterFacts.OwnerUIDs))
				}
			}

			for podName, afterFacts := range afterPods {
				if _, found := beforePods[podName]; !found {
					problems = append(problems, fmt.Sprintf(
						"%s/%s: new pod %s appeared (UID %s, node %s)",
						rbgName, role, podName, afterFacts.UID, afterFacts.NodeName))
				}
			}
		}
	}
	fs.add("pods were recreated, moved or added by the upgrade", problems)
}

// checkNoRestarts requires restart counts to be exactly equal. A >= check would
// pass a pod whose container the upgrade killed and the kubelet restarted in place,
// which keeps the pod UID and is therefore invisible to checkNoPodChurn.
//
// Both directions are walked. A container that only the after snapshot reports is a
// container the upgrade added to a pod that survived, which no comparison over the
// before-side container names can reach.
func checkNoRestarts(fs *findings, before, after map[string]RBGSnapshot) {
	var problems []string
	for rbgName, beforeSnap := range before {
		afterSnap, ok := after[rbgName]
		if !ok {
			continue
		}
		for role, beforePods := range beforeSnap.Roles {
			for podName, beforeFacts := range beforePods {
				afterFacts, found := afterSnap.Roles[role][podName]
				if !found {
					continue // reported by checkNoPodChurn
				}
				for container, beforeCount := range beforeFacts.RestartCounts {
					afterCount, hasContainer := afterFacts.RestartCounts[container]
					if !hasContainer {
						problems = append(problems, fmt.Sprintf(
							"%s/%s: pod %s no longer reports container %s",
							rbgName, role, podName, container))
						continue
					}
					if afterCount != beforeCount {
						problems = append(problems, fmt.Sprintf(
							"%s/%s: pod %s container %s restartCount changed (%d -> %d)",
							rbgName, role, podName, container, beforeCount, afterCount))
					}
				}
				for container := range afterFacts.RestartCounts {
					if _, hadBefore := beforeFacts.RestartCounts[container]; !hadBefore {
						problems = append(problems, fmt.Sprintf(
							"%s/%s: pod %s reports a container %s it did not have before",
							rbgName, role, podName, container))
					}
				}
			}
		}
	}
	fs.add("containers were restarted by the upgrade", problems)
}

// checkPodMetadataStable compares the pod fields that a rewrite can move without
// touching pod identity: labels, annotations and readiness gates.
//
// This is the detector the identity-label drift needed and did not have. That drift
// gave one ordinal's pods another ordinal's identity labels while leaving the UID, the
// node and the restart count exactly as they were, so every check above it passed and
// the problem was found by reading pods by hand.
//
// It compares and does not validate: a label holding the wrong value from the start is
// a question for the correctness suites, and this suite can only speak about what the
// upgrade changed.
func checkPodMetadataStable(fs *findings, before, after map[string]RBGSnapshot) {
	var labelProblems, annotationProblems, gateProblems []string
	for rbgName, beforeSnap := range before {
		afterSnap, ok := after[rbgName]
		if !ok {
			continue
		}
		for role, beforePods := range beforeSnap.Roles {
			for podName, beforeFacts := range beforePods {
				afterFacts, found := afterSnap.Roles[role][podName]
				if !found {
					continue // reported by checkNoPodChurn
				}
				where := fmt.Sprintf("%s/%s: pod %s", rbgName, role, podName)

				for _, diff := range stringMapDiff(beforeFacts.Labels, afterFacts.Labels) {
					labelProblems = append(labelProblems, where+" label "+diff)
				}
				for _, diff := range stringMapDiff(beforeFacts.Annotations, afterFacts.Annotations) {
					annotationProblems = append(annotationProblems, where+" annotation "+diff)
				}
				if strings.Join(beforeFacts.ReadinessGates, ",") != strings.Join(afterFacts.ReadinessGates, ",") {
					gateProblems = append(gateProblems, fmt.Sprintf(
						"%s readinessGates changed (%v -> %v)",
						where, beforeFacts.ReadinessGates, afterFacts.ReadinessGates))
				}
			}
		}
	}
	fs.add("pod labels were changed by the upgrade", labelProblems)
	fs.add("pod annotations were changed by the upgrade", annotationProblems)
	// A gate appearing means the pod went through an in-place update, which is a
	// rewrite of its template even though the pod itself survived.
	fs.add("pod readiness gates were changed by the upgrade", gateProblems)
}

// stringMapDiff describes how two maps differ, one line per key. Reporting per key is
// what makes a failure readable: a pod carries a dozen labels and several annotations,
// and dumping both maps leaves the reader to spot the difference.
func stringMapDiff(before, after map[string]string) []string {
	var out []string
	for key, beforeValue := range before {
		afterValue, found := after[key]
		if !found {
			out = append(out, fmt.Sprintf("%s was removed (was %q)", key, beforeValue))
			continue
		}
		if afterValue != beforeValue {
			out = append(out, fmt.Sprintf("%s changed (%q -> %q)", key, beforeValue, afterValue))
		}
	}
	for key, afterValue := range after {
		if _, found := before[key]; !found {
			out = append(out, fmt.Sprintf("%s was added (%q)", key, afterValue))
		}
	}
	return out
}

// specJSONMap renders a spec as the JSON the apiserver actually stores.
//
// The JSON round trip is what makes a comparison of two specs readable. gomega.Equal on
// these structs dumps both objects in full, and a pod template is large enough that
// Gomega truncates the dump before reaching the field that differs. Comparing the
// structs with cmp directly is not an option either: the unexported fields inside
// resource.Quantity make it panic.
func specJSONMap(spec any) map[string]any {
	raw, err := json.Marshal(spec)
	gomega.Expect(err).ToNot(gomega.HaveOccurred(), "could not marshal a spec for comparison")
	var m map[string]any
	gomega.Expect(json.Unmarshal(raw, &m)).To(gomega.Succeed(), "could not parse a spec for comparison")
	return m
}

// storedSpecDiff reports how two stored specs differ, as a path-level diff, ignoring the
// one rewrite recorded in foldRestartPolicyShape. An empty result means they are
// equivalent.
func storedSpecDiff(before, after any) string {
	beforeMap, afterMap := specJSONMap(before), specJSONMap(after)
	foldRestartPolicyShape(beforeMap)
	foldRestartPolicyShape(afterMap)
	return cmp.Diff(beforeMap, afterMap)
}

// Defaults the CRD materializes inside restartPolicyConfig once its parent object
// exists. Kept here so a change to either default shows up as a failing diff rather
// than being folded away silently.
const (
	defaultRestartBaseDelaySeconds = float64(30)
	defaultRestartMaxDelaySeconds  = float64(600)
)

// foldRestartPolicyShape rewrites every role pattern's restartPolicyConfig back into the
// deprecated restartPolicy string, so that comparing two stored specs ignores this one
// rewrite and nothing else.
//
// The v1alpha1 write path materializes restartPolicyConfig where v0.7.0 stored the
// string, and the apiserver then fills in the two delay fields inside it. The pair says
// the same thing, since the v1alpha2 getters fold the string in and default the delays
// to these very values.
//
// It is recorded rather than reported because on the roles that reach this fold the
// rewrite stops at the stored RBG spec. Only a LeaderWorkerPattern or a CustomComponents
// role carries a restart policy at all, and every such role written through v1alpha1
// here is reconciled into a LeaderWorkerSet, a workload with no restart policy the RBG
// could move. That is narrower than it looks: on the RoleInstanceSet path the controller
// writes the same shape into the RoleInstance template, so the identical flip would move
// the revision hash and roll the role -- which is why the reconciler keeps writing the
// deprecated string unless the role configures delays. No role that round-trips through
// v1alpha1 here is on that path. The pod checks in the spec that calls this stay strict,
// and they are what holds this reasoning to account.
//
// Only a config carrying the default delays is folded. Different delays would be a real
// semantic change, and leaving such a config in place is what makes it show up as a
// difference.
func foldRestartPolicyShape(spec map[string]any) {
	// A RoleBasedGroupSet holds its roles one level down, in the template it stamps out.
	// The conversion webhook shares one function across both kinds, so the same rewrite
	// lands there and the same fold has to reach it.
	if template, ok := spec["groupTemplate"].(map[string]any); ok {
		if inner, ok := template["spec"].(map[string]any); ok {
			foldRestartPolicyShape(inner)
		}
	}

	roles, _ := spec["roles"].([]any)
	for _, role := range roles {
		roleMap, ok := role.(map[string]any)
		if !ok {
			continue
		}
		for _, patternKey := range []string{
			"leaderWorkerPattern", "customComponentsPattern", "standalonePattern",
		} {
			pattern, ok := roleMap[patternKey].(map[string]any)
			if !ok {
				continue
			}
			config, ok := pattern["restartPolicyConfig"].(map[string]any)
			if !ok || !hasDefaultRestartDelays(config) {
				continue
			}
			delete(pattern, "restartPolicyConfig")
			if policyType, ok := config["type"].(string); ok && policyType != "" {
				pattern["restartPolicy"] = policyType
			}
		}
	}
}

// hasDefaultRestartDelays is whether a restartPolicyConfig carries exactly the delays
// the v1alpha2 getters default the deprecated string to. A key that is absent does not
// count as matching: the fold is only sound because the pair says the same thing, and a
// config that is not the defaulted shape is a difference worth reporting.
func hasDefaultRestartDelays(config map[string]any) bool {
	for key, want := range map[string]float64{
		"baseDelaySeconds": defaultRestartBaseDelaySeconds,
		"maxDelaySeconds":  defaultRestartMaxDelaySeconds,
	} {
		value, present := config[key]
		if !present || value != want {
			return false
		}
	}
	return true
}

// recordedRewrites is what one action is known to change. Each comparison passes the
// record for the action it spans -- upgradeRewrites across the hop, the zero value
// across an action that must change nothing -- so a change recorded for one action is
// not silently tolerated for the others.
//
// An entry is only allowed here with the change it stands for named. A difference nobody
// can attribute is a finding to report, not an entry to add.
type recordedRewrites struct {
	// specRewrites is the rewrite one named object ("Kind/name" as checkOwnersStable
	// keys it, the root object under its own kind and name) is recorded to undergo,
	// expressed on the before snapshot's spec. Applying it yields the spec the object
	// is expected to hold afterwards, so an entry asserts in both directions: the
	// recorded rewrite happens -- its precondition is checked against the before
	// snapshot -- and nothing beyond it does.
	//
	// Content is asserted rather than generation. Generation also advances on writes
	// whose stored bytes change without moving a field the typed spec round-trips:
	// the LeaderWorkerSet reconciler, for one, patches on every controller start
	// because its DeepEqual reads the CRD-defaulted rollingUpdateConfiguration
	// against a nil one, yet the patch changes no field of the stored spec. Counting
	// those writes made every comparison depend on how many times the controller
	// happened to start within the interval. What a content-equal bump could still
	// hide -- a moved revision hash -- is checkNoRevisionExplosion's job, and it
	// stays strict.
	specRewrites map[string]func(map[string]any) error

	// revisionAdds is how many new ControllerRevisions each named RoleBasedGroup
	// is allowed to gain within the interval. A new revision is the fingerprint of
	// a changed hash, so an entry is only allowed together with the specRewrites
	// entry that moved the hash. Removed revisions are never tolerated, and a
	// count that does not match exactly is still reported with the content diff.
	revisionAdds map[string]int

	// leaderOnlyServices are the shared Services whose selector the upgrade narrows to
	// the leader component. Only the exact narrowing is folded: it must be visible in
	// this Service's selector diff, the added selector key must be
	// component-name=leader and nothing else, no endpoint may be added, and every
	// endpoint that left must belong to a pod that is not a leader.
	leaderOnlyServices map[string]bool
}

// upgradeRewrites records the v0.7.0 -> current changes, one entry per change.
//
// RoleInstanceSet and RoleInstance generations were both 1 while the reconciler rewrote
// the stored restartPolicy string into restartPolicyConfig. That rewrite moved the
// RoleInstanceSet revision hash and rolled every role on upgrade, and the RoleInstance
// bump was its consequence: only the resulting in-place update reached the code that
// adds the RoleInstanceInPlaceUpdateReady gate. With the reconciler no longer touching
// the template, both kinds are left alone entirely, so neither belongs here.
var upgradeRewrites = recordedRewrites{
	specRewrites: map[string]func(map[string]any) error{
		// The legacy-strategy fixture stores the v1alpha1 spelling "Recreate" of the
		// update strategy type, which v0.7.0 copied verbatim into the RoleInstanceSet.
		// The new RoleInstanceSet CRD enum rejects that value, so the mutating webhook
		// heals it to "RecreatePod" on the first write the upgraded controller sends.
		// The heal is a one-off -- the next apply is a no-op.
		"RoleInstanceSet/" + legacyStrategyRISName(): healStoredStrategyType,
		// The legacy-set fixture's child owns its own RoleInstanceSet, which v0.7.0
		// also wrote with the legacy spelling copied verbatim from the template, so
		// it is healed on the upgraded controller's first reconcile too.
		"RoleInstanceSet/" + legacySetChildRISName(): healStoredStrategyType,
		// The same heal reaches the child RoleBasedGroup itself: the RBGS controller
		// re-applies the child from its groupTemplate with the strategy type
		// normalized, so the stored child spec changes on its first reconcile. A
		// top-level RoleBasedGroup has no writer above it, so up-legacy keeps the
		// legacy spelling -- only the child is re-applied.
		"RoleBasedGroup/" + legacySetChildName(): healRoleStrategyTypes,
	},

	// Healing the child's stored spec moved the RBG-layer revision hash, so the
	// controller stamps one new revision for it. The hash moves but no pod does:
	// the RoleInstanceSet layer is repaired in place and its revision is stable,
	// which the RoleInstanceSet entries above assert through content. No revision
	// may be removed, and any count other than one is still reported with its
	// content diff.
	revisionAdds: map[string]int{
		legacySetChildName(): 1,
	},

	// KEP 260 flips the default of sharedServiceSelection: v0.7.0 treated an unset
	// field as All, and the current release resolves it to LeaderOnly for
	// RoleInstanceSet leader-worker roles. The controller then patches the selector of
	// the shared Service in place, which drops every worker pod IP from its
	// EndpointSlice without touching a pod. keps/260-leaderonly-service/README.md:251
	// records this as the breaking case for endpoints and tells affected workloads to
	// set sharedServiceSelection: All.
	//
	// This suite cannot say whether that is the right default, only that it is what the
	// upgrade does. The entry is the fixture's own Service, so a role whose selector is
	// narrowed anywhere else still fails.
	leaderOnlyServices: map[string]bool{sharedServiceName(fxLwp, lwpRole): true},
}

// healStoredStrategyType is the rewrite the mutating webhook performs on a stored
// RoleInstanceSet whose spec.updateStrategy.type carries the v1alpha1 "Recreate"
// spelling: the first write through the new apiserver heals it to "RecreatePod".
// Applied to the before snapshot it yields the expected after spec. The legacy
// spelling must be there to replace -- without it the recording is stale, and that
// is reported rather than silently passing.
func healStoredStrategyType(spec map[string]any) error {
	strategy, _ := spec["updateStrategy"].(map[string]any)
	if got := strategy["type"]; got != string(workloadsv1alpha2.LegacyRecreateUpdateStrategyType) {
		return fmt.Errorf("updateStrategy.type is %v, not the legacy spelling the upgrade is recorded to heal", got)
	}
	strategy["type"] = string(workloadsv1alpha2.RecreatePodUpdateStrategyType)
	return nil
}

// healRoleStrategyTypes is the same heal one level up, on a stored RoleBasedGroup:
// every role carrying the v1alpha1 "Recreate" spelling in
// roles[].rolloutStrategy.rollingUpdate.type comes out of the write as
// "RecreatePod". At least one role must carry it, for the same reason as
// healStoredStrategyType.
func healRoleStrategyTypes(spec map[string]any) error {
	roles, _ := spec["roles"].([]any)
	healed := false
	for _, item := range roles {
		role, _ := item.(map[string]any)
		strategy, _ := role["rolloutStrategy"].(map[string]any)
		rolling, _ := strategy["rollingUpdate"].(map[string]any)
		if rolling["type"] == string(workloadsv1alpha2.LegacyRecreateUpdateStrategyType) {
			rolling["type"] = string(workloadsv1alpha2.RecreatePodUpdateStrategyType)
			healed = true
		}
	}
	if !healed {
		return fmt.Errorf("no role carries the legacy strategy spelling the upgrade is recorded to heal")
	}
	return nil
}

// checkOwnersStable checks the workload objects behind each role, and the root object
// itself. Rather than counting writes, each object's after spec is compared against
// what the action is recorded to produce: the before spec with the recorded rewrite
// applied, or the before spec itself when nothing is recorded. The comparison is
// exact in both directions -- a change nobody recorded fails, and a recorded change
// that never happened fails the rewrite's precondition.
func checkOwnersStable(
	fs *findings,
	before, after map[string]RBGSnapshot,
	specRewrites map[string]func(map[string]any) error,
) {
	var problems []string
	for rbgName, beforeSnap := range before {
		afterSnap, ok := after[rbgName]
		if !ok {
			continue
		}

		if afterSnap.RBGUID != beforeSnap.RBGUID {
			problems = append(problems, fmt.Sprintf(
				"%s: %s was recreated (UID %s -> %s)",
				rbgName, beforeSnap.Kind, beforeSnap.RBGUID, afterSnap.RBGUID))
		}
		if problem := compareWithExpected(rbgName, beforeSnap.Kind+"/"+rbgName, beforeSnap.Spec, afterSnap.Spec, specRewrites); problem != "" {
			problems = append(problems, problem)
		}

		for key, beforeOwner := range beforeSnap.Owners {
			afterOwner, found := afterSnap.Owners[key]
			if !found {
				problems = append(problems, fmt.Sprintf("%s: owner %s is gone", rbgName, key))
				continue
			}
			if afterOwner.UID != beforeOwner.UID {
				problems = append(problems, fmt.Sprintf(
					"%s: owner %s was recreated (UID %s -> %s)",
					rbgName, key, beforeOwner.UID, afterOwner.UID))
			}
			if problem := compareWithExpected(rbgName, key, beforeOwner.Spec, afterOwner.Spec, specRewrites); problem != "" {
				problems = append(problems, problem)
			}
		}
		for key := range afterSnap.Owners {
			if _, found := beforeSnap.Owners[key]; !found {
				problems = append(problems, fmt.Sprintf("%s: new owner %s appeared", rbgName, key))
			}
		}
	}
	fs.add("workload objects were replaced or their specs rewritten past what is recorded", problems)
}

// compareWithExpected applies the rewrite recorded for key to a deep copy of
// beforeSpec -- which yields the spec the object is expected to hold after the
// action -- and compares it against afterSpec. An empty return means the after
// spec is exactly what is recorded.
func compareWithExpected(
	rbgName, key string,
	beforeSpec, afterSpec map[string]any,
	specRewrites map[string]func(map[string]any) error,
) string {
	expected := runtime.DeepCopyJSON(beforeSpec)
	if rewrite, recorded := specRewrites[key]; recorded {
		if err := rewrite(expected); err != nil {
			return fmt.Sprintf(
				"%s: %s is recorded to be rewritten, but the before spec does not carry what "+
					"the rewrite replaces: %v", rbgName, key, err)
		}
	}
	if diff := cmp.Diff(expected, afterSpec); diff != "" {
		return fmt.Sprintf(
			"%s: %s spec differs from what the action is recorded to produce (- expected + after):\n%s",
			rbgName, key, indentLines(diff, "      "))
	}
	return ""
}

// checkServicesStable checks the Services in front of each role, and the endpoints
// behind them.
//
// A recreated Service loses its cluster IP, and a Service whose selector was rewritten
// keeps every field a client can see while quietly matching nothing. Neither shows up in
// any pod-level check, because the pods are fine -- it is the path to them that broke.
//
// leaderOnly names the Services whose narrowing to the leader component is recorded in
// recordedRewrites. Being listed is a permission, not the fold itself: the narrowing has
// to show up in this Service's selector diff before the non-leader endpoints it removes
// are folded away, so a lost endpoint on a selector that never moved is still reported.
// Every other difference on the same Service is reported either way.
func checkServicesStable(
	fs *findings,
	before, after map[string]RBGSnapshot,
	leaderOnly map[string]bool,
) {
	var problems, endpointProblems []string
	for rbgName, beforeSnap := range before {
		afterSnap, ok := after[rbgName]
		if !ok {
			continue
		}

		for name, beforeSvc := range beforeSnap.Services {
			afterSvc, found := afterSnap.Services[name]
			if !found {
				problems = append(problems, fmt.Sprintf(
					"%s: service %s is gone (was UID %s)", rbgName, name, beforeSvc.UID))
				continue
			}
			where := fmt.Sprintf("%s: service %s", rbgName, name)

			if afterSvc.UID != beforeSvc.UID {
				problems = append(problems, fmt.Sprintf(
					"%s was recreated (UID %s -> %s)", where, beforeSvc.UID, afterSvc.UID))
			}
			if afterSvc.ClusterIP != beforeSvc.ClusterIP {
				problems = append(problems, fmt.Sprintf(
					"%s clusterIP changed (%s -> %s)", where, beforeSvc.ClusterIP, afterSvc.ClusterIP))
			}
			if strings.Join(beforeSvc.Ports, ",") != strings.Join(afterSvc.Ports, ",") {
				problems = append(problems, fmt.Sprintf(
					"%s ports changed (%v -> %v)", where, beforeSvc.Ports, afterSvc.Ports))
			}

			// narrowed is whether the recorded narrowing is what this Service's selector
			// actually did, which is not the same as the Service being on the list. Only
			// the former may fold endpoints: a Service whose selector never moved has
			// nothing recorded to explain a lost endpoint, and folding one anyway would
			// hide it on the very Service where no other detector would report it.
			narrowed := false
			for _, diff := range stringMapDiff(beforeSvc.Selector, afterSvc.Selector) {
				if leaderOnly[name] && diff == leaderComponentSelectorAdded {
					narrowed = true
					continue
				}
				problems = append(problems, where+" selector "+diff)
			}

			added := missingFrom(afterSvc.Endpoints, beforeSvc.Endpoints)
			removed := missingFrom(beforeSvc.Endpoints, afterSvc.Endpoints)
			if narrowed {
				removed = leaderEndpoints(removed, beforeSnap)
			}
			if len(added) > 0 || len(removed) > 0 {
				endpointProblems = append(endpointProblems, fmt.Sprintf(
					"%s endpoints changed\n    added:   %v\n    removed: %v", where, added, removed))
			}
		}

		for name, afterSvc := range afterSnap.Services {
			if _, found := beforeSnap.Services[name]; !found {
				problems = append(problems, fmt.Sprintf(
					"%s: new service %s appeared (UID %s)", rbgName, name, afterSvc.UID))
			}
		}
	}
	fs.add("Services were replaced or rewritten by the upgrade", problems)
	// Kept separate from the field comparisons above: this is the section that means
	// clients stopped reaching the pods, rather than an object having been rewritten.
	fs.add("the endpoints behind a Service changed across the upgrade", endpointProblems)
}

// leaderComponentSelectorAdded is the one selector difference a recorded leader-only
// narrowing is allowed to produce. Written as the line stringMapDiff emits so that a
// different value, or the key being changed rather than added, does not match.
var leaderComponentSelectorAdded = fmt.Sprintf(
	"%s was added (%q)", constants.ComponentNameLabelKey, constants.LeaderComponentType)

// leaderEndpoints returns the endpoints of eps that belong to a leader pod.
//
// It is what remains reportable after a recorded leader-only narrowing: losing a worker
// endpoint is the recorded consequence, losing a leader endpoint is not, and an endpoint
// whose pod is not in the snapshot at all is kept because nothing here can vouch for it.
func leaderEndpoints(eps []string, snap RBGSnapshot) []string {
	var out []string
	for _, ep := range eps {
		podName, _, _ := strings.Cut(ep, "=")
		facts, found := podFactsByName(snap, podName)
		if found && facts.Labels[constants.ComponentNameLabelKey] != string(constants.LeaderComponentType) {
			continue
		}
		out = append(out, ep)
	}
	return out
}

// podFactsByName finds a pod in a snapshot without knowing which role it belongs to.
func podFactsByName(snap RBGSnapshot, podName string) (PodFacts, bool) {
	for _, pods := range snap.Roles {
		if facts, found := pods[podName]; found {
			return facts, true
		}
	}
	return PodFacts{}, false
}

// checkNoRevisionExplosion checks no ControllerRevision was added or removed.
//
// A new revision name is the fingerprint of a changed revision hash. This is
// corroborating evidence, not the verdict: the authority is the pod identity
// assertions above, so the message is worded as a suspicion.
//
// revisionAdds records the new revisions a known rewrite is expected to stamp:
// an entry passes only when exactly that many revisions were added and none were
// removed. When a change is found, the added revisions are fetched back and
// diffed against the surviving pre-upgrade revision of the same owner, so the
// report says which field moved the hash rather than only that it moved.
// ControllerRevision data is written once and never mutated, so reading the
// older revision now still shows what it held before the upgrade.
func checkNoRevisionExplosion(fs *findings, f *framework.Framework, before, after map[string]RBGSnapshot, revisionAdds map[string]int) {
	var problems []string
	for rbgName, beforeSnap := range before {
		afterSnap, ok := after[rbgName]
		if !ok {
			continue
		}
		if strings.Join(beforeSnap.RevisionNames, ",") == strings.Join(afterSnap.RevisionNames, ",") {
			continue
		}
		added := missingFrom(afterSnap.RevisionNames, beforeSnap.RevisionNames)
		removed := missingFrom(beforeSnap.RevisionNames, afterSnap.RevisionNames)
		if len(removed) == 0 && len(added) == revisionAdds[rbgName] {
			continue
		}
		problems = append(problems, describeRevisionChange(f, rbgName, beforeSnap, afterSnap))
	}
	// The name encodes which layer produced it: the RBG layer names revisions
	// <rbg>-<hash>-<n>, the RoleInstanceSet layer names them <set>-<hash>. Both carry
	// GroupNameLabelKey, so this list spans both and the added names say which moved.
	fs.add("ControllerRevisions changed across the upgrade", problems)
}

// describeRevisionChange renders one RBG's revision change, including a content diff
// for every added revision against the newest surviving revision of the same owner.
// A fetch failure degrades to the name lists: those already prove the change, and a
// diagnostic read must never be what fails the spec.
func describeRevisionChange(
	f *framework.Framework,
	rbgName string,
	beforeSnap, afterSnap RBGSnapshot,
) string {
	added := missingFrom(afterSnap.RevisionNames, beforeSnap.RevisionNames)
	removed := missingFrom(beforeSnap.RevisionNames, afterSnap.RevisionNames)

	head := fmt.Sprintf(
		"%s: ControllerRevisions changed, so the revision hash may have changed"+
			"\n    added:   %v\n    removed: %v\n    before:  %v\n    after:   %v",
		rbgName, added, removed, beforeSnap.RevisionNames, afterSnap.RevisionNames)

	revList := &appsv1.ControllerRevisionList{}
	if err := f.Client.List(f.Ctx, revList,
		client.InNamespace(f.Namespace),
		client.MatchingLabels{constants.GroupNameLabelKey: rbgName},
	); err != nil {
		return head + fmt.Sprintf("\n    could not list revisions for a content diff: %v", err)
	}

	byName := make(map[string]*appsv1.ControllerRevision, len(revList.Items))
	for i := range revList.Items {
		byName[revList.Items[i].Name] = &revList.Items[i]
	}

	var details []string
	for _, name := range added {
		rev, found := byName[name]
		if !found {
			details = append(details, fmt.Sprintf(
				"    revision %s disappeared before its content could be read", name))
			continue
		}
		details = append(details, describeAddedRevision(name, rev, byName, beforeSnap.RevisionNames))
	}
	if len(details) == 0 {
		return head
	}
	return head + "\n" + strings.Join(details, "\n")
}

// describeAddedRevision describes one added ControllerRevision: who owns it, and how
// its data differs from the newest pre-upgrade revision of the same owner.
//
// The baseline is matched by owner UID rather than by name prefix because the two
// naming schemes share prefixes: <rbg>-<hash>-<n> and <set>-<hash> both start with
// the object name, so a prefix match could pair revisions of different owners.
func describeAddedRevision(
	name string,
	rev *appsv1.ControllerRevision,
	byName map[string]*appsv1.ControllerRevision,
	beforeNames []string,
) string {
	owner := "<no owner>"
	var ownerUID types.UID
	if ref := metav1.GetControllerOfNoCopy(rev); ref != nil {
		owner = fmt.Sprintf("%s/%s", ref.Kind, ref.Name)
		ownerUID = ref.UID
	}
	out := fmt.Sprintf(
		"    revision %s: revision=%d owner=%s created=%s",
		name, rev.Revision, owner, rev.CreationTimestamp.Format(time.RFC3339))

	var baseline *appsv1.ControllerRevision
	for _, beforeName := range beforeNames {
		candidate, found := byName[beforeName]
		if !found {
			continue
		}
		if ref := metav1.GetControllerOfNoCopy(candidate); ref == nil || ref.UID != ownerUID {
			continue
		}
		if baseline == nil || candidate.Revision > baseline.Revision {
			baseline = candidate
		}
	}
	if baseline == nil {
		return out + fmt.Sprintf(
			"\n      no pre-upgrade revision of %s survives to diff against; full data:\n%s",
			owner, indentLines(string(rev.Data.Raw), "        "))
	}

	diff, err := revisionDataDiff(baseline, rev)
	if err != nil {
		return out + fmt.Sprintf("\n      could not diff against %s: %v", baseline.Name, err)
	}
	if diff == "" {
		return out + fmt.Sprintf(
			"\n      data is identical to %s; only the revision number moved", baseline.Name)
	}
	return out + fmt.Sprintf(
		"\n      diff against %s (- before + after):\n%s", baseline.Name, indentLines(diff, "        "))
}

// revisionDataDiff diffs the patch two ControllerRevisions carry. The data is JSON,
// so it is compared as parsed maps: a byte diff would report formatting, not fields.
func revisionDataDiff(before, after *appsv1.ControllerRevision) (string, error) {
	beforeMap := map[string]any{}
	if err := json.Unmarshal(before.Data.Raw, &beforeMap); err != nil {
		return "", fmt.Errorf("parsing %s: %w", before.Name, err)
	}
	afterMap := map[string]any{}
	if err := json.Unmarshal(after.Data.Raw, &afterMap); err != nil {
		return "", fmt.Errorf("parsing %s: %w", after.Name, err)
	}
	return cmp.Diff(beforeMap, afterMap), nil
}

// indentLines prefixes every line of text with indent, so a nested diff stays
// readable inside the report's own indentation.
func indentLines(text, indent string) string {
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	for i := range lines {
		lines[i] = indent + lines[i]
	}
	return strings.Join(lines, "\n")
}

// checkStillReady requires every role to be as ready as it was, the RBG's Ready
// condition to still hold, and every surviving pod to be in the phase it was in.
// lastTransitionTime is deliberately not compared: the new controller may legitimately
// rewrite conditions without changing their meaning.
//
// Phase is compared rather than required to be Running, because one fixture is
// deliberately Pending: the question is whether the upgrade moved a pod out of the
// phase it was in, not whether that phase was a healthy one.
func checkStillReady(fs *findings, before, after map[string]RBGSnapshot) {
	var problems, phaseProblems []string
	for rbgName, beforeSnap := range before {
		afterSnap, ok := after[rbgName]
		if !ok {
			continue
		}
		if beforeSnap.RBGReady && !afterSnap.RBGReady {
			problems = append(problems, fmt.Sprintf("%s: Ready condition is no longer True", rbgName))
		}
		for role, beforeReady := range beforeSnap.ReadyByRole {
			afterReady, found := afterSnap.ReadyByRole[role]
			if !found {
				problems = append(problems, fmt.Sprintf("%s: role %s has no status any more", rbgName, role))
				continue
			}
			if afterReady != beforeReady {
				problems = append(problems, fmt.Sprintf(
					"%s: role %s readyReplicas changed (%d -> %d)", rbgName, role, beforeReady, afterReady))
			}
		}
		for role := range afterSnap.ReadyByRole {
			if _, found := beforeSnap.ReadyByRole[role]; !found {
				problems = append(problems, fmt.Sprintf(
					"%s: role %s has a status it did not have before", rbgName, role))
			}
		}

		for role, beforePods := range beforeSnap.Roles {
			for podName, beforeFacts := range beforePods {
				afterFacts, found := afterSnap.Roles[role][podName]
				if !found {
					continue // reported by checkNoPodChurn
				}
				if afterFacts.Phase != beforeFacts.Phase {
					phaseProblems = append(phaseProblems, fmt.Sprintf(
						"%s/%s: pod %s went from %s to %s",
						rbgName, role, podName, beforeFacts.Phase, afterFacts.Phase))
				}
			}
		}
	}
	fs.add("roles are no longer as ready as they were before the upgrade", problems)
	// Separate from the role counts above: a pod that left Running is a pod that stopped
	// serving, which readyReplicas can hide when the controller has already replaced it.
	fs.add("pods are no longer in the phase they were in before the upgrade", phaseProblems)
}

// churnEventReasons are the event reasons that mean a pod appeared or went away.
//
// SuccessfulCreate is in here even though a create is not damage by itself. Every
// fixture is running and ready before the mark, so any create after it is churn -- and
// without it, a deletion of an object that is in neither snapshot cannot be attributed,
// which is the difference between "the upgrade destroyed something that was running"
// and "the upgrade created something it then reaped".
var churnEventReasons = map[string]bool{
	"Killing":          true,
	"SuccessfulDelete": true,
	"SuccessfulCreate": true,
	"Preempted":        true,
	"Evicted":          true,
}

// checkNoKillingEvents looks for pod churn events in the test namespace after the
// given mark. This is corroborating evidence only: events have a TTL and are
// best-effort, so it must never be the sole basis for a verdict. Its value is the
// message, which names what did the killing.
//
// skip names the RBGs whose churn a spec asked for. Filtering by name prefix is what the
// event API allows: an event names the pod or workload object it is about, not the RBG,
// and every object an RBG owns is named after it.
func checkNoKillingEvents(fs *findings, f *framework.Framework, since metav1.Time, skip []string) {
	eventList := &corev1.EventList{}
	if err := f.Client.List(f.Ctx, eventList, client.InNamespace(f.Namespace)); err != nil {
		ginkgo.GinkgoWriter.Printf("[events] could not list events: %v\n", err)
		return
	}

	var problems []string
	for i := range eventList.Items {
		ev := &eventList.Items[i]
		if !churnEventReasons[ev.Reason] {
			continue
		}
		if ownedByAny(ev.InvolvedObject.Name, skip) {
			continue
		}
		at := eventTime(ev)
		if at.Before(&since) {
			continue
		}
		problems = append(problems, fmt.Sprintf(
			"%s %s %s/%s: %s (%s)",
			at.Format(time.RFC3339), ev.Reason, ev.InvolvedObject.Kind, ev.InvolvedObject.Name,
			ev.Message, ev.Source.Component))
	}
	// Sorted because the API returns events unordered and the sequence is the point: a
	// create followed by its own delete is a transient the upgrade made, a lone delete is
	// something it destroyed. The RFC3339 prefix sorts chronologically as a string.
	sort.Strings(problems)
	fs.add("pod churn events were recorded after the upgrade started", problems)
}

// ownedByAny reports whether objName is one of the owners or an object named after one.
// The separator is required so that a fixture name is not treated as a prefix of a longer
// fixture name.
func ownedByAny(objName string, owners []string) bool {
	for _, owner := range owners {
		if objName == owner || strings.HasPrefix(objName, owner+"-") {
			return true
		}
	}
	return false
}

func eventTime(ev *corev1.Event) metav1.Time {
	if !ev.LastTimestamp.IsZero() {
		return ev.LastTimestamp
	}
	if !ev.EventTime.IsZero() {
		return metav1.NewTime(ev.EventTime.Time)
	}
	return ev.FirstTimestamp
}

// findings collects what several detectors found so they can be raised together.
// Each detector answers a different question about the same upgrade, so letting the
// first one abort the spec would hide the rest -- the opposite of what this suite is
// for.
type findings struct {
	sections []string
}

func (fs *findings) add(headline string, problems []string) {
	if len(problems) == 0 {
		return
	}
	sort.Strings(problems)
	fs.sections = append(fs.sections, fmt.Sprintf("%s:\n  - %s", headline, strings.Join(problems, "\n  - ")))
}

func (fs *findings) report() {
	gomega.Expect(fs.sections).To(gomega.BeEmpty(), strings.Join(fs.sections, "\n\n"))
}

// reportProblems fails once with every problem found, so a single run shows the full
// blast radius instead of the first pod that happened to be compared.
func reportProblems(headline string, problems []string) {
	if len(problems) == 0 {
		return
	}
	sort.Strings(problems)
	gomega.Expect(problems).To(gomega.BeEmpty(),
		"%s:\n  - %s", headline, strings.Join(problems, "\n  - "))
}

// dumpUpgradeDebugInfo prints the state the churn assertions could not express,
// including the resourceVersions those assertions deliberately ignore.
//
// It runs on an already-failed spec, so it must not raise a failure of its own: a
// second failure here would mask the one being diagnosed. Reads therefore go through
// a Gomega whose fail handler only writes to the report.
func dumpUpgradeDebugInfo(f *framework.Framework, before *map[string]RBGSnapshot) {
	w := ginkgo.GinkgoWriter
	quiet := gomega.NewGomega(func(message string, _ ...int) {
		w.Printf("[debug dump] read failed: %s\n", message)
	})

	w.Printf("\n========== Upgrade Debug Info ==========\n")
	w.Printf("namespace=%s release=%s ns=%s from=%s to=%s:%s\n",
		f.Namespace, helmRelease(), controllerNamespace(), fromTag(), toRepo(), toTag())

	if before != nil && *before != nil {
		w.Printf("\n--- snapshot taken before the upgrade ---\n")
		printSnapshots(*before)
	} else {
		w.Printf("\n--- no pre-upgrade snapshot was captured, so the failure is before phase 3 ---\n")
	}

	w.Printf("\n--- current state ---\n")
	printSnapshots(captureAll(quiet, f))

	w.Printf("\n--- controller pods in %s ---\n", controllerNamespace())
	pods := &corev1.PodList{}
	if err := f.Client.List(f.Ctx, pods, client.InNamespace(controllerNamespace())); err == nil {
		for i := range pods.Items {
			pod := &pods.Items[i]
			w.Printf("  %s phase=%s\n", pod.Name, pod.Status.Phase)
			for _, c := range pod.Spec.Containers {
				w.Printf("    container %s image=%s\n", c.Name, c.Image)
			}
		}
	}

	dumpCRDUpgradeJobs(f)
	w.Printf("\n========== End Upgrade Debug Info ==========\n")
}

func printSnapshots(snaps map[string]RBGSnapshot) {
	w := ginkgo.GinkgoWriter
	names := make([]string, 0, len(snaps))
	for name := range snaps {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		snap := snaps[name]
		w.Printf("  %s %s uid=%s generation=%d ready=%v revisions=%v\n",
			snap.Kind, snap.Name, snap.RBGUID, snap.Generation, snap.RBGReady, snap.RevisionNames)

		ownerKeys := make([]string, 0, len(snap.Owners))
		for key := range snap.Owners {
			ownerKeys = append(ownerKeys, key)
		}
		sort.Strings(ownerKeys)
		for _, key := range ownerKeys {
			w.Printf("    owner %s uid=%s generation=%d\n",
				key, snap.Owners[key].UID, snap.Owners[key].Generation)
		}

		svcNames := make([]string, 0, len(snap.Services))
		for name := range snap.Services {
			svcNames = append(svcNames, name)
		}
		sort.Strings(svcNames)
		for _, name := range svcNames {
			svc := snap.Services[name]
			w.Printf("    service %s uid=%s clusterIP=%s ports=%v\n",
				name, svc.UID, svc.ClusterIP, svc.Ports)
			w.Printf("      endpoints=%v\n", svc.Endpoints)
		}

		roles := make([]string, 0, len(snap.Roles))
		for role := range snap.Roles {
			roles = append(roles, role)
		}
		sort.Strings(roles)
		for _, role := range roles {
			w.Printf("    role %s ready=%d\n", role, snap.ReadyByRole[role])
			podNames := make([]string, 0, len(snap.Roles[role]))
			for podName := range snap.Roles[role] {
				podNames = append(podNames, podName)
			}
			sort.Strings(podNames)
			for _, podName := range podNames {
				facts := snap.Roles[role][podName]
				// Annotations are left out: the detector prints the keys that differ,
				// and a full annotation map per pod would bury everything else here.
				w.Printf("      pod %s uid=%s node=%s phase=%s rv=%s created=%s restarts=%v gates=%v\n",
					podName, facts.UID, facts.NodeName, facts.Phase, facts.ResourceVersion,
					facts.CreationTimestamp, facts.RestartCounts, facts.ReadinessGates)
				w.Printf("        labels=%v\n", facts.Labels)
			}
		}
	}
}
