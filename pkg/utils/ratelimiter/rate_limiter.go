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

package ratelimiter

import (
	"flag"
	"time"

	"golang.org/x/time/rate"
	"k8s.io/client-go/util/workqueue"
)

func init() {
	flag.DurationVar(&baseDelay, "rate-limiter-base-delay", time.Millisecond*5, "The base delay for rate limiter. Defaults 5ms")
	flag.DurationVar(&maxDelay, "rate-limiter-max-delay", time.Second*1000, "The max delay for rate limiter. Defaults 1000s")
	flag.IntVar(&qps, "rate-limiter-qps", 10, "The qps for rate limiter. Defaults 10")
	flag.IntVar(&bucketSize, "rate-limiter-bucket-size", 100, "The bucket size for rate limiter. Defaults 100")
}

var baseDelay, maxDelay time.Duration
var qps, bucketSize int

func DefaultControllerRateLimiter[T comparable]() workqueue.TypedRateLimiter[T] {
	return workqueue.NewTypedMaxOfRateLimiter(
		workqueue.NewTypedItemExponentialFailureRateLimiter[T](baseDelay, maxDelay),
		&workqueue.TypedBucketRateLimiter[T]{Limiter: rate.NewLimiter(rate.Limit(qps), bucketSize)},
	)
}
