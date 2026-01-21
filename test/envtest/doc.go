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

// Package envtest contains integration tests using envtest framework.
// Test cases are organized in the testcase/ subdirectory by controller:
//   - testcase/rbg/         - RoleBasedGroup controller tests
//   - testcase/rbgs/        - RoleBasedGroupSet controller tests
//   - testcase/instanceset/ - InstanceSet controller tests
//   - testcase/instance/    - Instance controller tests
//   - testcase/pod/         - Pod controller tests
//   - testcase/scalingadapter/ - ScalingAdapter controller tests
//
// Shared test utilities are in testutil/ package.
//
// To run all tests:
//
//	go test ./test/envtest/testcase/...
//
// To run tests for a specific controller:
//
//	go test ./test/envtest/testcase/rbg/
package envtest
