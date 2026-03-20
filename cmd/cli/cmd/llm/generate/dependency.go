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

package generate

import (
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"

	"k8s.io/klog/v2"
)

// CheckConfiguratorAvailability checks if the specified configurator tool is installed
func CheckConfiguratorAvailability(toolName string) error {
	// Check if the tool command exists in PATH using cross-platform LookPath
	if _, err := exec.LookPath(toolName); err != nil {
		return fmt.Errorf("%s is not installed or not found in PATH\n\n"+
			"Please ensure the tool is installed and available in your system PATH", toolName)
	}

	klog.Infof("Found configurator tool: %s", toolName)
	return nil
}

// CheckAIConfiguratorAvailability checks if aiconfigurator is installed
func CheckAIConfiguratorAvailability() error {
	return CheckAIConfiguratorAvailabilityWithVersion()
}

// CheckAIConfiguratorAvailabilityWithVersion checks if aiconfigurator is installed and meets version requirements
func CheckAIConfiguratorAvailabilityWithVersion() error {
	// Check if aiconfigurator command exists in PATH using cross-platform LookPath
	if _, err := exec.LookPath(AIConfigurator); err != nil {
		return fmt.Errorf("aiconfigurator is not installed\n\n" +
			"Please install it using one of the following methods:\n" +
			"  pip install aiconfigurator\n" +
			"Or visit: https://github.com/ai-dynamo/aiconfigurator")
	}

	// Try to get version information
	versionCmd := exec.Command(AIConfigurator, "version")
	output, err := versionCmd.CombinedOutput()
	if err != nil {
		// Version command failed, but tool exists - continue with warning
		klog.Warning("Could not determine aiconfigurator version, but tool is available")
		return nil
	}

	// Extract version number from output using regex
	// Expected format: "aiconfigurator 0.5.0" or just "0.5.0"
	outputStr := string(output)
	klog.V(2).Infof("aiconfigurator version output: %s", outputStr)

	versionRegex := regexp.MustCompile(`(\d+\.\d+\.\d+)`)
	matches := versionRegex.FindStringSubmatch(outputStr)

	if len(matches) < 2 {
		klog.Warning("Could not parse aiconfigurator version from output, but tool is available")
		return nil
	}

	versionStr := matches[1]
	klog.Infof("Found aiconfigurator version: %s", versionStr)

	// Check if version >= 0.5.0
	if err := checkMinVersion(versionStr, "0.5.0"); err != nil {
		return fmt.Errorf("aiconfigurator version %s is too old (minimum required: 0.5.0)\n\n"+
			"Please upgrade using:\n"+
			"  pip install --upgrade aiconfigurator", versionStr)
	}

	return nil
}

// checkMinVersion checks if actual version >= required version
// Both versions should be in format "x.y.z"
func checkMinVersion(actual, required string) error {
	actualParts := strings.Split(actual, ".")
	requiredParts := strings.Split(required, ".")

	if len(actualParts) != 3 || len(requiredParts) != 3 {
		return fmt.Errorf("invalid version format")
	}

	for i := 0; i < 3; i++ {
		actualNum, err := strconv.Atoi(actualParts[i])
		if err != nil {
			return fmt.Errorf("invalid version number: %s", actual)
		}

		requiredNum, err := strconv.Atoi(requiredParts[i])
		if err != nil {
			return fmt.Errorf("invalid version number: %s", required)
		}

		if actualNum > requiredNum {
			return nil // actual version is higher
		}
		if actualNum < requiredNum {
			return fmt.Errorf("version too old") // actual version is lower
		}
		// If equal, continue to check next part
	}

	// All parts are equal, version is exactly the required version
	return nil
}
