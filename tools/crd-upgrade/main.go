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

// Package main implements a CRD upgrader that replaces the previous
// ensure-crds-up-to-date.sh + kubectl approach. It reads CRD YAML files
// from /rbgs/crds, creates or replaces each CRD in the cluster, then
// patches the conversion webhook clientConfig on CRDs that need it.
//
// By compiling this program with the same Go toolchain and vendored
// dependency tree as the manager binary, the resulting image carries
// no pre-built kubectl binary and inherits all the vulnerability fixes
// already applied in go.mod / vendor.
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	apiextv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var (
	scheme = runtime.NewScheme()
	codecs serializer.CodecFactory
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(apiextv1.AddToScheme(scheme))
	codecs = serializer.NewCodecFactory(scheme)
}

const (
	defaultWebhookNamespace = "rbgs-system"
	defaultWebhookService   = "rbgs-webhook-service"
	crdDir                  = "/rbgs/crds"
)

// conversionCRDs is the list of CRDs that require a conversion webhook
// clientConfig. After create/replace, spec.conversion is patched with the
// correct service name and namespace so Kubernetes routes conversion
// requests to the webhook.
var conversionCRDs = []string{
	"rolebasedgroups.workloads.x-k8s.io",
	"rolebasedgroupsets.workloads.x-k8s.io",
}

func main() {
	webhookNamespace := os.Getenv("WEBHOOK_NAMESPACE")
	if webhookNamespace == "" {
		webhookNamespace = defaultWebhookNamespace
	}
	webhookService := os.Getenv("WEBHOOK_SERVICE")
	if webhookService == "" {
		webhookService = defaultWebhookService
	}

	config := ctrl.GetConfigOrDie()

	k8sClient, err := client.New(config, client.Options{Scheme: scheme})
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to create Kubernetes client: %v\n", err)
		os.Exit(1)
	}

	ctx := context.Background()

	// Read and process each CRD YAML file.
	entries, err := os.ReadDir(crdDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to read CRD directory %s: %v\n", crdDir, err)
		os.Exit(1)
	}

	for _, entry := range entries {
		ext := filepath.Ext(entry.Name())
		if ext != ".yaml" && ext != ".yml" {
			continue
		}
		path := filepath.Join(crdDir, entry.Name())
		if err := ensureCRD(ctx, k8sClient, path); err != nil {
			fmt.Fprintf(os.Stderr, "failed to ensure CRD from %s: %v\n", path, err)
			os.Exit(1)
		}
	}

	// Patch spec.conversion on CRDs that use the conversion webhook.
	// The base CRD files (from controller-gen) do not include spec.conversion;
	// it must be applied separately, mirroring what Kustomize does via
	// config/crd/patches/webhook_in_*.yaml.
	for _, crdName := range conversionCRDs {
		if err := patchConversionWebhook(ctx, k8sClient, crdName, webhookNamespace, webhookService); err != nil {
			fmt.Fprintf(os.Stderr, "failed to patch conversion webhook on CRD %s: %v\n", crdName, err)
			os.Exit(1)
		}
	}
}

// ensureCRD reads a CRD YAML file and creates or replaces the CRD in the
// cluster. This is equivalent to the shell script's
//
//	kubectl get  --ignore-not-found -f "$crdfile"
//	kubectl replace -f "$crdfile"   # if exists
//	kubectl create -f "$crdfile"    # if not
//
// sequence.
func ensureCRD(ctx context.Context, k8sClient client.Client, path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("reading %s: %w", path, err)
	}

	crd := &apiextv1.CustomResourceDefinition{}
	if err := runtime.DecodeInto(codecs.UniversalDeserializer(), data, crd); err != nil {
		return fmt.Errorf("decoding %s: %w", path, err)
	}

	crdName := crd.Name
	existing := &apiextv1.CustomResourceDefinition{}
	err = k8sClient.Get(ctx, client.ObjectKey{Name: crdName}, existing)

	if err == nil {
		// CRD exists — replace it (equivalent to `kubectl replace -f`).
		fmt.Printf("%s found, replacing its crd...\n", crdName)
		// Preserve server-managed metadata for optimistic concurrency.
		crd.UID = existing.UID
		crd.ResourceVersion = existing.ResourceVersion
		if err := k8sClient.Update(ctx, crd); err != nil {
			return fmt.Errorf("replacing CRD %s: %w", crdName, err)
		}
	} else if client.IgnoreNotFound(err) == nil {
		// CRD does not exist — create it.
		fmt.Printf("%s not found, creating its crd...\n", crdName)
		if err := k8sClient.Create(ctx, crd); err != nil {
			return fmt.Errorf("creating CRD %s: %w", crdName, err)
		}
	} else {
		return fmt.Errorf("getting CRD %s: %w", crdName, err)
	}

	return nil
}

// patchConversionWebhook patches spec.conversion on the named CRD so that
// Kubernetes routes conversion requests to the in-cluster webhook service.
// This is equivalent to
//
//	kubectl patch crd "$crd" --type=merge -p "${CONVERSION_PATCH}"
//
// but uses typed structs and client.MergeFrom so that serialization is
// handled by json.Marshal, which escapes special characters automatically.
func patchConversionWebhook(ctx context.Context, k8sClient client.Client, crdName, namespace, serviceName string) error {
	fmt.Printf("Patching conversion webhook on CRD: %s\n", crdName)

	convertPath := "/convert"

	base := &apiextv1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{
			Name: crdName,
		},
	}
	modified := base.DeepCopy()
	modified.Spec.Conversion = &apiextv1.CustomResourceConversion{
		Strategy: apiextv1.WebhookConverter,
		Webhook: &apiextv1.WebhookConversion{
			ConversionReviewVersions: []string{"v1"},
			ClientConfig: &apiextv1.WebhookClientConfig{
				Service: &apiextv1.ServiceReference{
					Namespace: namespace,
					Name:      serviceName,
					Path:      &convertPath,
				},
			},
		},
	}

	if err := k8sClient.Patch(ctx, modified, client.MergeFrom(base)); err != nil {
		return fmt.Errorf("patching conversion webhook on CRD %s: %w", crdName, err)
	}
	return nil
}
