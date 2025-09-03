package dependency

import (
	"context"
	"reflect"
	"testing"

	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// TestDependencyOrder tests the DependencyOrder function with various dependency scenarios
func TestDependencyOrder(t *testing.T) {
	tests := []struct {
		name         string
		dependencies map[string][]string
		want         []string
		wantErr      bool
	}{
		{
			name: "simple chain",
			dependencies: map[string][]string{
				"a": {"b"},
				"b": {"c"},
				"c": {},
			},
			want:    []string{"c", "b", "a"},
			wantErr: false,
		},
		{
			name: "no dependencies",
			dependencies: map[string][]string{
				"a": {},
				"b": {},
				"c": {},
			},
			want:    []string{"a", "b", "c"},
			wantErr: false,
		},
		{
			name: "cycle detection",
			dependencies: map[string][]string{
				"a": {"b"},
				"b": {"c"},
				"c": {"a"},
			},
			want:    nil,
			wantErr: true,
		},
		{
			name: "cycle detection",
			dependencies: map[string][]string{
				"a": {"b"},
				"b": {},
				"c": {"d"},
				"d": {"c"},
			},
			want:    nil,
			wantErr: true,
		},
		{
			name: "same dependencies",
			dependencies: map[string][]string{
				"a": {"c"},
				"b": {"c"},
				"c": {},
			},
			want:    []string{"c", "a", "b"},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := log.IntoContext(context.TODO(), klog.NewKlogr())
			got, err := dependencyOrder(ctx, tt.dependencies)
			if tt.wantErr && err == nil {
				t.Errorf("DependencyOrder() expected error for cycle")
			}
			if !tt.wantErr && !reflect.DeepEqual(got, tt.want) {
				t.Errorf("DependencyOrder() = %v, want %v", got, tt.want)
			}
		})
	}
}
