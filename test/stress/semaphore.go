/*
Copyright 2025 The RBG Authors.

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

package main

import "context"

// semaphore bounds the number of concurrent goroutines.
// A nil semaphore is unlimited.
type semaphore chan struct{}

// newSemaphore returns a semaphore with the given limit.
// If limit <= 0, returns nil (unlimited).
func newSemaphore(limit int) semaphore {
	if limit <= 0 {
		return nil
	}
	return make(chan struct{}, limit)
}

// Acquire blocks until a slot is available or ctx is done.
func (s semaphore) Acquire(ctx context.Context) {
	if s == nil {
		return
	}
	select {
	case s <- struct{}{}:
	case <-ctx.Done():
	}
}

// Release frees one slot.
func (s semaphore) Release() {
	if s == nil {
		return
	}
	select {
	case <-s:
	default:
	}
}
