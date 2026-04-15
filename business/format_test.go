package business

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

// TestPathParamUUIDValidation pins the generator's UUID pre-flight
// check: a malformed ID fails locally before any HTTP call is
// issued. accountID on Accounts.Get is declared `format: uuid`
// in the spec.
func TestPathParamUUIDValidation(t *testing.T) {
	// Transport server returns 200 with an empty body — used only
	// to prove valid UUIDs reach the network path. Validation
	// errors are surfaced before the request ever fires, so the
	// handler doesn't see them.
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))

	cases := []struct {
		name, id string
		wantSub  string // substring of expected error
		wantKind string // "format" or "other"
	}{
		{"empty", "", "is required", "format"},
		{"too short", "abc", "must be a valid UUID", "format"},
		{"missing hyphens", "6516e61cd2794b53ac68f9ae1b4a31e0", "must be a valid UUID", "format"},
		{"non-hex char", "6516e61c-d279-4b53-ac68-f9ae1b4a31zz", "must be a valid UUID", "format"},
		// Valid UUID makes it past validation and hits the test
		// server; response is empty JSON which unmarshals fine.
		{"valid uuid reaches transport", "6516e61c-d279-4b53-ac68-f9ae1b4a31e0", "", "ok"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := c.Accounts.Get(context.Background(), tc.id)
			switch tc.wantKind {
			case "format":
				if err == nil {
					t.Fatal("want format error")
				}
				if !strings.Contains(err.Error(), tc.wantSub) {
					t.Errorf("format error missing substring %q: %v", tc.wantSub, err)
				}
			case "ok":
				if err != nil && (strings.Contains(err.Error(), "must be a valid UUID") || strings.Contains(err.Error(), "is required")) {
					t.Errorf("valid UUID was rejected by format validation: %v", err)
				}
			}
		})
	}
}
