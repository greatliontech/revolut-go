package openbanking

import (
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// RequestObjectConfig configures the JWT (the "request object")
// that the OBIE consent flow embeds in the `request` query
// parameter of the authorization URL.
//
// Per the FAPI Advanced Profile, the auth server treats the
// signed request object as the source of truth for the OAuth
// parameters; the URL-level parameters are advisory. So every
// field below corresponds to a JWS claim, not a query string.
type RequestObjectConfig struct {
	// ClientID identifies the TPP. Used for both `iss` and
	// `client_id` claims. Required.
	ClientID string

	// Audience is the auth-server identifier. Typically the
	// authorization-endpoint URL or the issuer URL. Required.
	Audience string

	// RedirectURI is the callback URL Revolut redirects the user
	// to after consent. Must match a value registered with the
	// portal. Required.
	RedirectURI string

	// Scope is the OAuth scope set the request asks for. For
	// AISP: "openid accounts". For PISP: "openid payments".
	// Required.
	Scope string

	// ConsentID is the OBIE consent identifier returned by the
	// CreateAccountAccessConsents (AISP) or CreateConsents
	// (PISP) call. The auth server pins this consent to the
	// resulting access token via the openbanking_intent_id
	// claim. Required.
	ConsentID string

	// State is the CSRF token the SDK echoes back on the
	// redirect; the caller verifies the round-trip on /callback.
	// Required.
	State string

	// Nonce is the OIDC nonce used to bind the issued id_token
	// to this auth request. Required.
	Nonce string

	// Kid identifies the signing key in the published JWKS.
	// Required.
	Kid string

	// PrivateKey signs the request object. Required.
	PrivateKey *rsa.PrivateKey

	// Alg overrides the JWS algorithm. Default PS256, matching
	// what RegisterApplication advertises in
	// `request_object_signing_alg`.
	Alg string

	// Lifetime overrides RequestObjectDefaultLifetime.
	Lifetime time.Duration

	// Now lets tests inject a clock; nil uses time.Now().UTC().
	Now func() time.Time
}

// RequestObjectDefaultLifetime is the default validity window for
// the request object JWT. Long enough that a user can complete a
// browser login on a slow connection; short enough that a leaked
// URL stops being useful quickly.
const RequestObjectDefaultLifetime = 5 * time.Minute

// SignRequestObject builds and signs the request-object JWT. The
// returned compact JWS goes into the `?request=...` parameter of
// the authorization URL.
func SignRequestObject(cfg RequestObjectConfig) (string, error) {
	if cfg.ClientID == "" {
		return "", errors.New("openbanking: request object needs ClientID")
	}
	if cfg.Audience == "" {
		return "", errors.New("openbanking: request object needs Audience")
	}
	if cfg.RedirectURI == "" {
		return "", errors.New("openbanking: request object needs RedirectURI")
	}
	if cfg.Scope == "" {
		return "", errors.New("openbanking: request object needs Scope")
	}
	if cfg.ConsentID == "" {
		return "", errors.New("openbanking: request object needs ConsentID")
	}
	if cfg.State == "" || cfg.Nonce == "" {
		return "", errors.New("openbanking: request object needs State and Nonce")
	}
	if cfg.Kid == "" {
		return "", errors.New("openbanking: request object needs Kid")
	}
	if cfg.PrivateKey == nil {
		return "", errors.New("openbanking: request object needs PrivateKey")
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
		lifetime = RequestObjectDefaultLifetime
	}
	issued := now()

	header := map[string]any{
		"alg": alg,
		"kid": cfg.Kid,
		"typ": "JWT",
	}
	// The OBIE FAPI claim set: openbanking_intent_id binds the
	// resulting authorization to the consent we just minted; acr
	// asks the AS to require strong customer authentication
	// (PSD2 SCA) before consenting.
	intent := map[string]any{
		"value":     cfg.ConsentID,
		"essential": true,
	}
	claims := map[string]any{
		"iss":           cfg.ClientID,
		"aud":           cfg.Audience,
		"response_type": "code id_token",
		"client_id":     cfg.ClientID,
		"redirect_uri":  cfg.RedirectURI,
		"scope":         cfg.Scope,
		"state":         cfg.State,
		"nonce":         cfg.Nonce,
		"exp":           issued.Add(lifetime).Unix(),
		"iat":           issued.Unix(),
		"claims": map[string]any{
			"id_token": map[string]any{
				"openbanking_intent_id": intent,
				"acr": map[string]any{
					"essential": true,
					"values":    []string{"urn:openbanking:psd2:sca"},
				},
			},
			"userinfo": map[string]any{
				"openbanking_intent_id": intent,
			},
		},
	}

	headerJSON, err := json.Marshal(header)
	if err != nil {
		return "", fmt.Errorf("openbanking: marshal request-object header: %w", err)
	}
	claimsJSON, err := json.Marshal(claims)
	if err != nil {
		return "", fmt.Errorf("openbanking: marshal request-object claims: %w", err)
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
