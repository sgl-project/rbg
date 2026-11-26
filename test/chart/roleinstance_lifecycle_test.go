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

package chart

import (
	"testing"

	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	"sigs.k8s.io/yaml"
)

func TestRoleInstanceLifecycleHooksChartRegistersValidationWebhook(t *testing.T) {
	manifest := renderChart(t)
	obj, ok := splitObjects(t, manifest)["ValidatingWebhookConfiguration/rbgs-validating-webhook-configuration"]
	if !ok {
		t.Fatalf("ValidatingWebhookConfiguration %q not found in rendered chart",
			"rbgs-validating-webhook-configuration")
	}
	var configuration admissionregistrationv1.ValidatingWebhookConfiguration
	if err := yaml.Unmarshal([]byte(obj.raw), &configuration); err != nil {
		t.Fatalf("unmarshal ValidatingWebhookConfiguration: %v", err)
	}
	webhook := findWebhook(t, configuration.Webhooks, "vroleinstanceset.kb.io")
	if path := webhook.ClientConfig.Service.Path; path == nil || *path != "/validate-workloads-x-k8s-io-v1alpha2-roleinstanceset" {
		t.Errorf("vroleinstanceset.kb.io path = %v, want /validate-workloads-x-k8s-io-v1alpha2-roleinstanceset", path)
	}
	if !ruleMatches(webhook.Rules, []string{"CREATE", "UPDATE"}, "roleinstancesets") {
		t.Fatalf("vroleinstanceset.kb.io does not validate roleinstancesets: %+v", webhook.Rules)
	}
	// The safety-critical, hand-maintained routing: if this selector drifts,
	// every ordinary RoleInstanceSet write depends on webhook availability
	// during a mixed version rollout.
	if webhook.ObjectSelector == nil ||
		webhook.ObjectSelector.MatchLabels["rbg.workloads.x-k8s.io/role-instance-lifecycle-configured"] != "true" {
		t.Fatalf("vroleinstanceset.kb.io objectSelector = %+v, want the lifecycle-configured label",
			webhook.ObjectSelector)
	}
}

// The RoleInstance DELETE guard is hand-maintained in the chart. Pin its
// name, its DELETE-only rule, and the exact objectSelector: a rule or label
// drift silently disables the preDelete protection.
func TestRoleInstanceLifecycleHooksChartPinsDeleteGuardWebhook(t *testing.T) {
	manifest := renderChart(t)
	obj, ok := splitObjects(t, manifest)["ValidatingWebhookConfiguration/rbgs-validating-webhook-configuration"]
	if !ok {
		t.Fatalf("ValidatingWebhookConfiguration %q not found in rendered chart",
			"rbgs-validating-webhook-configuration")
	}
	var configuration admissionregistrationv1.ValidatingWebhookConfiguration
	if err := yaml.Unmarshal([]byte(obj.raw), &configuration); err != nil {
		t.Fatalf("unmarshal ValidatingWebhookConfiguration: %v", err)
	}
	webhook := findWebhook(t, configuration.Webhooks, "vroleinstance.kb.io")
	if !ruleMatches(webhook.Rules, []string{"DELETE"}, "roleinstances") {
		t.Fatalf("vroleinstance.kb.io must be DELETE-only on roleinstances: %+v", webhook.Rules)
	}
	if webhook.ObjectSelector == nil || len(webhook.ObjectSelector.MatchLabels) != 1 ||
		webhook.ObjectSelector.MatchLabels["rbg.workloads.x-k8s.io/role-instance-pre-delete-armed"] != "true" {
		t.Fatalf("vroleinstance.kb.io objectSelector = %+v, want exactly the pre-delete-armed label",
			webhook.ObjectSelector)
	}
}

func findWebhook(t *testing.T, webhooks []admissionregistrationv1.ValidatingWebhook, name string) admissionregistrationv1.ValidatingWebhook {
	t.Helper()
	for _, webhook := range webhooks {
		if webhook.Name == name {
			return webhook
		}
	}
	t.Fatalf("webhook %q not registered", name)
	return admissionregistrationv1.ValidatingWebhook{}
}

func ruleMatches(rules []admissionregistrationv1.RuleWithOperations, operations []string, resource string) bool {
	for _, rule := range rules {
		if len(rule.Operations) != len(operations) {
			continue
		}
		operationsMatch := true
		for i, operation := range rule.Operations {
			if string(operation) != operations[i] {
				operationsMatch = false
				break
			}
		}
		if !operationsMatch {
			continue
		}
		for _, candidate := range rule.Resources {
			if candidate == resource {
				return true
			}
		}
	}
	return false
}
