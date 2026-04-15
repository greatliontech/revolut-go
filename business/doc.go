// Package business is the Revolut Business API client.
//
// Most users should not construct a Client directly — use
// revolut.NewBusinessClient. The [New] function here exists so the root
// package can wire it up without importing revolut (which would create a
// cycle with the shared types in internal/core).
//
// The Client struct and its resource fields (Accounts, Transfers, ...)
// are generated from specs/business.yaml by cmd/revogen. Run
// `task gen` after bumping the spec.
package business
