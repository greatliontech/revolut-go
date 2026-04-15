// Package core holds the canonical shared types used across every Revolut
// API client. Users should import them via the root revolut package, which
// re-exports them as type aliases (revolut.Money, revolut.Authenticator,
// ...). This package exists purely to break the import cycle between the
// root package and the per-API sub-packages (business, merchant, ...).
package core

import "net/http"

// Environment selects between Revolut's sandbox and production hosts.
type Environment int

const (
	// EnvironmentSandbox targets Revolut's sandbox APIs.
	EnvironmentSandbox Environment = iota
	// EnvironmentProduction targets Revolut's production APIs.
	EnvironmentProduction
)

// Authenticator mutates an outgoing HTTP request to satisfy a Revolut API's
// authentication scheme. Implementations include the JWT-based flow used by
// the Business API and the API-key flow used by the Merchant API.
//
// Apply is called for every request and must be safe for concurrent use.
type Authenticator interface {
	Apply(*http.Request) error
}

// AuthenticatorFunc adapts a plain function to the Authenticator interface.
type AuthenticatorFunc func(*http.Request) error

// Apply implements [Authenticator].
func (f AuthenticatorFunc) Apply(r *http.Request) error { return f(r) }
