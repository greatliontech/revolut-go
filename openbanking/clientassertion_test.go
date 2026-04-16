package openbanking

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// TestSignClientAssertion_HeaderAndClaims pins the JWT shape RFC
// 7523 and the FAPI profile require: header carries alg/kid/typ,
// claims carry iss=sub=client_id, aud=token URL, jti, iat, exp.
func TestSignClientAssertion_HeaderAndClaims(t *testing.T) {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	at := time.Date(2026, 4, 16, 12, 0, 0, 0, time.UTC)
	jwt, err := SignClientAssertion(ClientAssertionConfig{
		ClientID:   "client-1",
		TokenURL:   "https://issuer.example.com/token",
		Kid:        "kid-1",
		PrivateKey: key,
		Now:        func() time.Time { return at },
	})
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	parts := strings.Split(jwt, ".")
	if len(parts) != 3 {
		t.Fatalf("want 3 JWT segments; got %d", len(parts))
	}
	hdr := decodeJSON(t, parts[0])
	if hdr["alg"] != "PS256" || hdr["kid"] != "kid-1" || hdr["typ"] != "JWT" {
		t.Errorf("header: %+v", hdr)
	}
	claims := decodeJSON(t, parts[1])
	if claims["iss"] != "client-1" || claims["sub"] != "client-1" {
		t.Errorf("iss/sub: %+v", claims)
	}
	if claims["aud"] != "https://issuer.example.com/token" {
		t.Errorf("aud: %v", claims["aud"])
	}
	if claims["jti"] == nil || claims["jti"] == "" {
		t.Errorf("jti missing")
	}
	if int64(claims["iat"].(float64)) != at.Unix() {
		t.Errorf("iat: %v; want %d", claims["iat"], at.Unix())
	}
	if int64(claims["exp"].(float64)) != at.Add(ClientAssertionDefaultLifetime).Unix() {
		t.Errorf("exp: %v", claims["exp"])
	}
}

// TestSignClientAssertion_FreshJTI: every emitted assertion has a
// distinct jti so an AS that tracks replays sees fresh
// identifiers per request.
func TestSignClientAssertion_FreshJTI(t *testing.T) {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	cfg := ClientAssertionConfig{
		ClientID: "c", TokenURL: "https://x/token", Kid: "k", PrivateKey: key,
	}
	a, _ := SignClientAssertion(cfg)
	b, _ := SignClientAssertion(cfg)
	if claimVal(t, a, "jti") == claimVal(t, b, "jti") {
		t.Errorf("jti reused across calls")
	}
}

// TestSignClientAssertion_VerifiesAgainstPublicKey: the emitted
// JWT verifies against the same RSA public key with the standard
// JWS signing input — confirms we're producing a valid PS256 JWS.
func TestSignClientAssertion_VerifiesAgainstPublicKey(t *testing.T) {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	jwt, err := SignClientAssertion(ClientAssertionConfig{
		ClientID: "c", TokenURL: "https://x/token", Kid: "k", PrivateKey: key,
	})
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(jwt, ".")
	signingInput := []byte(parts[0] + "." + parts[1])
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		t.Fatal(err)
	}
	h, _ := hashFor(AlgPS256)
	h.Write(signingInput)
	if err := rsa.VerifyPSS(&key.PublicKey, crypto.SHA256, h.Sum(nil), sig, &rsa.PSSOptions{
		SaltLength: rsa.PSSSaltLengthEqualsHash,
		Hash:       crypto.SHA256,
	}); err != nil {
		t.Errorf("PS256 verification failed: %v", err)
	}
}

// TestSignClientAssertion_RejectsBadConfig pins the constructor
// preconditions — we want loud failures, not malformed JWTs that
// surface as opaque "invalid_client" rejections from Revolut.
func TestSignClientAssertion_RejectsBadConfig(t *testing.T) {
	good, _ := rsa.GenerateKey(rand.Reader, 2048)
	cases := []struct {
		name string
		cfg  ClientAssertionConfig
		want string
	}{
		{"no client id", ClientAssertionConfig{TokenURL: "x", Kid: "k", PrivateKey: good}, "ClientID"},
		{"no token url", ClientAssertionConfig{ClientID: "c", Kid: "k", PrivateKey: good}, "TokenURL"},
		{"no kid", ClientAssertionConfig{ClientID: "c", TokenURL: "x", PrivateKey: good}, "Kid"},
		{"no key", ClientAssertionConfig{ClientID: "c", TokenURL: "x", Kid: "k"}, "PrivateKey"},
		{"bad alg", ClientAssertionConfig{ClientID: "c", TokenURL: "x", Kid: "k", PrivateKey: good, Alg: "HS256"}, "unsupported"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := SignClientAssertion(tc.cfg)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err=%v; want substring %q", err, tc.want)
			}
		})
	}
}

func decodeJSON(t *testing.T, segment string) map[string]any {
	t.Helper()
	raw, err := base64.RawURLEncoding.DecodeString(segment)
	if err != nil {
		t.Fatalf("decode segment: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("parse segment: %v", err)
	}
	return out
}

func claimVal(t *testing.T, jwt, key string) any {
	t.Helper()
	parts := strings.Split(jwt, ".")
	return decodeJSON(t, parts[1])[key]
}
