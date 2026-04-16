package openbanking

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// SignerOptions configure a Signer. Zero value uses PS256 with the
// OBIE-standard trust anchor.
type SignerOptions struct {
	// Alg names the JWS algorithm. Supported: PS256, PS384, PS512,
	// RS256, RS384, RS512. Empty defaults to PS256, OBIE's
	// recommended algorithm.
	Alg string

	// TrustAnchor is the value of the OBIE
	// `http://openbanking.org.uk/tan` claim. Empty defaults to
	// "openbanking.org.uk", the canonical anchor.
	TrustAnchor string

	// Now lets tests inject a clock for the
	// `http://openbanking.org.uk/iat` issued-at claim. Nil uses
	// time.Now().UTC().
	Now func() time.Time
}

// Signer produces detached JWS signatures suitable for the
// x-jws-signature header on Revolut Open Banking POST/PUT requests.
//
// OBIE compliance notes encoded into the signer:
//
//   - JWS uses b64=false: the signing input is
//     base64url(header) + "." + payload (raw bytes), not
//     base64url(payload). The wire form is the detached compact
//     serialisation `base64url(header)..base64url(signature)`.
//   - The protected header always carries the OBIE-required
//     vendor claims `iat`, `iss`, `tan` and lists them (plus `b64`)
//     in `crit`. Verifiers MUST understand each crit entry per
//     RFC 7515 §4.1.11; openbanking's verify side accepts these.
//   - Algorithm defaults to PS256 (RSASSA-PSS over SHA-256), the
//     OBIE-recommended choice. RS256 (PKCS#1 v1.5) is supported
//     for ASPSPs that haven't migrated.
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
	tan := opts.TrustAnchor
	if tan == "" {
		tan = "openbanking.org.uk"
	}
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
		"alg":                                   s.alg,
		"kid":                                   s.kid,
		"typ":                                   "JOSE",
		"cty":                                   "application/json",
		"b64":                                   false,
		"http://openbanking.org.uk/iat":         s.now().Unix(),
		"http://openbanking.org.uk/iss":         s.issuer,
		"http://openbanking.org.uk/tan":         s.trustAnchor,
		"crit": []string{
			"b64",
			"http://openbanking.org.uk/iat",
			"http://openbanking.org.uk/iss",
			"http://openbanking.org.uk/tan",
		},
	}
	headerJSON, err := json.Marshal(header)
	if err != nil {
		return "", fmt.Errorf("openbanking: marshal JWS header: %w", err)
	}
	headerEnc := base64.RawURLEncoding.EncodeToString(headerJSON)

	// b64=false signing input: base64url(header) + "." + raw payload.
	signingInput := append([]byte(headerEnc+"."), payload...)
	hash, cryptoHash := hashFor(s.alg)
	hash.Write(signingInput)
	digest := hash.Sum(nil)

	var sig []byte
	switch s.alg {
	case "PS256", "PS384", "PS512":
		opts := &rsa.PSSOptions{SaltLength: rsa.PSSSaltLengthEqualsHash, Hash: cryptoHash}
		sig, err = rsa.SignPSS(rand.Reader, s.key, cryptoHash, digest, opts)
	case "RS256", "RS384", "RS512":
		sig, err = rsa.SignPKCS1v15(rand.Reader, s.key, cryptoHash, digest)
	default:
		return "", fmt.Errorf("openbanking: unreachable alg %q", s.alg)
	}
	if err != nil {
		return "", fmt.Errorf("openbanking: sign JWS: %w", err)
	}
	return headerEnc + ".." + base64.RawURLEncoding.EncodeToString(sig), nil
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
