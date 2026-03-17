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
	"crypto"
	cryptorand "crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math"
	"math/big"
	"time"

	"k8s.io/client-go/util/cert"
	"k8s.io/client-go/util/keyutil"
)

const rsaKeySize = 2048

// Artifacts holds the PEM-encoded certificate material produced by a generator.
type Artifacts struct {
	// CAKey is the PEM-encoded CA private key.
	CAKey []byte
	// CACert is the PEM-encoded CA certificate.
	CACert []byte
	// Key is the PEM-encoded server private key.
	Key []byte
	// Cert is the PEM-encoded server certificate signed by the CA.
	Cert []byte
	// ResourceVersion is the Kubernetes ResourceVersion of the backing Secret (if any).
	ResourceVersion string
}

// CertGenerator generates self-signed certificates.
type CertGenerator interface {
	Generate(dnsName string) (*Artifacts, error)
	SetCA(caKey, caCert []byte)
}

// SelfSignedCertGenerator generates self-signed TLS certificates backed by an
// in-memory (or reused) CA.
type SelfSignedCertGenerator struct {
	caKey  []byte
	caCert []byte
}

var _ CertGenerator = &SelfSignedCertGenerator{}

// SetCA sets the PEM-encoded CA key and certificate to reuse for signing.
func (g *SelfSignedCertGenerator) SetCA(caKey, caCert []byte) {
	g.caKey = caKey
	g.caCert = caCert
}

// Generate creates a server certificate for dnsName signed by a (possibly reused) CA.
func (g *SelfSignedCertGenerator) Generate(dnsName string) (*Artifacts, error) {
	var (
		signingKey  *rsa.PrivateKey
		signingCert *x509.Certificate
		err         error
	)

	valid, signingKey, signingCert := g.validCACert()
	if !valid {
		signingKey, err = newPrivateKey()
		if err != nil {
			return nil, fmt.Errorf("failed to create CA private key: %w", err)
		}
		signingCert, err = cert.NewSelfSignedCACert(cert.Config{CommonName: "webhook-ca"}, signingKey)
		if err != nil {
			return nil, fmt.Errorf("failed to create CA cert: %w", err)
		}
	}

	serverKey, err := newPrivateKey()
	if err != nil {
		return nil, fmt.Errorf("failed to create server private key: %w", err)
	}
	serverCert, err := newSignedCert(
		cert.Config{
			CommonName: dnsName,
			AltNames:   cert.AltNames{DNSNames: []string{dnsName, "localhost"}},
			Usages:     []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		},
		serverKey, signingCert, signingKey,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create server cert: %w", err)
	}

	return &Artifacts{
		CAKey:  encodePrivateKeyPEM(signingKey),
		CACert: encodeCertPEM(signingCert),
		Key:    encodePrivateKeyPEM(serverKey),
		Cert:   encodeCertPEM(serverCert),
	}, nil
}

// ServiceToCommonName returns the DNS name used as the certificate CommonName
// for a Kubernetes Service: <service>.<namespace>.svc
func ServiceToCommonName(namespace, service string) string {
	return fmt.Sprintf("%s.%s.svc", service, namespace)
}

// ValidCert returns true if the key/cert pair is valid, signed by caCert, covers
// dnsName, and will not expire before the given time.
//
// Passing an empty dnsName skips DNS name verification; this is intentional for
// CA self-checks (a CA cert carries no DNS SANs). Callers that have a concrete
// service DNS name should always supply it.
func ValidCert(key, certPEM, caCert []byte, dnsName string, at time.Time) bool {
	if len(key) == 0 || len(certPEM) == 0 || len(caCert) == 0 {
		return false
	}
	if _, err := tls.X509KeyPair(certPEM, key); err != nil {
		return false
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caCert) {
		return false
	}
	block, _ := pem.Decode(certPEM)
	if block == nil {
		return false
	}
	c, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return false
	}
	_, err = c.Verify(x509.VerifyOptions{
		DNSName:     dnsName,
		Roots:       pool,
		CurrentTime: at,
	})
	return err == nil
}

// --------------------------------------------------------------------------
// internal helpers
// --------------------------------------------------------------------------

func (g *SelfSignedCertGenerator) validCACert() (bool, *rsa.PrivateKey, *x509.Certificate) {
	// Consider the CA valid only if it will still be valid 1 year from now.
	if !ValidCert(g.caKey, g.caCert, g.caCert, "", time.Now().AddDate(1, 0, 0)) {
		return false, nil, nil
	}
	key, err := keyutil.ParsePrivateKeyPEM(g.caKey)
	if err != nil {
		return false, nil, nil
	}
	pk, ok := key.(*rsa.PrivateKey)
	if !ok {
		return false, nil, nil
	}
	certs, err := cert.ParseCertsPEM(g.caCert)
	if err != nil || len(certs) != 1 {
		return false, nil, nil
	}
	return true, pk, certs[0]
}

func newPrivateKey() (*rsa.PrivateKey, error) {
	return rsa.GenerateKey(cryptorand.Reader, rsaKeySize)
}

func newSignedCert(cfg cert.Config, key crypto.Signer, caCert *x509.Certificate, caKey crypto.Signer) (*x509.Certificate, error) {
	if cfg.CommonName == "" {
		return nil, errors.New("CommonName must not be empty")
	}
	if len(cfg.Usages) == 0 {
		return nil, errors.New("at least one ExtKeyUsage must be specified")
	}
	serial, err := cryptorand.Int(cryptorand.Reader, new(big.Int).SetInt64(math.MaxInt64))
	if err != nil {
		return nil, err
	}
	tmpl := x509.Certificate{
		Subject:      pkix.Name{CommonName: cfg.CommonName, Organization: cfg.Organization},
		DNSNames:     cfg.AltNames.DNSNames,
		IPAddresses:  cfg.AltNames.IPs,
		SerialNumber: serial,
		NotBefore:    caCert.NotBefore,
		NotAfter:     time.Now().Add(10 * 365 * 24 * time.Hour).UTC(),
		KeyUsage:     x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  cfg.Usages,
	}
	raw, err := x509.CreateCertificate(cryptorand.Reader, &tmpl, caCert, key.Public(), caKey)
	if err != nil {
		return nil, err
	}
	return x509.ParseCertificate(raw)
}

func encodePrivateKeyPEM(key *rsa.PrivateKey) []byte {
	return pem.EncodeToMemory(&pem.Block{
		Type:  keyutil.RSAPrivateKeyBlockType,
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	})
}

func encodeCertPEM(c *x509.Certificate) []byte {
	return pem.EncodeToMemory(&pem.Block{
		Type:  cert.CertificateBlockType,
		Bytes: c.Raw,
	})
}
