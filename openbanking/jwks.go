package openbanking

import (
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
)

// JWK represents one key in a JWKS document. Fields are RFC 7517
// (JWK) + RFC 7638 (thumbprint) + RFC 7517 §4.7 / §4.8 (x5c, x5t#S256).
//
// Marshaling preserves the precise ordering and key set OBIE
// verifiers expect; the SDK builds JWKs via [JWKFromRSA] rather
// than callers populating them by hand.
type JWK struct {
	Kty            string   `json:"kty"`
	Use            string   `json:"use,omitempty"`
	Alg            string   `json:"alg,omitempty"`
	Kid            string   `json:"kid"`
	N              string   `json:"n"`
	E              string   `json:"e"`
	X5C            []string `json:"x5c,omitempty"`
	X5TSHA256      string   `json:"x5t#S256,omitempty"`
}

// JWKS is the JSON wire form of a key set.
type JWKS struct {
	Keys []JWK `json:"keys"`
}

// JWKEntry is the input to [BuildJWKS]: a public key plus optional
// metadata about how it's used. Cert, when non-nil, lands in x5c
// and contributes the x5t#S256 thumbprint; Revolut's portal/JWKS
// fetcher uses x5c to bind the JWS to the issued sandbox cert.
type JWKEntry struct {
	Public *rsa.PublicKey
	// CertDER is the X.509 certificate that wraps Public, in DER
	// form. Optional; when supplied, the JWK includes x5c and
	// x5t#S256 so Revolut's verifier can pin the cert.
	CertDER []byte
	// Use is the RFC 7517 "use" value: typically "sig". Empty
	// omits the field.
	Use string
	// Alg is the JWS algorithm this key signs with. Empty omits
	// the field. Use one of the Alg* constants from this package.
	Alg string
	// Kid overrides the kid value. Empty derives the kid from the
	// RFC 7638 JWK thumbprint, which is the most portable choice
	// because Revolut's verifier picks the JWK by kid lookup.
	Kid string
}

// BuildJWKS assembles a JWKS document from one or more keys, in
// the order given. The output is canonical JSON ready to host at a
// public URL — Revolut fetches it during DCR / token verification.
//
// Each entry's Kid (or its RFC 7638 thumbprint when Kid is empty)
// must match the `kid` header in JWS objects signed with the
// corresponding private key. The SDK's [Signer] does not yet read
// kid from the JWKS; callers compose the two by passing the same
// string through both sides.
func BuildJWKS(entries ...JWKEntry) ([]byte, error) {
	if len(entries) == 0 {
		return nil, errors.New("openbanking: BuildJWKS needs at least one entry")
	}
	keys := make([]JWK, 0, len(entries))
	for i, e := range entries {
		if e.Public == nil {
			return nil, fmt.Errorf("openbanking: entry %d: nil public key", i)
		}
		jwk := publicKeyToJWK(e.Public)
		jwk.Use = e.Use
		jwk.Alg = e.Alg
		jwk.Kid = e.Kid
		if jwk.Kid == "" {
			jwk.Kid = rsaThumbprint(e.Public)
		}
		if len(e.CertDER) > 0 {
			// x5c is base64 (NOT base64url), no padding stripped —
			// per RFC 7517 §4.7 each entry is the standard base64
			// of the DER-encoded certificate.
			jwk.X5C = []string{base64.StdEncoding.EncodeToString(e.CertDER)}
			sum := sha256.Sum256(e.CertDER)
			jwk.X5TSHA256 = base64.RawURLEncoding.EncodeToString(sum[:])
			// Sanity check: the cert's public key must match the
			// JWK's public key, otherwise verifiers will reject.
			if cert, err := x509.ParseCertificate(e.CertDER); err == nil {
				if rsaPub, ok := cert.PublicKey.(*rsa.PublicKey); ok {
					if rsaPub.N.Cmp(e.Public.N) != 0 || rsaPub.E != e.Public.E {
						return nil, fmt.Errorf("openbanking: entry %d: cert public key does not match supplied key", i)
					}
				}
			}
		}
		keys = append(keys, jwk)
	}
	out, err := json.MarshalIndent(JWKS{Keys: keys}, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("openbanking: marshal JWKS: %w", err)
	}
	return out, nil
}

// publicKeyToJWK fills the kty/n/e fields of a JWK for an RSA
// public key. The use/alg/kid/x5c fields are populated by
// BuildJWKS from the entry's metadata.
func publicKeyToJWK(pub *rsa.PublicKey) JWK {
	return JWK{
		Kty: "RSA",
		N:   base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
		E:   base64URLEncodeInt(pub.E),
	}
}

// base64URLEncodeInt encodes an RSA exponent as the shortest
// big-endian byte representation, base64url with no padding —
// matching RFC 7518 §6.3.
func base64URLEncodeInt(v int) string {
	// Trim leading zero bytes; for the common e=65537 the result
	// is "AQAB", three bytes (0x01, 0x00, 0x01).
	buf := make([]byte, 0, 8)
	for v > 0 {
		buf = append([]byte{byte(v & 0xff)}, buf...)
		v >>= 8
	}
	if len(buf) == 0 {
		buf = []byte{0}
	}
	return base64.RawURLEncoding.EncodeToString(buf)
}

// rsaThumbprint returns the RFC 7638 SHA-256 JWK thumbprint of an
// RSA public key. The thumbprint is the base64url-encoded SHA-256
// of a canonical JSON form of the JWK with only the required
// members (e, kty, n) in alphabetical order — making it stable
// across implementations.
func rsaThumbprint(pub *rsa.PublicKey) string {
	canonical := struct {
		E   string `json:"e"`
		Kty string `json:"kty"`
		N   string `json:"n"`
	}{
		E:   base64URLEncodeInt(pub.E),
		Kty: "RSA",
		N:   base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
	}
	// json.Marshal preserves struct-field order; the canonical
	// form RFC 7638 specifies happens to match alphabetical order
	// of the required RSA members, so a struct works without
	// hand-rolling sort.
	buf, _ := json.Marshal(canonical)
	sum := sha256.Sum256(buf)
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// sorted returns a stably-sorted copy of in. Used for caller-
// supplied sets where iteration order would otherwise depend on
// map traversal.
func sorted(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}

var _ = sorted // reserved for upcoming sets that need stable order
