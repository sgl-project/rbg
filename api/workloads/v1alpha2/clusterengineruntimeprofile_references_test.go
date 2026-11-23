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
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestEngineRuntimeLabelConvergenceDriftEpoch(t *testing.T) {
	SetEngineRuntimeLabelConverged(false)

	epoch := BeginEngineRuntimeLabelScan()
	MarkEngineRuntimeLabelDrift()
	CompleteEngineRuntimeLabelScan(epoch)
	assert.False(t, EngineRuntimeLabelConverged(),
		"a drift event during a scan must invalidate that scan")

	epoch = BeginEngineRuntimeLabelScan()
	CompleteEngineRuntimeLabelScan(epoch)
	assert.True(t, EngineRuntimeLabelConverged(),
		"a scan with no intervening drift may converge")
}
