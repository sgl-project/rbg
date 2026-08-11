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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func newScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	require.NoError(t, admissionregistrationv1.AddToScheme(s))
	return s
}

func TestPatchWebhookCABundle_ValidatingNeedsUpdate(t *testing.T) {
	caCert := []byte("new-ca-bundle")
	oldCert := []byte("old-ca-bundle")

	vwc := &admissionregistrationv1.ValidatingWebhookConfiguration{
		ObjectMeta: metav1.ObjectMeta{Name: "test-vwc"},
		Webhooks: []admissionregistrationv1.ValidatingWebhook{
			{Name: "hook1", ClientConfig: admissionregistrationv1.WebhookClientConfig{CABundle: oldCert}},
			{Name: "hook2", ClientConfig: admissionregistrationv1.WebhookClientConfig{CABundle: oldCert}},
		},
	}

	s := newScheme(t)
	fakeClient := fake.NewClientBuilder().WithScheme(s).WithObjects(vwc).Build()
	m := &CertManager{client: fakeClient}

	err := m.patchWebhookCABundle(context.Background(), "test-vwc", "ValidatingWebhookConfiguration",
		&admissionregistrationv1.ValidatingWebhookConfiguration{}, caCert)
	require.NoError(t, err)

	// Verify the CABundle was updated
	got := &admissionregistrationv1.ValidatingWebhookConfiguration{}
	require.NoError(t, fakeClient.Get(context.Background(), client.ObjectKeyFromObject(vwc), got))
	for _, wh := range got.Webhooks {
		assert.Equal(t, caCert, wh.ClientConfig.CABundle)
	}
}

func TestPatchWebhookCABundle_MutatingNeedsUpdate(t *testing.T) {
	caCert := []byte("new-ca-bundle")
	oldCert := []byte("old-ca-bundle")

	mwc := &admissionregistrationv1.MutatingWebhookConfiguration{
		ObjectMeta: metav1.ObjectMeta{Name: "test-mwc"},
		Webhooks: []admissionregistrationv1.MutatingWebhook{
			{Name: "hook1", ClientConfig: admissionregistrationv1.WebhookClientConfig{CABundle: oldCert}},
		},
	}

	s := newScheme(t)
	fakeClient := fake.NewClientBuilder().WithScheme(s).WithObjects(mwc).Build()
	m := &CertManager{client: fakeClient}

	err := m.patchWebhookCABundle(context.Background(), "test-mwc", "MutatingWebhookConfiguration",
		&admissionregistrationv1.MutatingWebhookConfiguration{}, caCert)
	require.NoError(t, err)

	got := &admissionregistrationv1.MutatingWebhookConfiguration{}
	require.NoError(t, fakeClient.Get(context.Background(), client.ObjectKeyFromObject(mwc), got))
	assert.Equal(t, caCert, got.Webhooks[0].ClientConfig.CABundle)
}

func TestPatchWebhookCABundle_AlreadyUpToDate(t *testing.T) {
	caCert := []byte("current-ca-bundle")

	vwc := &admissionregistrationv1.ValidatingWebhookConfiguration{
		ObjectMeta: metav1.ObjectMeta{Name: "test-vwc"},
		Webhooks: []admissionregistrationv1.ValidatingWebhook{
			{Name: "hook1", ClientConfig: admissionregistrationv1.WebhookClientConfig{CABundle: caCert}},
		},
	}

	s := newScheme(t)
	fakeClient := fake.NewClientBuilder().WithScheme(s).WithObjects(vwc).Build()
	m := &CertManager{client: fakeClient}

	// Should succeed without patching
	err := m.patchWebhookCABundle(context.Background(), "test-vwc", "ValidatingWebhookConfiguration",
		&admissionregistrationv1.ValidatingWebhookConfiguration{}, caCert)
	require.NoError(t, err)
}

func TestPatchWebhookCABundle_EmptyWebhooks(t *testing.T) {
	caCert := []byte("new-ca-bundle")

	vwc := &admissionregistrationv1.ValidatingWebhookConfiguration{
		ObjectMeta: metav1.ObjectMeta{Name: "test-vwc"},
		Webhooks:   []admissionregistrationv1.ValidatingWebhook{},
	}

	s := newScheme(t)
	fakeClient := fake.NewClientBuilder().WithScheme(s).WithObjects(vwc).Build()
	m := &CertManager{client: fakeClient}

	err := m.patchWebhookCABundle(context.Background(), "test-vwc", "ValidatingWebhookConfiguration",
		&admissionregistrationv1.ValidatingWebhookConfiguration{}, caCert)
	require.NoError(t, err)
}

func TestPatchWebhookCABundle_NotFound(t *testing.T) {
	caCert := []byte("new-ca-bundle")

	s := newScheme(t)
	fakeClient := fake.NewClientBuilder().WithScheme(s).Build()
	m := &CertManager{client: fakeClient}

	err := m.patchWebhookCABundle(context.Background(), "nonexistent", "ValidatingWebhookConfiguration",
		&admissionregistrationv1.ValidatingWebhookConfiguration{}, caCert)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "getting ValidatingWebhookConfiguration")
}
