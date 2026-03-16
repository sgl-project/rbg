package port_allocator

import workloadsv1alpha1 "sigs.k8s.io/rbgs/api/workloads/v1alpha1"

const (
	portAllocatorAnnotationKey string = workloadsv1alpha1.RBGPrefix + "port-allocator"
)
