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

package utils

import (
	"errors"
	"testing"
)

func TestMentionsAdmissionRejection(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"webhook", errors.New(`admission webhook "example" denied the request`), true},
		{"plugin", errors.New(`admission plugin "LimitRanger" rejected`), true},
		{"validating admission policy", errors.New(`ValidatingAdmissionPolicy 'p' with binding 'b' denied request: x`), true},
		{"quota", errors.New(`pods "ri-0" is forbidden: exceeded quota`), false},
		{"rbac", errors.New(`User "alice" cannot create resource "pods"`), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := MentionsAdmissionRejection(tc.err); got != tc.want {
				t.Fatalf("MentionsAdmissionRejection = %v, want %v", got, tc.want)
			}
		})
	}
}
