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

// RunDeletePhase deletes all RBGs at the configured QPS and waits for each to be fully removed.
func RunDeletePhase(ctx context.Context, client *StressClient, scenario *Scenario, recorder *TimingRecorder) error {
	limiter := rate.NewLimiter(rate.Limit(scenario.DeleteQPS), 1)

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

			startTime := time.Now()
			rec := OperationRecord{
				Name:      name,
				Operation: "delete",
				StartTime: startTime,
			}

			// Delete the RBG
			if err := client.DeleteRBG(ctx, scenario.Namespace, name); err != nil {
				rec.EndTime = time.Now()
				rec.Error = err.Error()
				recorder.Record(rec)
				errCh <- fmt.Errorf("delete %s: %w", name, err)
				return
			}

			// Wait for RBG to be fully deleted
			waitCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
			defer cancel()

			if err := client.WaitForRBGDeleted(waitCtx, scenario.Namespace, name); err != nil {
				rec.EndTime = time.Now()
				rec.Error = fmt.Sprintf("wait deleted: %v", err)
				recorder.Record(rec)
				errCh <- fmt.Errorf("wait deleted %s: %w", name, err)
				return
			}

			rec.EndTime = time.Now()
			recorder.Record(rec)
			log.Printf("[delete] %s removed in %v", name, rec.EndTime.Sub(startTime))
		}(i)
	}

	wg.Wait()
	close(errCh)

	errs := make([]error, 0, len(errCh))
	for err := range errCh {
		errs = append(errs, err)
	}

	if len(errs) > 0 {
		return fmt.Errorf("%d/%d deletes failed, first: %v", len(errs), scenario.TotalRBGs, errs[0])
	}
	return nil
}
