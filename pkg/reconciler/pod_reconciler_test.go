package reconciler

import (
	"context"
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	workloadsv1alpha1 "sigs.k8s.io/rbgs/api/workloads/v1alpha1"
	"sigs.k8s.io/rbgs/pkg/scheduler"
	"sigs.k8s.io/rbgs/test/wrappers"
)

func Test_podSpecEqual(t *testing.T) {
	type args struct {
		spec1 corev1.PodSpec
		spec2 corev1.PodSpec
	}
	tests := []struct {
		name    string
		args    args
		want    bool
		wantErr bool
	}{
		{
			name: "equal pod specs with containers",
			args: args{
				spec1: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  "container1",
							Image: "nginx:1.20",
						},
					},
				},
				spec2: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  "container1",
							Image: "nginx:1.20",
						},
					},
				},
			},
			want:    true,
			wantErr: false,
		},
		{
			name: "unequal container count",
			args: args{
				spec1: corev1.PodSpec{
					Containers: []corev1.Container{
						{Name: "container1", Image: "nginx:1.20"},
					},
				},
				spec2: corev1.PodSpec{
					Containers: []corev1.Container{
						{Name: "container1", Image: "nginx:1.20"},
						{Name: "container2", Image: "redis:6.0"},
					},
				},
			},
			want:    false,
			wantErr: true,
		},
		{
			name: "equal pod specs with volumes",
			args: args{
				spec1: corev1.PodSpec{
					Containers: []corev1.Container{
						{Name: "container1", Image: "nginx:1.20"},
					},
					Volumes: []corev1.Volume{
						{Name: "vol1", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
					},
				},
				spec2: corev1.PodSpec{
					Containers: []corev1.Container{
						{Name: "container1", Image: "nginx:1.20"},
					},
					Volumes: []corev1.Volume{
						{Name: "vol1", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
					},
				},
			},
			want:    true,
			wantErr: false,
		},
		{
			name: "unequal pod specs with different volumes",
			args: args{
				spec1: corev1.PodSpec{
					Containers: []corev1.Container{
						{Name: "container1", Image: "nginx:1.20"},
					},
					Volumes: []corev1.Volume{
						{Name: "vol1", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
					},
				},
				spec2: corev1.PodSpec{
					Containers: []corev1.Container{
						{Name: "container1", Image: "nginx:1.20"},
					},
					Volumes: []corev1.Volume{
						{Name: "vol2", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
					},
				},
			},
			want:    false,
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(
			tt.name, func(t *testing.T) {
				got, err := podSpecEqual(tt.args.spec1, tt.args.spec2)
				if (err != nil) != tt.wantErr {
					t.Errorf("podSpecEqual() error = %v, wantErr %v", err, tt.wantErr)
					return
				}
				if got != tt.want {
					t.Errorf("podSpecEqual() got = %v, want %v", got, tt.want)
				}
			},
		)
	}
}

func Test_mapsEqual(t *testing.T) {
	type args struct {
		map1 map[string]string
		map2 map[string]string
	}
	tests := []struct {
		name string
		args args
		want bool
	}{
		{
			name: "both nil maps",
			args: args{
				map1: nil,
				map2: nil,
			},
			want: true,
		},
		{
			name: "one nil one empty map",
			args: args{
				map1: nil,
				map2: map[string]string{},
			},
			want: true,
		},
		{
			name: "equal maps",
			args: args{
				map1: map[string]string{
					"key1": "value1",
					"key2": "value2",
				},
				map2: map[string]string{
					"key1": "value1",
					"key2": "value2",
				},
			},
			want: true,
		},
		{
			name: "different values",
			args: args{
				map1: map[string]string{
					"key1": "value1",
				},
				map2: map[string]string{
					"key1": "value2",
				},
			},
			want: false,
		},
		{
			name: "different keys",
			args: args{
				map1: map[string]string{
					"key1": "value1",
				},
				map2: map[string]string{
					"key2": "value1",
				},
			},
			want: false,
		},
		{
			name: "different map sizes",
			args: args{
				map1: map[string]string{
					"key1": "value1",
					"key2": "value2",
				},
				map2: map[string]string{
					"key1": "value1",
				},
			},
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(
			tt.name, func(t *testing.T) {
				if got := mapsEqual(tt.args.map1, tt.args.map2); got != tt.want {
					t.Errorf("mapsEqual() = %v, want %v", got, tt.want)
				}
			},
		)
	}
}

func Test_podTemplateSpecEqual(t *testing.T) {
	type args struct {
		template1 corev1.PodTemplateSpec
		template2 corev1.PodTemplateSpec
	}
	tests := []struct {
		name    string
		args    args
		want    bool
		wantErr bool
	}{
		{
			name: "equal pod template specs",
			args: args{
				template1: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Labels: map[string]string{
							"app": "test",
						},
					},
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name:  "container1",
								Image: "nginx:1.20",
							},
						},
					},
				},
				template2: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Labels: map[string]string{
							"app": "test",
						},
					},
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name:  "container1",
								Image: "nginx:1.20",
							},
						},
					},
				},
			},
			want:    true,
			wantErr: false,
		},
		{
			name: "different pod template specs metadata",
			args: args{
				template1: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Labels: map[string]string{
							"app": "test1",
						},
					},
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name:  "container1",
								Image: "nginx:1.20",
							},
						},
					},
				},
				template2: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Labels: map[string]string{
							"app": "test2",
						},
					},
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name:  "container1",
								Image: "nginx:1.20",
							},
						},
					},
				},
			},
			want:    false,
			wantErr: true,
		},
		{
			name: "different pod template specs containers",
			args: args{
				template1: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Labels: map[string]string{
							"app": "test",
						},
					},
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name:  "container1",
								Image: "nginx:1.20",
							},
						},
					},
				},
				template2: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Labels: map[string]string{
							"app": "test",
						},
					},
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name:  "container2",
								Image: "nginx:1.20",
							},
						},
					},
				},
			},
			want:    false,
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(
			tt.name, func(t *testing.T) {
				got, err := podTemplateSpecEqual(tt.args.template1, tt.args.template2)
				if (err != nil) != tt.wantErr {
					t.Errorf("podTemplateSpecEqual() error = %v, wantErr %v", err, tt.wantErr)
					return
				}
				if got != tt.want {
					t.Errorf("podTemplateSpecEqual() got = %v, want %v", got, tt.want)
				}
			},
		)
	}
}

