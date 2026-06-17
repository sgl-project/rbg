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

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// RunCreatePhase creates RBGs at the configured QPS and waits for each to become Ready.
func RunCreatePhase(ctx context.Context, client *StressClient, scenario *Scenario, recorder *TimingRecorder) error {
	limiter := rate.NewLimiter(rate.Limit(scenario.CreateQPS), 1)
	sem := newSemaphore(scenario.MaxConcurrentWaiters)

	var wg sync.WaitGroup
	errCh := make(chan error, scenario.TotalRBGs)

	for i := 0; i < scenario.TotalRBGs; i++ {
		if err := limiter.Wait(ctx); err != nil {
			return fmt.Errorf("rate limiter: %w", err)
		}

		sem.Acquire(ctx)
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			defer sem.Release()

			rbg := GenerateRBG(index, scenario)
			name := rbg.GetName()

			startTime := time.Now()
			rec := OperationRecord{
				Name:      name,
				Operation: "create",
				StartTime: startTime,
			}

			// Create the RBG
			if err := client.CreateRBG(ctx, scenario.Namespace, rbg); err != nil {
				rec.EndTime = time.Now()
				rec.Error = err.Error()
				recorder.Record(rec)
				errCh <- fmt.Errorf("create %s: %w", name, err)
				return
			}

			// Wait for Ready condition
			waitCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
			defer cancel()

			if err := client.WaitForRBGReady(waitCtx, scenario.Namespace, name); err != nil {
				rec.EndTime = time.Now()
				rec.Error = fmt.Sprintf("wait ready: %v", err)
				recorder.Record(rec)
				errCh <- fmt.Errorf("wait ready %s: %w", name, err)
				return
			}

			rec.EndTime = time.Now()
			recorder.Record(rec)
			log.Printf("[create] %s ready in %v", name, rec.EndTime.Sub(startTime))
		}(i)
	}

	wg.Wait()
	close(errCh)

	errs := make([]error, 0, len(errCh))
	for err := range errCh {
		errs = append(errs, err)
	}

	if len(errs) > 0 {
		return fmt.Errorf("%d/%d creates failed, first: %v", len(errs), scenario.TotalRBGs, errs[0])
	}
	return nil
}
