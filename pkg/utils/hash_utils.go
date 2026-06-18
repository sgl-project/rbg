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
	"encoding/json"
	"hash"
	"hash/fnv"

	"github.com/davecgh/go-spew/spew"
	corev1 "k8s.io/api/core/v1"
)

func HashVolume(volume *corev1.Volume) uint64 {
	hash := fnv.New32a()
	volumeJson, _ := json.Marshal(volume)
	DeepHashObject(hash, volumeJson)
	return uint64(hash.Sum32())
}

// HashContainer returns the hash of the container. It is used to compare
// the running container with its desired spec.
// Note: remember to update hashValues in container_hash_test.go as well.
func HashContainer(container *corev1.Container) uint64 {
	hash := fnv.New32a()
	// Omit nil or empty field when calculating hash value
	// Please see https://github.com/kubernetes/kubernetes/issues/53644
	containerJSON, _ := json.Marshal(container)
	DeepHashObject(hash, containerJSON)
	return uint64(hash.Sum32())
}

// DeepHashObject writes specified object to hash using the spew library
// which follows pointers and prints actual values of the nested objects
// ensuring the hash does not change when a pointer changes.
func DeepHashObject(hasher hash.Hash, objectToWrite interface{}) {
	hasher.Reset()
	printer := spew.ConfigState{
		Indent:         " ",
		SortKeys:       true,
		DisableMethods: true,
		SpewKeys:       true,
	}
	_, _ = printer.Fprintf(hasher, "%#v", objectToWrite)
}
