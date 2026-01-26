package workloads

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/rbgs/pkg/reconciler"
	"sigs.k8s.io/rbgs/pkg/utils"
	"sigs.k8s.io/yaml"
)

// ConfigBuilder builds cluster configuration for RoleBasedGroup.
type ConfigBuilder struct {
	Client    client.Client
	GroupName string
	// Roles contains all roles in the RBG for building cluster config
	Roles []*reconciler.RoleData
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

// Build generates the cluster configuration YAML.
func (b *ConfigBuilder) Build() ([]byte, error) {
	roles, err := b.buildRolesInfo()
	if err != nil {
		return nil, err
	}

	config := ClusterConfig{
		Group: GroupInfo{
			Name:  b.GroupName,
			Size:  len(b.Roles),
			Roles: b.getRoleNames(),
		},
		Roles: roles,
	}
	return yaml.Marshal(config)
}

func (b *ConfigBuilder) getRoleNames() []string {
	names := make([]string, 0, len(b.Roles))
	for _, r := range b.Roles {
		names = append(names, r.Spec.Name)
	}
	return names
}

func (b *ConfigBuilder) buildRolesInfo() (RolesInfo, error) {
	roles := make(RolesInfo)
	for _, roleData := range b.Roles {
		instances, err := b.buildInstances(roleData)
		if err != nil {
			return nil, err
		}
		roles[roleData.Spec.Name] = RoleInstances{
			Size:      int(*roleData.Spec.Replicas),
			Instances: instances,
		}
	}
	return roles, nil
}

func (b *ConfigBuilder) buildInstances(roleData *reconciler.RoleData) ([]Instance, error) {
	instances := make([]Instance, 0, *roleData.Spec.Replicas)
	serviceName, err := getCompatibleHeadlessServiceNameFromRoleData(context.TODO(), b.Client, roleData)
	if err != nil {
		return nil, fmt.Errorf("GetCompatibleHeadlessServiceName error: %s", err.Error())
	}

	for i := 0; i < int(*roleData.Spec.Replicas); i++ {
		instance := Instance{
			Address: fmt.Sprintf("%s-%d.%s", roleData.Spec.Name, i, serviceName),
			Ports:   make(map[string]int32),
		}

		for _, port := range roleData.Spec.ServicePorts {
			portName := generatePortKey(port)
			instance.Ports[portName] = port.Port
		}

		instances = append(instances, instance)
	}
	return instances, nil
}

// getCompatibleHeadlessServiceNameFromRoleData returns the compatible headless service name from roleData.
// It checks if the old service name (workloadName) exists, otherwise uses the new service name.
func getCompatibleHeadlessServiceNameFromRoleData(
	ctx context.Context, kclient client.Client, roleData *reconciler.RoleData,
) (string, error) {
	svc := &corev1.Service{}
	err := kclient.Get(ctx, types.NamespacedName{Name: roleData.WorkloadName, Namespace: roleData.OwnerInfo.Namespace}, svc)
	if err == nil {
		// if oldService exists, use old ServiceName (workloadName)
		return roleData.WorkloadName, nil
	} else {
		// if oldService not exists, use new ServiceName
		if apierrors.IsNotFound(err) {
			metadata := reconciler.Metadata{
				Name:      roleData.OwnerInfo.Name,
				Namespace: roleData.OwnerInfo.Namespace,
				UID:       roleData.OwnerInfo.UID,
			}
			return metadata.GetServiceName(roleData.Spec), nil
		}
	}
	return "", err
}

func generatePortKey(port corev1.ServicePort) string {
	if port.Name != "" {
		return strings.ToLower(strings.ReplaceAll(port.Name, "-", "_"))
	}
	return fmt.Sprintf("port%d", port.Port)
}

// SemanticallyEqualConfigmap compares two ConfigMaps semantically, ignoring system annotations and metadata.
func SemanticallyEqualConfigmap(old, new *corev1.ConfigMap) (bool, string) {
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
		cmpopts.SortSlices(
			func(a, b metav1.OwnerReference) bool {
				return a.UID < b.UID // Make OwnerReferences comparison order-insensitive
			},
		),
		cmpopts.EquateEmpty(),
	}

	diff := cmp.Diff(oldCopy, newCopy, opts)
	return diff == "", diff
}
