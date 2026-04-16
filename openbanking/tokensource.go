package openbanking

import (
	"context"
	"crypto/rsa"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/greatliontech/revolut-go/internal/core"
)

// ClientCredentialsConfig configures a client_credentials access
// token source. The /token request is sent over MTLS using the
// transport certificate Revolut issued, and authenticated with a
// `private_key_jwt` client_assertion signed by the same key the
// JWKS publishes.
type ClientCredentialsConfig struct {
	// ClientID returned by the Revolut developer portal (or DCR).
	ClientID string

	// TokenURL is the absolute URL of the OAuth /token endpoint.
	// For the sandbox: https://sandbox-oba-auth.revolut.com/token
	TokenURL string

	// Kid identifies the signing key in the JWKS. Must match the
	// `kid` field on the JWK that wraps PrivateKey.
	Kid string

	// PrivateKey signs the client_assertion JWT.
	PrivateKey *rsa.PrivateKey

	// TransportCert is the MTLS client certificate Revolut
	// expects on the TLS handshake. Build via [LoadTransportCert].
	TransportCert tls.Certificate

	// Scope is the space-separated OAuth scope set requested.
	// Typical values: "accounts" (AISP), "payments" (PISP),
	// "fundsconfirmations" (CBPII). Empty omits the parameter,
	// letting the AS apply its default.
	Scope string

	// Alg overrides the client_assertion signing algorithm.
	// Default PS256.
	Alg string

	// HTTPClient overrides the MTLS-configured *http.Client used
	// to call /token. Default: [MTLSHTTPClient] built from
	// TransportCert with the system root pool. Override to inject
	// a custom RoundTripper (test fakes, observability, etc.) —
	// the override MUST still present TransportCert during MTLS
	// or Revolut's edge will reject the handshake.
	HTTPClient *http.Client

	// Now lets tests inject a clock for the assertion's iat/exp.
	// Nil uses time.Now().UTC().
	Now func() time.Time

	// EarlyRefresh shaves this duration off the AS-reported
	// expires_in so the cached token is refreshed slightly before
	// it actually expires. Default 30s.
	EarlyRefresh time.Duration
}

// ClientCredentialsTokenSource caches a client-credentials access
// token and refreshes it as it nears expiry. Implements
// [github.com/greatliontech/revolut-go.Authenticator] via Apply,
// so an instance can be passed directly to [revolut.NewOpenBankingClient].
//
// Concurrent Apply calls share a single in-flight refresh — the
// AS sees one token request per real expiry, regardless of how
// many goroutines are dispatching API calls.
type ClientCredentialsTokenSource struct {
	cfg  ClientCredentialsConfig
	httc *http.Client

	mu      sync.Mutex
	token   string
	expires time.Time
}

// DefaultEarlyRefresh is the default headroom subtracted from the
// AS-reported lifetime before the cached token is treated as
// expired.
const DefaultEarlyRefresh = 30 * time.Second

// NewClientCredentialsTokenSource validates the config and returns
// a usable token source. It does NOT pre-fetch a token; the first
// call to Apply (or [ClientCredentialsTokenSource.Token]) does
// that.
func NewClientCredentialsTokenSource(cfg ClientCredentialsConfig) (*ClientCredentialsTokenSource, error) {
	if cfg.ClientID == "" {
		return nil, errors.New("openbanking: ClientCredentialsTokenSource needs ClientID")
	}
	if cfg.TokenURL == "" {
		return nil, errors.New("openbanking: ClientCredentialsTokenSource needs TokenURL")
	}
	if cfg.Kid == "" {
		return nil, errors.New("openbanking: ClientCredentialsTokenSource needs Kid")
	}
	if cfg.PrivateKey == nil {
		return nil, errors.New("openbanking: ClientCredentialsTokenSource needs PrivateKey")
	}
	if len(cfg.TransportCert.Certificate) == 0 {
		return nil, errors.New("openbanking: ClientCredentialsTokenSource needs TransportCert (use LoadTransportCert)")
	}
	if cfg.EarlyRefresh < 0 {
		return nil, errors.New("openbanking: EarlyRefresh must be ≥ 0")
	}
	if cfg.EarlyRefresh == 0 {
		cfg.EarlyRefresh = DefaultEarlyRefresh
	}
	httc := cfg.HTTPClient
	if httc == nil {
		httc = MTLSHTTPClient(cfg.TransportCert, nil)
	}
	return &ClientCredentialsTokenSource{cfg: cfg, httc: httc}, nil
}

