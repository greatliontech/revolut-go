package revolut

import (
	"errors"

	"github.com/greatliontech/revolut-go/business"
	"github.com/greatliontech/revolut-go/internal/transport"
)

const (
	businessProductionURL = "https://b2b.revolut.com/api/1.0/"
	businessSandboxURL    = "https://sandbox-b2b.revolut.com/api/1.0/"
)

// NewBusinessClient builds a Revolut Business API client. The Business
// API uses an OAuth2 access token refreshed with a JWT client assertion
// — wire up
// [github.com/greatliontech/revolut-go/auth/jwt.NewAccessTokenSource]
// to produce an [Authenticator]. Run
// [github.com/greatliontech/revolut-go/cmd/auth-bootstrap] once per
// sandbox account to capture the initial refresh token.
func NewBusinessClient(auth Authenticator, opts ...Option) (*business.Client, error) {
	if auth == nil {
		return nil, errors.New("revolut: NewBusinessClient: auth is required")
	}
	o := resolveOptions(opts)
	baseURL := o.baseURL
	if baseURL == "" {
		baseURL = businessBaseURL(o.env)
	}
	t, err := transport.New(transport.Config{
		BaseURL:     baseURL,
		HTTPClient:  o.httpClient,
		Auth:        auth,
		UserAgent:   o.userAgent,
		HostAliases: sandboxAliases(o, business.SandboxHostAliases),
		RetryPolicy: o.retry,
	})
	if err != nil {
		return nil, err
	}
	return business.New(t), nil
}

func businessBaseURL(env Environment) string {
	if env == EnvironmentProduction {
		return businessProductionURL
	}
	return businessSandboxURL
}
