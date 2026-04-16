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

func newTestSigner(t *testing.T, opts SignerOptions) (*Signer, *rsa.PrivateKey) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	s, err := NewSigner(key, "kid-1", "CN=tpp.example.com,O=ExampleTPP", opts)
	if err != nil {
		t.Fatalf("NewSigner: %v", err)
	}
	return s, key
}

// TestSigner_RoundTripPS256 pins the canonical OBIE flow: sign a
// payload with PS256, hand the resulting wire string to the verify
// path, and watch it pass with the corresponding public key.
func TestSigner_RoundTripPS256(t *testing.T) {
	signer, key := newTestSigner(t, SignerOptions{})
	payload := []byte(`{"Data":{"ConsentId":"c-1"}}`)
	sig, err := signer.Sign(payload)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if !strings.Contains(sig, "..") {
		t.Errorf("wire form lacks detached separator: %q", sig)
	}
	signed := Signed[int]{Raw: payload, Metadata: ResponseMetadata{JWSSignature: sig}}
	if err := signed.Verify(KeyResolverFunc(func(JWSHeader) (crypto.PublicKey, error) {
		return &key.PublicKey, nil
	})); err != nil {
		t.Errorf("verify: %v", err)
	}
}

// TestSigner_RoundTripRS256 covers the pre-PS migration path some
// ASPSPs still need.
func TestSigner_RoundTripRS256(t *testing.T) {
	signer, key := newTestSigner(t, SignerOptions{Alg: AlgRS256})
	payload := []byte(`{"x":1}`)
	sig, err := signer.Sign(payload)
	if err != nil {
		t.Fatal(err)
	}
	signed := Signed[int]{Raw: payload, Metadata: ResponseMetadata{JWSSignature: sig}}
	if err := signed.Verify(KeyResolverFunc(func(JWSHeader) (crypto.PublicKey, error) {
		return &key.PublicKey, nil
	})); err != nil {
		t.Errorf("verify: %v", err)
	}
}

// TestSigner_HeaderClaims verifies the OBIE-required vendor claims
// land in the protected header and crit lists them. Without
// http://openbanking.org.uk/iat / iss / tan in crit, an OBIE
// verifier rejects the JWS even when the math checks out.
func TestSigner_HeaderClaims(t *testing.T) {
	at := time.Date(2026, 4, 16, 12, 0, 0, 0, time.UTC)
	signer, _ := newTestSigner(t, SignerOptions{Now: func() time.Time { return at }})
	sig, err := signer.Sign([]byte("payload"))
	if err != nil {
		t.Fatal(err)
	}
	headerEnc := strings.SplitN(sig, "..", 2)[0]
	headerJSON, err := base64.RawURLEncoding.DecodeString(headerEnc)
	if err != nil {
		t.Fatalf("decode header: %v", err)
	}
	var hdr map[string]any
	if err := json.Unmarshal(headerJSON, &hdr); err != nil {
		t.Fatalf("parse header: %v", err)
	}
	for _, k := range []string{"alg", "kid", "typ", "cty", "b64",
		"http://openbanking.org.uk/iat", "http://openbanking.org.uk/iss",
		"http://openbanking.org.uk/tan", "crit"} {
		if _, ok := hdr[k]; !ok {
			t.Errorf("header missing %q: got keys %v", k, mapKeys(hdr))
		}
	}
	if hdr["alg"] != "PS256" {
		t.Errorf("alg=%v; want PS256", hdr["alg"])
	}
	if hdr["b64"] != false {
		t.Errorf("b64=%v; want false (b64=false detached payload mode)", hdr["b64"])
	}
	if got := hdr["http://openbanking.org.uk/iat"]; got != float64(at.Unix()) {
		t.Errorf("iat=%v; want %d", got, at.Unix())
	}
	if got := hdr["http://openbanking.org.uk/tan"]; got != "openbanking.org.uk" {
		t.Errorf("tan=%v; want openbanking.org.uk default", got)
	}
	crit, _ := hdr["crit"].([]any)
	wantCrit := map[string]bool{
		"b64":                           false,
		"http://openbanking.org.uk/iat": false,
		"http://openbanking.org.uk/iss": false,
		"http://openbanking.org.uk/tan": false,
	}
	for _, c := range crit {
		wantCrit[c.(string)] = true
	}
	for k, present := range wantCrit {
		if !present {
			t.Errorf("crit missing %q", k)
		}
	}
}

