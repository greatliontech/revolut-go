package revolut

import (
	"errors"

	"github.com/greatliontech/revolut-go/internal/transport"
	"github.com/greatliontech/revolut-go/openbanking"
)

const (
	openBankingProductionURL = "https://oba-auth.revolut.com/"
	openBankingSandboxURL    = "https://sandbox-oba-auth.revolut.com/"
)

// NewOpenBankingClient builds a Revolut Open Banking API client. Open
// Banking uses a PSD2/FAPI-compliant OAuth2 flow with MTLS and signed
// request objects; pass an [Authenticator] that attaches the access
// token to each request.
//
// Some Open Banking endpoints (under /draft-payments, for example) are
// served from a separate host that the generator hard-codes as the
// production URL. Calling those endpoints against the sandbox client
// will therefore still hit production; see
// [github.com/greatliontech/revolut-go/openbanking.DraftPayment] for
// the affected surface. Tracking fix is outside this constructor.
func NewOpenBankingClient(auth Authenticator, opts ...Option) (*openbanking.Client, error) {
	if auth == nil {
		return nil, errors.New("revolut: NewOpenBankingClient: auth is required")
	}
	o := resolveOptions(opts)
	baseURL := o.baseURL
	if baseURL == "" {
		baseURL = openBankingBaseURL(o.env)
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
	return openbanking.New(t), nil
}

func openBankingBaseURL(env Environment) string {
	if env == EnvironmentProduction {
		return openBankingProductionURL
	}
	return openBankingSandboxURL
}
