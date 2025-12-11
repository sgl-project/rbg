package v1alpha1

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
)

func TestValidateRoleTemplates(t *testing.T) {
	tests := []struct {
		name    string
		rbg     *RoleBasedGroup
		wantErr bool
		errMsg  string
	}{
		{
			name: "valid roleTemplate",
			rbg: &RoleBasedGroup{
				Spec: RoleBasedGroupSpec{
					RoleTemplates: []RoleTemplate{
						{
							Name: "base",
							Template: corev1.PodTemplateSpec{
								Spec: corev1.PodSpec{
									Containers: []corev1.Container{
										{Name: "app", Image: "nginx"},
									},
								},
							},
						},
					},
				},
			},
			wantErr: false,
		},
		{
			name: "duplicate template name",
			rbg: &RoleBasedGroup{
				Spec: RoleBasedGroupSpec{
					RoleTemplates: []RoleTemplate{
						{
							Name: "base",
							Template: corev1.PodTemplateSpec{
								Spec: corev1.PodSpec{
									Containers: []corev1.Container{{Name: "app"}},
								},
							},
						},
						{
							Name: "base", // duplicate
							Template: corev1.PodTemplateSpec{
								Spec: corev1.PodSpec{
									Containers: []corev1.Container{{Name: "app"}},
								},
							},
						},
					},
				},
			},
			wantErr: true,
			errMsg:  "duplicate template name",
		},
		{
			name: "template without containers",
			rbg: &RoleBasedGroup{
				Spec: RoleBasedGroupSpec{
					RoleTemplates: []RoleTemplate{
						{
							Name:     "base",
							Template: corev1.PodTemplateSpec{},
						},
					},
				},
			},
			wantErr: true,
			errMsg:  "must have at least one container",
		},
		{
			name: "invalid template name format",
			rbg: &RoleBasedGroup{
				Spec: RoleBasedGroupSpec{
					RoleTemplates: []RoleTemplate{
						{
							Name: "Base", // invalid: uppercase
							Template: corev1.PodTemplateSpec{
								Spec: corev1.PodSpec{
									Containers: []corev1.Container{{Name: "app"}},
								},
							},
						},
					},
				},
			},
			wantErr: true,
			errMsg:  "not a valid DNS label",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateRoleTemplates(tt.rbg)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateRoleTemplates() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if err != nil && tt.errMsg != "" && !strings.Contains(err.Error(), tt.errMsg) {
				t.Errorf("error = %q, want to contain %q", err.Error(), tt.errMsg)
			}
		})
	}
}

func TestValidateRoleTemplateReferences(t *testing.T) {
	baseTemplate := []RoleTemplate{
		{
			Name: "base",
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: "app"}},
				},
			},
		},
	}

	tests := []struct {
		name    string
		rbg     *RoleBasedGroup
		wantErr bool
		errMsg  string
	}{
		{
			name: "valid templateRef",
			rbg: &RoleBasedGroup{
				Spec: RoleBasedGroupSpec{
					RoleTemplates: baseTemplate,
					Roles: []RoleSpec{
						{
							Name:          "prefill",
							Replicas:      ptr.To(int32(1)),
							TemplateRef:   &TemplateRef{Name: "base"},
							TemplatePatch: runtime.RawExtension{Raw: []byte(`{}`)},
						},
					},
				},
			},
			wantErr: false,
		},
		{
			name: "templateRef to non-existent template",
			rbg: &RoleBasedGroup{
				Spec: RoleBasedGroupSpec{
					Roles: []RoleSpec{
						{
							Name:          "prefill",
							Replicas:      ptr.To(int32(1)),
							TemplateRef:   &TemplateRef{Name: "nonexistent"},
							TemplatePatch: runtime.RawExtension{Raw: []byte(`{}`)},
						},
					},
				},
			},
			wantErr: true,
			errMsg:  "not found in spec.roleTemplates",
		},
		{
			name: "templateRef without templatePatch",
			rbg: &RoleBasedGroup{
				Spec: RoleBasedGroupSpec{
					RoleTemplates: baseTemplate,
					Roles: []RoleSpec{
						{
							Name:        "prefill",
							Replicas:    ptr.To(int32(1)),
							TemplateRef: &TemplateRef{Name: "base"},
						},
					},
				},
			},
			wantErr: true,
			errMsg:  "templatePatch: required when templateRef is set",
		},
		{
			name: "mutual exclusivity: templateRef and template both set",
			rbg: &RoleBasedGroup{
				Spec: RoleBasedGroupSpec{
					RoleTemplates: baseTemplate,
					Roles: []RoleSpec{
						{
							Name:        "prefill",
							Replicas:    ptr.To(int32(1)),
							TemplateRef: &TemplateRef{Name: "base"},
							Template: &corev1.PodTemplateSpec{
								Spec: corev1.PodSpec{
									Containers: []corev1.Container{{Name: "app"}},
								},
							},
							TemplatePatch: runtime.RawExtension{Raw: []byte(`{}`)},
						},
					},
				},
			},
			wantErr: true,
			errMsg:  "templateRef and template are mutually exclusive",
		},
		{
			name: "templatePatch without templateRef",
			rbg: &RoleBasedGroup{
				Spec: RoleBasedGroupSpec{
					Roles: []RoleSpec{
						{
							Name:     "prefill",
							Replicas: ptr.To(int32(1)),
							Template: &corev1.PodTemplateSpec{
								Spec: corev1.PodSpec{
									Containers: []corev1.Container{{Name: "app"}},
								},
							},
							TemplatePatch: runtime.RawExtension{Raw: []byte(`{}`)},
						},
					},
				},
			},
			wantErr: true,
			errMsg:  "only valid when templateRef is set",
		},
		{
			name: "traditional mode: template only",
			rbg: &RoleBasedGroup{
				Spec: RoleBasedGroupSpec{
					Roles: []RoleSpec{
						{
							Name:     "prefill",
							Replicas: ptr.To(int32(1)),
							Template: &corev1.PodTemplateSpec{
								Spec: corev1.PodSpec{
									Containers: []corev1.Container{{Name: "app"}},
								},
							},
						},
					},
				},
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateRoleTemplateReferences(tt.rbg)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateRoleTemplateReferences() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if err != nil && tt.errMsg != "" && !strings.Contains(err.Error(), tt.errMsg) {
				t.Errorf("error = %q, want to contain %q", err.Error(), tt.errMsg)
			}
		})
	}
}

func TestIsDNSLabel(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"nginx-base", true},
		{"sglang-v0-5-1", true},
		{"-invalid", false},  // starts with hyphen
		{"invalid-", false},  // ends with hyphen
		{"Has-Upper", false}, // uppercase
		{"", false},          // empty
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			if got := isDNSLabel(tt.input); got != tt.want {
				t.Errorf("isDNSLabel(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}
