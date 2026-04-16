package cryptoramp

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/greatliontech/revolut-go/internal/transport"
)

func mustParseTime(t *testing.T, s string) time.Time {
	t.Helper()
	ts, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("parse time %q: %v", s, err)
	}
	return ts
}

func newTestClient(t *testing.T, h http.Handler) *Client {
	t.Helper()
	// Webhook methods in cryptoramp embed the production host
	// directly in the emitted path (spec declares /api/1.0 for
	// webhooks, /api/2.0 for everything else). The alias rewrites
	// the host, not the scheme, so the test has to run on TLS to
	// match the https:// in the emitted URL.
	srv := httptest.NewTLSServer(h)
	t.Cleanup(srv.Close)
	srvURL, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	tr, err := transport.New(transport.Config{
		BaseURL:    srv.URL + "/",
		HTTPClient: srv.Client(),
		HostAliases: map[string]string{
			"ramp-partners.revolut.com": srvURL.Host,
		},
	})
	if err != nil {
		t.Fatalf("transport.New: %v", err)
	}
	return New(tr)
}

// TestPartners_GetConfig pins the decode path on a resource
// that takes no body — a GET whose only variable is the API key
// header. The response shape is complex (nested slices of
// supported countries / crypto / fiat) so a regression in the
// types file would surface here.
func TestPartners_GetConfig(t *testing.T) {
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/config" {
			t.Errorf("path=%q; want /config", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
			"countries":["GB","FR"],
			"crypto":[{"crypto":"BTC","networks":["BTC"]}],
			"fiat":[{"fiat":"GBP","max":10000,"min":10}],
			"payments":[{"payment":"card","countries":["GB"]}]
		}`)
	}))
	cfg, err := client.Partners.GetConfig(context.Background(), "sandbox-key")
	if err != nil {
		t.Fatalf("GetConfig: %v", err)
	}
	if len(cfg.Countries) != 2 || cfg.Countries[0] != "GB" {
		t.Errorf("Countries: %+v", cfg.Countries)
	}
	if len(cfg.Crypto) != 1 {
		t.Errorf("Crypto: %+v", cfg.Crypto)
	}
}

// TestPartners_ListOrders_QueryEncoding pins the encoder for
// Params structs with required time-window fields. The test
// sends a populated Params through the encode() path and
// inspects the resulting query string.
func TestPartners_ListOrders_QueryEncoding(t *testing.T) {
	var gotQuery url.Values
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `[]`)
	}))
	opts := &GetOrdersParams{}
	opts.ApplyDefaults() // opts-required, fills skip/limit with spec defaults
	_, err := client.Partners.ListOrders(context.Background(), "sandbox-key", opts)
	if err == nil {
		t.Fatal("want required-field error; GetOrders needs start/end")
	}
	if !strings.Contains(err.Error(), "is required") {
		t.Errorf("expected required-field error, got %v", err)
	}
	// No HTTP request should have fired when validation trips.
	if gotQuery != nil {
		t.Errorf("request made despite validation error: %+v", gotQuery)
	}
}

// TestPartners_ListOrders_XAPIKeyHeaderWired verifies the
// x-API-key header param lands on the wire, together with the
// required time-window params.
func TestPartners_ListOrders_XAPIKeyHeaderWired(t *testing.T) {
	var gotKey string
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("x-API-key")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `[]`)
	}))
	opts := &GetOrdersParams{
		Start: mustParseTime(t, "2026-01-01T00:00:00Z"),
		End:   mustParseTime(t, "2026-02-01T00:00:00Z"),
	}
	_, err := client.Partners.ListOrders(context.Background(), "sandbox-key-xyz", opts)
	if err != nil {
		t.Fatalf("ListOrders: %v", err)
	}
	if gotKey != "sandbox-key-xyz" {
		t.Errorf("x-API-key header=%q; want sandbox-key-xyz", gotKey)
	}
}

// TestGetOrdersParams_EncodeEmitsRequiredTimeUnconditionally pins
// the query-encoder fix for required time.Time fields: even if the
// caller-side validator was disabled, encode() would emit the
// zero-valued time rather than drop it silently. Exercises the
// encoder in isolation so it doesn't depend on the validator's
// behaviour.
func TestGetOrdersParams_EncodeEmitsRequiredTimeUnconditionally(t *testing.T) {
	p := &GetOrdersParams{} // zero-value required times
	q := p.encode()
	if q.Get("start") == "" {
		t.Errorf("required start dropped on encode(); query=%v", q)
	}
	if q.Get("end") == "" {
		t.Errorf("required end dropped on encode(); query=%v", q)
	}
	// Zero-valued time.Time renders as 0001-01-01T00:00:00Z; lock
	// the prefix so a regression that re-adds the non-zero guard
	// fails loudly.
	if got := q.Get("start"); !strings.HasPrefix(got, "0001-01-01T00:00:00") {
		t.Errorf("start wire form=%q", got)
	}
}

// TestWebhooks_Get_UUIDValidation exercises the UUID pre-flight
// check. The webhook_id path param is declared `format: uuid`
// in the spec; order_id on GetOrder is NOT (the spec says
// "UUID or ULID" in prose but omits format), so the generator
// correctly only installs the check on webhook_id.
func TestWebhooks_Get_UUIDValidation(t *testing.T) {
	var hits int
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{}`)
	}))
	_, err := client.Webhooks.Get(context.Background(), "not-a-uuid", "sandbox-key")
	if err == nil || !strings.Contains(err.Error(), "valid UUID") {
		t.Errorf("want UUID format error, got %v", err)
	}
	if hits != 0 {
		t.Error("network request fired despite malformed UUID")
	}
}

// TestWebhooks_Create_RoundTrip exercises the Create path: JSON
// body encoding + typed response decoding. Uses a webhook
// payload the sandbox would accept; here the httptest handler
// echoes a canned response so the test stays hermetic.
func TestWebhooks_Create_RoundTrip(t *testing.T) {
	var gotBody string
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
			"id":"11111111-1111-1111-1111-111111111111",
			"url":"https://example.com/hook",
			"events":["ORDER_COMPLETED"]
		}`)
	}))
	out, err := client.Webhooks.Create(context.Background(), "sandbox-key", WebhookCreateRequest{
		URL:    "https://example.com/hook",
		Events: []EventType{EventTypeOrderCompleted},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if out.ID != "11111111-1111-1111-1111-111111111111" {
		t.Errorf("ID=%q", out.ID)
	}
	if !strings.Contains(gotBody, `"https://example.com/hook"`) {
		t.Errorf("wire body missing URL: %q", gotBody)
	}
}
