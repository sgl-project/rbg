package discovery

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	workloadsv1alpha1 "sigs.k8s.io/rbgs/api/workloads/v1alpha1"
)

// TestConfigBuilder_Build tests the Build method of ConfigBuilder
func TestConfigBuilder_Build(t *testing.T) {
	replicas3 := int32(3)
	replicas1 := int32(1)

	tests := []struct {
		name     string
		rbg      *workloadsv1alpha1.RoleBasedGroup
		role     *workloadsv1alpha1.RoleSpec
		expected string
		wantErr  bool
	}{
		{
			name: "simple cluster config",
			rbg: &workloadsv1alpha1.RoleBasedGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-cluster",
				},
				Spec: workloadsv1alpha1.RoleBasedGroupSpec{
					Roles: []workloadsv1alpha1.RoleSpec{
						{
							Name:     "worker",
							Replicas: &replicas3,
							ServicePorts: []corev1.ServicePort{
								{
									Name: "http",
									Port: 8080,
								},
							},
						},
						{
							Name:     "leader",
							Replicas: &replicas1,
							ServicePorts: []corev1.ServicePort{
								{
									Name: "api",
									Port: 6443,
								},
							},
						},
					},
				},
			},
			role: &workloadsv1alpha1.RoleSpec{
				Name:     "worker",
				Replicas: &replicas3,
			},
			expected: `group:
  name: test-cluster
  roles:
  - worker
  - leader
  size: 2
roles:
  leader:
    instances:
    - address: leader-0.test-cluster-leader
      ports:
        api: 6443
    size: 1
  worker:
    instances:
    - address: worker-0.test-cluster-worker
      ports:
        http: 8080
    - address: worker-1.test-cluster-worker
      ports:
        http: 8080
    - address: worker-2.test-cluster-worker
      ports:
        http: 8080
    size: 3
`,
			wantErr: false,
		},
		{
			name: "role with unnamed ports",
			rbg: &workloadsv1alpha1.RoleBasedGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-cluster",
				},
				Spec: workloadsv1alpha1.RoleBasedGroupSpec{
					Roles: []workloadsv1alpha1.RoleSpec{
						{
							Name:     "web",
							Replicas: &replicas1,
							ServicePorts: []corev1.ServicePort{
								{
									Port: 80,
								},
								{
									Port: 443,
								},
							},
						},
					},
				},
			},
			role: &workloadsv1alpha1.RoleSpec{
				Name:     "web",
				Replicas: &replicas1,
			},
			expected: `group:
  name: test-cluster
  roles:
  - web
  size: 1
roles:
  web:
    instances:
    - address: web-0.test-cluster-web
      ports:
        port80: 80
        port443: 443
    size: 1
`,
			wantErr: false,
		},
		{
			name: "rbg name start with numeric",
			rbg: &workloadsv1alpha1.RoleBasedGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name: "1-test-cluster",
				},
				Spec: workloadsv1alpha1.RoleBasedGroupSpec{
					Roles: []workloadsv1alpha1.RoleSpec{
						{
							Name:     "worker",
							Replicas: &replicas3,
							ServicePorts: []corev1.ServicePort{
								{
									Name: "http",
									Port: 8080,
								},
							},
						},
						{
							Name:     "leader",
							Replicas: &replicas1,
							ServicePorts: []corev1.ServicePort{
								{
									Name: "api",
									Port: 6443,
								},
							},
						},
					},
				},
			},
			role: &workloadsv1alpha1.RoleSpec{
				Name:     "worker",
				Replicas: &replicas3,
			},
			expected: `group:
  name: 1-test-cluster
  roles:
  - worker
  - leader
  size: 2
roles:
  leader:
    instances:
    - address: leader-0.s-1-test-cluster-leader
      ports:
        api: 6443
    size: 1
  worker:
    instances:
    - address: worker-0.s-1-test-cluster-worker
      ports:
        http: 8080
    - address: worker-1.s-1-test-cluster-worker
      ports:
        http: 8080
    - address: worker-2.s-1-test-cluster-worker
      ports:
        http: 8080
    size: 3
`,
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(
			tt.name, func(t *testing.T) {
				b := &ConfigBuilder{
					rbg:  tt.rbg,
					role: tt.role,
				}
				got, err := b.Build()
				if (err != nil) != tt.wantErr {
					t.Errorf("ConfigBuilder.Build() error = %v, wantErr %v", err, tt.wantErr)
					return
				}

				if !tt.wantErr {
					if diff := cmp.Diff(string(got), tt.expected); diff != "" {
						t.Errorf("ConfigBuilder.Build() mismatch (-want +got):\n%s", diff)
					}
				}
			},
		)
	}
}

