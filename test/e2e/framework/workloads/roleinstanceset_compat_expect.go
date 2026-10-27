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

	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"sigs.k8s.io/rbgs/api/workloads/constants"
	"sigs.k8s.io/rbgs/api/workloads/v1alpha1"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	"sigs.k8s.io/rbgs/test/utils"
)

// RoleInstanceSetCompatChecker lets v1alpha1 e2e fixtures explicitly select the
// v1alpha2 RoleInstanceSet workload while retaining the v1alpha1 assertion API.
type RoleInstanceSetCompatChecker struct {
	ctx    context.Context
	client client.Client
}

var _ WorkloadEqualChecker = &RoleInstanceSetCompatChecker{}

func NewRoleInstanceSetCompatChecker(ctx context.Context, c client.Client) *RoleInstanceSetCompatChecker {
	return &RoleInstanceSetCompatChecker{ctx: ctx, client: c}
}

func (s *RoleInstanceSetCompatChecker) get(
	rbg *v1alpha1.RoleBasedGroup, role v1alpha1.RoleSpec,
) (*workloadsv1alpha2.RoleInstanceSet, error) {
	obj := &workloadsv1alpha2.RoleInstanceSet{}
	err := s.client.Get(s.ctx, client.ObjectKey{
		Name:      rbg.GetWorkloadName(&role),
		Namespace: rbg.Namespace,
	}, obj)
	if err != nil {
		return nil, err
	}
	return obj, nil
}

func (s *RoleInstanceSetCompatChecker) listPods(
	rbg *v1alpha1.RoleBasedGroup, role v1alpha1.RoleSpec,
) (*corev1.PodList, error) {
	pods := &corev1.PodList{}
	err := s.client.List(s.ctx, pods,
		client.InNamespace(rbg.Namespace),
		client.MatchingLabels{
			constants.GroupNameLabelKey: rbg.Name,
			constants.RoleNameLabelKey:  role.Name,
		},
	)
	return pods, err
}

func (s *RoleInstanceSetCompatChecker) ExpectWorkloadEqual(
	rbg *v1alpha1.RoleBasedGroup, role v1alpha1.RoleSpec,
) error {
	newRbg := &workloadsv1alpha2.RoleBasedGroup{}
	err := s.client.Get(s.ctx, client.ObjectKey{Name: rbg.Name, Namespace: rbg.Namespace}, newRbg)
	if err != nil {
		return fmt.Errorf("failed to get v1alpha2 RoleBasedGroup: %w", err)
	}
	roleStatus, found := newRbg.GetRoleStatus(role.Name)
	if !found {
		return fmt.Errorf("role %s status not found", role.Name)
	}
	desired := int32(1)
	if role.Replicas != nil {
		desired = *role.Replicas
	}
	if roleStatus.ReadyReplicas != desired {
		return fmt.Errorf("role %s not ready: readyReplicas=%d, desired=%d", role.Name, roleStatus.ReadyReplicas, desired)
	}
	return nil
}

func (s *RoleInstanceSetCompatChecker) ExpectLabelContains(
	rbg *v1alpha1.RoleBasedGroup, role v1alpha1.RoleSpec, labels ...map[string]string,
) error {
	obj, err := s.get(rbg, role)
	if err != nil {
		return fmt.Errorf("failed to get RoleInstanceSet: %w", err)
	}
	if len(labels) == 0 {
		return fmt.Errorf("labels is empty")
	}
	for _, labelMap := range labels {
		for key, value := range labelMap {
			if !utils.MapContains(obj.Labels, key, value) {
				return fmt.Errorf("RoleInstanceSet labels do not have key %s, value: %s", key, value)
			}
		}
	}
	return nil
}

func (s *RoleInstanceSetCompatChecker) ExpectPodTemplateLabelContains(
	rbg *v1alpha1.RoleBasedGroup, role v1alpha1.RoleSpec, labels ...map[string]string,
) error {
	return s.expectPodMetadataContains(rbg, role, labels, true)
}

func (s *RoleInstanceSetCompatChecker) ExpectPodTemplateAnnotationContains(
	rbg *v1alpha1.RoleBasedGroup, role v1alpha1.RoleSpec, annotations ...map[string]string,
) error {
	return s.expectPodMetadataContains(rbg, role, annotations, false)
}

