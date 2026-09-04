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

package common

import (
	"errors"
	"fmt"
)

// IncompatibleGangConfigError marks a gang configuration that the current
// RoleBasedGroup and scheduler cannot satisfy. Resolving it takes a deliberate
// change: editing the CoordinatedPolicy or the RoleBasedGroup, upgrading Volcano, or
// switching --scheduler-name. None of those happen on their own, so the reconciler
// must not retry it on the workqueue's error backoff.
type IncompatibleGangConfigError struct {
	msg string
}

// Error implements error.
func (e *IncompatibleGangConfigError) Error() string { return e.msg }

// NewIncompatibleGangConfigError builds an IncompatibleGangConfigError.
func NewIncompatibleGangConfigError(format string, args ...any) error {
	return &IncompatibleGangConfigError{msg: fmt.Sprintf(format, args...)}
}

// IsIncompatibleGangConfig reports whether err marks an unsatisfiable gang
// configuration rather than a transient failure.
func IsIncompatibleGangConfig(err error) bool {
	var target *IncompatibleGangConfigError
	return errors.As(err, &target)
}
