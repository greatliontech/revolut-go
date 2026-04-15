package business

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"time"

	"github.com/greatliontech/revolut-go/internal/core"
	"github.com/greatliontech/revolut-go/internal/transport"
)

// AccountState reports whether the account is active or inactive.
type AccountState string

const (
	AccountStateActive   AccountState = "active"
	AccountStateInactive AccountState = "inactive"
)

// Account is a Revolut Business account.
//
// Balance is stored as a [json.Number] to preserve the decimal
// representation exactly as Revolut returns it. Use [Account.BalanceFloat]
// for an approximate float64.
type Account struct {
	ID        string        `json:"id"`
	Name      string        `json:"name,omitempty"`
	Balance   json.Number   `json:"balance"`
	Currency  core.Currency `json:"currency"`
	State     AccountState  `json:"state"`
	Public    bool          `json:"public"`
	CreatedAt time.Time     `json:"created_at"`
	UpdatedAt time.Time     `json:"updated_at"`
}

// BalanceFloat returns Balance parsed as a float64. The bool is false if
// the balance is unset or not numeric.
func (a Account) BalanceFloat() (float64, bool) {
	if a.Balance == "" {
		return 0, false
	}
	v, err := a.Balance.Float64()
	if err != nil {
		return 0, false
	}
	return v, true
}

// Accounts groups the /accounts endpoints.
type Accounts struct {
	t *transport.Transport
}

// List retrieves every account visible to the authenticated business.
//
// Docs: https://developer.revolut.com/docs/business/get-accounts
func (a *Accounts) List(ctx context.Context) ([]Account, error) {
	var out []Account
	if err := a.t.Do(ctx, http.MethodGet, "accounts", nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// Get retrieves a single account by its UUID.
//
// Docs: https://developer.revolut.com/docs/business/get-account
func (a *Accounts) Get(ctx context.Context, id string) (*Account, error) {
	if id == "" {
		return nil, errors.New("business: account id is required")
	}
	var out Account
	if err := a.t.Do(ctx, http.MethodGet, "accounts/"+url.PathEscape(id), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