func Test_slicesEqualByName(t *testing.T) {
	type testStruct struct {
		Name  string
		Value string
	}

	type args struct {
		a        []testStruct
		b        []testStruct
		name     func(testStruct) string
		itemType string
	}
	tests := []struct {
		name    string
		args    args
		want    bool
		wantErr bool
	}{
		{
			name: "equal slices",
			args: args{
				a: []testStruct{
					{Name: "item1", Value: "value1"},
					{Name: "item2", Value: "value2"},
				},
				b: []testStruct{
					{Name: "item1", Value: "value1"},
					{Name: "item2", Value: "value2"},
				},
				name:     func(ts testStruct) string { return ts.Name },
				itemType: "testStruct",
			},
			want:    true,
			wantErr: false,
		},
		{
			name: "equal slices different order",
			args: args{
				a: []testStruct{
					{Name: "item2", Value: "value2"},
					{Name: "item1", Value: "value1"},
				},
				b: []testStruct{
					{Name: "item1", Value: "value1"},
					{Name: "item2", Value: "value2"},
				},
				name:     func(ts testStruct) string { return ts.Name },
				itemType: "testStruct",
			},
			want:    true,
			wantErr: false,
		},
		{
			name: "different slice lengths",
			args: args{
				a: []testStruct{
					{Name: "item1", Value: "value1"},
				},
				b: []testStruct{
					{Name: "item1", Value: "value1"},
					{Name: "item2", Value: "value2"},
				},
				name:     func(ts testStruct) string { return ts.Name },
				itemType: "testStruct",
			},
			want:    false,
			wantErr: true,
		},
		{
			name: "different item names",
			args: args{
				a: []testStruct{
					{Name: "item1", Value: "value1"},
				},
				b: []testStruct{
					{Name: "item2", Value: "value1"},
				},
				name:     func(ts testStruct) string { return ts.Name },
				itemType: "testStruct",
			},
			want:    false,
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(
			tt.name, func(t *testing.T) {
				got, err := slicesEqualByName(tt.args.a, tt.args.b, tt.args.name, tt.args.itemType)
				if (err != nil) != tt.wantErr {
					t.Errorf("slicesEqualByName() error = %v, wantErr %v", err, tt.wantErr)
					return
				}
				if got != tt.want {
					t.Errorf("slicesEqualByName() got = %v, want %v", got, tt.want)
				}
			},
		)
	}
}

func TestPodReconciler_SetInjectors(t *testing.T) {
	scheme := runtime.NewScheme()
	client := fake.NewClientBuilder().WithScheme(scheme).Build()
	reconciler := NewPodReconciler(scheme, client)

	injectors := []string{"config", "sidecar"}
	reconciler.SetInjectors(injectors)

	assert.Equal(t, injectors, reconciler.injectObjects)
}

func TestPodReconciler_ConstructPodTemplateSpecApplyConfiguration(t *testing.T) {
	// Setup
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = workloadsv1alpha1.AddToScheme(scheme)

	client := fake.NewClientBuilder().WithScheme(scheme).Build()
	reconciler := NewPodReconciler(scheme, client)

	// Test data
	rbg := wrappers.BuildBasicRoleBasedGroup("test-rbg", "test-ns").Obj()
	role := &rbg.Spec.Roles[0]

	tests := []struct {
		name        string
		podLabels   map[string]string
		podTmpls    []corev1.PodTemplateSpec
		expectError bool
		setupFunc   func(*PodReconciler)
	}{
		{
			name:        "basic pod template construction",
			podLabels:   map[string]string{"role": "worker"},
			expectError: false,
		},
		{
			name: "with gang scheduling enabled",
			podLabels: map[string]string{
				"custom-label": "custom-value",
			},
			expectError: false,
			setupFunc: func(pr *PodReconciler) {
				// Enable gang scheduling by adding the annotation
				rbg.Spec.PodGroupPolicy = &workloadsv1alpha1.PodGroupPolicy{
					PodGroupPolicySource: workloadsv1alpha1.PodGroupPolicySource{
						KubeScheduling: &workloadsv1alpha1.KubeSchedulingPodGroupPolicySource{},
					},
				}
			},
		},
		{
			name: "with custom pod template",
			podLabels: map[string]string{
				"custom-label": "custom-value",
			},
			podTmpls: []corev1.PodTemplateSpec{
				{
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name:  "custom-container",
								Image: "redis:latest",
							},
						},
					},
				},
			},
			expectError: false,
		},
		{
			name: "with injectors disabled",
			podLabels: map[string]string{
				"test-label": "test-value",
			},
			setupFunc: func(pr *PodReconciler) {
				pr.SetInjectors([]string{}) // Disable all injectors
			},
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(
			tt.name, func(t *testing.T) {
				// Run setup function if provided
				if tt.setupFunc != nil {
					tt.setupFunc(reconciler)
				}

				// Execute the method under test
				result, err := reconciler.ConstructPodTemplateSpecApplyConfiguration(
					context.Background(),
					rbg,
					role,
					tt.podLabels,
					tt.podTmpls...,
				)

				// Check error expectations
				if tt.expectError {
					assert.Error(t, err)
					assert.Nil(t, result)
				} else {
					assert.NoError(t, err)
					assert.NotNil(t, result)

					// Check that labels are properly applied
					if tt.podLabels != nil {
						for k, v := range tt.podLabels {
							assert.Equal(t, v, result.Labels[k])
						}
					}

					// If gang scheduling is enabled, check for pod group label
					if rbg.EnableGangScheduling() {
						assert.Equal(t, rbg.Name, result.Labels[scheduler.KubePodGroupLabelKey])
					}
				}
			},
		)
	}
}

