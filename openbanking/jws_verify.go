// Package openbanking's detached-JWS verification helper.
//
// Revolut's Open Banking endpoints sign response bodies with a
// detached JWS: the x-jws-signature header carries
// "<base64url(header)>..<base64url(signature)>" with the payload
// omitted. Callers verify by reconstructing
// base64url(header) + "." + base64url(payload) + "." + base64url(signature)
// and running the usual JWS verification flow against the header's
// declared algorithm.
//
// This helper supports RS256/RS384/RS512 (rsa.PublicKey) and
// ES256/ES384/ES512 (ecdsa.PublicKey), matching OBIE's permitted set.
// JWKS fetching is left to the caller: supply a KeyResolver that
// maps a JWS header's `kid` to the right public key.
package openbanking

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"math/big"
	"strings"
)

// KeyResolver returns the public key to verify a JWS header with.
// The header argument is the parsed JWS protected header so callers
// can dispatch on `kid`, `alg`, or other fields. Return an error to
// abort verification (e.g. unknown kid).
type KeyResolver interface {
	Resolve(header JWSHeader) (crypto.PublicKey, error)
}

// KeyResolverFunc adapts a plain function to KeyResolver.
type KeyResolverFunc func(JWSHeader) (crypto.PublicKey, error)

// Resolve implements KeyResolver.
func (f KeyResolverFunc) Resolve(h JWSHeader) (crypto.PublicKey, error) { return f(h) }

// JWSHeader is the parsed protected header of a detached JWS. Only
// the fields the verifier consults are typed; the rest of the header
// is preserved in Extra so callers can inspect OBIE-specific claims
// like http://openbanking.org.uk/iat without re-parsing.
type JWSHeader struct {
	Alg  string   `json:"alg"`
	Kid  string   `json:"kid"`
	Typ  string   `json:"typ,omitempty"`
	Crit []string `json:"crit,omitempty"`
	// B64Encoded reports the b64 protected-header value (RFC 7797).
	// When true (the JWS default), the signing input is
	// base64url(payload). When false — OBIE's standard for
	// detached signatures — the signing input is the raw payload.
	// Default true when the header omits the field, matching JWS
	// behaviour.
	B64Encoded bool           `json:"-"`
	Extra      map[string]any `json:"-"`
}

// Verify validates the Signed response's detached JWS against the
// resolved public key. Returns nil on success; any tampering,
// unknown algorithm, or missing header produces a non-nil error.
//
// The caller must pass a resolver that can hand back the correct
// public key for the header's kid. Typical implementations fetch a
// JWKS from the Revolut .well-known endpoint and cache it.
func (s Signed[T]) Verify(resolver KeyResolver) error {
	if resolver == nil {
		return errors.New("openbanking: KeyResolver is nil")
	}
	sig := s.Metadata.JWSSignature
	if sig == "" {
		return errors.New("openbanking: x-jws-signature header was absent; nothing to verify")
	}
	if len(s.Raw) == 0 {
		return errors.New("openbanking: Signed.Raw is empty; re-marshalling the typed value loses byte-for-byte fidelity")
	}
	return verifyDetachedJWS(sig, s.Raw, resolver)
}

