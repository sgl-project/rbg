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

package util

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"k8s.io/cli-runtime/pkg/genericclioptions"
)

func TestGetRBGClient(t *testing.T) {
	cf := genericclioptions.NewConfigFlags(true)
	client, err := GetRBGClient(cf)
	assert.NotNil(t, client)
	assert.NoError(t, err)
}

func TestGetK8SClientSet(t *testing.T) {
	cf := genericclioptions.NewConfigFlags(true)
	client, err := GetK8SClientSet(cf)
	assert.NotNil(t, client)
	assert.NoError(t, err)
}