func TestPodReconciler_ConstructPodTemplateSpecApplyConfiguration_WithInjectors(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = workloadsv1alpha1.AddToScheme(scheme)

	client := fake.NewClientBuilder().WithScheme(scheme).Build()
	reconciler := NewPodReconciler(scheme, client)

	rbg := wrappers.BuildBasicRoleBasedGroup("test-rbg", "default").Obj()
	role := &rbg.Spec.Roles[0]

	t.Run(
		"with config injector enabled", func(t *testing.T) {
			reconciler.SetInjectors([]string{"config"})

			result, err := reconciler.ConstructPodTemplateSpecApplyConfiguration(
				context.Background(),
				rbg,
				role,
				map[string]string{"test": "label"},
			)

			// Note: Since we're using a fake client without actual config objects,
			// the injector might not do anything, but we're mainly testing that
			// the code path executes without panicking
			assert.NoError(t, err)
			assert.NotNil(t, result)
		},
	)

	t.Run(
		"with sidecar injector enabled", func(t *testing.T) {
			reconciler.SetInjectors([]string{"sidecar"})

			result, err := reconciler.ConstructPodTemplateSpecApplyConfiguration(
				context.Background(),
				rbg,
				role,
				map[string]string{"test": "label"},
			)

			// Note: Since we're using a fake client without actual sidecar configurations,
			// the injector might not do anything, but we're mainly testing that
			// the code path executes without panicking
			assert.NoError(t, err)
			assert.NotNil(t, result)
		},
	)

	t.Run(
		"with env injector enabled", func(t *testing.T) {
			reconciler.SetInjectors([]string{"common_env"})

			result, err := reconciler.ConstructPodTemplateSpecApplyConfiguration(
				context.Background(),
				rbg,
				role,
				map[string]string{"test": "label"},
			)

			// Note: Since we're using a fake client without actual env configurations,
			// the injector might not do anything, but we're mainly testing that
			// the code path executes without panicking
			assert.NoError(t, err)
			assert.NotNil(t, result)
		},
	)
}

