package reconciler

import (
	"testing"

	"k8s.io/utils/ptr"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	lwsv1 "sigs.k8s.io/lws/api/leaderworkerset/v1"
	workloadsv1alpha1 "sigs.k8s.io/rbgs/api/workloads/v1alpha1"
	"sigs.k8s.io/rbgs/pkg/utils"
)

var (
	defaultPodTemplate = corev1.PodTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pod-1",
			Namespace: "default",
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name:    "nginx",
					Image:   "nginx:1.15.1",
					Command: []string{"nginx"},
					Env: []corev1.EnvVar{
						{
							Name:  "nginx-env",
							Value: "value-1",
						},
					},
					VolumeMounts: []corev1.VolumeMount{
						{
							Name:      "nginx-volume",
							MountPath: "/data/nginx",
						},
					},
				},
				{
					Name:    "test-sidecar",
					Image:   "test-image:v1",
					Command: []string{"nginx"},
					Env: []corev1.EnvVar{
						{
							Name:  "IS_INJECTED",
							Value: "true",
						},
					},
				},
			},
			Volumes: []corev1.Volume{
				{
					Name: "nginx-volume",
				},
			},
		},
	}

	defaultRbg = workloadsv1alpha1.RoleBasedGroup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-rbg",
			Namespace: "default",
		},
		Spec: workloadsv1alpha1.RoleBasedGroupSpec{
			Roles: []workloadsv1alpha1.RoleSpec{
				{
					Name:     "prefill",
					Replicas: ptr.To(int32(4)),
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "test-pod-1",
							Namespace: "default",
						},
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Name:    "nginx",
									Image:   "nginx:1.15.1",
									Command: []string{"nginx"},
									Env: []corev1.EnvVar{
										{
											Name:  "nginx-env",
											Value: "value-1",
										},
									},
									VolumeMounts: []corev1.VolumeMount{
										{
											Name:      "nginx-volume",
											MountPath: "/data/nginx",
										},
									},
								},
							},
							Volumes: []corev1.Volume{
								{
									Name: "nginx-volume",
								},
							},
						},
					},
					LeaderWorkerSet: workloadsv1alpha1.LeaderWorkerTemplate{
						Size: ptr.To(int32(2)),
					},
				},
			},
		},
	}

	defaultLws = lwsv1.LeaderWorkerSet{
		Spec: lwsv1.LeaderWorkerSetSpec{
			Replicas: ptr.To(int32(4)),
			LeaderWorkerTemplate: lwsv1.LeaderWorkerTemplate{
				Size: ptr.To(int32(2)),
				LeaderTemplate: &corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name:    "nginx",
								Image:   "nginx:1.15.1",
								Command: []string{"nginx"},
								Env: []corev1.EnvVar{
									{
										Name:  "nginx-env",
										Value: "value-1",
									},
								},
								VolumeMounts: []corev1.VolumeMount{
									{
										Name:      "nginx-volume",
										MountPath: "/data/nginx",
									},
								},
							},
						},
						Volumes: []corev1.Volume{
							{
								Name: "nginx-volume",
							},
						},
					},
				},
				WorkerTemplate: corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name:    "nginx",
								Image:   "nginx:1.15.1",
								Command: []string{"nginx"},
								Env: []corev1.EnvVar{
									{
										Name:  "nginx-env",
										Value: "value-1",
									},
								},
								VolumeMounts: []corev1.VolumeMount{
									{
										Name:      "nginx-volume",
										MountPath: "/data/nginx",
									},
								},
							},
						},
						Volumes: []corev1.Volume{
							{
								Name: "nginx-volume",
							},
						},
					},
				},
				RestartPolicy: lwsv1.NoneRestartPolicy,
			},
		},
	}
)