// TestConfigBuilder_getRoleNames tests the getRoleNames method of ConfigBuilder
func TestConfigBuilder_getRoleNames(t *testing.T) {
	replicas := int32(1)

	rbg := &workloadsv1alpha1.RoleBasedGroup{
		Spec: workloadsv1alpha1.RoleBasedGroupSpec{
			Roles: []workloadsv1alpha1.RoleSpec{
				{
					Name:     "role1",
					Replicas: &replicas,
				},
				{
					Name:     "role2",
					Replicas: &replicas,
				},
				{
					Name:     "role3",
					Replicas: &replicas,
				},
			},
		},
	}

	b := &ConfigBuilder{
		rbg: rbg,
	}

	expected := []string{"role1", "role2", "role3"}
	got := b.getRoleNames()

	if diff := cmp.Diff(got, expected); diff != "" {
		t.Errorf("ConfigBuilder.getRoleNames() mismatch (-want +got):\n%s", diff)
	}
}

// TestConfigBuilder_buildRolesInfo tests the buildRolesInfo method of ConfigBuilder
func TestConfigBuilder_buildRolesInfo(t *testing.T) {
	replicas3 := int32(3)
	replicas1 := int32(1)

	rbg := &workloadsv1alpha1.RoleBasedGroup{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-cluster",
		},
		Spec: workloadsv1alpha1.RoleBasedGroupSpec{
			Roles: []workloadsv1alpha1.RoleSpec{
				{
					Name:     "worker",
					Replicas: &replicas3,
					ServicePorts: []corev1.ServicePort{
						{
							Name: "http",
							Port: 8080,
						},
					},
				},
				{
					Name:     "master",
					Replicas: &replicas1,
					ServicePorts: []corev1.ServicePort{
						{
							Name: "api",
							Port: 6443,
						},
					},
				},
			},
		},
	}

	b := &ConfigBuilder{
		rbg: rbg,
	}

	rolesInfo := b.buildRolesInfo()

	// Verify number of roles
	if len(rolesInfo) != 2 {
		t.Errorf("Expected 2 roles, got %d", len(rolesInfo))
	}

	// Verify worker role
	workerRole, exists := rolesInfo["worker"]
	if !exists {
		t.Error("Expected 'worker' role not found")
	} else {
		if workerRole.Size != 3 {
			t.Errorf("Expected worker size 3, got %d", workerRole.Size)
		}
		if len(workerRole.Instances) != 3 {
			t.Errorf("Expected 3 worker instances, got %d", len(workerRole.Instances))
		}
	}

	// Verify master role
	masterRole, exists := rolesInfo["master"]
	if !exists {
		t.Error("Expected 'master' role not found")
	} else {
		if masterRole.Size != 1 {
			t.Errorf("Expected master size 1, got %d", masterRole.Size)
		}
		if len(masterRole.Instances) != 1 {
			t.Errorf("Expected 1 master instance, got %d", len(masterRole.Instances))
		}
	}
}