func Test_setExclusiveAffinities(t *testing.T) {
	tests := []struct {
		name                                   string
		pod                                    *corev1.PodTemplateSpec
		uniqueKey, topologyKey, podAffinityKey string
		want                                   *corev1.PodTemplateSpec
		wantErr                                bool
	}{
		{
			name:           "empty pod: create affinity/anti-affinity from scratch",
			pod:            &corev1.PodTemplateSpec{},
			uniqueKey:      "abcd1234",
			topologyKey:    "kubernetes.io/hostname",
			podAffinityKey: workloadsv1alpha1.SetGroupUniqueHashLabelKey,
			wantErr:        false,
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
			wantErr:        false,
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
			wantErr:        false,
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
		}, {
			name:           "empty topology key should return error",
			pod:            &corev1.PodTemplateSpec{},
			uniqueKey:      "key",
			topologyKey:    "", // illegal
			podAffinityKey: workloadsv1alpha1.SetGroupUniqueHashLabelKey,
			want:           &corev1.PodTemplateSpec{}, // No change
			wantErr:        true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := setExclusiveAffinities(tt.pod, tt.uniqueKey, tt.topologyKey, tt.podAffinityKey)
			if tt.wantErr {
				assert.Error(t, err, "expected error but got nil")
				return
			}
			assert.NoError(t, err)
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

func Test_applyStrategicMergePatch(t *testing.T) {
	type args struct {
		base  corev1.PodTemplateSpec
		patch runtime.RawExtension
	}
	tests := []struct {
		name    string
		args    args
		want    corev1.PodTemplateSpec
		wantErr bool
	}{
		{
			name: "nil or empty patch returns base unchanged",
			args: args{
				base: corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{Name: "app", Image: "nginx:1.20"},
						},
					},
				},
				patch: runtime.RawExtension{Raw: nil},
			},
			want: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{Name: "app", Image: "nginx:1.20"},
					},
				},
			},
			wantErr: false,
		},
		{
			name: "patch adds new field",
			args: args{
				base: corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{Name: "app", Image: "nginx:1.20"},
						},
					},
				},
				patch: runtime.RawExtension{
					Raw: []byte(`{"spec":{"restartPolicy":"Always"}}`),
				},
			},
			want: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{Name: "app", Image: "nginx:1.20"},
					},
					RestartPolicy: corev1.RestartPolicyAlways,
				},
			},
			wantErr: false,
		},
		{
			name: "patch overrides existing field",
			args: args{
				base: corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{Name: "app", Image: "nginx:1.20"},
						},
						RestartPolicy: corev1.RestartPolicyNever,
					},
				},
				patch: runtime.RawExtension{
					Raw: []byte(`{"spec":{"restartPolicy":"Always"}}`),
				},
			},
			want: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{Name: "app", Image: "nginx:1.20"},
					},
					RestartPolicy: corev1.RestartPolicyAlways,
				},
			},
			wantErr: false,
		},
		{
			name: "patch modifies container",
			args: args{
				base: corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{Name: "app", Image: "nginx:1.20"},
						},
					},
				},
				patch: runtime.RawExtension{
					Raw: []byte(`{"spec":{"containers":[{"name":"app","command":["sh","-c","echo hello"]}]}}`),
				},
			},
			want: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{Name: "app", Image: "nginx:1.20", Command: []string{"sh", "-c", "echo hello"}},
					},
				},
			},
			wantErr: false,
		},
		{
			name: "invalid JSON patch returns error",
			args: args{
				base: corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{Name: "app", Image: "nginx:1.20"},
						},
					},
				},
				patch: runtime.RawExtension{
					Raw: []byte(`{invalid json}`),
				},
			},
			want:    corev1.PodTemplateSpec{},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := applyStrategicMergePatch(tt.args.base, tt.args.patch)
			if (err != nil) != tt.wantErr {
				t.Errorf("applyStrategicMergePatch() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr && !reflect.DeepEqual(got, tt.want) {
				t.Errorf("applyStrategicMergePatch() got = %v, want %v", got, tt.want)
			}
		})
	}
}
