package openbanking

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"
)

// ClientAssertionConfig configures the JWT used for FAPI
// `private_key_jwt` / `tls_client_auth` flows at /token.
//
// Per RFC 7523 the JWT carries:
//
//   - iss = sub = the client_id (so the AS can look up the client)
//   - aud = the token URL (binds the JWT to a specific endpoint)
//   - exp ≤ now + ClientAssertionMaxLifetime (sandbox accepts up to 5m)
//   - jti = random per-call identifier so replays are detectable
//
// The header carries the kid so the AS can find the matching JWK
// in the JWKS we publish.
type ClientAssertionConfig struct {
	ClientID   string
	TokenURL   string
	Kid        string
	PrivateKey *rsa.PrivateKey
	// Alg defaults to PS256.
	Alg string
	// Lifetime overrides ClientAssertionDefaultLifetime.
	Lifetime time.Duration
	// Now lets tests inject a clock; nil uses time.Now().UTC().
	Now func() time.Time
}

// ClientAssertionDefaultLifetime is the default validity window
// for an emitted client_assertion JWT. Short on purpose — the JWT
// is only used to mint an access token, and a tight window cuts
// the replay surface.
const ClientAssertionDefaultLifetime = 60 * time.Second

// SignClientAssertion produces the compact JWS the /token endpoint
// expects in the `client_assertion` form parameter. Generates a
// fresh `jti` per call so caching this output is unsafe — call
// once per token request.
func SignClientAssertion(cfg ClientAssertionConfig) (string, error) {
	if cfg.ClientID == "" {
		return "", errors.New("openbanking: client assertion needs ClientID")
	}
	if cfg.TokenURL == "" {
		return "", errors.New("openbanking: client assertion needs TokenURL")
	}
	if cfg.Kid == "" {
		return "", errors.New("openbanking: client assertion needs Kid")
	}
	if cfg.PrivateKey == nil {
		return "", errors.New("openbanking: client assertion needs PrivateKey")
	}
	alg := cfg.Alg
	if alg == "" {
		alg = AlgPS256
	}
	switch alg {
	case AlgPS256, AlgPS384, AlgPS512, AlgRS256, AlgRS384, AlgRS512:
	default:
		return "", fmt.Errorf("openbanking: unsupported alg %q", alg)
	}
	now := cfg.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	lifetime := cfg.Lifetime
	if lifetime <= 0 {
		lifetime = ClientAssertionDefaultLifetime
	}
	issued := now()
	jti, err := randomJTI()
	if err != nil {
		return "", err
	}
	header := map[string]any{
		"alg": alg,
		"kid": cfg.Kid,
		"typ": "JWT",
	}
	claims := map[string]any{
		"iss": cfg.ClientID,
		"sub": cfg.ClientID,
		"aud": cfg.TokenURL,
		"jti": jti,
		"iat": issued.Unix(),
		"exp": issued.Add(lifetime).Unix(),
	}
	headerJSON, err := json.Marshal(header)
	if err != nil {
		return "", fmt.Errorf("openbanking: marshal assertion header: %w", err)
	}
	claimsJSON, err := json.Marshal(claims)
	if err != nil {
		return "", fmt.Errorf("openbanking: marshal assertion claims: %w", err)
	}
	headerEnc := base64.RawURLEncoding.EncodeToString(headerJSON)
	claimsEnc := base64.RawURLEncoding.EncodeToString(claimsJSON)
	signingInput := []byte(headerEnc + "." + claimsEnc)
	sig, err := signRSA(alg, cfg.PrivateKey, signingInput)
	if err != nil {
		return "", err
	}
	return headerEnc + "." + claimsEnc + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

// signRSA signs the JWS signing input with the configured RSA
// algorithm. PS variants use RSASSA-PSS with SHA-256/384/512 and a
// salt length equal to the hash; RS variants use PKCS#1 v1.5.
// The hash dispatch reuses hashFor from jws_verify.go so the
// signer and verifier stay in lockstep on the digest choice.
func signRSA(alg string, key *rsa.PrivateKey, input []byte) ([]byte, error) {
	h, cryptoHash := hashFor(alg)
	h.Write(input)
	digest := h.Sum(nil)
	switch alg {
	case AlgPS256, AlgPS384, AlgPS512:
		return rsa.SignPSS(rand.Reader, key, cryptoHash, digest, &rsa.PSSOptions{
			SaltLength: rsa.PSSSaltLengthEqualsHash,
			Hash:       cryptoHash,
		})
	case AlgRS256, AlgRS384, AlgRS512:
		return rsa.SignPKCS1v15(rand.Reader, key, cryptoHash, digest)
	}
	return nil, fmt.Errorf("openbanking: unreachable alg %q", alg)
}

// randomJTI returns a 128-bit random identifier suitable for the
// JWT jti claim. base64url so it survives form-encoding without
// further escaping.
func randomJTI() (string, error) {
	buf := make([]byte, 16)
	if _, err := io.ReadFull(rand.Reader, buf); err != nil {
		return "", fmt.Errorf("openbanking: jti: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}
