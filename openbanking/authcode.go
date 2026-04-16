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

// AuthCodeConfig configures a per-PSU access-token source. After
// the consent flow completes, the caller exchanges the one-shot
// `code` for an access + refresh token pair via [ExchangeAuthCode];
// the resulting token wraps that pair and refreshes itself in
// place as the access token nears expiry.
//
// All FAPI / MTLS materials are the same as the client-credentials
// path — a single Signer / TransportCert / Kid drives both sides.
type AuthCodeConfig struct {
	ClientID      string
	TokenURL      string
	Kid           string
	PrivateKey    *rsa.PrivateKey
	TransportCert tls.Certificate
	Alg           string

	// HTTPClient overrides the MTLS-configured client. Default:
	// MTLSHTTPClient(TransportCert, nil) — pass an MTLS client
	// pre-loaded with the OBIE PP CA bundle for the sandbox.
	HTTPClient *http.Client

	// Now lets tests inject a clock; nil uses time.Now().UTC().
	Now func() time.Time

	// EarlyRefresh shaves this duration off the AS-reported
	// expires_in; default DefaultEarlyRefresh.
	EarlyRefresh time.Duration
}

// AuthCodeToken is the persisted output of the consent flow: the
// access token, its expiry, and the long-lived refresh token used
// to renew it. The marshaled form is what cmd/ob-bootstrap writes
// to disk for the integration test to load.
type AuthCodeToken struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token,omitempty"`
	IDToken      string    `json:"id_token,omitempty"`
	TokenType    string    `json:"token_type,omitempty"`
	Scope        string    `json:"scope,omitempty"`
	ExpiresAt    time.Time `json:"expires_at"`
}

// ExchangeAuthCode trades a one-shot authorization code for the
// initial AuthCodeToken. Call once after capturing the redirect
// from the consent UI; persist the result so subsequent runs
// hydrate via NewAuthCodeTokenSource without needing the user to
// re-consent.
func ExchangeAuthCode(ctx context.Context, cfg AuthCodeConfig, code, redirectURI string) (*AuthCodeToken, error) {
	if code == "" {
		return nil, errors.New("openbanking: ExchangeAuthCode needs a code")
	}
	if redirectURI == "" {
		return nil, errors.New("openbanking: ExchangeAuthCode needs the same redirect_uri the consent URL used")
	}
	httc, err := authCodeHTTPClient(cfg)
	if err != nil {
		return nil, err
	}
	form := url.Values{
		"grant_type":   {"authorization_code"},
		"code":         {code},
		"redirect_uri": {redirectURI},
	}
	return postAuthCodeToken(ctx, httc, cfg, form)
}

// AuthCodeTokenSource is the SDK Authenticator backed by an
// access + refresh token pair. Stamps Authorization: Bearer …
// on every API call and silently refreshes when the access
// token is within EarlyRefresh of expiring.
type AuthCodeTokenSource struct {
	cfg  AuthCodeConfig
	httc *http.Client

	mu  sync.Mutex
	tok AuthCodeToken
}

// NewAuthCodeTokenSource hydrates a source from a previously
// persisted AuthCodeToken — typically the JSON cmd/ob-bootstrap
// writes after the one-time consent flow.
//
// A missing RefreshToken is allowed: the source will hand out the
// cached access token until it expires, then return an error
// (rather than attempt a refresh against a token that doesn't
// exist). PISP payment-scope tokens are typically single-use and
// don't carry a refresh; AISP account-scope tokens usually do.
func NewAuthCodeTokenSource(cfg AuthCodeConfig, initial AuthCodeToken) (*AuthCodeTokenSource, error) {
	if initial.AccessToken == "" {
		return nil, errors.New("openbanking: AuthCodeTokenSource needs a non-empty AccessToken")
	}
	httc, err := authCodeHTTPClient(cfg)
	if err != nil {
		return nil, err
	}
	if cfg.EarlyRefresh < 0 {
		return nil, errors.New("openbanking: EarlyRefresh must be ≥ 0")
	}
	if cfg.EarlyRefresh == 0 {
		cfg.EarlyRefresh = DefaultEarlyRefresh
	}
	return &AuthCodeTokenSource{cfg: cfg, httc: httc, tok: initial}, nil
}

// Apply implements core.Authenticator.
func (s *AuthCodeTokenSource) Apply(req *http.Request) error {
	tok, err := s.Token(req.Context())
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	return nil
}