// TestGeneratePortKey tests the generatePortKey function
func TestGeneratePortKey(t *testing.T) {
	tests := []struct {
		name     string
		port     corev1.ServicePort
		expected string
	}{
		{
			name: "named port",
			port: corev1.ServicePort{
				Name: "http",
				Port: 8080,
			},
			expected: "http",
		},
		{
			name: "named port with dash",
			port: corev1.ServicePort{
				Name: "http-api",
				Port: 8080,
			},
			expected: "http_api",
		},
		{
			name: "unnamed port",
			port: corev1.ServicePort{
				Port: 80,
			},
			expected: "port80",
		},
	}

	for _, tt := range tests {
		t.Run(
			tt.name, func(t *testing.T) {
				got := generatePortKey(tt.port)
				if got != tt.expected {
					t.Errorf("generatePortKey() = %v, want %v", got, tt.expected)
				}
			},
		)
	}
}

// TestSemanticallyEqualConfigmap tests the semanticallyEqualConfigmap function
func TestSemanticallyEqualConfigmap(t *testing.T) {
	baseConfigMap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "test-config",
			Namespace:       "default",
			ResourceVersion: "1",
			UID:             "uid-1",
		},
		Data: map[string]string{
			"config.yaml": "test: data",
		},
	}

	tests := []struct {
		name     string
		old      *corev1.ConfigMap
		new      *corev1.ConfigMap
		expected bool
	}{
		{
			name:     "both nil",
			old:      nil,
			new:      nil,
			expected: true,
		},
		{
			name:     "one nil",
			old:      nil,
			new:      baseConfigMap,
			expected: false,
		},
		{
			name:     "equal ignoring metadata",
			old:      baseConfigMap,
			new:      baseConfigMap.DeepCopy(),
			expected: true,
		},
		{
			name: "different data",
			old:  baseConfigMap,
			new: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-config",
					Namespace: "default",
				},
				Data: map[string]string{
					"config.yaml": "different: data",
				},
			},
			expected: false,
		},
		{
			name: "same data, different metadata",
			old:  baseConfigMap,
			new: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:            "test-config",
					Namespace:       "default",
					ResourceVersion: "2",
					UID:             "uid-2",
				},
				Data: map[string]string{
					"config.yaml": "test: data",
				},
			},
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(
			tt.name, func(t *testing.T) {
				got, _ := semanticallyEqualConfigmap(tt.old, tt.new)
				if got != tt.expected {
					t.Errorf("semanticallyEqualConfigmap() = %v, want %v", got, tt.expected)
				}
			},
		)
	}
}

// TestConfigBuilder_buildInstances tests the buildInstances method of ConfigBuilder
func TestConfigBuilder_buildInstances(t *testing.T) {
	replicas := int32(2)

	rbg := &workloadsv1alpha1.RoleBasedGroup{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-cluster",
		},
	}

	role := &workloadsv1alpha1.RoleSpec{
		Name:     "server",
		Replicas: &replicas,
		ServicePorts: []corev1.ServicePort{
			{
				Name: "http",
				Port: 80,
			},
			{
				Name: "https",
				Port: 443,
			},
		},
	}

	b := &ConfigBuilder{
		rbg:  rbg,
		role: role,
	}

	instances := b.buildInstances(role)

	// Verify number of instances
	if len(instances) != int(replicas) {
		t.Errorf("Expected %d instances, got %d", replicas, len(instances))
	}

	// Verify first instance
	if instances[0].Address != "server-0.test-cluster-server" {
		t.Errorf("Expected address 'server-0.test-cluster-server', got '%s'", instances[0].Address)
	}

	if len(instances[0].Ports) != 2 {
		t.Errorf("Expected 2 ports, got %d", len(instances[0].Ports))
	}

	if port, exists := instances[0].Ports["http"]; !exists || port != 80 {
		t.Errorf("Expected http port 80, got %v", instances[0].Ports["http"])
	}

	if port, exists := instances[0].Ports["https"]; !exists || port != 443 {
		t.Errorf("Expected https port 443, got %v", instances[0].Ports["https"])
	}
}
