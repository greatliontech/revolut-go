package revolut

import (
	"errors"

	"github.com/greatliontech/revolut-go/cryptoramp"
	"github.com/greatliontech/revolut-go/internal/transport"
)

// The Crypto Ramp spec declares a single production server and no
// sandbox counterpart — [WithEnvironment] has no effect on the base
// URL. Use [WithBaseURL] when a staging host is needed.
const cryptoRampProductionURL = "https://ramp-partners.revolut.com/partners/api/2.0/"

// NewCryptoRampClient builds a Revolut Crypto Ramp API client. Auth
// uses a static partner API key — wrap it in an [AuthenticatorFunc]
// that sets the Authorization header on each request.
//
// The spec exposes only a production host; passing
// WithEnvironment(EnvironmentSandbox) is accepted but resolves to
// the same production URL. Override explicitly with [WithBaseURL] if
// Revolut issues a staging host for your partner account.
func NewCryptoRampClient(auth Authenticator, opts ...Option) (*cryptoramp.Client, error) {
	if auth == nil {
		return nil, errors.New("revolut: NewCryptoRampClient: auth is required")
	}
	o := resolveOptions(opts)
	baseURL := o.baseURL
	if baseURL == "" {
		baseURL = cryptoRampProductionURL
	}
	t, err := transport.New(transport.Config{
		BaseURL:     baseURL,
		HTTPClient:  o.httpClient,
		Auth:        auth,
		UserAgent:   o.userAgent,
		HostAliases: sandboxAliases(o, cryptoramp.SandboxHostAliases),
		RetryPolicy: o.retry,
	})
	if err != nil {
		return nil, err
	}
	return cryptoramp.New(t), nil
}