// Token returns a valid access token, refreshing via the cached
// refresh_token when the access token is past its EarlyRefresh
// window. If the cached pair has no RefreshToken (PISP tokens are
// typically single-use) and the access token is expired, the
// caller must re-run the consent flow — Token returns an error.
func (s *AuthCodeTokenSource) Token(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	if s.tok.AccessToken != "" && now.Before(s.tok.ExpiresAt) {
		return s.tok.AccessToken, nil
	}
	if s.tok.RefreshToken == "" {
		return "", errors.New("openbanking: access token expired and no refresh_token cached — re-run the consent flow")
	}
	form := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {s.tok.RefreshToken},
	}
	tok, err := postAuthCodeToken(ctx, s.httc, s.cfg, form)
	if err != nil {
		return "", err
	}
	// Some ASes don't rotate the refresh token on every refresh;
	// preserve the previous one when the response leaves the
	// field empty.
	if tok.RefreshToken == "" {
		tok.RefreshToken = s.tok.RefreshToken
	}
	s.tok = *tok
	return s.tok.AccessToken, nil
}

// Snapshot returns the current cached token. Useful for
// persisting after a refresh so the next process boot doesn't
// have to repeat the (usually) one-shot exchange.
func (s *AuthCodeTokenSource) Snapshot() AuthCodeToken {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.tok
}

func (s *AuthCodeTokenSource) now() time.Time {
	if s.cfg.Now != nil {
		return s.cfg.Now()
	}
	return time.Now().UTC()
}

// String redacts the cached tokens so fmt printing the source
// doesn't leak credentials.
func (s *AuthCodeTokenSource) String() string {
	return fmt.Sprintf("openbanking.AuthCodeTokenSource{ClientID:%q TokenURL:%q AccessToken:[REDACTED] RefreshToken:[REDACTED]}",
		s.cfg.ClientID, s.cfg.TokenURL)
}

// postAuthCodeToken is the shared /token POST used by both
// ExchangeAuthCode (initial code-for-token swap) and
// AuthCodeTokenSource.Token (refresh-token swap). The MTLS
// client + private_key_jwt client_assertion are identical;
// only the grant-specific form fields differ.
func postAuthCodeToken(ctx context.Context, httc *http.Client, cfg AuthCodeConfig, extra url.Values) (*AuthCodeToken, error) {
	assertion, err := SignClientAssertion(ClientAssertionConfig{
		ClientID:   cfg.ClientID,
		TokenURL:   cfg.TokenURL,
		Kid:        cfg.Kid,
		PrivateKey: cfg.PrivateKey,
		Alg:        cfg.Alg,
		Now:        cfg.Now,
	})
	if err != nil {
		return nil, err
	}
	form := url.Values{
		"client_id":             {cfg.ClientID},
		"client_assertion_type": {"urn:ietf:params:oauth:client-assertion-type:jwt-bearer"},
		"client_assertion":      {assertion},
	}
	for k, v := range extra {
		form[k] = v
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("openbanking: build /token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := httc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("openbanking: /token request: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("openbanking: read /token response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &TokenError{StatusCode: resp.StatusCode, Body: body}
	}
	var raw struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		IDToken      string `json:"id_token"`
		TokenType    string `json:"token_type"`
		Scope        string `json:"scope"`
		ExpiresIn    int    `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("openbanking: decode /token response: %w", err)
	}
	if raw.AccessToken == "" {
		return nil, fmt.Errorf("openbanking: /token returned no access_token; body=%s", body)
	}
	if raw.ExpiresIn <= 0 {
		raw.ExpiresIn = 300
	}
	now := time.Now().UTC()
	if cfg.Now != nil {
		now = cfg.Now()
	}
	return &AuthCodeToken{
		AccessToken:  raw.AccessToken,
		RefreshToken: raw.RefreshToken,
		IDToken:      raw.IDToken,
		TokenType:    raw.TokenType,
		Scope:        raw.Scope,
		ExpiresAt:    now.Add(time.Duration(raw.ExpiresIn-int(cfg.EarlyRefresh.Seconds())) * time.Second),
	}, nil
}

func authCodeHTTPClient(cfg AuthCodeConfig) (*http.Client, error) {
	if cfg.ClientID == "" || cfg.TokenURL == "" || cfg.Kid == "" || cfg.PrivateKey == nil {
		return nil, errors.New("openbanking: AuthCodeConfig needs ClientID/TokenURL/Kid/PrivateKey")
	}
	if cfg.HTTPClient != nil {
		return cfg.HTTPClient, nil
	}
	if len(cfg.TransportCert.Certificate) == 0 {
		return nil, errors.New("openbanking: AuthCodeConfig needs TransportCert (or HTTPClient)")
	}
	return MTLSHTTPClient(cfg.TransportCert, nil), nil
}

var _ core.Authenticator = (*AuthCodeTokenSource)(nil)