func verifyDetachedJWS(sig string, payload []byte, resolver KeyResolver) error {
	headerEnc, sigEnc, ok := splitDetached(sig)
	if !ok {
		return fmt.Errorf("openbanking: x-jws-signature %q not in detached header..signature form", sig)
	}
	headerBytes, err := base64.RawURLEncoding.DecodeString(headerEnc)
	if err != nil {
		return fmt.Errorf("openbanking: decode JWS header: %w", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(headerBytes, &raw); err != nil {
		return fmt.Errorf("openbanking: parse JWS header JSON: %w", err)
	}
	h := JWSHeader{B64Encoded: true, Extra: map[string]any{}}
	for k, v := range raw {
		switch k {
		case "alg":
			if s, ok := v.(string); ok {
				h.Alg = s
			}
		case "kid":
			if s, ok := v.(string); ok {
				h.Kid = s
			}
		case "typ":
			if s, ok := v.(string); ok {
				h.Typ = s
			}
		case "b64":
			if b, ok := v.(bool); ok {
				h.B64Encoded = b
			}
		case "crit":
			if arr, ok := v.([]any); ok {
				for _, x := range arr {
					if s, ok := x.(string); ok {
						h.Crit = append(h.Crit, s)
					}
				}
			}
		default:
			h.Extra[k] = v
		}
	}
	if h.Alg == "" {
		return errors.New("openbanking: JWS header missing alg")
	}
	// RFC 7515 §4.1.11: if crit is present, the verifier MUST
	// understand every listed extension or reject the JWS. Only the
	// OBIE-specific claim names below are recognised here; anything
	// else is treated as an unknown extension and rejected.
	for _, c := range h.Crit {
		if !isUnderstoodCrit(c) {
			return fmt.Errorf("openbanking: JWS header has unknown crit extension %q", c)
		}
	}
	sigBytes, err := base64.RawURLEncoding.DecodeString(sigEnc)
	if err != nil {
		return fmt.Errorf("openbanking: decode JWS signature: %w", err)
	}
	pubKey, err := resolver.Resolve(h)
	if err != nil {
		return fmt.Errorf("openbanking: resolve JWS key for kid=%q: %w", h.Kid, err)
	}
	// b64=false (RFC 7797): signing input is the raw payload.
	// b64=true (default): signing input is base64url(payload).
	var signingInput []byte
	if h.B64Encoded {
		payloadEnc := base64.RawURLEncoding.EncodeToString(payload)
		signingInput = []byte(headerEnc + "." + payloadEnc)
	} else {
		signingInput = append([]byte(headerEnc+"."), payload...)
	}
	return verifyAlg(h.Alg, signingInput, sigBytes, pubKey)
}

// isUnderstoodCrit reports whether a crit entry is one the verifier
// recognises. OBIE defines several vendor-specific claims; we
// accept the common set by URI so signatures from TPPs that ship
// either the OBIE-prod or Revolut-sandbox header set validate
// cleanly. Unknown entries are rejected per RFC 7515 §4.1.11.
func isUnderstoodCrit(c string) bool {
	switch c {
	case
		"http://openbanking.org.uk/iat",
		"http://openbanking.org.uk/iss",
		"http://openbanking.org.uk/tan",
		"b64":
		return true
	}
	return false
}

// splitDetached accepts both shapes Revolut produces:
//
//   - "<header>..<sig>" — RFC 7797 detached b64=false form
//     (the original OBIE prod style)
//   - "<header>.<payload>.<sig>" — full compact JWS form
//     (Revolut sandbox style)
//
// Returns headerEnc + sigEnc; the caller already has the payload
// from the request body so the middle segment is discarded.
func splitDetached(sig string) (string, string, bool) {
	if parts := strings.SplitN(sig, "..", 2); len(parts) == 2 && parts[0] != "" && parts[1] != "" {
		return parts[0], parts[1], true
	}
	if parts := strings.Split(sig, "."); len(parts) == 3 && parts[0] != "" && parts[2] != "" {
		return parts[0], parts[2], true
	}
	return "", "", false
}

func verifyAlg(alg string, signingInput, sig []byte, pub crypto.PublicKey) error {
	switch alg {
	case "RS256", "RS384", "RS512":
		rsaKey, ok := pub.(*rsa.PublicKey)
		if !ok {
			return fmt.Errorf("openbanking: alg=%s requires *rsa.PublicKey, got %T", alg, pub)
		}
		h, cryptoHash := hashFor(alg)
		h.Write(signingInput)
		return rsa.VerifyPKCS1v15(rsaKey, cryptoHash, h.Sum(nil), sig)
	case "PS256", "PS384", "PS512":
		rsaKey, ok := pub.(*rsa.PublicKey)
		if !ok {
			return fmt.Errorf("openbanking: alg=%s requires *rsa.PublicKey, got %T", alg, pub)
		}
		h, cryptoHash := hashFor(alg)
		h.Write(signingInput)
		opts := &rsa.PSSOptions{SaltLength: rsa.PSSSaltLengthEqualsHash, Hash: cryptoHash}
		return rsa.VerifyPSS(rsaKey, cryptoHash, h.Sum(nil), sig, opts)
	case "ES256", "ES384", "ES512":
		ecKey, ok := pub.(*ecdsa.PublicKey)
		if !ok {
			return fmt.Errorf("openbanking: alg=%s requires *ecdsa.PublicKey, got %T", alg, pub)
		}
		h, _ := hashFor(alg)
		h.Write(signingInput)
		sum := h.Sum(nil)
		half := len(sig) / 2
		if half*2 != len(sig) || half == 0 {
			return fmt.Errorf("openbanking: ECDSA signature %d bytes, want even", len(sig))
		}
		r := new(big.Int).SetBytes(sig[:half])
		sParam := new(big.Int).SetBytes(sig[half:])
		if !ecdsa.Verify(ecKey, sum, r, sParam) {
			return errors.New("openbanking: ECDSA signature verification failed")
		}
		return nil
	default:
		return fmt.Errorf("openbanking: unsupported alg %q", alg)
	}
}

func hashFor(alg string) (hash.Hash, crypto.Hash) {
	switch alg {
	case "RS256", "PS256", "ES256":
		return sha256.New(), crypto.SHA256
	case "RS384", "PS384", "ES384":
		return sha512.New384(), crypto.SHA384
	case "RS512", "PS512", "ES512":
		return sha512.New(), crypto.SHA512
	}
	return sha256.New(), crypto.SHA256
}
