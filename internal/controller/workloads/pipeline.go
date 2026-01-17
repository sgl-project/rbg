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

package workloads

import (
	"context"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// ReconcileStep 管道步骤接口
type ReconcileStep interface {
	Execute(ctx context.Context, data *ReconcileData) (ctrl.Result, error)
	Name() string
}

// Pipeline 管道执行器
type Pipeline struct {
	steps []ReconcileStep
}

func NewPipeline(steps ...ReconcileStep) *Pipeline {
	return &Pipeline{steps: steps}
}

func (p *Pipeline) Execute(ctx context.Context, data *ReconcileData) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	for _, step := range p.steps {
		logger.Info("Executing pipeline step", "step", step.Name())
		result, err := step.Execute(ctx, data)
		if err != nil {
			logger.Error(err, "Pipeline step failed", "step", step.Name())
			return result, err
		}
		if result.RequeueAfter > 0 {
			logger.Info("Pipeline step requested requeue",
				"step", step.Name(),
				"requeueAfter", result.RequeueAfter)
			return result, nil
		}
	}
	return ctrl.Result{}, nil
}
