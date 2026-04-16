package revolut

import (
	"errors"

	"github.com/greatliontech/revolut-go/internal/transport"
	"github.com/greatliontech/revolut-go/merchant"
)

const (
	merchantProductionURL = "https://merchant.revolut.com/"
	merchantSandboxURL    = "https://sandbox-merchant.revolut.com/"
)

// NewMerchantClient builds a Revolut Merchant API client. The Merchant
// API authenticates every request with a static secret issued from the
// Revolut Business dashboard. Wrap it in an [Authenticator] such as
// [AuthenticatorFunc] that calls
// req.Header.Set("Authorization", "Bearer "+secret). No consent flow,
// no refresh token — the generated cmd/auth-bootstrap tool is not
// needed for Merchant.
func NewMerchantClient(auth Authenticator, opts ...Option) (*merchant.Client, error) {
	if auth == nil {
		return nil, errors.New("revolut: NewMerchantClient: auth is required")
	}
	o := resolveOptions(opts)
	baseURL := o.baseURL
	if baseURL == "" {
		baseURL = merchantBaseURL(o.env)
	}
	t, err := transport.New(transport.Config{
		BaseURL:     baseURL,
		HTTPClient:  o.httpClient,
		Auth:        auth,
		UserAgent:   o.userAgent,
		HostAliases: sandboxAliases(o, merchant.SandboxHostAliases),
		RetryPolicy: o.retry,
	})
	if err != nil {
		return nil, err
	}
	return merchant.New(t), nil
}

func merchantBaseURL(env Environment) string {
	if env == EnvironmentProduction {
		return merchantProductionURL
	}
	return merchantSandboxURL
}
