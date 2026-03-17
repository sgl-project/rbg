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

package writer

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"sigs.k8s.io/rbgs/pkg/webhook/cert/generator"
)

const (
	// Secret data keys
	CAKeyName      = "ca-key.pem"
	CACertName     = "ca-cert.pem"
	ServerKeyName  = "tls.key"
	ServerCertName = "tls.crt"

	// certRenewBefore is how far before expiry we proactively renew.
	certRenewBefore = 6 * 30 * 24 * time.Hour // ~6 months
)

var log = ctrl.Log.WithName("webhook-cert-writer")

// Options configure the cert writer.
type Options struct {
	// Client is used to read/write the backing Secret.
	Client client.Client
	// Secret identifies the Secret that stores the certificate material.
	Secret types.NamespacedName
	// CertGenerator optionally overrides the certificate generator.
	// Defaults to SelfSignedCertGenerator.
	CertGenerator generator.CertGenerator
}

// CertWriter provisions and persists TLS certificates.
type CertWriter struct {
	opts Options
}

// New creates a CertWriter from the given options.
func New(opts Options) (*CertWriter, error) {
	if opts.Client == nil {
		return nil, fmt.Errorf("opts.Client must not be nil")
	}
	if opts.Secret.Name == "" || opts.Secret.Namespace == "" {
		return nil, fmt.Errorf("opts.Secret must have a name and namespace")
	}
	if opts.CertGenerator == nil {
		opts.CertGenerator = &generator.SelfSignedCertGenerator{}
	}
	return &CertWriter{opts: opts}, nil
}

// EnsureCert returns valid certificate artifacts for dnsName, creating or
// renewing them as needed. changed is true when new certificates were written.
func (w *CertWriter) EnsureCert(ctx context.Context, dnsName string) (artifacts *generator.Artifacts, changed bool, err error) {
	artifacts, err = w.read(ctx)
	if apierrors.IsNotFound(err) {
		artifacts, err = w.generate(ctx, dnsName)
		return artifacts, true, err
	}
	if err != nil {
		return nil, false, err
	}

	// Reload CA into generator so it signs with the same CA.
	if artifacts.CAKey != nil && artifacts.CACert != nil {
		w.opts.CertGenerator.SetCA(artifacts.CAKey, artifacts.CACert)
	}

	// Renew if the cert is invalid or expiring within certRenewBefore.
	if !generator.ValidCert(artifacts.Key, artifacts.Cert, artifacts.CACert, dnsName, time.Now().Add(certRenewBefore)) {
		log.Info("webhook cert is invalid or expiring, regenerating", "dnsName", dnsName)
		artifacts, err = w.generate(ctx, dnsName)
		return artifacts, true, err
	}

	return artifacts, false, nil
}

// WriteCertsToDir writes tls.crt and tls.key into dir so the webhook server can load them.
func WriteCertsToDir(dir string, artifacts *generator.Artifacts) error {
	if err := os.MkdirAll(dir, 0750); err != nil {
		return fmt.Errorf("creating cert dir %s: %w", dir, err)
	}
	files := map[string][]byte{
		ServerCertName: artifacts.Cert,
		ServerKeyName:  artifacts.Key,
	}
	for name, data := range files {
		path := filepath.Join(dir, name)
		// Private key written read-only by owner (0400); cert may be world-readable (0644).
		perm := os.FileMode(0644)
		if name == ServerKeyName {
			perm = 0400
		}
		if err := os.WriteFile(path, data, perm); err != nil {
			return fmt.Errorf("writing %s: %w", path, err)
		}
	}
	return nil
}

// --------------------------------------------------------------------------
// internal helpers
// --------------------------------------------------------------------------

func (w *CertWriter) read(ctx context.Context) (*generator.Artifacts, error) {
	secret := &corev1.Secret{}
	if err := w.opts.Client.Get(ctx, w.opts.Secret, secret); err != nil {
		return nil, err
	}
	a := &generator.Artifacts{ResourceVersion: secret.ResourceVersion}
	if secret.Data != nil {
		a.CAKey = secret.Data[CAKeyName]
		a.CACert = secret.Data[CACertName]
		a.Key = secret.Data[ServerKeyName]
		a.Cert = secret.Data[ServerCertName]
	}
	return a, nil
}

func (w *CertWriter) generate(ctx context.Context, dnsName string) (*generator.Artifacts, error) {
	artifacts, err := w.opts.CertGenerator.Generate(dnsName)
	if err != nil {
		return nil, fmt.Errorf("generating cert for %s: %w", dnsName, err)
	}
	if err := w.upsertSecret(ctx, artifacts); err != nil {
		return nil, err
	}
	return artifacts, nil
}

func (w *CertWriter) upsertSecret(ctx context.Context, artifacts *generator.Artifacts) error {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: w.opts.Secret.Namespace,
			Name:      w.opts.Secret.Name,
		},
		Data: map[string][]byte{
			CAKeyName:      artifacts.CAKey,
			CACertName:     artifacts.CACert,
			ServerKeyName:  artifacts.Key,
			ServerCertName: artifacts.Cert,
		},
	}

	existing := &corev1.Secret{}
	err := w.opts.Client.Get(ctx, w.opts.Secret, existing)
	if apierrors.IsNotFound(err) {
		log.Info("creating webhook cert secret", "secret", w.opts.Secret)
		return w.opts.Client.Create(ctx, secret)
	}
	if err != nil {
		return err
	}
	secret.ResourceVersion = existing.ResourceVersion
	log.Info("updating webhook cert secret", "secret", w.opts.Secret)
	return w.opts.Client.Update(ctx, secret)
}
