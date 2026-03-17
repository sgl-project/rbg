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
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"sigs.k8s.io/rbgs/pkg/webhook/cert/generator"
)

// scheme used by the fake client
var testScheme = func() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(s)
	return s
}()

func secretKey() types.NamespacedName {
	return types.NamespacedName{Namespace: "test-ns", Name: "webhook-cert"}
}

func newWriter(t *testing.T, objs ...client.Object) *CertWriter {
	t.Helper()
	cl := fake.NewClientBuilder().WithScheme(testScheme).WithObjects(objs...).Build()
	w, err := New(Options{Client: cl, Secret: secretKey()})
	require.NoError(t, err)
	return w
}

// ── New ──────────────────────────────────────────────────────────────────────

func TestNew_RejectsNilClient(t *testing.T) {
	_, err := New(Options{Secret: secretKey()})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Client")
}

func TestNew_RejectsMissingSecretName(t *testing.T) {
	cl := fake.NewClientBuilder().WithScheme(testScheme).Build()
	_, err := New(Options{Client: cl, Secret: types.NamespacedName{Namespace: "ns"}})
	require.Error(t, err)
}

func TestNew_RejectsMissingSecretNamespace(t *testing.T) {
	cl := fake.NewClientBuilder().WithScheme(testScheme).Build()
	_, err := New(Options{Client: cl, Secret: types.NamespacedName{Name: "name"}})
	require.Error(t, err)
}

// ── EnsureCert ───────────────────────────────────────────────────────────────

func TestEnsureCert_CreatesSecretWhenAbsent(t *testing.T) {
	w := newWriter(t) // no pre-existing secret
	ctx := context.Background()

	artifacts, changed, err := w.EnsureCert(ctx, "webhook.test.svc")
	require.NoError(t, err)
	assert.True(t, changed, "should report changed=true when creating a new cert")
	assert.NotEmpty(t, artifacts.CACert)
	assert.NotEmpty(t, artifacts.Cert)
	assert.NotEmpty(t, artifacts.Key)

	// Secret must now exist in the fake store.
	secret := &corev1.Secret{}
	require.NoError(t, w.opts.Client.Get(ctx, secretKey(), secret))
	assert.NotEmpty(t, secret.Data[CACertName])
	assert.NotEmpty(t, secret.Data[ServerCertName])
	assert.NotEmpty(t, secret.Data[ServerKeyName])
}

func TestEnsureCert_ReusesValidExistingCert(t *testing.T) {
	// Generate a fresh cert and pre-load it in the fake store.
	g := &generator.SelfSignedCertGenerator{}
	a, err := g.Generate("webhook.test.svc")
	require.NoError(t, err)

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: "test-ns", Name: "webhook-cert"},
		Data: map[string][]byte{
			CAKeyName:      a.CAKey,
			CACertName:     a.CACert,
			ServerKeyName:  a.Key,
			ServerCertName: a.Cert,
		},
	}
	w := newWriter(t, secret)

	artifacts, changed, err := w.EnsureCert(context.Background(), "webhook.test.svc")
	require.NoError(t, err)
	assert.False(t, changed, "should report changed=false when cert is still valid")
	assert.Equal(t, a.Cert, artifacts.Cert, "returned cert must match the stored one")
}

func TestEnsureCert_RenewsWhenCertInvalid(t *testing.T) {
	// Store a Secret with corrupted/empty cert data — ValidCert will return false,
	// triggering the renewal path.
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: "test-ns", Name: "webhook-cert"},
		Data: map[string][]byte{
			CAKeyName:      []byte("not-a-real-key"),
			CACertName:     []byte("not-a-real-cert"),
			ServerKeyName:  []byte("not-a-real-key"),
			ServerCertName: []byte("not-a-real-cert"),
		},
	}
	w := newWriter(t, secret)

	_, changed, err := w.EnsureCert(context.Background(), "webhook.test.svc")
	require.NoError(t, err)
	assert.True(t, changed, "should renew when stored cert is invalid")
}

// ── WriteCertsToDir ───────────────────────────────────────────────────────────

func TestWriteCertsToDir_WritesFiles(t *testing.T) {
	g := &generator.SelfSignedCertGenerator{}
	a, err := g.Generate("webhook.test.svc")
	require.NoError(t, err)

	dir := t.TempDir()
	require.NoError(t, WriteCertsToDir(dir, a))

	certBytes, err := os.ReadFile(filepath.Join(dir, ServerCertName))
	require.NoError(t, err)
	assert.Equal(t, a.Cert, certBytes)

	keyBytes, err := os.ReadFile(filepath.Join(dir, ServerKeyName))
	require.NoError(t, err)
	assert.Equal(t, a.Key, keyBytes)
}

func TestWriteCertsToDir_KeyFilePermission(t *testing.T) {
	g := &generator.SelfSignedCertGenerator{}
	a, err := g.Generate("svc.ns.svc")
	require.NoError(t, err)

	dir := t.TempDir()
	require.NoError(t, WriteCertsToDir(dir, a))

	info, err := os.Stat(filepath.Join(dir, ServerKeyName))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0400), info.Mode().Perm(),
		"private key must be read-only by owner (0400)")
}

func TestWriteCertsToDir_CertFilePermission(t *testing.T) {
	g := &generator.SelfSignedCertGenerator{}
	a, err := g.Generate("svc.ns.svc")
	require.NoError(t, err)

	dir := t.TempDir()
	require.NoError(t, WriteCertsToDir(dir, a))

	info, err := os.Stat(filepath.Join(dir, ServerCertName))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0644), info.Mode().Perm(),
		"cert file must be world-readable (0644)")
}

func TestWriteCertsToDir_CreatesDirectoryIfAbsent(t *testing.T) {
	g := &generator.SelfSignedCertGenerator{}
	a, err := g.Generate("svc.ns.svc")
	require.NoError(t, err)

	dir := filepath.Join(t.TempDir(), "nested", "dir")
	require.NoError(t, WriteCertsToDir(dir, a))
	assert.FileExists(t, filepath.Join(dir, ServerCertName))
}
