package openbanking

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"net/url"
	"strings"
	"testing"
	"time"
)

// TestSignRequestObject_Claims pins the claim set OBIE wants in a
// PSU consent request_object: iss=client_id, aud=AS, FAPI scope,
// the intent_id binding under id_token + userinfo, the SCA
// requirement, plus state/nonce that the AS echoes on callback.
func TestSignRequestObject_Claims(t *testing.T) {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	at := time.Date(2026, 4, 16, 12, 0, 0, 0, time.UTC)
	jwt, err := SignRequestObject(RequestObjectConfig{
		ClientID:    "client-1",
		Audience:    "https://oba-auth.example.com",
		RedirectURI: "https://127.0.0.1/callback",
		Scope:       "openid accounts",
		ConsentID:   "consent-7",
		State:       "state-x",
		Nonce:       "nonce-y",
		Kid:         "kid-1",
		PrivateKey:  key,
		Now:         func() time.Time { return at },
	})
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	parts := strings.Split(jwt, ".")
	if len(parts) != 3 {
		t.Fatalf("want 3 segments; got %d", len(parts))
	}
	claims := decodeJSON(t, parts[1])
	for k, want := range map[string]any{
		"iss":           "client-1",
		"aud":           "https://oba-auth.example.com",
		"client_id":     "client-1",
		"redirect_uri":  "https://127.0.0.1/callback",
		"scope":         "openid accounts",
		"state":         "state-x",
		"nonce":         "nonce-y",
		"response_type": "code id_token",
	} {
		if claims[k] != want {
			t.Errorf("claim %q = %v; want %v", k, claims[k], want)
		}
	}
	if int64(claims["exp"].(float64)) != at.Add(RequestObjectDefaultLifetime).Unix() {
		t.Errorf("exp wrong: %v", claims["exp"])
	}
	// Verify the OBIE intent claim and SCA requirement are
	// nested correctly under claims.id_token.
	c, _ := claims["claims"].(map[string]any)
	idtok, _ := c["id_token"].(map[string]any)
	intent, _ := idtok["openbanking_intent_id"].(map[string]any)
	if intent["value"] != "consent-7" {
		t.Errorf("intent value=%v; want consent-7", intent["value"])
	}
	if intent["essential"] != true {
		t.Errorf("intent must be essential=true")
	}
	acr, _ := idtok["acr"].(map[string]any)
	if acr == nil || acr["essential"] != true {
		t.Errorf("acr SCA claim missing or not essential: %+v", acr)
	}
}

// TestSignRequestObject_VerifiesAgainstPublicKey: the emitted JWT
// is a valid PS256 JWS over its own header.payload — hashing the
// signing input and verifying with the corresponding public key
// must succeed. Catches subtle signing-input bugs (wrong base64
// alphabet, wrong segment separator) that wouldn't surface until
// Revolut's verifier rejects with an opaque error.
func TestSignRequestObject_VerifiesAgainstPublicKey(t *testing.T) {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	jwt, err := SignRequestObject(RequestObjectConfig{
		ClientID: "c", Audience: "a", RedirectURI: "r", Scope: "openid accounts",
		ConsentID: "x", State: "s", Nonce: "n", Kid: "k", PrivateKey: key,
	})
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(jwt, ".")
	signingInput := []byte(parts[0] + "." + parts[1])
	sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
	h, _ := hashFor(AlgPS256)
	h.Write(signingInput)
	if err := rsa.VerifyPSS(&key.PublicKey, crypto.SHA256, h.Sum(nil), sig, &rsa.PSSOptions{
		SaltLength: rsa.PSSSaltLengthEqualsHash,
		Hash:       crypto.SHA256,
	}); err != nil {
		t.Errorf("verify: %v", err)
	}
}

// TestSignRequestObject_RejectsBadConfig pins each constructor
// precondition.
func TestSignRequestObject_RejectsBadConfig(t *testing.T) {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	good := RequestObjectConfig{
		ClientID: "c", Audience: "a", RedirectURI: "r", Scope: "openid accounts",
		ConsentID: "x", State: "s", Nonce: "n", Kid: "k", PrivateKey: key,
	}
	clear := func(field string) RequestObjectConfig {
		c := good
		switch field {
		case "ClientID":
			c.ClientID = ""
		case "Audience":
			c.Audience = ""
		case "RedirectURI":
			c.RedirectURI = ""
		case "Scope":
			c.Scope = ""
		case "ConsentID":
			c.ConsentID = ""
		case "State":
			c.State = ""
		case "Nonce":
			c.Nonce = ""
		case "Kid":
			c.Kid = ""
		case "PrivateKey":
			c.PrivateKey = nil
		}
		return c
	}
	for _, name := range []string{"ClientID", "Audience", "RedirectURI", "Scope", "ConsentID", "State", "Nonce", "Kid", "PrivateKey"} {
		t.Run(name, func(t *testing.T) {
			_, err := SignRequestObject(clear(name))
			if err == nil {
				t.Errorf("clearing %s should error", name)
			}
		})
	}
}

// TestBuildAuthorizationURL composes the consent URL: required
// query params plus the request_object JWT. The parsed result
// must round-trip every input.
func TestBuildAuthorizationURL(t *testing.T) {
	got, err := BuildAuthorizationURL(
		"https://oba-auth.example.com/authorize",
		AuthorizationURLParams{
			ClientID:    "c",
			RedirectURI: "https://127.0.0.1/callback",
			Scope:       "openid accounts",
			State:       "state-x",
			Nonce:       "nonce-y",
		},
		"jwt-blob",
	)
	if err != nil {
		t.Fatalf("BuildAuthorizationURL: %v", err)
	}
	u, err := url.Parse(got)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	for k, want := range map[string]string{
		"response_type": "code id_token",
		"client_id":     "c",
		"redirect_uri":  "https://127.0.0.1/callback",
		"scope":         "openid accounts",
		"state":         "state-x",
		"nonce":         "nonce-y",
		"request":       "jwt-blob",
	} {
		if q.Get(k) != want {
			t.Errorf("q[%q]=%q; want %q", k, q.Get(k), want)
		}
	}
}

// TestParseAuthorizationCallback covers the callback parser:
// success extracts the code; an `?error=` reply surfaces as an
// explicit error; missing code is rejected.
func TestParseAuthorizationCallback(t *testing.T) {
	cb, err := ParseAuthorizationCallback("code=abc&state=xyz&id_token=tok")
	if err != nil {
		t.Fatal(err)
	}
	if cb.Code != "abc" || cb.State != "xyz" || cb.IDToken != "tok" {
		t.Errorf("parsed: %+v", cb)
	}
	if _, err := ParseAuthorizationCallback("error=access_denied&error_description=user+said+no"); err == nil {
		t.Error("want error on ?error= response")
	}
	if _, err := ParseAuthorizationCallback("state=x"); err == nil {
		t.Error("want error on missing code")
	}
}

// TestSplitCallbackQueryAndFragment merges the OBIE-style hybrid
// callback (code in query, id_token in fragment) so a single
// ParseAuthorizationCallback call sees both.
func TestSplitCallbackQueryAndFragment(t *testing.T) {
	merged, err := SplitCallbackQueryAndFragment("https://x/cb?code=abc&state=s#id_token=tok")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(merged, "code=abc") || !strings.Contains(merged, "id_token=tok") {
		t.Errorf("merged=%q", merged)
	}
}
