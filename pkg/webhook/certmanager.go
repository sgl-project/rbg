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

package webhook

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"time"

	apiextv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"sigs.k8s.io/rbgs/pkg/webhook/cert/generator"
	certwriter "sigs.k8s.io/rbgs/pkg/webhook/cert/writer"
)

var certLog = ctrl.Log.WithName("webhook-cert-manager")

// Fixed deployment constants for the self-signed webhook certificate.
// These values must match the kustomize/helm deployment manifests.
const (
	// WebhookServiceName is the name of the Service that fronts the webhook server.
	WebhookServiceName = "rbgs-webhook-service"
	// WebhookCertSecretName is the name of the Secret that stores the TLS certificate.
	WebhookCertSecretName = "rbgs-webhook-cert"
	// WebhookCertDir is the directory where TLS certificate files are written for the webhook server.
	WebhookCertDir = "/tmp/k8s-webhook-server/certs"
)

// CertManager generates self-signed TLS certificates and keeps CRD conversion
// webhook caBundle fields up to date.
type CertManager struct {
	client     client.Client
	certWriter certwriter.CertWriter
}

// NewCertManager creates a CertManager that stores certificate material in the
// named Secret and keeps the given CRDs patched with the CA bundle.
func NewCertManager(c client.Client, secretName, secretNamespace string) (*CertManager, error) {
	cw, err := certwriter.NewSecretCertWriter(certwriter.SecretCertWriterOptions{
		Client: c,
		Secret: &types.NamespacedName{
			Name:      secretName,
			Namespace: secretNamespace,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("creating cert writer: %w", err)
	}
	return &CertManager{client: c, certWriter: cw}, nil
}

// BuildOrSync ensures a valid certificate exists for the webhook Service DNS name,
// writes it to certDir (for the webhook server to load), and returns the CA certificate.
func (m *CertManager) BuildOrSync(ctx context.Context, namespace, serviceName, certDir string) ([]byte, error) {
	dnsName := generator.ServiceToCommonName(namespace, serviceName)
	certLog.Info("ensuring webhook TLS certificate", "dnsName", dnsName)

	artifacts, changed, err := m.certWriter.EnsureCert(dnsName)
	if err != nil {
		return nil, fmt.Errorf("ensuring webhook cert: %w", err)
	}
	if changed {
		certLog.Info("webhook certificate was created or renewed")
	}

	if err := certwriter.WriteCertsToDir(certDir, artifacts); err != nil {
		return nil, fmt.Errorf("writing certs to %s: %w", certDir, err)
	}

	return artifacts.CACert, nil
}

// patchRetryAttempts is the number of times PatchCRDCABundle retries a failed
// patch before giving up. The delay between attempts follows exponential backoff
// starting at patchRetryBaseDelay.
const (
	patchRetryAttempts  = 5
	patchRetryBaseDelay = 500 * time.Millisecond
)

// PatchCRDCABundle patches spec.conversion.webhook.clientConfig.caBundle on each
// named CRD with the given CA certificate. This is idempotent.
// Each CRD patch is retried up to patchRetryAttempts times with exponential
// backoff before being counted as a failure. All CRDs are attempted even if one
// fails; errors are aggregated.
func (m *CertManager) PatchCRDCABundle(ctx context.Context, crdNames []string, caCert []byte) error {
	var errs []error
	for _, name := range crdNames {
		if err := m.patchOneCRDWithRetry(ctx, name, caCert); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// patchOneCRDWithRetry calls patchOneCRD with exponential backoff.
func (m *CertManager) patchOneCRDWithRetry(ctx context.Context, crdName string, caCert []byte) error {
	delay := patchRetryBaseDelay
	var lastErr error
	for attempt := 1; attempt <= patchRetryAttempts; attempt++ {
		if lastErr = m.patchOneCRD(ctx, crdName, caCert); lastErr == nil {
			return nil
		}
		if attempt == patchRetryAttempts {
			break
		}
		certLog.Info("retrying caBundle patch", "crd", crdName, "attempt", attempt, "delay", delay, "error", lastErr)
		select {
		case <-ctx.Done():
			return fmt.Errorf("context cancelled while retrying caBundle patch for %s: %w", crdName, ctx.Err())
		case <-time.After(delay):
		}
		delay *= 2
	}
	return fmt.Errorf("patching caBundle on CRD %s failed after %d attempts: %w", crdName, patchRetryAttempts, lastErr)
}

func (m *CertManager) patchOneCRD(ctx context.Context, crdName string, caCert []byte) error {
	crd := &apiextv1.CustomResourceDefinition{}
	if err := m.client.Get(ctx, client.ObjectKey{Name: crdName}, crd); err != nil {
		return fmt.Errorf("getting CRD %s: %w", crdName, err)
	}

	if crd.Spec.Conversion == nil || crd.Spec.Conversion.Webhook == nil ||
		crd.Spec.Conversion.Webhook.ClientConfig == nil {
		certLog.Info("CRD has no conversion webhook clientConfig, skipping caBundle patch", "crd", crdName)
		return nil
	}

	if reflect.DeepEqual(crd.Spec.Conversion.Webhook.ClientConfig.CABundle, caCert) {
		certLog.V(1).Info("CRD caBundle already up to date", "crd", crdName)
		return nil
	}

	// client.MergeFrom requires a base snapshot; DeepCopy is used here so the
	// patch correctly represents only the caBundle field change. This function
	// is called at most once per CRD at startup and every ~10 minutes by the
	// cert controller — not a hot path, so the allocation is acceptable.
	patch := client.MergeFrom(crd.DeepCopy())
	crd.Spec.Conversion.Webhook.ClientConfig.CABundle = caCert
	if err := m.client.Patch(ctx, crd, patch); err != nil {
		return fmt.Errorf("patching caBundle on CRD %s: %w", crdName, err)
	}

	certLog.Info("patched caBundle on CRD", "crd", crdName)
	return nil
}

// ConversionWebhookCRDs returns the names of the CRDs that use the conversion webhook.
func ConversionWebhookCRDs() []string {
	return []string{
		"rolebasedgroups.workloads.x-k8s.io",
		"rolebasedgroupsets.workloads.x-k8s.io",
	}
}
