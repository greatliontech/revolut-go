package business

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/greatliontech/revolut-go/internal/core"
)

// TestPathParamEscaping_GeneratedSource verifies the generator
// wraps path params with url.PathEscape in the emitted method
// body. Static check — we can't exercise it via a real request
// because Revolut's path params are uniformly `format: uuid`,
// which the pre-flight validator rejects for any string that
// would actually need escaping. The escape logic still matters
// for future specs with non-UUID path params.
func TestPathParamEscaping_GeneratedSource(t *testing.T) {
	t.Parallel()
	src, err := os.ReadFile("gen_accounts.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(src), `url.PathEscape(accountID)`) {
		t.Errorf("gen_accounts.go missing url.PathEscape(accountID) call")
	}
}

func TestPathParamValidation_Empty(t *testing.T) {
	t.Parallel()
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("server should not be reached on empty path param")
	}))
	_, err := client.Accounts.Get(context.Background(), "")
	if err == nil || !strings.Contains(err.Error(), "account_id is required") {
		t.Errorf("want business: account_id is required; got %v", err)
	}
}

func TestRequiredFieldValidation_Nested(t *testing.T) {
	t.Parallel()
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("server should not be reached when validation fails")
	}))
	// ExchangeRequest requires .from.{account_id, currency} — exercise
	// the nested-struct recursion path in walkRequired.
	_, err := client.ForeignExchange.ExchangeMoney(context.Background(), ExchangeRequest{
		RequestID: "r",
		From:      ExchangePartFrom{Currency: "GBP"}, // missing account_id
		To:        ExchangePartTo{AccountID: "t", Currency: "EUR"},
	})
	if err == nil || !strings.Contains(err.Error(), "from.account_id is required") {
		t.Errorf("want nested-path error; got %v", err)
	}
}

func TestTransactionsListAll_PaginatesAndDedupes(t *testing.T) {
	t.Parallel()
	// Build a deterministic two-page response with a tie on the final
	// timestamp to catch cursor-precision regressions.
	tsA := "2026-04-01T10:00:00.111111Z"
	tsB := "2026-04-01T10:00:00.222222Z"
	tsC := "2026-04-01T10:00:00.333333Z"
	pageOne := fmt.Sprintf(`[
		{"id":"1","type":"transfer","state":"completed","created_at":%q,"updated_at":%q},
		{"id":"2","type":"transfer","state":"completed","created_at":%q,"updated_at":%q}
	]`, tsA, tsA, tsB, tsB)
	pageTwo := fmt.Sprintf(`[
		{"id":"3","type":"transfer","state":"completed","created_at":%q,"updated_at":%q}
	]`, tsC, tsC)
	pageThree := `[]`

	var calls atomic.Int32
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		switch n {
		case 1:
			if r.URL.Query().Get("to") != "" {
				t.Errorf("first call should have no 'to': %s", r.URL.RawQuery)
			}
			_, _ = io.WriteString(w, pageOne)
		case 2:
			if r.URL.Query().Get("to") != tsB {
				t.Errorf("second call 'to' = %s; want %s", r.URL.Query().Get("to"), tsB)
			}
			_, _ = io.WriteString(w, pageTwo)
		case 3:
			if r.URL.Query().Get("to") != tsC {
				t.Errorf("third call 'to' = %s; want %s", r.URL.Query().Get("to"), tsC)
			}
			_, _ = io.WriteString(w, pageThree)
		default:
			t.Errorf("too many calls: %d", n)
			_, _ = io.WriteString(w, `[]`)
		}
	}))

	seen := map[string]bool{}
	for tx, err := range client.Transactions.ListAll(context.Background(), &GetTransactionsParams{Count: 2}) {
		if err != nil {
			t.Fatalf("ListAll: %v", err)
		}
		if seen[tx.ID] {
			t.Fatalf("duplicate: %s", tx.ID)
		}
		seen[tx.ID] = true
	}
	if len(seen) != 3 {
		t.Errorf("saw %d items; want 3", len(seen))
	}
}

func TestListAll_BreakEarly(t *testing.T) {
	t.Parallel()
	page := `[
		{"id":"1","type":"transfer","state":"completed","created_at":"2026-04-01T10:00:00Z","updated_at":"2026-04-01T10:00:00Z"},
		{"id":"2","type":"transfer","state":"completed","created_at":"2026-04-01T10:00:01Z","updated_at":"2026-04-01T10:00:01Z"}
	]`
	var calls atomic.Int32
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, page)
	}))

	for _, err := range client.Transactions.ListAll(context.Background(), nil) {
		if err != nil {
			t.Fatalf("ListAll: %v", err)
		}
		break
	}
	if calls.Load() != 1 {
		t.Errorf("break didn't halt iteration: %d calls", calls.Load())
	}
}

func TestAPIError_Propagation(t *testing.T) {
	t.Parallel()
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `{"code":403,"message":"forbidden"}`)
	}))
	_, err := client.Accounts.List(context.Background())
	apiErr, ok := core.AsAPIError(err)
	if !ok {
		t.Fatalf("want APIError, got %T: %v", err, err)
	}
	if apiErr.StatusCode != 403 || apiErr.Message != "forbidden" {
		t.Errorf("unexpected APIError: %+v", apiErr)
	}
}

func TestUnionRequestMarshal(t *testing.T) {
	t.Parallel()
	var gotBody map[string]any
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		// Return a minimal UK variant shape; the probe decoder picks
		// the first variant (alphabetical) when required fields
		// overlap — it's enough that decoding succeeds.
		_, _ = io.WriteString(w, `{"result_code":"matched"}`)
	}))
	_, err := client.Counterparties.ValidateAccountName(context.Background(),
		ValidateAccountNameRequest{
			AccountNo:   "1234",
			SortCode:    "11-22-33",
			CompanyName: "Widgets Ltd",
		})
	if err != nil {
		t.Fatalf("AccountNameValidation: %v", err)
	}
	if gotBody["sort_code"] != "11-22-33" || gotBody["account_no"] != "1234" {
		t.Errorf("UK variant fields missing on wire: %+v", gotBody)
	}
}

func TestTransactionsList_CountQueryParam(t *testing.T) {
	t.Parallel()
	var gotQuery url.Values
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `[]`)
	}))
	if _, err := client.Transactions.List(context.Background(), &GetTransactionsParams{Count: 7}); err != nil {
		t.Fatalf("List: %v", err)
	}
	if gotQuery.Get("count") != "7" {
		t.Errorf("count query = %q; want 7", gotQuery.Get("count"))
	}
}

func TestListWithNilOpts(t *testing.T) {
	t.Parallel()
	var gotRawQuery string
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotRawQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `[]`)
	}))
	if _, err := client.Transactions.List(context.Background(), nil); err != nil {
		t.Fatalf("List(nil): %v", err)
	}
	if gotRawQuery != "" {
		t.Errorf("nil opts emitted query %q", gotRawQuery)
	}
}

// TestAPIError_Is validates errors.Is unwrap semantics against the
// typed APIError that the transport surfaces.
func TestAPIError_Is(t *testing.T) {
	t.Parallel()
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"code":404,"message":"nope"}`)
	}))
	_, err := client.Accounts.Get(context.Background(), "99999999-9999-4999-9999-999999999999")
	var apiErr *core.APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("errors.As did not unwrap APIError: %v", err)
	}
	if apiErr.StatusCode != 404 {
		t.Errorf("status: %d", apiErr.StatusCode)
	}
}
