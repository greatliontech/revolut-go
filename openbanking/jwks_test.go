package openbanking

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"math/big"
	"strings"
	"testing"
	"time"
)

func selfSignedDER(t *testing.T, key *rsa.PrivateKey) []byte {
	t.Helper()
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "test"},
		NotBefore:    time.Now(),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}
	return der
}

// TestBuildJWKS_RoundTripsThroughJSON verifies the emitted JWKS
// parses back into the same logical key set.
func TestBuildJWKS_RoundTripsThroughJSON(t *testing.T) {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	der := selfSignedDER(t, key)
	out, err := BuildJWKS(JWKEntry{
		Public:  &key.PublicKey,
		CertDER: der,
		Use:     "sig",
		Alg:     AlgPS256,
	})
	if err != nil {
		t.Fatalf("BuildJWKS: %v", err)
	}
	var parsed JWKS
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("re-parse: %v", err)
	}
	if len(parsed.Keys) != 1 {
		t.Fatalf("got %d keys; want 1", len(parsed.Keys))
	}
	jwk := parsed.Keys[0]
	if jwk.Kty != "RSA" {
		t.Errorf("kty=%q", jwk.Kty)
	}
	if jwk.Use != "sig" || jwk.Alg != AlgPS256 {
		t.Errorf("use/alg: %q/%q", jwk.Use, jwk.Alg)
	}
	if jwk.Kid == "" {
		t.Errorf("kid empty (RFC 7638 thumbprint should have populated it)")
	}
	if len(jwk.X5C) != 1 {
		t.Errorf("x5c missing or wrong length: %v", jwk.X5C)
	}
	if jwk.X5TSHA256 == "" {
		t.Errorf("x5t#S256 empty")
	}
}

// TestBuildJWKS_KidIsRFC7638Thumbprint pins the thumbprint
// derivation: same key in two different JWK metadata setups
// produces the same kid.
func TestBuildJWKS_KidIsRFC7638Thumbprint(t *testing.T) {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	a, _ := BuildJWKS(JWKEntry{Public: &key.PublicKey})
	b, _ := BuildJWKS(JWKEntry{Public: &key.PublicKey, Use: "sig", Alg: AlgPS256})
	var pa, pb JWKS
	_ = json.Unmarshal(a, &pa)
	_ = json.Unmarshal(b, &pb)
	if pa.Keys[0].Kid != pb.Keys[0].Kid {
		t.Errorf("thumbprint kid changed across metadata: %q vs %q", pa.Keys[0].Kid, pb.Keys[0].Kid)
	}
}

// TestBuildJWKS_RejectsCertMismatch: when the supplied cert wraps
// a different public key than the one given, we fail loud rather
// than emit a JWKS that verifiers will reject silently.
func TestBuildJWKS_RejectsCertMismatch(t *testing.T) {
	keyA, _ := rsa.GenerateKey(rand.Reader, 2048)
	keyB, _ := rsa.GenerateKey(rand.Reader, 2048)
	der := selfSignedDER(t, keyA)
	_, err := BuildJWKS(JWKEntry{Public: &keyB.PublicKey, CertDER: der})
	if err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Errorf("want cert-mismatch error, got %v", err)
	}
}

// TestBuildJWKS_HonoursExplicitKid: when the caller supplies a
// kid (matching what they put in JWS headers), we don't override
// it with the thumbprint.
func TestBuildJWKS_HonoursExplicitKid(t *testing.T) {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	out, _ := BuildJWKS(JWKEntry{Public: &key.PublicKey, Kid: "custom-kid-1"})
	var parsed JWKS
	_ = json.Unmarshal(out, &parsed)
	if parsed.Keys[0].Kid != "custom-kid-1" {
		t.Errorf("kid=%q; want custom-kid-1", parsed.Keys[0].Kid)
	}
}

// TestBuildJWKS_ExponentEncoding: e=65537 (the universal RSA
// exponent) must serialise as "AQAB", the canonical form RFC 7518
// §6.3 specifies. A regression here would make the JWKS rejectable
// by every spec-compliant verifier.
func TestBuildJWKS_ExponentEncoding(t *testing.T) {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	out, _ := BuildJWKS(JWKEntry{Public: &key.PublicKey})
	var parsed JWKS
	_ = json.Unmarshal(out, &parsed)
	if parsed.Keys[0].E != "AQAB" {
		t.Errorf("e=%q; want AQAB (RFC 7518 §6.3)", parsed.Keys[0].E)
	}
}

// TestBuildJWKS_EmptyEntries returns an error rather than an
// empty {"keys":[]} JWKS — the latter is technically valid JSON
// but never useful and a likely caller bug.
func TestBuildJWKS_EmptyEntries(t *testing.T) {
	if _, err := BuildJWKS(); err == nil {
		t.Error("want error on empty input")
	}
}
