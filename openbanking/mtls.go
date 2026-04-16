package openbanking

import (
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"os"
)

// LoadTransportCert reads a transport certificate and the matching
// private key from disk and returns a tls.Certificate ready for use
// in an MTLS-configured *tls.Config.
//
// certPath may point to a PEM- or DER-encoded X.509 certificate;
// the loader detects the format. keyPath must be a PEM-encoded RSA
// private key in either PKCS#1 ("RSA PRIVATE KEY") or PKCS#8
// ("PRIVATE KEY") form. The two files are sanity-checked to ensure
// the cert wraps the same public key as the key.
func LoadTransportCert(certPath, keyPath string) (tls.Certificate, error) {
	certBytes, err := os.ReadFile(certPath)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("openbanking: read transport cert: %w", err)
	}
	cert, certDER, err := parseCertAny(certBytes)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("openbanking: parse transport cert: %w", err)
	}
	keyBytes, err := os.ReadFile(keyPath)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("openbanking: read transport key: %w", err)
	}
	priv, err := parsePrivateKeyPEM(keyBytes)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("openbanking: parse transport key: %w", err)
	}
	// Public-key cross-check: a mismatch here would surface much
	// later as an opaque MTLS handshake failure.
	rsaPub, ok := cert.PublicKey.(*rsa.PublicKey)
	if !ok {
		return tls.Certificate{}, fmt.Errorf("openbanking: transport cert public key is %T, want *rsa.PublicKey", cert.PublicKey)
	}
	if rsaPub.N.Cmp(priv.PublicKey.N) != 0 || rsaPub.E != priv.PublicKey.E {
		return tls.Certificate{}, errors.New("openbanking: transport cert public key does not match the private key")
	}
	return tls.Certificate{
		Certificate: [][]byte{certDER},
		PrivateKey:  priv,
		Leaf:        cert,
	}, nil
}

// MTLSHTTPClient returns an *http.Client that presents cert during
// the TLS handshake. Used as the http.Client for both the /token
// call and the API call layer of the Open Banking transport.
//
// caCertPEM, when non-nil, replaces the system root pool; pass nil
// to use the system roots (which is what the sandbox needs).
func MTLSHTTPClient(cert tls.Certificate, caCertPEM []byte) *http.Client {
	cfg := &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS12,
	}
	if len(caCertPEM) > 0 {
		pool := x509.NewCertPool()
		pool.AppendCertsFromPEM(caCertPEM)
		cfg.RootCAs = pool
	}
	return &http.Client{
		Transport: &http.Transport{TLSClientConfig: cfg},
	}
}

// parseCertAny accepts either PEM or DER. Returns the parsed cert
// and the DER bytes (which tls.Certificate.Certificate wants).
func parseCertAny(b []byte) (*x509.Certificate, []byte, error) {
	if block, _ := pem.Decode(b); block != nil && block.Type == "CERTIFICATE" {
		c, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, nil, err
		}
		return c, block.Bytes, nil
	}
	c, err := x509.ParseCertificate(b)
	if err != nil {
		return nil, nil, err
	}
	return c, b, nil
}

// parsePrivateKeyPEM extracts an RSA private key from a PEM block.
// Accepts PKCS#1 (legacy openssl default) and PKCS#8 (modern
// openssl default). Anything else is rejected loudly.
func parsePrivateKeyPEM(b []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(b)
	if block == nil {
		return nil, errors.New("no PEM block")
	}
	switch block.Type {
	case "RSA PRIVATE KEY":
		return x509.ParsePKCS1PrivateKey(block.Bytes)
	case "PRIVATE KEY":
		k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, err
		}
		rsaKey, ok := k.(*rsa.PrivateKey)
		if !ok {
			return nil, fmt.Errorf("PKCS#8 key is %T, want *rsa.PrivateKey", k)
		}
		return rsaKey, nil
	}
	return nil, fmt.Errorf("unsupported PEM block %q", block.Type)
}