func TestPatchPodTemplate(t *testing.T) {
	cases := []struct {
		name        string
		getTemplate func() corev1.PodTemplateSpec
		getPatch    func() runtime.RawExtension
		expect      func() corev1.PodTemplateSpec
	}{
		{
			name: "test1, no patch",
			getTemplate: func() corev1.PodTemplateSpec {
				obj := defaultPodTemplate.DeepCopy()
				return *obj
			},
			getPatch: func() runtime.RawExtension {
				return runtime.RawExtension{}
			},
			expect: func() corev1.PodTemplateSpec {
				obj := defaultPodTemplate.DeepCopy()
				return *obj
			},
		},
		{
			name: "test1, patch nginx command, env, labels, annotations",
			getTemplate: func() corev1.PodTemplateSpec {
				obj := defaultPodTemplate.DeepCopy()
				return *obj
			},
			getPatch: func() runtime.RawExtension {
				cpuV, _ := resource.ParseQuantity("1000m")
				memV, _ := resource.ParseQuantity("2Gi")
				obj := corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Labels: map[string]string{
							"app": "nginx",
						},
						Annotations: map[string]string{
							"test": "annotation",
						},
					},
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name:    "nginx",
								Command: []string{"nginx -c /home/user/my-nginx.conf"},
								Env: []corev1.EnvVar{
									{
										Name:  "nginx-env",
										Value: "value-2",
									},
									{
										Name:  "new-env",
										Value: "value-1",
									},
								},
							},
							{
								Name:    "test-sidecar",
								Command: []string{"nginx -c /home/user/my-nginx.conf"},
								Resources: corev1.ResourceRequirements{
									Requests: map[corev1.ResourceName]resource.Quantity{
										corev1.ResourceCPU:    cpuV,
										corev1.ResourceMemory: memV,
									},
								},
							},
						},
					},
				}
				return runtime.RawExtension{
					Raw: []byte(utils.DumpJSON(obj)),
				}
			},
			expect: func() corev1.PodTemplateSpec {
				cpuV, _ := resource.ParseQuantity("1000m")
				memV, _ := resource.ParseQuantity("2Gi")
				obj := corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-pod-1",
						Namespace: "default",
						Labels: map[string]string{
							"app": "nginx",
						},
						Annotations: map[string]string{
							"test": "annotation",
						},
					},
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name:    "nginx",
								Image:   "nginx:1.15.1",
								Command: []string{"nginx -c /home/user/my-nginx.conf"},
								Env: []corev1.EnvVar{
									{
										Name:  "nginx-env",
										Value: "value-2",
									},
									{
										Name:  "new-env",
										Value: "value-1",
									},
								},
								VolumeMounts: []corev1.VolumeMount{
									{
										Name:      "nginx-volume",
										MountPath: "/data/nginx",
									},
								},
							},
							{
								Name:    "test-sidecar",
								Image:   "test-image:v1",
								Command: []string{"nginx -c /home/user/my-nginx.conf"},
								Env: []corev1.EnvVar{
									{
										Name:  "IS_INJECTED",
										Value: "true",
									},
								},
								Resources: corev1.ResourceRequirements{
									Requests: map[corev1.ResourceName]resource.Quantity{
										corev1.ResourceCPU:    cpuV,
										corev1.ResourceMemory: memV,
									},
								},
							},
						},
						Volumes: []corev1.Volume{
							{
								Name: "nginx-volume",
							},
						},
					},
				}
				return obj
			},
		},
		{
			name: "test3, only patch labels & annotations",
			getTemplate: func() corev1.PodTemplateSpec {
				obj := defaultPodTemplate.DeepCopy()
				return *obj
			},
			getPatch: func() runtime.RawExtension {
				patchContent := `{"metadata":{"labels":{"app":"nginx"},"annotations":{"test":"annotation"}}}`

				return runtime.RawExtension{
					Raw: []byte(patchContent),
				}
			},
			expect: func() corev1.PodTemplateSpec {
				obj := defaultPodTemplate.DeepCopy()
				obj.Labels = map[string]string{"app": "nginx"}
				obj.Annotations = map[string]string{"test": "annotation"}
				return *obj
			},
		},
	}

	for _, cs := range cases {
		t.Run(
			cs.name, func(t *testing.T) {
				obj, err := patchPodTemplate(cs.getTemplate(), cs.getPatch())
				if err != nil {
					t.Fatalf("patchPodTemplate failed: %s", err.Error())
				}
				if utils.DumpJSON(cs.expect()) != utils.DumpJSON(obj) {
					t.Fatalf("expect(%s), but get(%s)", utils.DumpJSON(cs.expect()), utils.DumpJSON(obj))
				}
			},
		)
	}
}

func TestLwsReconciler(t *testing.T) {
	cases := []struct {
		name   string
		getRbg func() *workloadsv1alpha1.RoleBasedGroup
		expect func() *lwsv1.LeaderWorkerSet
	}{
		{
			name: "first create",
			getRbg: func() *workloadsv1alpha1.RoleBasedGroup {
				obj := defaultRbg.DeepCopy()
				leader := corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Labels: map[string]string{
							"app": "leader",
						},
					},
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name:    "nginx",
								Command: []string{"nginx -c /home/user/my-nginx.conf"},
							},
						},
					},
				}
				worker := corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Labels: map[string]string{
							"app": "worker",
						},
					},
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name:    "nginx",
								Command: []string{"nginx -c /home/user/my-nginx.conf"},
							},
						},
					},
				}
				obj.Spec.Roles[0].LeaderWorkerSet.PatchLeaderTemplate = runtime.RawExtension{
					Raw: []byte(utils.DumpJSON(leader)),
				}
				obj.Spec.Roles[0].LeaderWorkerSet.PatchWorkerTemplate = runtime.RawExtension{
					Raw: []byte(utils.DumpJSON(worker)),
				}
				return obj
			},
			expect: func() *lwsv1.LeaderWorkerSet {
				obj := defaultLws.DeepCopy()
				obj.Spec.LeaderWorkerTemplate.LeaderTemplate.Labels = map[string]string{"app": "leader"}
				obj.Spec.LeaderWorkerTemplate.LeaderTemplate.Spec.Containers[0].Command =
					[]string{"nginx -c /home/user/my-nginx.conf"}
				obj.Spec.LeaderWorkerTemplate.WorkerTemplate.Labels = map[string]string{"app": "worker"}
				obj.Spec.LeaderWorkerTemplate.WorkerTemplate.Spec.Containers[0].Command =
					[]string{"nginx -c /home/user/my-nginx.conf"}
				return obj
			},
		},
	}

	for _, cs := range cases {
		t.Run(
			cs.name, func(t *testing.T) {
				// TODO, apply patches are not supported in the fake client.
				// Follow https://github.com/kubernetes/kubernetes/issues/115598 for the current status
				/*
					rbg := cs.getRbg()
					role := &rbg.Spec.Roles[0]
					cm := &corev1.ConfigMap{
						ObjectMeta: metav1.ObjectMeta{
							Name:      rbg.GetWorkloadName(role),
							Namespace: rbg.Namespace,
						},
					}
					fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(cm).Build()
					reconcile := NewLeaderWorkerSetReconciler(scheme, fakeClient)
					err := reconcile.Reconciler(context.TODO(), rbg, role)
					if err != nil {
						t.Fatalf("reconciler failed: %s", err.Error())
					}*/

			},
		)
	}
}