// Apply implements [core.Authenticator]: fetch (or reuse) the
// cached access token and stamp it as a Bearer Authorization
// header on req.
func (s *ClientCredentialsTokenSource) Apply(req *http.Request) error {
	tok, err := s.Token(req.Context())
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	return nil
}

// Token returns a valid access token, refreshing if the cached one
// has expired (or is within EarlyRefresh of expiring). Safe for
// concurrent use.
func (s *ClientCredentialsTokenSource) Token(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	if s.token != "" && now.Before(s.expires) {
		return s.token, nil
	}
	tok, lifetime, err := s.fetch(ctx)
	if err != nil {
		return "", err
	}
	s.token = tok
	s.expires = now.Add(lifetime - s.cfg.EarlyRefresh)
	return s.token, nil
}

// fetch does the /token round-trip. Returns the access token and
// the Authorization Server-reported lifetime (expires_in).
func (s *ClientCredentialsTokenSource) fetch(ctx context.Context) (string, time.Duration, error) {
	assertion, err := SignClientAssertion(ClientAssertionConfig{
		ClientID:   s.cfg.ClientID,
		TokenURL:   s.cfg.TokenURL,
		Kid:        s.cfg.Kid,
		PrivateKey: s.cfg.PrivateKey,
		Alg:        s.cfg.Alg,
		Now:        s.cfg.Now,
	})
	if err != nil {
		return "", 0, err
	}
	form := url.Values{
		"grant_type":            {"client_credentials"},
		"client_id":             {s.cfg.ClientID},
		"client_assertion_type": {"urn:ietf:params:oauth:client-assertion-type:jwt-bearer"},
		"client_assertion":      {assertion},
	}
	if s.cfg.Scope != "" {
		form.Set("scope", s.cfg.Scope)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.cfg.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", 0, fmt.Errorf("openbanking: build /token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := s.httc.Do(req)
	if err != nil {
		return "", 0, fmt.Errorf("openbanking: /token request: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", 0, fmt.Errorf("openbanking: read /token response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", 0, &TokenError{StatusCode: resp.StatusCode, Body: body}
	}
	var tr struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &tr); err != nil {
		return "", 0, fmt.Errorf("openbanking: decode /token response: %w", err)
	}
	if tr.AccessToken == "" {
		return "", 0, fmt.Errorf("openbanking: /token returned no access_token; body=%s", body)
	}
	if tr.ExpiresIn <= 0 {
		// Spec-compliant ASes always set expires_in; fall back to
		// 5 minutes so a missing field doesn't yield a token that
		// caches forever.
		tr.ExpiresIn = 300
	}
	return tr.AccessToken, time.Duration(tr.ExpiresIn) * time.Second, nil
}

func (s *ClientCredentialsTokenSource) now() time.Time {
	if s.cfg.Now != nil {
		return s.cfg.Now()
	}
	return time.Now().UTC()
}

// String redacts the cached token so fmt printing the source
// doesn't leak the bearer.
func (s *ClientCredentialsTokenSource) String() string {
	return fmt.Sprintf("openbanking.ClientCredentialsTokenSource{ClientID:%q TokenURL:%q Kid:%q Token:[REDACTED]}",
		s.cfg.ClientID, s.cfg.TokenURL, s.cfg.Kid)
}

// TokenError carries a failing /token response. The error body is
// retained verbatim so support tickets carry the AS's diagnostic
// JSON.
type TokenError struct {
	StatusCode int
	Body       []byte
}

func (e *TokenError) Error() string {
	return fmt.Sprintf("openbanking: /token http %d: %s", e.StatusCode, e.Body)
}

// Compile-time check that ClientCredentialsTokenSource satisfies
// the SDK's Authenticator interface.
var _ core.Authenticator = (*ClientCredentialsTokenSource)(nil)
