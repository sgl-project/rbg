package reconciler

import (
	"testing"

	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func Test_objectMetaEqual(t *testing.T) {
	type args struct {
		meta1 v1.ObjectMeta
		meta2 v1.ObjectMeta
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
				meta1: v1.ObjectMeta{
					Labels: map[string]string{
						"app.kubernetes.io/component":            "lws",
						"app.kubernetes.io/instance":             "restart-policy",
						"app.kubernetes.io/managed-by":           "rolebasedgroup-controller",
						"app.kubernetes.io/name":                 "restart-policy",
						"rolebasedgroup.workloads.x-k8s.io/name": "restart-policy",
						"rolebasedgroup.workloads.x-k8s.io/role": "lws",
					},
				},
				meta2: v1.ObjectMeta{
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
				meta1: v1.ObjectMeta{
					Annotations: map[string]string{
						"deployment.kubernetes.io/revision":           "1",
						"rolebasedgroup.workloads.x-k8s.io/role-size": "4",
					},
				},
				meta2: v1.ObjectMeta{
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
				meta1: v1.ObjectMeta{
					Annotations: map[string]string{
						"rolebasedgroup.workloads.x-k8s.io/role-size": "4",
					},
				},
				meta2: v1.ObjectMeta{
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
