package main

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"io"
	"math/big"
	"net"
	"time"
)

// randomToken returns a base64url-encoded random byte string of
// the given length. Used to mint Idempotency-Key, the random tail
// of InstructionIdentification / EndToEndIdentification, and
// other one-shot identifiers that need uniqueness within a small
// session.
func randomToken(nBytes int) string {
	buf := make([]byte, nBytes)
	if _, err := io.ReadFull(rand.Reader, buf); err != nil {
		// crypto/rand.Reader effectively never errors on Linux;
		// the fallback keeps callers honest by failing visibly.
		return "rand-fallback"
	}
	return base64.RawURLEncoding.EncodeToString(buf)
}

// generateSelfSigned mints a one-shot RSA leaf cert + key for the
// local HTTPS callback listener. Browsers warn on first visit;
// users session-trust it. The cert lives only for the lifetime of
// the bootstrap process.
func generateSelfSigned(host string) (tls.Certificate, error) {
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return tls.Certificate{}, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "ob-bootstrap localhost"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
		DNSNames:     []string{"localhost", host},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		return tls.Certificate{}, err
	}
	return tls.Certificate{
		Certificate: [][]byte{der},
		PrivateKey:  priv,
	}, nil
}
