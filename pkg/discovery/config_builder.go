package discovery

import (
	"fmt"
	"strings"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/rbgs/pkg/utils"

	"sigs.k8s.io/yaml"

	corev1 "k8s.io/api/core/v1"
	workloadsv1alpha1 "sigs.k8s.io/rbgs/api/workloads/v1alpha1"
)

type ConfigBuilder struct {
	rbg  *workloadsv1alpha1.RoleBasedGroup
	role *workloadsv1alpha1.RoleSpec
}

type ClusterConfig struct {
	Group GroupInfo `json:"group"`
	Roles RolesInfo `json:"roles"`
}

type GroupInfo struct {
	Name  string   `json:"name"`
	Size  int      `json:"size"`
	Roles []string `json:"roles"`
}

type RolesInfo map[string]RoleInstances

type RoleInstances struct {
	Size      int        `json:"size"`
	Instances []Instance `json:"instances"`
}

type Instance struct {
	Address string           `json:"address"`
	Ports   map[string]int32 `json:"ports,omitempty"` // Key: port name, Value: port number
}

func (b *ConfigBuilder) Build() ([]byte, error) {
	config := ClusterConfig{
		Group: GroupInfo{
			Name:  b.rbg.Name,
			Size:  len(b.rbg.Spec.Roles),
			Roles: b.getRoleNames(),
		},
		Roles: b.buildRolesInfo(),
	}
	return yaml.Marshal(config)
}

func (b *ConfigBuilder) getRoleNames() []string {
	names := make([]string, 0, len(b.rbg.Spec.Roles))
	for _, r := range b.rbg.Spec.Roles {
		names = append(names, r.Name)
	}
	return names
}

func (b *ConfigBuilder) buildRolesInfo() RolesInfo {
	roles := make(RolesInfo)
	for _, role := range b.rbg.Spec.Roles {
		roles[role.Name] = RoleInstances{
			Size:      int(*role.Replicas),
			Instances: b.buildInstances(&role),
		}
	}
	return roles
}

func (b *ConfigBuilder) buildInstances(role *workloadsv1alpha1.RoleSpec) []Instance {
	instances := make([]Instance, 0, *role.Replicas)
	serviceName := b.rbg.GetWorkloadName(role)

	for i := 0; i < int(*role.Replicas); i++ {
		instance := Instance{
			Address: fmt.Sprintf("%s-%d.%s", role.Name, i, serviceName),
			Ports:   make(map[string]int32),
		}

		for _, port := range role.ServicePorts {
			portName := generatePortKey(port)
			instance.Ports[portName] = port.Port
		}

		instances = append(instances, instance)
	}
	return instances
}

func generatePortKey(port corev1.ServicePort) string {
	if port.Name != "" {
		return strings.ToLower(strings.ReplaceAll(port.Name, "-", "_"))
	}
	return fmt.Sprintf("port%d", port.Port)
}

func semanticallyEqualConfigmap(old, new *corev1.ConfigMap) (bool, string) {
	if old == nil && new == nil {
		return true, ""
	}
	if old == nil || new == nil {
		return false, fmt.Sprintf("nil mismatch: old=%v, new=%v", old, new)
	}
	// Defensive copy to prevent side effects
	oldCopy := old.DeepCopy()
	newCopy := new.DeepCopy()

	oldCopy.Annotations = utils.FilterSystemAnnotations(oldCopy.Annotations)
	newCopy.Annotations = utils.FilterSystemAnnotations(newCopy.Annotations)

	objectMetaIgnoreOpts := cmpopts.IgnoreFields(
		metav1.ObjectMeta{},
		"ResourceVersion",
		"UID",
		"CreationTimestamp",
		"Generation",
		"ManagedFields",
		"SelfLink",
	)

	opts := cmp.Options{
		objectMetaIgnoreOpts,
		cmpopts.SortSlices(func(a, b metav1.OwnerReference) bool {
			return a.UID < b.UID // Make OwnerReferences comparison order-insensitive
		}),
		cmpopts.EquateEmpty(),
	}

	diff := cmp.Diff(oldCopy, newCopy, opts)
	return diff == "", diff
}
