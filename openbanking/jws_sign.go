package openbanking

import (
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// SignerOptions configure a Signer. Zero value uses PS256.
type SignerOptions struct {
	// Alg names the JWS algorithm. Supported: PS256, PS384, PS512,
	// RS256, RS384, RS512. Empty defaults to PS256.
	Alg string

	// TrustAnchor is the value of the
	// `http://openbanking.org.uk/tan` claim — the DNS domain
	// hosting the TPP's JWKS. For Revolut sandbox callers
	// publishing JWKS on GitHub Pages this is something like
	// "greatliontech.github.io". Empty is rejected by NewSigner
	// because Revolut's PISP endpoints reject every JWS that
	// lacks a TAN value matching the JWKS host.
	TrustAnchor string

	// Now lets tests inject a clock. Nil uses time.Now().UTC().
	// Reserved for future header claims that need a timestamp;
	// the current Revolut-flavoured header doesn't carry iat.
	Now func() time.Time
}

// Signer produces compact JWS signatures suitable for the
// x-jws-signature header on Revolut Open Banking POST/PUT
// requests.
//
// Wire shape (matches what the Revolut sandbox accepts):
//
//   - Standard JWS, NOT b64=false detached. The signing input is
//     base64url(header) + "." + base64url(payload).
//   - The output is the full compact form
//     base64url(header) + "." + base64url(payload) + "." + base64url(sig).
//     The payload is duplicated between the request body and the
//     header — Revolut's verifier expects it that way.
//   - Header carries only alg, kid, crit:["http://openbanking.org.uk/tan"]
//     and the TAN claim itself. The full OBIE prod header (iat / iss /
//     b64=false) is not what the Revolut sandbox wants; their verifier
//     rejects extra crit entries with "Required '...' is missing"
//     when their meaning differs from prod, and rejects unknown TAN
//     values when iss/iat are present.
//   - Algorithm defaults to PS256 (RSASSA-PSS over SHA-256). PS384/512
//     and RS256/384/512 are also supported.
//
// The issuer string passed to NewSigner is retained for callers that
// later want to switch to the OBIE-prod header set (where iss is
// mandatory) without re-plumbing.
type Signer struct {
	key         *rsa.PrivateKey
	kid         string
	issuer      string
	alg         string
	trustAnchor string
	now         func() time.Time
}

// NewSigner constructs a Signer. key signs every JWS; kid is the
// OBIE-registered key identifier the ASPSP looks up to verify;
// issuer is the SSA-registered issuer (typically a DN matching the
// TPP's signing certificate). All three are required.
func NewSigner(key *rsa.PrivateKey, kid, issuer string, opts SignerOptions) (*Signer, error) {
	if key == nil {
		return nil, errors.New("openbanking: signer key is nil")
	}
	if kid == "" {
		return nil, errors.New("openbanking: signer kid is empty")
	}
	if issuer == "" {
		return nil, errors.New("openbanking: signer issuer is empty")
	}
	alg := opts.Alg
	if alg == "" {
		alg = "PS256"
	}
	switch alg {
	case "PS256", "PS384", "PS512", "RS256", "RS384", "RS512":
	default:
		return nil, fmt.Errorf("openbanking: unsupported signing alg %q", alg)
	}
	if opts.TrustAnchor == "" {
		return nil, errors.New("openbanking: signer TrustAnchor is empty (set it to the DNS host of your published JWKS, e.g. greatliontech.github.io)")
	}
	tan := opts.TrustAnchor
	now := opts.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &Signer{
		key:         key,
		kid:         kid,
		issuer:      issuer,
		alg:         alg,
		trustAnchor: tan,
		now:         now,
	}, nil
}

// Sign produces the x-jws-signature header value for a POST/PUT
// request body. payload is the exact bytes the request will carry on
// the wire — re-marshalling json.Marshal output produces a different
// byte sequence than what reaches the verifier, so callers must use
// the same buffer they hand to the request.
func (s *Signer) Sign(payload []byte) (string, error) {
	if len(payload) == 0 {
		return "", errors.New("openbanking: signing empty payload")
	}
	header := map[string]any{
		"alg":                           s.alg,
		"kid":                           s.kid,
		"crit":                          []string{"http://openbanking.org.uk/tan"},
		"http://openbanking.org.uk/tan": s.trustAnchor,
	}
	headerJSON, err := json.Marshal(header)
	if err != nil {
		return "", fmt.Errorf("openbanking: marshal JWS header: %w", err)
	}
	headerEnc := base64.RawURLEncoding.EncodeToString(headerJSON)
	payloadEnc := base64.RawURLEncoding.EncodeToString(payload)

	// Standard JWS (b64=true) signing input.
	signingInput := []byte(headerEnc + "." + payloadEnc)
	sig, err := signRSA(s.alg, s.key, signingInput)
	if err != nil {
		return "", fmt.Errorf("openbanking: sign JWS: %w", err)
	}
	// Full compact serialisation — Revolut's verifier expects the
	// payload duplicated in the JWS even though it's also in the
	// request body.
	return headerEnc + "." + payloadEnc + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

// Convenience: a known JWS algorithm constant set so callers don't
// reach for stringly-typed values.
const (
	AlgPS256 = "PS256"
	AlgPS384 = "PS384"
	AlgPS512 = "PS512"
	AlgRS256 = "RS256"
	AlgRS384 = "RS384"
	AlgRS512 = "RS512"
)
