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

package generator

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ── ServiceToCommonName ───────────────────────────────────────────────────────

func TestServiceToCommonName(t *testing.T) {
	assert.Equal(t, "webhook-service.rbgs-system.svc", ServiceToCommonName("rbgs-system", "webhook-service"))
	assert.Equal(t, "svc.default.svc", ServiceToCommonName("default", "svc"))
}

// ── SelfSignedCertGenerator.Generate ─────────────────────────────────────────

func TestSelfSignedCertGenerator_Generate_ProducesValidArtifacts(t *testing.T) {
	g := &SelfSignedCertGenerator{}
	dnsName := "webhook.test.svc"
	a, err := g.Generate(dnsName)
	require.NoError(t, err)
	require.NotNil(t, a)

	assert.NotEmpty(t, a.CAKey, "CA key must be set")
	assert.NotEmpty(t, a.CACert, "CA cert must be set")
	assert.NotEmpty(t, a.Key, "server key must be set")
	assert.NotEmpty(t, a.Cert, "server cert must be set")

	// Must form a valid TLS key pair.
	_, err = tls.X509KeyPair(a.Cert, a.Key)
	require.NoError(t, err)
}

func TestSelfSignedCertGenerator_Generate_CertCoversExpectedSANs(t *testing.T) {
	g := &SelfSignedCertGenerator{}
	dnsName := "my-webhook.system.svc"
	a, err := g.Generate(dnsName)
	require.NoError(t, err)

	block, _ := pem.Decode(a.Cert)
	require.NotNil(t, block)
	cert, err := x509.ParseCertificate(block.Bytes)
	require.NoError(t, err)

	assert.Contains(t, cert.DNSNames, dnsName, "SAN must contain the requested dnsName")
	assert.Contains(t, cert.DNSNames, "localhost", "SAN must contain localhost for local testing")
	assert.Equal(t, dnsName, cert.Subject.CommonName)
}

func TestSelfSignedCertGenerator_Generate_CertSignedByCA(t *testing.T) {
	g := &SelfSignedCertGenerator{}
	a, err := g.Generate("svc.ns.svc")
	require.NoError(t, err)

	pool := x509.NewCertPool()
	require.True(t, pool.AppendCertsFromPEM(a.CACert))
	block, _ := pem.Decode(a.Cert)
	cert, err := x509.ParseCertificate(block.Bytes)
	require.NoError(t, err)

	_, err = cert.Verify(x509.VerifyOptions{DNSName: "svc.ns.svc", Roots: pool})
	assert.NoError(t, err, "server cert must verify against its CA")
}

func TestSelfSignedCertGenerator_Generate_ReuseCA(t *testing.T) {
	g := &SelfSignedCertGenerator{}
	a1, err := g.Generate("svc.ns.svc")
	require.NoError(t, err)

	// Reload the CA so the second call reuses it.
	g.SetCA(a1.CAKey, a1.CACert)
	a2, err := g.Generate("svc.ns.svc")
	require.NoError(t, err)

	// Both server certs must be signed by the same CA.
	assert.Equal(t, a1.CACert, a2.CACert, "CA cert must be reused")
	assert.NotEqual(t, a1.Cert, a2.Cert, "server cert must be freshly generated")
}

func TestSelfSignedCertGenerator_Generate_FreshCAWhenExpired(t *testing.T) {
	g := &SelfSignedCertGenerator{}
	// Seed the generator with a CA that will appear expired to validCACert
	// by injecting obviously garbage PEM — ValidCert returns false, so a fresh
	// CA is minted on the next Generate call.
	g.SetCA([]byte("not-a-key"), []byte("not-a-cert"))
	a, err := g.Generate("svc.ns.svc")
	require.NoError(t, err)
	assert.NotEmpty(t, a.CACert, "a fresh CA must be generated when the seeded CA is invalid")
}

// ── ValidCert ─────────────────────────────────────────────────────────────────

func TestValidCert_ValidPair(t *testing.T) {
	g := &SelfSignedCertGenerator{}
	a, err := g.Generate("webhook.test.svc")
	require.NoError(t, err)

	assert.True(t, ValidCert(a.Key, a.Cert, a.CACert, "webhook.test.svc", time.Now()))
}

func TestValidCert_ReturnsFalseForExpiredTime(t *testing.T) {
	g := &SelfSignedCertGenerator{}
	a, err := g.Generate("svc.ns.svc")
	require.NoError(t, err)

	// Check far in the future — beyond the 10-year cert validity.
	future := time.Now().Add(20 * 365 * 24 * time.Hour)
	assert.False(t, ValidCert(a.Key, a.Cert, a.CACert, "svc.ns.svc", future),
		"cert should be considered invalid when checked beyond its expiry")
}

func TestValidCert_ReturnsFalseForWrongDNSName(t *testing.T) {
	g := &SelfSignedCertGenerator{}
	a, err := g.Generate("webhook.test.svc")
	require.NoError(t, err)

	assert.False(t, ValidCert(a.Key, a.Cert, a.CACert, "other.test.svc", time.Now()))
}

func TestValidCert_ReturnsFalseForEmptyInputs(t *testing.T) {
	g := &SelfSignedCertGenerator{}
	a, err := g.Generate("svc.ns.svc")
	require.NoError(t, err)

	assert.False(t, ValidCert(nil, a.Cert, a.CACert, "svc.ns.svc", time.Now()), "nil key")
	assert.False(t, ValidCert(a.Key, nil, a.CACert, "svc.ns.svc", time.Now()), "nil cert")
	assert.False(t, ValidCert(a.Key, a.Cert, nil, "svc.ns.svc", time.Now()), "nil CA cert")
}

func TestValidCert_EmptyDNSName_ValidatesChainOnly(t *testing.T) {
	// Empty dnsName intentionally skips DNS name verification (used for CA self-checks).
	g := &SelfSignedCertGenerator{}
	a, err := g.Generate("svc.ns.svc")
	require.NoError(t, err)

	// CA cert validated against itself with empty dnsName — must succeed.
	assert.True(t, ValidCert(a.CAKey, a.CACert, a.CACert, "", time.Now()),
		"CA self-check with empty dnsName must succeed")
}

func TestValidCert_ReturnsFalseForMismatchedKeyAndCert(t *testing.T) {
	g := &SelfSignedCertGenerator{}
	a1, err := g.Generate("svc.ns.svc")
	require.NoError(t, err)
	a2, err := g.Generate("svc.ns.svc")
	require.NoError(t, err)

	assert.False(t, ValidCert(a1.Key, a2.Cert, a2.CACert, "svc.ns.svc", time.Now()),
		"mismatched key/cert pair must be invalid")
}
