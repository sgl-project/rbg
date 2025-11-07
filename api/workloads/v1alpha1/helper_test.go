package v1alpha1

import (
	"testing"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestRoleBasedGroup_GenGroupUniqueKey(t *testing.T) {
	rbg := &RoleBasedGroup{
		ObjectMeta: metav1.ObjectMeta{Name: "test-rbg", Namespace: "test-ns"},
	}
	key := rbg.GenGroupUniqueKey()
	assert.Len(t, key, 40) // SHA1 hex = 40
	assert.Equal(t, key, rbg.GenGroupUniqueKey())
}

func TestRoleBasedGroup_GetExclusiveKey(t *testing.T) {
	tests := []struct {
		name        string
		annotations map[string]string
		wantKey     string
		wantFound   bool
	}{
		{"empty", nil, "", false},
		{"set", map[string]string{ExclusiveKeyAnnotationKey: "kubernetes.io/hostname"}, "kubernetes.io/hostname", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rbg := &RoleBasedGroup{
				ObjectMeta: metav1.ObjectMeta{Annotations: tt.annotations},
			}
			got, found := rbg.GetExclusiveKey()
			assert.Equal(t, tt.wantKey, got)
			assert.Equal(t, tt.wantFound, found)
		})
	}
}

func TestRoleBasedGroup_GetCoordination(t *testing.T) {
	replicas1 := int32(6)
	replicas2 := int32(3)
	rbg := &RoleBasedGroup{
		Spec: RoleBasedGroupSpec{
			Coordination: []Coordination{
				{
					Name:  "test-coord",
					Type:  AffinitySchedulingCoordination,
					Roles: []string{"role1", "role2"},
				},
			},
			Roles: []RoleSpec{
				{Name: "role1", Replicas: &replicas1},
				{Name: "role2", Replicas: &replicas2},
			},
		},
	}

	tests := []struct {
		name      string
		coordName string
		wantErr   bool
	}{
		{"found", "test-coord", false},
		{"not found", "nonexistent", true},
		{"empty name", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			coord, err := rbg.GetCoordination(tt.coordName)
			if tt.wantErr {
				assert.Error(t, err)
				assert.Nil(t, coord)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, coord)
				assert.Equal(t, tt.coordName, coord.Name)
			}
		})
	}
}

func TestCoordination_ValidateCoordination(t *testing.T) {
	replicas1 := int32(6)
	replicas2 := int32(3)
	rbg := &RoleBasedGroup{
		Spec: RoleBasedGroupSpec{
			Roles: []RoleSpec{
				{Name: "prefill", Replicas: &replicas1},
				{Name: "decode", Replicas: &replicas2},
			},
		},
	}

	tests := []struct {
		name         string
		coordination Coordination
		wantErr      bool
	}{
		{
			name: "valid affinity scheduling",
			coordination: Coordination{
				Name:  "valid",
				Type:  AffinitySchedulingCoordination,
				Roles: []string{"prefill", "decode"},
				Strategy: CoordinationStrategy{
					AffinityScheduling: &AffinitySchedulingStrategy{
						TopologyKey: "kubernetes.io/hostname",
						Ratios: map[string]int32{
							"prefill": 2,
							"decode":  1,
						},
						BatchMode: SequentialBatch,
					},
				},
			},
			wantErr: false,
		},
		{
			name: "non-existent role",
			coordination: Coordination{
				Name:  "invalid-role",
				Type:  AffinitySchedulingCoordination,
				Roles: []string{"prefill", "nonexistent"},
				Strategy: CoordinationStrategy{
					AffinityScheduling: &AffinitySchedulingStrategy{
						TopologyKey: "kubernetes.io/hostname",
						Ratios: map[string]int32{
							"prefill":     2,
							"nonexistent": 1,
						},
					},
				},
			},
			wantErr: true,
		},
		{
			name: "missing strategy",
			coordination: Coordination{
				Name:  "missing-strategy",
				Type:  AffinitySchedulingCoordination,
				Roles: []string{"prefill", "decode"},
			},
			wantErr: true,
		},
		{
			name: "indivisible replicas",
			coordination: Coordination{
				Name:  "indivisible",
				Type:  AffinitySchedulingCoordination,
				Roles: []string{"prefill", "decode"},
				Strategy: CoordinationStrategy{
					AffinityScheduling: &AffinitySchedulingStrategy{
						TopologyKey: "kubernetes.io/hostname",
						Ratios: map[string]int32{
							"prefill": 4, // 6 is not divisible by 4
							"decode":  1,
						},
					},
				},
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.coordination.ValidateCoordination(rbg)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestAffinitySchedulingStrategy_Validate(t *testing.T) {
	replicas1 := int32(6)
	replicas2 := int32(3)
	rbg := &RoleBasedGroup{
		Spec: RoleBasedGroupSpec{
			Roles: []RoleSpec{
				{Name: "prefill", Replicas: &replicas1},
				{Name: "decode", Replicas: &replicas2},
			},
		},
	}

	tests := []struct {
		name     string
		strategy AffinitySchedulingStrategy
		roles    []string
		wantErr  bool
	}{
		{
			name: "valid strategy",
			strategy: AffinitySchedulingStrategy{
				TopologyKey: "kubernetes.io/hostname",
				Ratios: map[string]int32{
					"prefill": 2,
					"decode":  1,
				},
			},
			roles:   []string{"prefill", "decode"},
			wantErr: false,
		},
		{
			name: "empty topology key",
			strategy: AffinitySchedulingStrategy{
				TopologyKey: "",
				Ratios: map[string]int32{
					"prefill": 2,
					"decode":  1,
				},
			},
			roles:   []string{"prefill", "decode"},
			wantErr: true,
		},
		{
			name: "missing role in ratios",
			strategy: AffinitySchedulingStrategy{
				TopologyKey: "kubernetes.io/hostname",
				Ratios: map[string]int32{
					"prefill": 2,
				},
			},
			roles:   []string{"prefill", "decode"},
			wantErr: true,
		},
		{
			name: "negative ratio",
			strategy: AffinitySchedulingStrategy{
				TopologyKey: "kubernetes.io/hostname",
				Ratios: map[string]int32{
					"prefill": -1,
					"decode":  1,
				},
			},
			roles:   []string{"prefill", "decode"},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.strategy.Validate(rbg, tt.roles)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}
