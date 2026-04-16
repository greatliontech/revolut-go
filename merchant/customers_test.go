package merchant

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
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

// TestCustomers_Create_EmitsVersionHeader pins the generator's
// named-string-enum header path: the Revolut-Api-Version param
// is typed as a named enum (CustomersRevolutAPIVersion), not a
// raw string; the emitter casts with string(version) in the
// headers.Set call so it reaches the wire as the enum's value.
func TestCustomers_Create_EmitsVersionHeader(t *testing.T) {
	var gotVersion string
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotVersion = r.Header.Get("Revolut-Api-Version")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"cus-1","email":"test@example.com","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"}`)
	}))
	_, err := client.Customers.Create(context.Background(), RevolutAPIVersion20240901Min20251204, CustomerCreationV2{
		Email: "test@example.com",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if gotVersion != "2025-12-04" {
		t.Errorf("Revolut-Api-Version=%q; want 2025-12-04", gotVersion)
	}
}

// TestCustomers_Get_UUIDValidation pins the UUID pre-flight
// on customer_id. The handler must not be reached for a
// malformed id.
func TestCustomers_Get_UUIDValidation(t *testing.T) {
	var hits int
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{}`)
	}))
	_, err := client.Customers.Get(context.Background(), "typo", RevolutAPIVersion20240901Min20251204)
	if err == nil || !strings.Contains(err.Error(), "valid UUID") {
		t.Errorf("want format error, got %v", err)
	}
	if hits != 0 {
		t.Error("request fired despite malformed UUID")
	}
}

// TestCustomers_List_QueryParamsEncoding: validates that a
// populated Params struct is serialised into the query string.
func TestCustomers_List_QueryParamsEncoding(t *testing.T) {
	var gotQuery string
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"customers":[]}`)
	}))
	_, err := client.Customers.GetList(context.Background(), RevolutAPIVersion20240901Min20251204, &RetrieveCustomerListParams{
		Limit: 25,
	})
	if err != nil {
		t.Fatalf("GetList: %v", err)
	}
	if !strings.Contains(gotQuery, "limit=25") {
		t.Errorf("query missing limit=25; got %q", gotQuery)
	}
}

// TestOrders_GetList_EmptyResponseDecodes: the wrapper-struct
// shape must decode even when the underlying array is empty.
func TestOrders_GetList_EmptyResponseDecodes(t *testing.T) {
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"orders":[]}`)
	}))
	resp, err := client.Orders.GetList(context.Background(), RevolutAPIVersion20251204, nil)
	if err != nil {
		t.Fatalf("GetList: %v", err)
	}
	if resp.Orders == nil {
		t.Error("want empty slice, got nil")
	}
}

// TestWebhooks_Delete_204: a successful Delete returns no body
// (204 No Content). The transport must not error on the empty
// body.
func TestWebhooks_Delete_204(t *testing.T) {
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	err := client.Webhooks.Delete(context.Background(), "11111111-1111-1111-1111-111111111111", RevolutAPIVersion20240901Min20251204)
	if err != nil {
		t.Errorf("Delete on 204: %v", err)
	}
}
