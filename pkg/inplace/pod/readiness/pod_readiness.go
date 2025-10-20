package readiness

import (
	"sort"

	v1 "k8s.io/api/core/v1"
	"sigs.k8s.io/rbgs/api/workloads/v1alpha1"
	podadapter "sigs.k8s.io/rbgs/pkg/inplace/pod/clientadapter"
	util "sigs.k8s.io/rbgs/pkg/utils"
)

type Interface interface {
	GetCondition(pod *v1.Pod) *v1.PodCondition
	ContainsReadinessGate(pod *v1.Pod) bool
	ContainsNotReadyKey(pod *v1.Pod, msg Message) bool
	AddNotReadyKey(pod *v1.Pod, msg Message) (updated bool, err error)
	RemoveNotReadyKey(pod *v1.Pod, msg Message) (updated bool, err error)
}

type Message struct {
	UserAgent string `json:"userAgent"`
	Key       string `json:"key"`
}

func NewForAdapter(adp podadapter.Adapter) Interface {
	return &commonControl{adp: adp}
}

type commonControl struct {
	adp podadapter.Adapter
}

func (c *commonControl) GetCondition(pod *v1.Pod) *v1.PodCondition {
	return getReadinessCondition(pod, v1alpha1.InstancePodReadyConditionType)
}

func (c *commonControl) ContainsReadinessGate(pod *v1.Pod) bool {
	return containsReadinessGate(pod, v1alpha1.InstancePodReadyConditionType)
}

func (c *commonControl) ContainsNotReadyKey(pod *v1.Pod, msg Message) bool {
	return alreadyHasKey(pod, msg, v1alpha1.InstancePodReadyConditionType)
}

func (c *commonControl) AddNotReadyKey(pod *v1.Pod, msg Message) (bool, error) {
	return addNotReadyKey(c.adp, pod, msg, v1alpha1.InstancePodReadyConditionType)
}

func (c *commonControl) RemoveNotReadyKey(pod *v1.Pod, msg Message) (bool, error) {
	return removeNotReadyKey(c.adp, pod, msg, v1alpha1.InstancePodReadyConditionType)
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
