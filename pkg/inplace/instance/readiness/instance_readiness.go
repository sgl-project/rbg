/*
Copyright 2025.

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

package readiness

import (
	"sort"

	"sigs.k8s.io/controller-runtime/pkg/client"

	appsv1alpha1 "sigs.k8s.io/rbgs/api/workloads/v1alpha1"
	"sigs.k8s.io/rbgs/pkg/inplace/instance/clientdapter"
	util "sigs.k8s.io/rbgs/pkg/utils"
)

type Interface interface {
	AddNotReadyKey(instance *appsv1alpha1.Instance, msg Message) error
	RemoveNotReadyKey(instance *appsv1alpha1.Instance, msg Message) error
}

func New(c client.Client) Interface {
	return &commonControl{adp: &clientdapter.AdapterRuntimeClient{Client: c}}
}

func NewForAdapter(adp clientdapter.Adapter) Interface {
	return &commonControl{adp: adp}
}

type commonControl struct {
	adp clientdapter.Adapter
}

func (c *commonControl) AddNotReadyKey(instance *appsv1alpha1.Instance, msg Message) error {
	return addNotReadyKey(c.adp, instance, msg, appsv1alpha1.InstanceCustomReady)
}

func (c *commonControl) RemoveNotReadyKey(instance *appsv1alpha1.Instance, msg Message) error {
	return removeNotReadyKey(c.adp, instance, msg, appsv1alpha1.InstanceCustomReady)
}

type Message struct {
	UserAgent string `json:"userAgent"`
	Key       string `json:"key"`
}

type messageList []Message

func (c messageList) Len() int      { return len(c) }
func (c messageList) Swap(i, j int) { c[i], c[j] = c[j], c[i] }
func (c messageList) Less(i, j int) bool {
	if c[i].UserAgent == c[j].UserAgent {
		return c[i].Key < c[j].Key
	}
	return c[i].UserAgent < c[j].UserAgent
}

func (c messageList) dump() string {
	sort.Sort(c)
	return util.DumpJSON(c)
}
