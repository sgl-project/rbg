package applyconfiguration

import (
	"time"

	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	metaapplyv1 "k8s.io/client-go/applyconfigurations/meta/v1"
	"sigs.k8s.io/rbgs/api/workloads/v1alpha1"
	"sigs.k8s.io/rbgs/pkg/utils"
)

type RbgScalingAdapterApplyConfiguration struct {
	metaapplyv1.TypeMetaApplyConfiguration    `json:",inline"`
	*metaapplyv1.ObjectMetaApplyConfiguration `json:"metadata,omitempty"`

	Spec   *RbgScalingAdapterSpecApplyConfiguration   `json:"spec,omitempty"`
	Status *RbgScalingAdapterStatusApplyConfiguration `json:"status,omitempty"`
}

func RoleBasedGroupScalingAdapter(
	rbgScalingAdapter *v1alpha1.RoleBasedGroupScalingAdapter,
) *RbgScalingAdapterApplyConfiguration {
	gkv := utils.GetRbgScalingAdapterGVK()
	b := &RbgScalingAdapterApplyConfiguration{}
	b.WithName(rbgScalingAdapter.Name)
	b.WithNamespace(rbgScalingAdapter.Namespace)
	b.WithKind(gkv.Kind)
	b.WithAPIVersion(gkv.GroupVersion().String())
	b.Spec = &RbgScalingAdapterSpecApplyConfiguration{
		Replicas:       rbgScalingAdapter.Spec.Replicas,
		ScaleTargetRef: rbgScalingAdapter.Spec.ScaleTargetRef,
	}
	return b
}

func (b *RbgScalingAdapterApplyConfiguration) IsApplyConfiguration() {}

func (b *RbgScalingAdapterApplyConfiguration) WithAPIVersion(value string) *RbgScalingAdapterApplyConfiguration {
	b.TypeMetaApplyConfiguration.APIVersion = &value
	return b
}

func (b *RbgScalingAdapterApplyConfiguration) WithKind(value string) *RbgScalingAdapterApplyConfiguration {
	b.TypeMetaApplyConfiguration.Kind = &value
	return b
}

func (b *RbgScalingAdapterApplyConfiguration) WithNamespace(value string) *RbgScalingAdapterApplyConfiguration {
	b.ensureObjectMetaApplyConfigurationExists()
	b.ObjectMetaApplyConfiguration.Namespace = &value
	return b
}

func (b *RbgScalingAdapterApplyConfiguration) WithName(value string) *RbgScalingAdapterApplyConfiguration {
	b.ensureObjectMetaApplyConfigurationExists()
	b.ObjectMetaApplyConfiguration.Name = &value
	return b
}

func (b *RbgScalingAdapterApplyConfiguration) WithOwnerReferences(
	values ...*metaapplyv1.OwnerReferenceApplyConfiguration,
) *RbgScalingAdapterApplyConfiguration {
	b.ensureObjectMetaApplyConfigurationExists()
	for i := range values {
		if values[i] == nil {
			panic("nil value passed to WithOwnerReferences")
		}
		b.ObjectMetaApplyConfiguration.OwnerReferences = append(
			b.ObjectMetaApplyConfiguration.OwnerReferences, *values[i],
		)
	}
	return b
}

type RbgScalingAdapterSpecApplyConfiguration struct {
	Replicas       *int32                          `json:"replicas,omitempty"`
	ScaleTargetRef *v1alpha1.AdapterScaleTargetRef `json:"scaleTargetRef"`
}

func RbgScalingAdapterSpec(
	spec v1alpha1.RoleBasedGroupScalingAdapterSpec,
) *RbgScalingAdapterSpecApplyConfiguration {
	return &RbgScalingAdapterSpecApplyConfiguration{
		Replicas:       spec.Replicas,
		ScaleTargetRef: spec.ScaleTargetRef,
	}
}

func (b *RbgScalingAdapterApplyConfiguration) WithSpec(
	value *RbgScalingAdapterSpecApplyConfiguration,
) *RbgScalingAdapterApplyConfiguration {
	b.Spec = value
	return b
}

func (b *RbgScalingAdapterSpecApplyConfiguration) WithReplicas(
	replicas *int32,
) *RbgScalingAdapterSpecApplyConfiguration {
	if replicas == nil {
		return b
	}
	b.Replicas = replicas
	return b
}

func (b *RbgScalingAdapterApplyConfiguration) WithStatus(
	value *RbgScalingAdapterStatusApplyConfiguration,
) *RbgScalingAdapterApplyConfiguration {
	b.Status = value
	return b
}

func (b *RbgScalingAdapterApplyConfiguration) ensureObjectMetaApplyConfigurationExists() {
	if b.ObjectMetaApplyConfiguration == nil {
		b.ObjectMetaApplyConfiguration = &metaapplyv1.ObjectMetaApplyConfiguration{}
	}
}

type RbgScalingAdapterStatusApplyConfiguration struct {
	Replicas      *int32                `json:"replicas,omitempty"`
	Phase         v1alpha1.AdapterPhase `json:"phase,omitempty"`
	Selector      string                `json:"selector,omitempty"`
	LastScaleTime *v1.Time              `json:"lastScaleTime,omitempty"`
}

func RbgScalingAdapterStatus(
	status v1alpha1.RoleBasedGroupScalingAdapterStatus,
) *RbgScalingAdapterStatusApplyConfiguration {
	return &RbgScalingAdapterStatusApplyConfiguration{
		Replicas:      status.Replicas,
		Phase:         status.Phase,
		Selector:      status.Selector,
		LastScaleTime: status.LastScaleTime,
	}
}

func (b *RbgScalingAdapterStatusApplyConfiguration) WithPhase(
	phase v1alpha1.AdapterPhase,
) *RbgScalingAdapterStatusApplyConfiguration {
	b.Phase = phase
	return b
}

func (b *RbgScalingAdapterStatusApplyConfiguration) WithSelector(
	selector string,
) *RbgScalingAdapterStatusApplyConfiguration {
	b.Selector = selector
	return b
}

func (b *RbgScalingAdapterStatusApplyConfiguration) WithReplicas(
	replicas *int32, scale bool,
) *RbgScalingAdapterStatusApplyConfiguration {
	if replicas == nil {
		return b
	}
	b.Replicas = replicas
	if scale {
		now := v1.NewTime(time.Now())
		b.LastScaleTime = &now
	}
	return b
}
