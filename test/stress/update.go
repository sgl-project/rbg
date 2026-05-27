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

// RunUpdatePhase updates all existing RBGs at the configured QPS and waits for each to become Ready again.
func RunUpdatePhase(ctx context.Context, client *StressClient, scenario *Scenario, recorder *TimingRecorder) error {
	limiter := rate.NewLimiter(rate.Limit(scenario.UpdateQPS), 1)

	var wg sync.WaitGroup
	errCh := make(chan error, scenario.TotalRBGs)

	for i := 0; i < scenario.TotalRBGs; i++ {
		if err := limiter.Wait(ctx); err != nil {
			return fmt.Errorf("rate limiter: %w", err)
		}

		wg.Add(1)
		go func(index int) {
			defer wg.Done()

			name := fmt.Sprintf("stress-rbg-%04d", index)

			// Get current RBG
			rbg, err := client.GetRBG(ctx, scenario.Namespace, name)
			if err != nil {
				rec := OperationRecord{
					Name:      name,
					Operation: "update",
					StartTime: time.Now(),
					EndTime:   time.Now(),
					Error:     fmt.Sprintf("get: %v", err),
				}
				recorder.Record(rec)
				errCh <- fmt.Errorf("get %s: %w", name, err)
				return
			}

			// Apply update mutation
			ApplyUpdate(rbg, scenario.InPlaceUpdate)

			startTime := time.Now()
			rec := OperationRecord{
				Name:      name,
				Operation: "update",
				StartTime: startTime,
			}

			// Update the RBG
			if err := client.UpdateRBG(ctx, scenario.Namespace, rbg); err != nil {
				rec.EndTime = time.Now()
				rec.Error = err.Error()
				recorder.Record(rec)
				errCh <- fmt.Errorf("update %s: %w", name, err)
				return
			}

			// Wait for Ready condition (after update propagates)
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
			log.Printf("[update] %s ready in %v", name, rec.EndTime.Sub(startTime))
		}(i)
	}

	wg.Wait()
	close(errCh)

	var errs []error
	for err := range errCh {
		errs = append(errs, err)
	}

	if len(errs) > 0 {
		return fmt.Errorf("%d/%d updates failed, first: %v", len(errs), scenario.TotalRBGs, errs[0])
	}
	return nil
}
