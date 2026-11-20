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

import "strings"

// MentionsAdmissionRejection reports whether an API error message identifies a
// rejecting admission path: admission webhooks, built-in admission plugins, or
// ValidatingAdmissionPolicy. A ValidatingAdmissionPolicy denial with the default
// reason is returned as 422 Invalid and mentions neither a webhook nor a
// plugin, so it needs its own match. Callers still classify the status-code
// family themselves; this helper only identifies the rejecting source.
func MentionsAdmissionRejection(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "admission webhook") ||
		strings.Contains(msg, "admission plugin") ||
		strings.Contains(msg, "ValidatingAdmissionPolicy")
}
