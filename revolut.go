// Package revolut is the public entry point of the Revolut Go SDK.
//
// Each Revolut API gets its own client constructor (NewBusinessClient,
// NewMerchantClient, ...) returning a typed client from a sub-package. The
// shared HTTP transport, error type, money type, and authenticator
// abstraction live here in the root package; per-API resource types live
// alongside their client.
package revolut

import (
	"net/http"

	"github.com/greatliontech/revolut-go/internal/core"
)

// Environment selects between Revolut's sandbox and production hosts.
type Environment = core.Environment

const (
	// EnvironmentSandbox targets Revolut's sandbox APIs.
	EnvironmentSandbox = core.EnvironmentSandbox
	// EnvironmentProduction targets Revolut's production APIs.
	EnvironmentProduction = core.EnvironmentProduction
)

// Authenticator mutates an outgoing HTTP request to satisfy a Revolut
// API's authentication scheme.
type Authenticator = core.Authenticator

// AuthenticatorFunc adapts a plain function to [Authenticator].
type AuthenticatorFunc = core.AuthenticatorFunc

// Money is a decimal amount paired with a currency. See
// [github.com/greatliontech/revolut-go/internal/core.Money] for the full
// behaviour of the JSON codec.
type Money = core.Money

// Currency is an ISO 4217 currency code.
type Currency = core.Currency

// APIError is returned when a Revolut endpoint responds with a non-2xx
// status. Use [AsAPIError] to extract it from a wrapped error.
type APIError = core.APIError

// AsAPIError unwraps err into an *APIError if possible.
func AsAPIError(err error) (*APIError, bool) { return core.AsAPIError(err) }

// Ptr returns a pointer to v. It is convenient when populating required
// *bool or *int64 fields on generated request-body structs, where the
// pointer shape is used to distinguish "unset" from the zero value.
func Ptr[T any](v T) *T { return &v }

// Option configures a client constructor. Options are applied in order;
// later options override earlier ones.
type Option func(*clientOptions)

type clientOptions struct {
	env         Environment
	baseURL     string // overrides env-derived base URL when non-empty
	httpClient  *http.Client
	userAgent   string
	hostAliases map[string]string // extra caller-supplied aliases
}

// WithEnvironment selects sandbox or production. Default is sandbox.
func WithEnvironment(e Environment) Option {
	return func(o *clientOptions) { o.env = e }
}

// WithBaseURL overrides the per-API base URL. Useful for tests against a
// local mock server.
func WithBaseURL(u string) Option {
	return func(o *clientOptions) { o.baseURL = u }
}

// WithHTTPClient sets the underlying *http.Client. Default is
// [http.DefaultClient].
func WithHTTPClient(c *http.Client) Option {
	return func(o *clientOptions) { o.httpClient = c }
}

// WithUserAgent overrides the User-Agent header sent on every request.
func WithUserAgent(ua string) Option {
	return func(o *clientOptions) { o.userAgent = ua }
}

// WithHostAliases supplies additional absolute-URL host rewrites that
// apply to every request. Useful when pointing the client at a local
// mock: map apis.revolut.com → 127.0.0.1:8080 so operations whose
// spec declares a per-operation server: override still land on the
// mock. Caller-supplied aliases layer on top of the environment's
// built-in sandbox aliases.
func WithHostAliases(m map[string]string) Option {
	return func(o *clientOptions) {
		if len(m) == 0 {
			return
		}
		if o.hostAliases == nil {
			o.hostAliases = make(map[string]string, len(m))
		}
		for k, v := range m {
			o.hostAliases[k] = v
		}
	}
}

func resolveOptions(opts []Option) clientOptions {
	o := clientOptions{env: EnvironmentSandbox}
	for _, opt := range opts {
		opt(&o)
	}
	return o
}

// sandboxAliases assembles the alias map to pass to the transport.
// When sandbox is selected the package's built-in production→sandbox
// mapping is always applied — that catches per-operation server:
// overrides that embed absolute production hostnames into generated
// methods, and it fires regardless of WithBaseURL because a custom
// base URL only redirects relative-path requests. Caller-supplied
// aliases (WithHostAliases) layer on top and win on conflict so a
// local mock can still redirect the production host.
func sandboxAliases(o clientOptions, aliases map[string]string) map[string]string {
	if len(aliases) == 0 && len(o.hostAliases) == 0 {
		return nil
	}
	out := make(map[string]string, len(aliases)+len(o.hostAliases))
	if o.env == EnvironmentSandbox {
		for k, v := range aliases {
			out[k] = v
		}
	}
	for k, v := range o.hostAliases {
		out[k] = v
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
