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
	"fmt"
	"time"
)

// Scenario holds the stress test configuration.
type Scenario struct {
	Kubeconfig          string
	Namespace           string
	TotalRBGs           int
	RolesPerRBG         int
	LWSRoles            int
	LWSSize             int
	CreateQPS           float64
	UpdateQPS           float64
	DeleteQPS           float64
	InPlaceUpdate       bool
	UseKwokNodes        bool
	PprofAddr           string
	OutputDir           string
	Timeout             time.Duration
	ControllerNamespace string
	ControllerLabel     string

	// Collected at runtime
	ControllerInfo ControllerInfo
}

// ControllerInfo holds runtime info about the deployed controller.
type ControllerInfo struct {
	Image         string
	Replicas      int
	CPULimit      string
	MemoryLimit   string
	CPURequest    string
	MemoryRequest string
	Args          []string
	NodeName      string
}

// Validate checks the scenario configuration.
func (s *Scenario) Validate() error {
	if s.TotalRBGs <= 0 {
		return fmt.Errorf("total-rbgs must be positive, got %d", s.TotalRBGs)
	}
	if s.RolesPerRBG <= 0 {
		return fmt.Errorf("roles-per-rbg must be positive, got %d", s.RolesPerRBG)
	}
	if s.LWSRoles > s.RolesPerRBG {
		return fmt.Errorf("lws-roles (%d) cannot exceed roles-per-rbg (%d)", s.LWSRoles, s.RolesPerRBG)
	}
	if s.LWSSize < 2 && s.LWSRoles > 0 {
		return fmt.Errorf("lws-size must be at least 2, got %d", s.LWSSize)
	}
	if s.CreateQPS <= 0 || s.UpdateQPS <= 0 || s.DeleteQPS <= 0 {
		return fmt.Errorf("QPS values must be positive")
	}
	return nil
}
