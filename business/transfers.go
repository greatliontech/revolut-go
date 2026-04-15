package business

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/greatliontech/revolut-go/internal/core"
	"github.com/greatliontech/revolut-go/internal/transport"
)

// TransactionState reports where a transaction sits in Revolut's
// lifecycle. See https://developer.revolut.com/docs/business/business-api.
type TransactionState string

const (
	TransactionStateCreated   TransactionState = "created"
	TransactionStatePending   TransactionState = "pending"
	TransactionStateCompleted TransactionState = "completed"
	TransactionStateDeclined  TransactionState = "declined"
	TransactionStateFailed    TransactionState = "failed"
	TransactionStateReverted  TransactionState = "reverted"
)

// ChargeBearer selects who pays network fees on a transfer. See Revolut's
// guide on charge bearers for details.
type ChargeBearer string

const (
	// ChargeBearerShared ("SHA") splits fees between sender and recipient.
	ChargeBearerShared ChargeBearer = "shared"
	// ChargeBearerDebtor ("OUR") makes the sender pay all explicit fees.
	ChargeBearerDebtor ChargeBearer = "debtor"
)

// TransferResponse is returned by both /transfer and /pay. CompletedAt is
// nil for transfers that have not yet settled.
type TransferResponse struct {
	ID          string           `json:"id"`
	State       TransactionState `json:"state"`
	CreatedAt   time.Time        `json:"created_at"`
	CompletedAt *time.Time       `json:"completed_at,omitempty"`
}

// TransferRequest moves funds between two accounts of the same business
// in the same currency.
//
// Amount is a [json.Number] so callers can pass "10", "10.50", or any
// valid decimal string without losing precision. The JSON encoder emits
// it as a JSON number, matching Revolut's schema.
type TransferRequest struct {
	// RequestID is a caller-chosen idempotency key (<=40 chars). Reusing
	// the same RequestID within ~2 weeks replays the original result
	// rather than creating a new transfer.
	RequestID       string        `json:"request_id"`
	SourceAccountID string        `json:"source_account_id"`
	TargetAccountID string        `json:"target_account_id"`
	Amount          json.Number   `json:"amount"`
	Currency        core.Currency `json:"currency"`
	Reference       string        `json:"reference,omitempty"`
}

// PaymentReceiver identifies the recipient of a /pay call. When the
// counterparty has multiple payment methods attached (e.g. a bank
// account and a card), set AccountID or CardID to disambiguate.
type PaymentReceiver struct {
	CounterpartyID string `json:"counterparty_id"`
	AccountID      string `json:"account_id,omitempty"`
	CardID         string `json:"card_id,omitempty"`
}

// PaymentRequest sends funds to a counterparty via bank transfer or card.
// Per Revolut's schema, Currency is optional — if omitted, Revolut uses
// the source account's currency.
type PaymentRequest struct {
	RequestID          string          `json:"request_id"`
	AccountID          string          `json:"account_id"`
	Receiver           PaymentReceiver `json:"receiver"`
	Amount             json.Number     `json:"amount"`
	Currency           core.Currency   `json:"currency,omitempty"`
	Reference          string          `json:"reference,omitempty"`
	ChargeBearer       ChargeBearer    `json:"charge_bearer,omitempty"`
	TransferReasonCode string          `json:"transfer_reason_code,omitempty"`
	ExchangeReasonCode string          `json:"exchange_reason_code,omitempty"`
}

// Transfers groups Revolut's money-movement endpoints (/transfer, /pay).
type Transfers struct {
	t *transport.Transport
}

// Create moves funds between two accounts of the same business in the
// same currency (POST /transfer). Returns the resulting transaction.
//
// Docs: https://developer.revolut.com/docs/business/create-transfer
func (s *Transfers) Create(ctx context.Context, req TransferRequest) (*TransferResponse, error) {
	if err := validateTransferRequest(req); err != nil {
		return nil, err
	}
	var out TransferResponse
	if err := s.t.Do(ctx, http.MethodPost, "transfer", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Pay sends funds to a counterparty (POST /pay). Use this for bank
// transfers or card transfers to external recipients.
//
// Docs: https://developer.revolut.com/docs/business/create-payment
func (s *Transfers) Pay(ctx context.Context, req PaymentRequest) (*TransferResponse, error) {
	if err := validatePaymentRequest(req); err != nil {
		return nil, err
	}
	var out TransferResponse
	if err := s.t.Do(ctx, http.MethodPost, "pay", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func validateTransferRequest(req TransferRequest) error {
	switch {
	case req.RequestID == "":
		return errors.New("business: TransferRequest.RequestID is required")
	case req.SourceAccountID == "":
		return errors.New("business: TransferRequest.SourceAccountID is required")
	case req.TargetAccountID == "":
		return errors.New("business: TransferRequest.TargetAccountID is required")
	case req.Amount == "":
		return errors.New("business: TransferRequest.Amount is required")
	case req.Currency == "":
		return errors.New("business: TransferRequest.Currency is required")
	}
	return nil
}

func validatePaymentRequest(req PaymentRequest) error {
	switch {
	case req.RequestID == "":
		return errors.New("business: PaymentRequest.RequestID is required")
	case req.AccountID == "":
		return errors.New("business: PaymentRequest.AccountID is required")
	case req.Receiver.CounterpartyID == "":
		return errors.New("business: PaymentRequest.Receiver.CounterpartyID is required")
	case req.Amount == "":
		return errors.New("business: PaymentRequest.Amount is required")
	}
	return nil
}
