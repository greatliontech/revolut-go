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
	if opts.TrustAnchor == "" {
		opts.TrustAnchor = "tests.example.com"
	}
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

// TestSigner_RoundTripPS256 pins the canonical Revolut sandbox flow:
// sign a payload with PS256, hand the resulting wire string to the
// verify path, and watch it pass with the corresponding public key.
func TestSigner_RoundTripPS256(t *testing.T) {
	signer, key := newTestSigner(t, SignerOptions{})
	payload := []byte(`{"Data":{"ConsentId":"c-1"}}`)
	sig, err := signer.Sign(payload)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if strings.Count(sig, ".") != 2 {
		t.Errorf("wire form should be a 3-segment compact JWS; got %q", sig)
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

// TestSigner_HeaderClaims pins the Revolut-flavoured header set:
// the only required claim is the TAN (DNS host of the JWKS), and
// crit lists exactly that. Extra OBIE prod claims (iat / iss /
// b64=false) are deliberately absent — the Revolut sandbox
// verifier rejects them.
func TestSigner_HeaderClaims(t *testing.T) {
	signer, _ := newTestSigner(t, SignerOptions{TrustAnchor: "greatliontech.github.io"})
	sig, err := signer.Sign([]byte("payload"))
	if err != nil {
		t.Fatal(err)
	}
	headerEnc := strings.Split(sig, ".")[0]
	headerJSON, err := base64.RawURLEncoding.DecodeString(headerEnc)
	if err != nil {
		t.Fatalf("decode header: %v", err)
	}
	var hdr map[string]any
	if err := json.Unmarshal(headerJSON, &hdr); err != nil {
		t.Fatalf("parse header: %v", err)
	}
	want := map[string]any{
		"alg":                           "PS256",
		"kid":                           "kid-1",
		"http://openbanking.org.uk/tan": "greatliontech.github.io",
	}
	for k, v := range want {
		if hdr[k] != v {
			t.Errorf("hdr[%q]=%v; want %v", k, hdr[k], v)
		}
	}
	crit, _ := hdr["crit"].([]any)
	if len(crit) != 1 || crit[0] != "http://openbanking.org.uk/tan" {
		t.Errorf("crit=%v; want [\"http://openbanking.org.uk/tan\"]", crit)
	}
	for _, unwanted := range []string{
		"http://openbanking.org.uk/iat",
		"http://openbanking.org.uk/iss",
		"b64", "typ", "cty",
	} {
		if _, present := hdr[unwanted]; present {
			t.Errorf("header carries unwanted claim %q (Revolut sandbox rejects it)", unwanted)
		}
	}
}

// TestSigner_RejectsTamperedPayload: verify catches a payload
// edited after signing, proving the b64=true signing input is
// being checked against the actual bytes.
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

// TestNewSigner_RejectsBadInput pins the constructor preconditions —
// crypto material is too important to silently accept zero values.
func TestNewSigner_RejectsBadInput(t *testing.T) {
	good, _ := rsa.GenerateKey(rand.Reader, 2048)
	cases := []struct {
		name        string
		key         *rsa.PrivateKey
		kid         string
		issuer      string
		alg         string
		trustAnchor string
		want        string
	}{
		{"nil key", nil, "k", "i", "", "tests.example.com", "key is nil"},
		{"empty kid", good, "", "i", "", "tests.example.com", "kid is empty"},
		{"empty issuer", good, "k", "", "", "tests.example.com", "issuer is empty"},
		{"bad alg", good, "k", "i", "HS256", "tests.example.com", "unsupported"},
		{"empty trust anchor", good, "k", "i", "", "", "TrustAnchor"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewSigner(tc.key, tc.kid, tc.issuer, SignerOptions{Alg: tc.alg, TrustAnchor: tc.trustAnchor})
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
// RS256 produces the same wire string. (PS256 has random salt;
// RS256 is deterministic and a stability pin for the encoding.)
func TestSigner_OutputIsStable(t *testing.T) {
	signer, _ := newTestSigner(t, SignerOptions{Alg: AlgRS256})
	payload := []byte(`{"x":1}`)
	a, err := signer.Sign(payload)
	if err != nil {
		t.Fatal(err)
	}
	b, err := signer.Sign(payload)
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Errorf("RS256 sign output is not deterministic:\n a=%s\n b=%s", a, b)
	}
}

// _ keeps time imported even when the test set doesn't reference it
// directly (kept around for the future iat-bearing variants).
var _ = time.Now

func mapKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
