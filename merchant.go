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

// NewMerchantClient builds a Revolut Merchant API client. The Merchant API
// authenticates every request with a static API-key secret — pass an
// [Authenticator] that sets the Authorization header accordingly.
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
		BaseURL:    baseURL,
		HTTPClient: o.httpClient,
		Auth:       auth,
		UserAgent:  o.userAgent,
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
