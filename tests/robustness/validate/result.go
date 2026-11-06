// Copyright 2025 The etcd Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package validate

import (
	"errors"
	"fmt"

	"github.com/anishathalye/porcupine"
	"go.uber.org/zap"
)

type RobustnessResult struct {
	Assumptions   Result
	Linearization LinearizationResult
	Watch         Result
	Serializable  Result
}

type Result struct {
	Status  ResultStatus
	Message string
}

type ResultStatus string

var (
	Unknown ResultStatus
	Success ResultStatus = "Success"
	Failure ResultStatus = "Failure"
	Timeout ResultStatus = "Timeout"
	Skipped ResultStatus = "Skipped"
)

func (r RobustnessResult) Error() error {
	if err := r.Assumptions.Error(); err != nil {
		return fmt.Errorf("assumptions: %w", err)
	}
	if err := r.Linearization.Error(); err != nil {
		return fmt.Errorf("linearization: %w", err)
	}
	if err := r.Watch.Error(); err != nil {
		return fmt.Errorf("watch: %w", err)
	}
	if err := r.Serializable.Error(); err != nil {
		return fmt.Errorf("serializable: %w", err)
	}
	return nil
}

func ResultFromError(err error) Result {
	if err != nil {
		return Result{
			Status:  Failure,
			Message: err.Error(),
		}
	}
	return Result{
		Status: Success,
	}
}

func (r Result) Error() error {
	switch r.Status {
	case Success, Unknown:
		return nil
	default:
		return errors.New(r.String())
	}
}

func (r Result) String() string {
	if r.Message != "" {
		return fmt.Sprintf("%s: %s", r.Status, r.Message)
	}
	return string(r.Status)
}

type LinearizationResult struct {
	Info  porcupine.LinearizationInfo
	Model porcupine.Model
	Result
}

func (r *LinearizationResult) Visualize(lg *zap.Logger, path string) error {
	if r.Model.Step == nil && r.Model.StepContext == nil {
		lg.Info("Skipping linearization visualization as model is not initialized")
		return nil
	}
	err := r.Error()
	if err != nil {
		lg.Info("Skipping linearization visualization", zap.Error(err))
		return nil
	}

	lg.Info("Saving visualization", zap.String("path", path))
	err = porcupine.VisualizePath(r.Model, r.Info, path)
	if err != nil {
		return fmt.Errorf("failed to visualize, err: %w", err)
	}
	return nil
}

func (r *LinearizationResult) AddToVisualization(serializable []porcupine.Operation) {
	annotations := []porcupine.Annotation{}
	for _, op := range serializable {
		annotations = append(annotations, porcupine.Annotation{
			ClientId:    op.ClientId,
			Start:       op.Call,
			End:         op.Return,
			Description: r.Model.DescribeOperation(op.Input, op.Output),
			Details:     r.Model.DescribeOperationMetadata(op.Metadata),
		})
	}
	r.Info.AddAnnotations(annotations)
}
