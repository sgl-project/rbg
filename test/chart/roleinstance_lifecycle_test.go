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
	for _, webhook := range configuration.Webhooks {
		if webhook.Name != "vroleinstanceset.kb.io" {
			continue
		}
		if path := webhook.ClientConfig.Service.Path; path == nil || *path != "/validate-workloads-x-k8s-io-v1alpha2-roleinstanceset" {
			t.Errorf("vroleinstanceset.kb.io path = %v, want /validate-workloads-x-k8s-io-v1alpha2-roleinstanceset", path)
		}
		for _, rule := range webhook.Rules {
			for _, resource := range rule.Resources {
				if resource == "roleinstancesets" {
					return
				}
			}
		}
		t.Fatalf("vroleinstanceset.kb.io does not validate roleinstancesets: %+v", webhook.Rules)
	}
	t.Fatal("vroleinstanceset.kb.io webhook not registered")
}
