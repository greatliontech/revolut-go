package jwt

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// refreshSkew shortens the cached token's effective lifetime so we refresh
// slightly before the real expiry.
const refreshSkew = 60 * time.Second

// Source is a long-lived credential holder that produces bearer access
// tokens by refreshing against Revolut's /auth/token endpoint.
//
// It implements the Authenticator interface consumed by the Revolut SDK
// (see the root revolut package). The zero value is not usable;
// construct via [NewSource].
//
// Source is safe for concurrent use.
type Source struct {
	signer       *Signer
	tokenURL     string
	httpc        *http.Client
	refreshToken string

	mu          sync.Mutex
	accessToken string
	expiresAt   time.Time
	now         func() time.Time
}

// SourceConfig configures a [Source].
type SourceConfig struct {
	Signer       *Signer
	TokenURL     string
	RefreshToken string
	// HTTPClient is used for token-refresh traffic. If nil,
	// http.DefaultClient is used.
	HTTPClient *http.Client
	// Now overrides the clock. Used in tests.
	Now func() time.Time
}

// NewSource validates cfg and returns a ready Source.
func NewSource(cfg SourceConfig) (*Source, error) {
	if cfg.Signer == nil {
		return nil, errors.New("jwt: Signer is required")
	}
	if cfg.TokenURL == "" {
		return nil, errors.New("jwt: TokenURL is required")
	}
	if cfg.RefreshToken == "" {
		return nil, errors.New("jwt: RefreshToken is required")
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	return &Source{
		signer:       cfg.Signer,
		tokenURL:     cfg.TokenURL,
		httpc:        cfg.HTTPClient,
		refreshToken: cfg.RefreshToken,
		now:          now,
	}, nil
}

// Token returns a non-expired access token, refreshing if needed.
func (s *Source) Token(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.accessToken != "" && s.now().Before(s.expiresAt) {
		return s.accessToken, nil
	}
	tr, err := Refresh(ctx, s.httpc, s.tokenURL, s.signer, s.refreshToken)
	if err != nil {
		return "", err
	}
	s.accessToken = tr.AccessToken
	ttl := time.Duration(tr.ExpiresIn) * time.Second
	if ttl <= refreshSkew {
		// Revolut access tokens are 40 min; zero/negative here would be
		// a malformed response. Fall back to a short TTL so we retry on
		// the next call rather than pinning forever.
		ttl = refreshSkew
	}
	s.expiresAt = s.now().Add(ttl - refreshSkew)
	return s.accessToken, nil
}

// Apply sets the Authorization header on req. It implements the
// Authenticator interface from the revolut root package.
func (s *Source) Apply(req *http.Request) error {
	tok, err := s.Token(req.Context())
	if err != nil {
		return fmt.Errorf("jwt: obtain access token: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	return nil
}
