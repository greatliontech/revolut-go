package revolut

import (
	"errors"

	"github.com/greatliontech/revolut-go/internal/transport"
	"github.com/greatliontech/revolut-go/revolutx"
)

const (
	revolutXProductionURL = "https://revx.revolut.com/api/1.0/"
	// Revolut X's "sandbox" is actually the .codes dev host — it
	// does not follow the usual `sandbox-<prod>` naming pattern so
	// automatic host-alias rewriting in
	// [github.com/greatliontech/revolut-go/revolutx.SandboxHostAliases]
	// won't catch it. The base-URL is swapped here directly when
	// WithEnvironment(EnvironmentSandbox) is selected.
	revolutXSandboxURL = "https://revx.revolut.codes/api/1.0/"
)

// NewRevolutXClient builds a Revolut X API client. Auth uses two
// custom headers (X-Revx-Timestamp + X-Revx-Signature) that the
// caller computes per-request from their API key and HMAC secret —
// wrap the signing logic in an [AuthenticatorFunc] that sets both
// headers before the transport issues the request. The generated
// methods surface the signature headers as positional parameters,
// so callers can alternatively pass them through per call.
func NewRevolutXClient(auth Authenticator, opts ...Option) (*revolutx.Client, error) {
	if auth == nil {
		return nil, errors.New("revolut: NewRevolutXClient: auth is required")
	}
	o := resolveOptions(opts)
	baseURL := o.baseURL
	if baseURL == "" {
		baseURL = revolutXBaseURL(o.env)
	}
	t, err := transport.New(transport.Config{
		BaseURL:     baseURL,
		HTTPClient:  o.httpClient,
		Auth:        auth,
		UserAgent:   o.userAgent,
		HostAliases: sandboxAliases(o, revolutx.SandboxHostAliases),
	})
	if err != nil {
		return nil, err
	}
	return revolutx.New(t), nil
}

func revolutXBaseURL(env Environment) string {
	if env == EnvironmentProduction {
		return revolutXProductionURL
	}
	return revolutXSandboxURL
}
