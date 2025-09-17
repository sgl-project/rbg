package reconciler

import (
	"testing"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	workloadsv1alpha1 "sigs.k8s.io/rbgs/api/workloads/v1alpha1"
)

func Test_objectMetaEqual(t *testing.T) {
	type args struct {
		meta1 metav1.ObjectMeta
		meta2 metav1.ObjectMeta
	}
	tests := []struct {
		name    string
		args    args
		want    bool
		wantErr bool
	}{
		{
			name: "test system labels",
			args: args{
				meta1: metav1.ObjectMeta{
					Labels: map[string]string{
						"app.kubernetes.io/component":            "lws",
						"app.kubernetes.io/instance":             "restart-policy",
						"app.kubernetes.io/managed-by":           "rolebasedgroup-controller",
						"app.kubernetes.io/name":                 "restart-policy",
						"rolebasedgroup.workloads.x-k8s.io/name": "restart-policy",
						"rolebasedgroup.workloads.x-k8s.io/role": "lws",
					},
				},
				meta2: metav1.ObjectMeta{
					Labels: map[string]string{
						"rolebasedgroup.workloads.x-k8s.io/name": "restart-policy",
						"rolebasedgroup.workloads.x-k8s.io/role": "lws",
					},
				},
			},
			want:    true,
			wantErr: false,
		},
		{
			name: "test system annotations",
			args: args{
				meta1: metav1.ObjectMeta{
					Annotations: map[string]string{
						"deployment.kubernetes.io/revision":           "1",
						"rolebasedgroup.workloads.x-k8s.io/role-size": "4",
					},
				},
				meta2: metav1.ObjectMeta{
					Annotations: map[string]string{
						"rolebasedgroup.workloads.x-k8s.io/role-size": "4",
					},
				},
			},
			want:    true,
			wantErr: false,
		},
		{
			name: "test system annotations",
			args: args{
				meta1: metav1.ObjectMeta{
					Annotations: map[string]string{
						"rolebasedgroup.workloads.x-k8s.io/role-size": "4",
					},
				},
				meta2: metav1.ObjectMeta{
					Annotations: nil,
				},
			},
			want:    true,
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := objectMetaEqual(tt.args.meta1, tt.args.meta2)
			if (err != nil) != tt.wantErr {
				t.Errorf("objectMetaEqual() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("objectMetaEqual() got = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_setExclusiveAffinities(t *testing.T) {
	tests := []struct {
		name                                   string
		pod                                    *corev1.PodTemplateSpec
		uniqueKey, topologyKey, podAffinityKey string
		want                                   *corev1.PodTemplateSpec
	}{
		{
			name:           "empty pod: create affinity/anti-affinity from scratch",
			pod:            &corev1.PodTemplateSpec{},
			uniqueKey:      "abcd1234",
			topologyKey:    "kubernetes.io/hostname",
			podAffinityKey: workloadsv1alpha1.SetGroupUniqueHashLabelKey,
			want: &corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Affinity: &corev1.Affinity{
						PodAffinity: &corev1.PodAffinity{
							RequiredDuringSchedulingIgnoredDuringExecution: []corev1.PodAffinityTerm{
								{
									TopologyKey: "kubernetes.io/hostname",
									LabelSelector: &metav1.LabelSelector{
										MatchExpressions: []metav1.LabelSelectorRequirement{
											{
												Key:      workloadsv1alpha1.SetGroupUniqueHashLabelKey,
												Operator: metav1.LabelSelectorOpIn,
												Values:   []string{"abcd1234"},
											},
										},
									},
								},
							},
						},
						PodAntiAffinity: &corev1.PodAntiAffinity{
							RequiredDuringSchedulingIgnoredDuringExecution: []corev1.PodAffinityTerm{
								{
									TopologyKey: "kubernetes.io/hostname",
									LabelSelector: &metav1.LabelSelector{
										MatchExpressions: []metav1.LabelSelectorRequirement{
											{
												Key:      workloadsv1alpha1.SetGroupUniqueHashLabelKey,
												Operator: metav1.LabelSelectorOpExists,
											},
											{
												Key:      workloadsv1alpha1.SetGroupUniqueHashLabelKey,
												Operator: metav1.LabelSelectorOpNotIn,
												Values:   []string{"abcd1234"},
											},
										},
									},
								},
							},
						},
					},
				},
			},
		},
		{
			name: "pod with pre-existing terms: append new ones",
			pod: &corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Affinity: &corev1.Affinity{
						PodAffinity: &corev1.PodAffinity{
							RequiredDuringSchedulingIgnoredDuringExecution: []corev1.PodAffinityTerm{
								{TopologyKey: "zone"},
							},
						},
						PodAntiAffinity: &corev1.PodAntiAffinity{
							RequiredDuringSchedulingIgnoredDuringExecution: []corev1.PodAffinityTerm{
								{TopologyKey: "zone"},
							},
						},
					},
				},
			},
			uniqueKey:      "xyz5678",
			topologyKey:    "node",
			podAffinityKey: workloadsv1alpha1.SetGroupUniqueHashLabelKey,
			want: &corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Affinity: &corev1.Affinity{
						PodAffinity: &corev1.PodAffinity{
							RequiredDuringSchedulingIgnoredDuringExecution: []corev1.PodAffinityTerm{
								{TopologyKey: "zone"},
								{
									TopologyKey: "node",
									LabelSelector: &metav1.LabelSelector{
										MatchExpressions: []metav1.LabelSelectorRequirement{
											{
												Key:      workloadsv1alpha1.SetGroupUniqueHashLabelKey,
												Operator: metav1.LabelSelectorOpIn,
												Values:   []string{"xyz5678"},
											},
										},
									},
								},
							},
						},
						PodAntiAffinity: &corev1.PodAntiAffinity{
							RequiredDuringSchedulingIgnoredDuringExecution: []corev1.PodAffinityTerm{
								{TopologyKey: "zone"},
								{
									TopologyKey: "node",
									LabelSelector: &metav1.LabelSelector{
										MatchExpressions: []metav1.LabelSelectorRequirement{
											{
												Key:      workloadsv1alpha1.SetGroupUniqueHashLabelKey,
												Operator: metav1.LabelSelectorOpExists,
											},
											{
												Key:      workloadsv1alpha1.SetGroupUniqueHashLabelKey,
												Operator: metav1.LabelSelectorOpNotIn,
												Values:   []string{"xyz5678"},
											},
										},
									},
								},
							},
						},
					},
				},
			},
		},
		{
			name: "topology already exists: expect no change",
			pod: &corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Affinity: &corev1.Affinity{
						PodAffinity: &corev1.PodAffinity{
							RequiredDuringSchedulingIgnoredDuringExecution: []corev1.PodAffinityTerm{
								{
									TopologyKey: "rack",
									LabelSelector: &metav1.LabelSelector{
										MatchExpressions: []metav1.LabelSelectorRequirement{
											{
												Key:      workloadsv1alpha1.SetGroupUniqueHashLabelKey,
												Operator: metav1.LabelSelectorOpIn,
												Values:   []string{"old"},
											},
										},
									},
								},
							},
						},
						PodAntiAffinity: &corev1.PodAntiAffinity{
							RequiredDuringSchedulingIgnoredDuringExecution: []corev1.PodAffinityTerm{
								{TopologyKey: "rack"},
							},
						},
					},
				},
			},
			uniqueKey:      "newkey",
			topologyKey:    "rack",
			podAffinityKey: workloadsv1alpha1.SetGroupUniqueHashLabelKey,
			want: &corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Affinity: &corev1.Affinity{
						PodAffinity: &corev1.PodAffinity{
							RequiredDuringSchedulingIgnoredDuringExecution: []corev1.PodAffinityTerm{
								{
									TopologyKey: "rack",
									LabelSelector: &metav1.LabelSelector{
										MatchExpressions: []metav1.LabelSelectorRequirement{
											{
												Key:      workloadsv1alpha1.SetGroupUniqueHashLabelKey,
												Operator: metav1.LabelSelectorOpIn,
												Values:   []string{"old"},
											},
										},
									},
								},
							},
						},
						PodAntiAffinity: &corev1.PodAntiAffinity{
							RequiredDuringSchedulingIgnoredDuringExecution: []corev1.PodAffinityTerm{
								{TopologyKey: "rack"},
							},
						},
					},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setExclusiveAffinities(tt.pod, tt.uniqueKey, tt.topologyKey, tt.podAffinityKey)
			assert.Equal(t, tt.want, tt.pod, "unexpected PodTemplateSpec after injection")
		})
	}
}

