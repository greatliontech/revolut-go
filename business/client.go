// Package business is the Revolut Business API client.
//
// Most users should not construct a Client directly — use
// revolut.NewBusinessClient. The [New] function here exists so the root
// package can wire it up without importing revolut (which would create a
// cycle with the shared types in internal/core).
package business

import "github.com/greatliontech/revolut-go/internal/transport"

// Client is the Revolut Business API client.
//
// Resource fields (Accounts, Transfers, ...) group endpoints by their
// Revolut API path prefix. Access them as, e.g., client.Accounts.List(ctx).
type Client struct {
	transport *transport.Transport

	// Accounts groups the /accounts endpoints.
	Accounts *Accounts
}

// New wraps an HTTP transport in a Business client. Callers configure the
// transport (base URL, authenticator, HTTP client) at the root revolut
// package level.
func New(t *transport.Transport) *Client {
	c := &Client{transport: t}
	c.Accounts = &Accounts{t: t}
	return c
}
