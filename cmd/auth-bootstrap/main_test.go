package main

import (
	"net/url"
	"strings"
	"testing"
)

// TestBuildConsentURL_IncludesState pins the OAuth CSRF-protection
// fix: the consent URL MUST include the state param so the
// /callback handler can verify the round-trip. Revolut propagates
// state back unchanged on success; the handler rejects mismatches.
func TestBuildConsentURL_IncludesState(t *testing.T) {
	cfg := config{
		env:      envSandbox,
		clientID: "client-123",
		redirect: "https://127.0.0.1:8787/callback",
	}
	raw := buildConsentURL(cfg, "state-xyz")
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	q := u.Query()
	if q.Get("state") != "state-xyz" {
		t.Errorf("state=%q; want state-xyz", q.Get("state"))
	}
	// client_id, redirect_uri, response_type are all present so a
	// regression that silently dropped state instead of a different
	// param would still be caught here.
	for _, k := range []string{"client_id", "redirect_uri", "response_type"} {
		if q.Get(k) == "" {
			t.Errorf("consent URL missing %s; query=%v", k, q)
		}
	}
	if !strings.Contains(raw, "/app-confirm") {
		t.Errorf("consent URL path=%q", u.Path)
	}
}
