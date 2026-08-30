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
	ReadyByRole   map[string]int32
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
			out[source.kind+"/"+accessor.GetName()] = ownerFacts{
				UID:        accessor.GetUID(),
				Generation: accessor.GetGeneration(),
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

	checkSnapshotDiff(fs, before, after, rec)
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
			checkSnapshotDiff(fs, exclude(first, skip...), exclude(latest, skip...), recordedRewrites{})
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
func checkSnapshotDiff(fs *findings, before, after map[string]RBGSnapshot, rec recordedRewrites) {
	checkSameRBGSet(fs, before, after)
	checkNoPodChurn(fs, before, after)
	checkNoRestarts(fs, before, after)
	checkPodMetadataStable(fs, before, after)
	checkServicesStable(fs, before, after, rec.leaderOnlyServices)
	checkOwnersStable(fs, before, after, rec.generationBumps)
	checkNoRevisionExplosion(fs, before, after)
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
// record for the action it spans -- upgradeRewrites across the hop,
// controllerStartRewrites across a controller restart, the zero value across an action
// that must change nothing -- so a change recorded for one action is not silently
// tolerated for the others.
//
// An entry is only allowed here with the change it stands for named. A difference nobody
// can attribute is a finding to report, not an entry to add.
type recordedRewrites struct {
	// generationBumps is how many times each owner kind's spec is rewritten per
	// controller process start within the interval. A kind that is absent expects zero,
	// which is what every kind but one now sees.
	//
	// It is per start rather than per interval because the one entry there is fires on
	// every controller start; a comparison that spans several scales it with
	// acrossStarts. The count is asserted exactly rather than required to be zero so
	// that a known rewrite does not have to be reported every run.
	generationBumps map[string]int64

	// leaderOnlyServices are the shared Services whose selector the upgrade narrows to
	// the leader component. Only the exact narrowing is folded: it must be visible in
	// this Service's selector diff, the added selector key must be
	// component-name=leader and nothing else, no endpoint may be added, and every
	// endpoint that left must belong to a pod that is not a leader.
	leaderOnlyServices map[string]bool
}

// acrossStarts scales the per-start rewrites to an interval containing that many
// controller starts, so a comparison against the pre-upgrade snapshot stays exact as
// later phases restart the controller. leaderOnlyServices is a one-off narrowing of a
// selector and is carried through unscaled: once narrowed it stays narrowed.
func (r recordedRewrites) acrossStarts(starts int64) recordedRewrites {
	scaled := make(map[string]int64, len(r.generationBumps))
	for kind, perStart := range r.generationBumps {
		scaled[kind] = perStart * starts
	}
	return recordedRewrites{generationBumps: scaled, leaderOnlyServices: r.leaderOnlyServices}
}

// controllerStartRewrites is what starting a controller process rewrites, on this
// release and on v0.7.0 alike. It is not upgrade behavior, so it is recorded separately
// from what the hop itself does and passed by every comparison that spans a controller
// start.
var controllerStartRewrites = recordedRewrites{
	generationBumps: map[string]int64{
		// lwsSpecEqual can never report equal for a role that leaves
		// rolloutStrategy.rollingUpdate unset, which every leader-worker fixture here
		// does. The apply configuration then omits spec.rolloutStrategy entirely while
		// the LeaderWorkerSet CRD defaults rollingUpdateConfiguration to
		// {maxSurge: 0, maxUnavailable: 1, partition: 0}, and the reflect.DeepEqual at
		// lws_reconciler.go:423 compares that against a nil configuration and reports
		// "RolloutStrategy not equal". So the reconciler patches on every reconcile,
		// and a converged RoleBasedGroup only reconciles when a controller process
		// starts -- which is why this reads as exactly one rewrite per start.
		//
		// The patch changes no field of the stored spec: the `rbg` field manager does
		// not own spec.rolloutStrategy, so nothing is added or removed and no pod
		// moves. Only generation advances. v0.7.0 carries the same comparison, so the
		// upgrade neither introduces nor fixes this; it is a product bug to report on
		// its own, not an upgrade regression.
		"LeaderWorkerSet": 1,
	},
}

// upgradeRewrites records the v0.7.0 -> current changes, one entry per change, plus
// what any controller start does: helm upgrade replaces the controller pods.
//
// RoleInstanceSet and RoleInstance generations were both 1 while the reconciler rewrote
// the stored restartPolicy string into restartPolicyConfig. That rewrite moved the
// RoleInstanceSet revision hash and rolled every role on upgrade, and the RoleInstance
// bump was its consequence: only the resulting in-place update reached the code that
// adds the RoleInstanceInPlaceUpdateReady gate. With the reconciler no longer touching
// the template, both kinds are left alone entirely, so neither belongs here.
var upgradeRewrites = recordedRewrites{
	generationBumps: controllerStartRewrites.generationBumps,

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

// checkOwnersStable checks the workload objects behind each role. A generation bump
// means the new controller rewrote the spec, which is the earliest signal of a
// revision hash change and shows up before any pod is actually replaced.
//
// The root object itself is held to the same standard, under its own kind's bumps key:
// what the controller hashes is the RBG spec, so a rewrite there reaches every role at
// once. A RoleBasedGroupSet root has no recorded bump at all, so any rewrite of the
// object that would restamp its children is reported.
func checkOwnersStable(fs *findings, before, after map[string]RBGSnapshot, bumps map[string]int64) {
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
		if got, want := afterSnap.Generation-beforeSnap.Generation, bumps[beforeSnap.Kind]; got != want {
			problems = append(problems, fmt.Sprintf(
				"%s: %s spec was rewritten %d time(s) (generation %d -> %d), not the %d this "+
					"comparison expects",
				rbgName, beforeSnap.Kind, got, beforeSnap.Generation, afterSnap.Generation, want))
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
			kind, _, _ := strings.Cut(key, "/")
			got := afterOwner.Generation - beforeOwner.Generation
			if want := bumps[kind]; got != want {
				problems = append(problems, fmt.Sprintf(
					"%s: owner %s spec was rewritten %d time(s) (generation %d -> %d), not the %d "+
						"this comparison expects for %s; more means a rewrite nobody has "+
						"attributed yet, fewer means the recorded one is gone and "+
						"upgradeRewrites.generationBumps is stale",
					rbgName, key, got, beforeOwner.Generation, afterOwner.Generation, want, kind))
			}
		}
		for key := range afterSnap.Owners {
			if _, found := beforeSnap.Owners[key]; !found {
				problems = append(problems, fmt.Sprintf("%s: new owner %s appeared", rbgName, key))
			}
		}
	}
	fs.add("workload objects were replaced, or rewritten a different number of times than recorded", problems)
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
func checkNoRevisionExplosion(fs *findings, before, after map[string]RBGSnapshot) {
	var problems []string
	for rbgName, beforeSnap := range before {
		afterSnap, ok := after[rbgName]
		if !ok {
			continue
		}
		if strings.Join(beforeSnap.RevisionNames, ",") == strings.Join(afterSnap.RevisionNames, ",") {
			continue
		}
		problems = append(problems, fmt.Sprintf(
			"%s: ControllerRevisions changed, so the revision hash may have changed"+
				"\n    added:   %v\n    removed: %v\n    before:  %v\n    after:   %v",
			rbgName,
			missingFrom(afterSnap.RevisionNames, beforeSnap.RevisionNames),
			missingFrom(beforeSnap.RevisionNames, afterSnap.RevisionNames),
			beforeSnap.RevisionNames, afterSnap.RevisionNames))
	}
	// The name encodes which layer produced it: the RBG layer names revisions
	// <rbg>-<hash>-<n>, the RoleInstanceSet layer names them <set>-<hash>. Both carry
	// GroupNameLabelKey, so this list spans both and the added names say which moved.
	fs.add("ControllerRevisions changed across the upgrade", problems)
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
