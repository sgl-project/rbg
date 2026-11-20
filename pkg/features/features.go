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

package features

import "sync/atomic"

var roleInstanceLifecycleHooks atomic.Bool

// SetRoleInstanceLifecycleHooks enables or disables the KEP-527
// RoleInstance lifecycle-hook feature. It is set once during manager startup.
func SetRoleInstanceLifecycleHooks(enabled bool) {
	roleInstanceLifecycleHooks.Store(enabled)
}

// RoleInstanceLifecycleHooksEnabled reports whether KEP-527
// RoleInstance lifecycle hooks are enabled.
func RoleInstanceLifecycleHooksEnabled() bool {
	return roleInstanceLifecycleHooks.Load()
}
