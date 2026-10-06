/*
Copyright 2026 The RBG Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package v1alpha2

import (
	"reflect"
	"testing"
)

func TestKubectlRestartArgs(t *testing.T) {
	tests := []struct {
		name       string
		kubeconfig string
		want       []string
	}{
		{
			name: "default kubeconfig",
			want: []string{"exec", "-n", "test-ns", "test-pod", "-c", "nginx", "--", "nginx", "-s", "quit"},
		},
		{
			name:       "explicit kubeconfig",
			kubeconfig: "/tmp/e2e.kubeconfig",
			want:       []string{"--kubeconfig", "/tmp/e2e.kubeconfig", "exec", "-n", "test-ns", "test-pod", "-c", "nginx", "--", "nginx", "-s", "quit"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := kubectlRestartArgs(tt.kubeconfig, "test-ns", "test-pod")
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("kubectlRestartArgs() = %v, want %v", got, tt.want)
			}
		})
	}
}
