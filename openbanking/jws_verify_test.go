package openbanking

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

// signDetached produces a detached JWS in header..signature form
// over the given payload using RS256.
func signDetached(t *testing.T, priv *rsa.PrivateKey, kid string, payload []byte) string {
	t.Helper()
	hdr := map[string]any{"alg": "RS256", "kid": kid, "typ": "JOSE"}
	hdrJSON, _ := json.Marshal(hdr)
	hdrEnc := base64.RawURLEncoding.EncodeToString(hdrJSON)
	payEnc := base64.RawURLEncoding.EncodeToString(payload)
	input := []byte(hdrEnc + "." + payEnc)
	h := sha256.New()
	h.Write(input)
	sum := h.Sum(nil)
	sig, err := rsa.SignPKCS1v15(rand.Reader, priv, crypto.SHA256, sum)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return hdrEnc + ".." + base64.RawURLEncoding.EncodeToString(sig)
}

func TestSigned_Verify_OK(t *testing.T) {
	priv, _ := rsa.GenerateKey(rand.Reader, 2048)
	body := []byte(`{"Data":{"X":1}}`)
	sig := signDetached(t, priv, "kid-1", body)
	signed := Signed[int]{Raw: body, Metadata: ResponseMetadata{JWSSignature: sig}}
	err := signed.Verify(KeyResolverFunc(func(h JWSHeader) (crypto.PublicKey, error) {
		if h.Kid != "kid-1" {
			t.Errorf("resolver got kid=%q", h.Kid)
		}
		if h.Alg != "RS256" {
			t.Errorf("resolver got alg=%q", h.Alg)
		}
		return &priv.PublicKey, nil
	}))
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
}

func TestSigned_Verify_TamperedPayloadFails(t *testing.T) {
	priv, _ := rsa.GenerateKey(rand.Reader, 2048)
	body := []byte(`{"Data":{"X":1}}`)
	sig := signDetached(t, priv, "kid-1", body)
	tampered := []byte(`{"Data":{"X":2}}`)
	signed := Signed[int]{Raw: tampered, Metadata: ResponseMetadata{JWSSignature: sig}}
	err := signed.Verify(KeyResolverFunc(func(h JWSHeader) (crypto.PublicKey, error) {
		return &priv.PublicKey, nil
	}))
	if err == nil {
		t.Fatal("want verification failure on tampered payload")
	}
}

func TestSigned_Verify_ResolverError(t *testing.T) {
	priv, _ := rsa.GenerateKey(rand.Reader, 2048)
	sig := signDetached(t, priv, "kid-7", []byte(`{}`))
	signed := Signed[int]{Raw: []byte(`{}`), Metadata: ResponseMetadata{JWSSignature: sig}}
	want := "no such kid"
	err := signed.Verify(KeyResolverFunc(func(h JWSHeader) (crypto.PublicKey, error) {
		return nil, &testErr{msg: want}
	}))
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("want %q, got %v", want, err)
	}
}

func TestSigned_Verify_EmptyRaw(t *testing.T) {
	signed := Signed[int]{Raw: nil, Metadata: ResponseMetadata{JWSSignature: "h..s"}}
	if err := signed.Verify(KeyResolverFunc(func(JWSHeader) (crypto.PublicKey, error) { return nil, nil })); err == nil {
		t.Error("want error on empty Raw")
	}
}

func TestSigned_Verify_MalformedHeader(t *testing.T) {
	signed := Signed[int]{Raw: []byte(`{}`), Metadata: ResponseMetadata{JWSSignature: "not-detached"}}
	if err := signed.Verify(KeyResolverFunc(func(JWSHeader) (crypto.PublicKey, error) { return nil, nil })); err == nil {
		t.Error("want error on non-detached form")
	}
}

type testErr struct{ msg string }

func (e *testErr) Error() string { return e.msg }
