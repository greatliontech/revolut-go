// Package jwt implements the JWT client_assertion flow used by the Revolut
// Business API. Users sign JWTs with an RSA private key whose public
// certificate they have uploaded to the Revolut developer portal, and
// exchange an authorization code (or refresh token) for an access token.
//
// Construct a Signer from your private key, then either call ExchangeCode
// once (after the browser consent flow) to bootstrap a refresh token, or
// wrap an existing refresh token in a Source to produce a
// revolut.Authenticator that refreshes access tokens on demand.
package jwt

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"time"
)

// Default values Revolut expects on the client_assertion.
const (
	defaultAudience = "https://revolut.com"
	defaultTTL      = 90 * time.Second
)

// Signer produces signed JWT client assertions for a single Revolut API
// client. A zero Signer is not usable; construct via [NewSigner].
//
// Signers are safe for concurrent use.
type Signer struct {
	key      *rsa.PrivateKey
	issuer   string
	clientID string
	audience string
	ttl      time.Duration
	now      func() time.Time
}

// Config configures a [Signer].
type Config struct {
	// PrivateKey is the RSA private key matching the public certificate
	// uploaded to the Revolut developer portal.
	PrivateKey *rsa.PrivateKey
	// Issuer is the "iss" claim. Revolut expects a domain registered
	// alongside the certificate.
	Issuer string
	// ClientID is the "sub" claim (the client_id shown in Revolut's UI
	// after uploading the certificate).
	ClientID string
	// Audience overrides the "aud" claim. Default: "https://revolut.com".
	Audience string
	// TTL is the lifetime of each client_assertion JWT. Default: 90s.
	TTL time.Duration
	// Now overrides the clock. Used in tests.
	Now func() time.Time
}

// NewSigner validates cfg and returns a Signer.
func NewSigner(cfg Config) (*Signer, error) {
	if cfg.PrivateKey == nil {
		return nil, errors.New("jwt: PrivateKey is required")
	}
	if cfg.Issuer == "" {
		return nil, errors.New("jwt: Issuer is required")
	}
	if cfg.ClientID == "" {
		return nil, errors.New("jwt: ClientID is required")
	}
	s := &Signer{
		key:      cfg.PrivateKey,
		issuer:   cfg.Issuer,
		clientID: cfg.ClientID,
		audience: cfg.Audience,
		ttl:      cfg.TTL,
		now:      cfg.Now,
	}
	if s.audience == "" {
		s.audience = defaultAudience
	}
	if s.ttl <= 0 {
		s.ttl = defaultTTL
	}
	if s.now == nil {
		s.now = time.Now
	}
	return s, nil
}

// ClientID returns the configured sub/client_id. Convenient for the form
// body of the token exchange.
func (s *Signer) ClientID() string { return s.clientID }

// Sign builds and signs an RS256 JWT suitable for use as a Revolut
// client_assertion.
func (s *Signer) Sign() (string, error) {
	now := s.now()
	header := map[string]string{"alg": "RS256", "typ": "JWT"}
	claims := map[string]any{
		"iss": s.issuer,
		"sub": s.clientID,
		"aud": s.audience,
		"iat": now.Unix(),
		"exp": now.Add(s.ttl).Unix(),
	}
	hb, err := json.Marshal(header)
	if err != nil {
		return "", fmt.Errorf("jwt: marshal header: %w", err)
	}
	cb, err := json.Marshal(claims)
	if err != nil {
		return "", fmt.Errorf("jwt: marshal claims: %w", err)
	}
	signing := base64url(hb) + "." + base64url(cb)
	h := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, s.key, crypto.SHA256, h[:])
	if err != nil {
		return "", fmt.Errorf("jwt: sign: %w", err)
	}
	return signing + "." + base64url(sig), nil
}

func base64url(b []byte) string {
	return base64.RawURLEncoding.EncodeToString(b)
}

// LoadPrivateKeyPEM decodes an RSA private key from PEM-encoded bytes.
// Both PKCS#1 ("RSA PRIVATE KEY") and PKCS#8 ("PRIVATE KEY") blocks are
// accepted.
func LoadPrivateKeyPEM(data []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, errors.New("jwt: no PEM block found")
	}
	switch block.Type {
	case "RSA PRIVATE KEY":
		return x509.ParsePKCS1PrivateKey(block.Bytes)
	case "PRIVATE KEY":
		key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("jwt: parse PKCS#8 key: %w", err)
		}
		rsaKey, ok := key.(*rsa.PrivateKey)
		if !ok {
			return nil, fmt.Errorf("jwt: PKCS#8 key is %T, want *rsa.PrivateKey", key)
		}
		return rsaKey, nil
	default:
		return nil, fmt.Errorf("jwt: unsupported PEM block type %q", block.Type)
	}
}

// LoadPrivateKeyFile reads and decodes an RSA private key from a PEM file.
func LoadPrivateKeyFile(path string) (*rsa.PrivateKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("jwt: read key: %w", err)
	}
	return LoadPrivateKeyPEM(data)
}
