/*
Copyright 2025.

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

// Hub marks RoleBasedGroup as the conversion hub.
// All other versions (e.g. v1alpha1) implement ConvertTo/ConvertFrom targeting this type.
func (*RoleBasedGroup) Hub() {}

// Hub marks RoleBasedGroupSet as the conversion hub.
func (*RoleBasedGroupSet) Hub() {}
