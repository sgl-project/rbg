package benchmark

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
)

func TestBuildDashboardPod(t *testing.T) {
	// Save and restore global dashboardOpts
	old := dashboardOpts
	defer func() { dashboardOpts = old }()

	dashboardOpts = DashboardOptions{
		image: "my-dashboard:latest",
	}

	t.Run("basic pod without sub-path", func(t *testing.T) {
		pvc := &PVCComponents{
			PVCName: "benchmark-pvc",
		}

		pod := buildDashboardPod("test-ns", pvc)

		assert.Equal(t, "test-ns", pod.Namespace)
		assert.Equal(t, "rbg-benchmark-dashboard-", pod.GenerateName)
		assert.Equal(t, dashboardLabelValue, pod.Labels[dashboardLabelKey])

		// Check container
		require.Len(t, pod.Spec.Containers, 1)
		container := pod.Spec.Containers[0]
		assert.Equal(t, dashboardContainerName, container.Name)
		assert.Equal(t, "my-dashboard:latest", container.Image)
		assert.Equal(t, []string{"/benchmark-dashboard"}, container.Command)
		assert.Equal(t, []string{"--data-dir", dashboardDataMountPath, "--port", "8080"}, container.Args)

		// Check ports
		require.Len(t, container.Ports, 1)
		assert.Equal(t, int32(defaultDashboardPort), container.Ports[0].ContainerPort)
		assert.Equal(t, corev1.ProtocolTCP, container.Ports[0].Protocol)

		// Check volume mount
		require.Len(t, container.VolumeMounts, 1)
		assert.Equal(t, dashboardDataVolumeName, container.VolumeMounts[0].Name)
		assert.Equal(t, dashboardDataMountPath, container.VolumeMounts[0].MountPath)
		assert.True(t, container.VolumeMounts[0].ReadOnly)
		assert.Empty(t, container.VolumeMounts[0].SubPath)

		// Check volume
		require.Len(t, pod.Spec.Volumes, 1)
		assert.Equal(t, dashboardDataVolumeName, pod.Spec.Volumes[0].Name)
		assert.Equal(t, "benchmark-pvc", pod.Spec.Volumes[0].VolumeSource.PersistentVolumeClaim.ClaimName)
		assert.True(t, pod.Spec.Volumes[0].VolumeSource.PersistentVolumeClaim.ReadOnly)

		// Check restart policy
		assert.Equal(t, corev1.RestartPolicyNever, pod.Spec.RestartPolicy)

		// Check readiness probe
		require.NotNil(t, container.ReadinessProbe)
		assert.Equal(t, int32(2), container.ReadinessProbe.InitialDelaySeconds)
		assert.Equal(t, int32(3), container.ReadinessProbe.PeriodSeconds)

		// Check resources
		assert.Equal(t, "100m", container.Resources.Requests.Cpu().String())
		assert.Equal(t, "128Mi", container.Resources.Requests.Memory().String())
		assert.Equal(t, "500m", container.Resources.Limits.Cpu().String())
		assert.Equal(t, "512Mi", container.Resources.Limits.Memory().String())
	})

	t.Run("pod with sub-path", func(t *testing.T) {
		pvc := &PVCComponents{
			PVCName: "benchmark-pvc",
			SubPath: "experiments/run1",
		}

		pod := buildDashboardPod("test-ns", pvc)

		require.Len(t, pod.Spec.Containers, 1)
		require.Len(t, pod.Spec.Containers[0].VolumeMounts, 1)
		assert.Equal(t, "experiments/run1", pod.Spec.Containers[0].VolumeMounts[0].SubPath)
	})

}
