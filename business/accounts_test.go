package business

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/greatliontech/revolut-go/internal/transport"
)

func newTestClient(t *testing.T, h http.Handler) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	tr, err := transport.New(transport.Config{BaseURL: srv.URL + "/"})
	if err != nil {
		t.Fatalf("transport.New: %v", err)
	}
	return New(tr)
}

func TestAccounts_List(t *testing.T) {
	t.Parallel()
	body := `[
		{"id":"11111111-1111-1111-1111-111111111111","name":"GBP","balance":3171.89,"currency":"GBP","state":"active","public":false,"created_at":"2026-01-01T00:00:00Z","updated_at":"2026-02-01T00:00:00Z"},
		{"id":"22222222-2222-2222-2222-222222222222","name":"EUR","balance":411561.89,"currency":"EUR","state":"inactive","public":true,"created_at":"2026-01-02T00:00:00Z","updated_at":"2026-02-02T00:00:00Z"}
	]`
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/accounts" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, body)
	}))

	got, err := client.Accounts.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 accounts, got %d", len(got))
	}
	if got[0].Currency != "GBP" || got[0].State != AccountStateActive {
		t.Errorf("acct[0]: %+v", got[0])
	}
	if bal, ok := got[0].BalanceFloat(); !ok || bal != 3171.89 {
		t.Errorf("acct[0] balance: %v %v", bal, ok)
	}
	if got[1].State != AccountStateInactive || !got[1].Public {
		t.Errorf("acct[1]: %+v", got[1])
	}
}

func TestAccounts_Get(t *testing.T) {
	t.Parallel()
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/accounts/abc123" {
			t.Errorf("path: %s", r.URL.Path)
		}
		_, _ = io.WriteString(w, `{"id":"abc123","balance":1,"currency":"USD","state":"active","public":false,"created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"}`)
	}))

	got, err := client.Accounts.Get(context.Background(), "abc123")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.ID != "abc123" || got.Currency != "USD" {
		t.Errorf("got: %+v", got)
	}
}

func TestAccounts_Get_EmptyID(t *testing.T) {
	t.Parallel()
	client := New(nil)
	if _, err := client.Accounts.Get(context.Background(), ""); err == nil {
		t.Fatal("want error for empty id")
	}
}