func Test_exclusiveAffinityApplied(t *testing.T) {
	tests := []struct {
		name string // description of this test case
		// Named input parameters for target function.
		podTemplateSpec corev1.PodTemplateSpec
		topologyKey     string
		want            bool
	}{
		{
			name:            "empty affinity: should return false",
			podTemplateSpec: corev1.PodTemplateSpec{},
			topologyKey:     "kubernetes.io/hostname",
			want:            false,
		},
		{
			name: "both affinity and anti-affinity contain the required topology key: should return true",
			podTemplateSpec: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Affinity: &corev1.Affinity{
						PodAffinity: &corev1.PodAffinity{
							RequiredDuringSchedulingIgnoredDuringExecution: []corev1.PodAffinityTerm{
								{TopologyKey: "kubernetes.io/hostname"},
							},
						},
						PodAntiAffinity: &corev1.PodAntiAffinity{
							RequiredDuringSchedulingIgnoredDuringExecution: []corev1.PodAffinityTerm{
								{TopologyKey: "kubernetes.io/hostname"},
							},
						},
					},
				},
			},
			topologyKey: "kubernetes.io/hostname",
			want:        true,
		},
		{
			name: "only affinity contains the topology key: should return false",
			podTemplateSpec: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Affinity: &corev1.Affinity{
						PodAffinity: &corev1.PodAffinity{
							RequiredDuringSchedulingIgnoredDuringExecution: []corev1.PodAffinityTerm{
								{TopologyKey: "kubernetes.io/hostname"},
							},
						},
						PodAntiAffinity: &corev1.PodAntiAffinity{
							RequiredDuringSchedulingIgnoredDuringExecution: []corev1.PodAffinityTerm{
								{TopologyKey: "zone"},
							},
						},
					},
				},
			},
			topologyKey: "kubernetes.io/hostname",
			want:        false,
		},
		{
			name: "only anti-affinity contains the topology key: should return false",
			podTemplateSpec: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Affinity: &corev1.Affinity{
						PodAffinity: &corev1.PodAffinity{
							RequiredDuringSchedulingIgnoredDuringExecution: []corev1.PodAffinityTerm{
								{TopologyKey: "zone"},
							},
						},
						PodAntiAffinity: &corev1.PodAntiAffinity{
							RequiredDuringSchedulingIgnoredDuringExecution: []corev1.PodAffinityTerm{
								{TopologyKey: "kubernetes.io/hostname"},
							},
						},
					},
				},
			},
			topologyKey: "kubernetes.io/hostname",
			want:        false,
		},
		{
			name: "topology key does not match: should return false",
			podTemplateSpec: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Affinity: &corev1.Affinity{
						PodAffinity: &corev1.PodAffinity{
							RequiredDuringSchedulingIgnoredDuringExecution: []corev1.PodAffinityTerm{
								{TopologyKey: "zone"},
							},
						},
						PodAntiAffinity: &corev1.PodAntiAffinity{
							RequiredDuringSchedulingIgnoredDuringExecution: []corev1.PodAffinityTerm{
								{TopologyKey: "zone"},
							},
						},
					},
				},
			},
			topologyKey: "kubernetes.io/hostname",
			want:        false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := exclusiveAffinityApplied(tt.podTemplateSpec, tt.topologyKey)
			if got != tt.want {
				t.Errorf("exclusiveAffinityApplied() = %v, want %v", got, tt.want)
			}
		})
	}
}
