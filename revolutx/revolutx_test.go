package revolutx

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

// TestBalance_HeaderWiring pins the HMAC-signature header path:
// the generator emits X-Revx-Timestamp as a numeric header
// (strconv.Itoa) and X-Revx-Signature as a string, reflecting
// the spec's `integer` / `string` declarations. Both must reach
// the wire.
func TestBalance_HeaderWiring(t *testing.T) {
	var gotTS, gotSig string
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotTS = r.Header.Get("X-Revx-Timestamp")
		gotSig = r.Header.Get("X-Revx-Signature")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `[]`)
	}))
	_, err := client.Balance.GetAllBalances(context.Background(), 1700000000, "test-signature")
	if err != nil {
		t.Fatalf("GetAllBalances: %v", err)
	}
	if gotTS != "1700000000" {
		t.Errorf("X-Revx-Timestamp=%q; want 1700000000", gotTS)
	}
	if gotSig != "test-signature" {
		t.Errorf("X-Revx-Signature=%q", gotSig)
	}
}

// TestConfiguration_GetAllCurrencies pins the JSON decode path
// on a map-shaped response type (CurrenciesResponse is an alias
// for map[string]Currency).
func TestConfiguration_GetAllCurrencies(t *testing.T) {
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"BTC":{"currency":"BTC","asset_type":"crypto","status":"active"}}`)
	}))
	resp, err := client.Configuration.GetAllCurrencies(context.Background(), 0, "sig")
	if err != nil {
		t.Fatalf("GetAllCurrencies: %v", err)
	}
	if _, ok := (*resp)["BTC"]; !ok {
		t.Fatalf("missing BTC in response: %+v", *resp)
	}
}

// TestOrders_PlaceOrder_BodyEncoded verifies the JSON request
// body is serialised on the wire with the expected fields.
func TestOrders_PlaceOrder_BodyEncoded(t *testing.T) {
	var gotBody string
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{}`)
	}))
	_, err := client.Orders.PlaceOrder(context.Background(), 0, "sig", OrderPlacementRequest{
		ClientOrderID:      "cli-1",
		Symbol:             "BTCGBP",
		Side:               SideBuy,
		OrderConfiguration: &OrderPlacementRequestOrderConfiguration{},
	})
	if err != nil {
		t.Fatalf("PlaceOrder: %v", err)
	}
	for _, want := range []string{`"client_order_id":"cli-1"`, `"symbol":"BTCGBP"`, `"side":"buy"`} {
		if !strings.Contains(gotBody, want) {
			t.Errorf("wire body missing %q; got %s", want, gotBody)
		}
	}
}

// TestOrders_GetFills_APIError pins the error path: a 5xx
// response comes back unwrapped via revolut-go/internal/core.
func TestOrders_GetFills_APIError(t *testing.T) {
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, `{"code":500,"message":"boom"}`)
	}))
	_, err := client.Orders.GetFills(context.Background(), "11111111-1111-1111-1111-111111111111", 0, "sig")
	if err == nil {
		t.Fatal("want error")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("expected 500 in error; got %v", err)
	}
}