// TestSigner_RejectsTamperedPayload: verify catches a payload
// edited after signing — proves we're really exercising the b64=false
// detached signing input (raw payload, not the base64-encoded form).
func TestSigner_RejectsTamperedPayload(t *testing.T) {
	signer, key := newTestSigner(t, SignerOptions{})
	original := []byte(`{"amount":"1.00"}`)
	tampered := []byte(`{"amount":"9999.00"}`)
	sig, err := signer.Sign(original)
	if err != nil {
		t.Fatal(err)
	}
	signed := Signed[int]{Raw: tampered, Metadata: ResponseMetadata{JWSSignature: sig}}
	err = signed.Verify(KeyResolverFunc(func(JWSHeader) (crypto.PublicKey, error) {
		return &key.PublicKey, nil
	}))
	if err == nil {
		t.Fatal("verify accepted tampered payload")
	}
}

// TestSigner_TrustAnchorOverride: the OBIE TAN claim is overridable
// for environments that route through an alternate trust anchor.
func TestSigner_TrustAnchorOverride(t *testing.T) {
	signer, _ := newTestSigner(t, SignerOptions{TrustAnchor: "trust.example.com"})
	sig, _ := signer.Sign([]byte("x"))
	headerJSON, _ := base64.RawURLEncoding.DecodeString(strings.SplitN(sig, "..", 2)[0])
	var hdr map[string]any
	_ = json.Unmarshal(headerJSON, &hdr)
	if hdr["http://openbanking.org.uk/tan"] != "trust.example.com" {
		t.Errorf("tan=%v; want override", hdr["http://openbanking.org.uk/tan"])
	}
}

// TestNewSigner_RejectsBadInput pins the constructor preconditions —
// crypto material is too important to silently accept zero values.
func TestNewSigner_RejectsBadInput(t *testing.T) {
	good, _ := rsa.GenerateKey(rand.Reader, 2048)
	cases := []struct {
		name   string
		key    *rsa.PrivateKey
		kid    string
		issuer string
		alg    string
		want   string
	}{
		{"nil key", nil, "k", "i", "", "key is nil"},
		{"empty kid", good, "", "i", "", "kid is empty"},
		{"empty issuer", good, "k", "", "", "issuer is empty"},
		{"bad alg", good, "k", "i", "HS256", "unsupported"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewSigner(tc.key, tc.kid, tc.issuer, SignerOptions{Alg: tc.alg})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err=%v; want substring %q", err, tc.want)
			}
		})
	}
}

// TestSigner_RejectsEmptyPayload prevents a caller from accidentally
// signing zero bytes — likely a serialisation bug, not a valid call.
func TestSigner_RejectsEmptyPayload(t *testing.T) {
	signer, _ := newTestSigner(t, SignerOptions{})
	if _, err := signer.Sign(nil); err == nil {
		t.Error("want error on empty payload")
	}
}

// TestSigner_OutputIsStable: signing the same payload twice with
// the same iat produces the same wire string. Critical for
// integrators who hand the signature to a separate code path that
// also re-marshals the request.
func TestSigner_OutputIsStable(t *testing.T) {
	at := time.Date(2026, 4, 16, 12, 0, 0, 0, time.UTC)
	signer, _ := newTestSigner(t, SignerOptions{Now: func() time.Time { return at }, Alg: AlgRS256})
	payload := []byte(`{"x":1}`)
	a, err := signer.Sign(payload)
	if err != nil {
		t.Fatal(err)
	}
	b, err := signer.Sign(payload)
	if err != nil {
		t.Fatal(err)
	}
	// PS256 uses random salt so its output is non-deterministic;
	// RS256 (PKCS#1 v1.5) is deterministic and a good stability
	// pin for the header/encoding pipeline.
	if a != b {
		t.Errorf("RS256 sign output is not deterministic:\n a=%s\n b=%s", a, b)
	}
}

func mapKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
