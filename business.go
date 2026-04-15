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

// NewBusinessClient builds a Revolut Business API client. An
// [Authenticator] is required — the Business API uses an OAuth flow with a
// JWT client assertion; helpers to construct one will be added under the
// auth package.
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
