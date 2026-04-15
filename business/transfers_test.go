package business

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestTransfers_Create(t *testing.T) {
	t.Parallel()
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/transfer" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("content-type: %s", ct)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if body["request_id"] != "req-1" || body["source_account_id"] != "src" || body["target_account_id"] != "dst" {
			t.Errorf("bad body: %+v", body)
		}
		if n, ok := body["amount"].(float64); !ok || n != 10.5 {
			t.Errorf("amount not emitted as number: %v (%T)", body["amount"], body["amount"])
		}
		if body["currency"] != "GBP" {
			t.Errorf("currency: %v", body["currency"])
		}
		_, _ = io.WriteString(w, `{"id":"tx1","state":"completed","created_at":"2026-04-15T10:00:00Z","completed_at":"2026-04-15T10:00:01Z"}`)
	}))

	got, err := client.Transfers.Create(context.Background(), TransferRequest{
		RequestID:       "req-1",
		SourceAccountID: "src",
		TargetAccountID: "dst",
		Amount:          "10.5",
		Currency:        "GBP",
		Reference:       "test",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if got.ID != "tx1" || got.State != TransactionStateCompleted {
		t.Errorf("got: %+v", got)
	}
	if got.CompletedAt == nil {
		t.Errorf("expected completed_at to be set")
	}
}

func TestTransfers_Create_Validation(t *testing.T) {
	t.Parallel()
	client := New(nil)
	cases := []struct {
		name string
		req  TransferRequest
	}{
		{"no request id", TransferRequest{SourceAccountID: "s", TargetAccountID: "t", Amount: "1", Currency: "GBP"}},
		{"no source", TransferRequest{RequestID: "r", TargetAccountID: "t", Amount: "1", Currency: "GBP"}},
		{"no target", TransferRequest{RequestID: "r", SourceAccountID: "s", Amount: "1", Currency: "GBP"}},
		{"no amount", TransferRequest{RequestID: "r", SourceAccountID: "s", TargetAccountID: "t", Currency: "GBP"}},
		{"no currency", TransferRequest{RequestID: "r", SourceAccountID: "s", TargetAccountID: "t", Amount: "1"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if _, err := client.Transfers.Create(context.Background(), tc.req); err == nil {
				t.Fatal("want error, got nil")
			}
		})
	}
}

func TestTransfers_Pay(t *testing.T) {
	t.Parallel()
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/pay" {
			t.Errorf("path: %s", r.URL.Path)
		}
		raw, _ := io.ReadAll(r.Body)
		s := string(raw)
		// charge_bearer should be emitted since it's non-zero.
		if !strings.Contains(s, `"charge_bearer":"shared"`) {
			t.Errorf("missing charge_bearer: %s", s)
		}
		// currency omitted when empty (omitempty tag).
		if strings.Contains(s, `"currency"`) {
			t.Errorf("currency should be omitted: %s", s)
		}
		// receiver must be a nested object carrying both ids.
		if !strings.Contains(s, `"counterparty_id":"cp-1"`) || !strings.Contains(s, `"account_id":"acct-1"`) {
			t.Errorf("receiver shape: %s", s)
		}
		_, _ = io.WriteString(w, `{"id":"tx2","state":"pending","created_at":"2026-04-15T10:00:00Z"}`)
	}))

	got, err := client.Transfers.Pay(context.Background(), TransactionPaymentRequest{
		RequestID:    "req-2",
		AccountID:    "src",
		Receiver:     PaymentReceiver{CounterpartyID: "cp-1", AccountID: "acct-1"},
		Amount:       "25",
		ChargeBearer: ChargeBearerShared,
	})
	if err != nil {
		t.Fatalf("Pay: %v", err)
	}
	if got.ID != "tx2" || got.State != TransactionStatePending {
		t.Errorf("got: %+v", got)
	}
	if got.CompletedAt != nil {
		t.Errorf("expected completed_at nil, got %v", got.CompletedAt)
	}
}

func TestTransfers_Pay_Validation(t *testing.T) {
	t.Parallel()
	client := New(nil)
	cases := []struct {
		name string
		req  TransactionPaymentRequest
	}{
		{"no request id", TransactionPaymentRequest{AccountID: "a", Receiver: PaymentReceiver{CounterpartyID: "c"}, Amount: "1"}},
		{"no account", TransactionPaymentRequest{RequestID: "r", Receiver: PaymentReceiver{CounterpartyID: "c"}, Amount: "1"}},
		{"no amount", TransactionPaymentRequest{RequestID: "r", AccountID: "a", Receiver: PaymentReceiver{CounterpartyID: "c"}}},
		// Nested required fields (Receiver.CounterpartyID) aren't
		// currently validated by the generator — Revolut rejects
		// them with a 400 instead.
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if _, err := client.Transfers.Pay(context.Background(), tc.req); err == nil {
				t.Fatal("want error, got nil")
			}
		})
	}
}

func TestTransferRequest_JSONRoundtrip(t *testing.T) {
	t.Parallel()
	in := TransferRequest{
		RequestID:       "r",
		SourceAccountID: "s",
		TargetAccountID: "t",
		Amount:          "100.00",
		Currency:        "EUR",
		Reference:       "ref",
	}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	// amount should be encoded as a JSON number, not a string.
	if strings.Contains(string(b), `"amount":"100.00"`) {
		t.Errorf("amount should be a JSON number: %s", b)
	}
	if !strings.Contains(string(b), `"amount":100.00`) {
		t.Errorf("amount not emitted as number: %s", b)
	}
	var out TransferRequest
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out != in {
		t.Errorf("roundtrip mismatch: in=%+v out=%+v", in, out)
	}
}