func (s *RoleInstanceSetCompatChecker) expectPodMetadataContains(
	rbg *v1alpha1.RoleBasedGroup, role v1alpha1.RoleSpec, maps []map[string]string, labels bool,
) error {
	obj, err := s.get(rbg, role)
	if err != nil {
		return fmt.Errorf("failed to get RoleInstanceSet: %w", err)
	}
	if len(maps) == 0 {
		return fmt.Errorf("metadata maps is empty")
	}

	// Leader-worker roles preserve the previous LeaderWorkerSet checker contract:
	// one map targets the worker component, two maps target the leader and worker
	// components respectively. Standalone roles check the single component named
	// after the role, exactly like the former Deployment/StatefulSet checkers.
	var targetComponents []string
	if role.LeaderWorkerSet != nil {
		if len(maps) > 2 {
			return fmt.Errorf("at most two metadata maps are supported for leader-worker roles, got %d", len(maps))
		}
		targetComponents = make([]string, 0, len(maps))
		if len(maps) == 2 {
			targetComponents = append(targetComponents, string(constants.LeaderComponentType))
		}
		targetComponents = append(targetComponents, string(constants.WorkerComponentType))
	} else {
		targetComponents = make([]string, len(maps))
		for i := range targetComponents {
			targetComponents[i] = role.Name
		}
	}

	for i, metadataMap := range maps {
		component, err := findRoleInstanceComponent(obj, targetComponents[i])
		if err != nil {
			return err
		}
		for key, value := range metadataMap {
			if labels {
				if !utils.MapContains(component.Template.Labels, key, value) {
					return fmt.Errorf(
						"component %s template labels missing key=%s value=%s",
						component.Name, key, value,
					)
				}
				continue
			}
			if !utils.MapContains(component.Template.Annotations, key, value) {
				return fmt.Errorf(
					"component %s template annotations missing key=%s value=%s",
					component.Name, key, value,
				)
			}
		}
	}
	return nil
}

// findRoleInstanceComponent returns the component with the given name from the
// RoleInstanceSet's role instance template.
func findRoleInstanceComponent(
	obj *workloadsv1alpha2.RoleInstanceSet, name string,
) (*workloadsv1alpha2.RoleInstanceComponent, error) {
	for i := range obj.Spec.RoleInstanceTemplate.Components {
		component := &obj.Spec.RoleInstanceTemplate.Components[i]
		if component.Name == name {
			return component, nil
		}
	}
	return nil, fmt.Errorf("component %s not found in RoleInstanceSet %s/%s", name, obj.Namespace, obj.Name)
}

func (s *RoleInstanceSetCompatChecker) ExpectWorkloadNotExist(
	rbg *v1alpha1.RoleBasedGroup, role v1alpha1.RoleSpec,
) error {
	obj := &workloadsv1alpha2.RoleInstanceSet{}
	err := s.client.Get(s.ctx, client.ObjectKey{
		Name:      rbg.GetWorkloadName(&role),
		Namespace: rbg.Namespace,
	}, obj)
	if err == nil {
		return fmt.Errorf("RoleInstanceSet still exists")
	}
	if !apierrors.IsNotFound(err) {
		return err
	}
	return nil
}

func (s *RoleInstanceSetCompatChecker) ExpectTopologyAffinity(
	rbg *v1alpha1.RoleBasedGroup, role v1alpha1.RoleSpec, topologyKey string,
) error {
	var pods *corev1.PodList
	gomega.Eventually(
		func() error {
			listed, err := s.listPods(rbg, role)
			if err != nil {
				return err
			}
			if len(listed.Items) == 0 {
				return fmt.Errorf("no pods found for role %s", role.Name)
			}
			pods = listed
			return nil
		}, utils.Timeout, utils.Interval,
	).Should(gomega.Succeed())

	rbgTopologyKey, found := rbg.GetExclusiveKey()
	if !found {
		return fmt.Errorf("exclusive key not found")
	}
	if rbgTopologyKey != topologyKey {
		return fmt.Errorf("topologyKey not equal, got: %s, expect: %s", rbgTopologyKey, topologyKey)
	}
	if len(pods.Items) == 0 {
		return fmt.Errorf("no pods found for role %s", role.Name)
	}
	pod := pods.Items[0]
	if pod.Spec.Affinity == nil {
		return fmt.Errorf("pod affinity is nil")
	}
	return semanticallyEqualAffinity(pod.Spec.Affinity, topologyKey, rbg.GenGroupUniqueKey())
}
