package jwt

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	return k
}

func TestSign_ProducesValidRS256JWT(t *testing.T) {
	t.Parallel()
	key := testKey(t)
	fixedNow := time.Date(2026, 4, 15, 12, 0, 0, 0, time.UTC)
	s, err := NewSigner(Config{
		PrivateKey: key,
		Issuer:     "example.com",
		ClientID:   "client-abc",
		Now:        func() time.Time { return fixedNow },
	})
	if err != nil {
		t.Fatalf("NewSigner: %v", err)
	}
	tok, err := s.Sign()
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		t.Fatalf("want 3 JWT parts, got %d", len(parts))
	}

	var header map[string]string
	decodeJSONPart(t, parts[0], &header)
	if header["alg"] != "RS256" || header["typ"] != "JWT" {
		t.Fatalf("bad header: %+v", header)
	}

	var claims map[string]any
	decodeJSONPart(t, parts[1], &claims)
	if claims["iss"] != "example.com" {
		t.Fatalf("iss: %v", claims["iss"])
	}
	if claims["sub"] != "client-abc" {
		t.Fatalf("sub: %v", claims["sub"])
	}
	if claims["aud"] != defaultAudience {
		t.Fatalf("aud: %v", claims["aud"])
	}
	iat, _ := claims["iat"].(float64)
	exp, _ := claims["exp"].(float64)
	if int64(iat) != fixedNow.Unix() {
		t.Fatalf("iat: got %v want %d", iat, fixedNow.Unix())
	}
	if int64(exp) != fixedNow.Add(defaultTTL).Unix() {
		t.Fatalf("exp: got %v want %d", exp, fixedNow.Add(defaultTTL).Unix())
	}

	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		t.Fatalf("decode sig: %v", err)
	}
	h := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(&key.PublicKey, crypto.SHA256, h[:], sig); err != nil {
		t.Fatalf("verify: %v", err)
	}
}

func decodeJSONPart(t *testing.T, part string, dst any) {
	t.Helper()
	b, err := base64.RawURLEncoding.DecodeString(part)
	if err != nil {
		t.Fatalf("base64: %v", err)
	}
	if err := json.Unmarshal(b, dst); err != nil {
		t.Fatalf("json: %v", err)
	}
}

func TestNewSigner_Validation(t *testing.T) {
	t.Parallel()
	key := testKey(t)
	cases := []struct {
		name string
		cfg  Config
	}{
		{"no key", Config{Issuer: "x", ClientID: "y"}},
		{"no issuer", Config{PrivateKey: key, ClientID: "y"}},
		{"no client id", Config{PrivateKey: key, Issuer: "x"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if _, err := NewSigner(tc.cfg); err == nil {
				t.Fatal("expected error, got nil")
			}
		})
	}
}

func TestLoadPrivateKey_PKCS1AndPKCS8(t *testing.T) {
	t.Parallel()
	key := testKey(t)

	pkcs1 := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	})
	got1, err := LoadPrivateKeyPEM(pkcs1)
	if err != nil {
		t.Fatalf("PKCS1: %v", err)
	}
	if got1.N.Cmp(key.N) != 0 {
		t.Fatal("PKCS1: key modulus mismatch")
	}

	pkcs8Bytes, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("marshal PKCS8: %v", err)
	}
	pkcs8 := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pkcs8Bytes})
	got2, err := LoadPrivateKeyPEM(pkcs8)
	if err != nil {
		t.Fatalf("PKCS8: %v", err)
	}
	if got2.N.Cmp(key.N) != 0 {
		t.Fatal("PKCS8: key modulus mismatch")
	}
}

func TestLoadPrivateKey_Errors(t *testing.T) {
	t.Parallel()
	if _, err := LoadPrivateKeyPEM([]byte("not pem")); err == nil {
		t.Fatal("want error for non-PEM input")
	}
	bad := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte{0}})
	if _, err := LoadPrivateKeyPEM(bad); err == nil {
		t.Fatal("want error for wrong block type")
	}
}

func TestLoadPrivateKeyFile(t *testing.T) {
	t.Parallel()
	key := testKey(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "k.pem")
	data := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	})
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err := LoadPrivateKeyFile(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got.N.Cmp(key.N) != 0 {
		t.Fatal("key mismatch")
	}
}
