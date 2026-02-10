package benchmark

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

// buildDashboardPod creates a Pod specification for the benchmark results dashboard.
func buildDashboardPod(namespace string, pvcComponents *PVCComponents) *corev1.Pod {
	labels := map[string]string{
		dashboardLabelKey: dashboardLabelValue,
	}

	volumeMount := corev1.VolumeMount{
		Name:      dashboardDataVolumeName,
		MountPath: dashboardDataMountPath,
		ReadOnly:  true,
	}
	if pvcComponents.SubPath != "" {
		volumeMount.SubPath = pvcComponents.SubPath
	}

	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "rbg-benchmark-dashboard-",
			Namespace:    namespace,
			Labels:       labels,
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name:    dashboardContainerName,
					Image:   dashboardOpts.image,
					Command: []string{"/benchmark-dashboard"},
					Args:    []string{"--data-dir", dashboardDataMountPath, "--port", "8080"},
					Ports: []corev1.ContainerPort{
						{
							Name:          "http",
							ContainerPort: int32(defaultDashboardPort),
							Protocol:      corev1.ProtocolTCP,
						},
					},
					VolumeMounts: []corev1.VolumeMount{volumeMount},
					Resources: corev1.ResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("100m"),
							corev1.ResourceMemory: resource.MustParse("128Mi"),
						},
						Limits: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("500m"),
							corev1.ResourceMemory: resource.MustParse("512Mi"),
						},
					},
					ReadinessProbe: &corev1.Probe{
						ProbeHandler: corev1.ProbeHandler{
							TCPSocket: &corev1.TCPSocketAction{
								Port: intstr.FromInt(defaultDashboardPort),
							},
						},
						InitialDelaySeconds: 2,
						PeriodSeconds:       3,
					},
				},
			},
			Volumes: []corev1.Volume{
				{
					Name: dashboardDataVolumeName,
					VolumeSource: corev1.VolumeSource{
						PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
							ClaimName: pvcComponents.PVCName,
							ReadOnly:  true,
						},
					},
				},
			},
			RestartPolicy: corev1.RestartPolicyNever,
		},
	}
}
