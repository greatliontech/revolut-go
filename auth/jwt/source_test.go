package jwt

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/greatliontech/revolut-go"
)

// Compile-time proof that *Source satisfies revolut.Authenticator.
var _ revolut.Authenticator = (*Source)(nil)

func TestSource_CachesAccessToken(t *testing.T) {
	t.Parallel()
	signer := newTestSigner(t)
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"access_token":"at","token_type":"bearer","expires_in":2399}`)
	}))
	defer srv.Close()

	now := time.Unix(1_700_000_000, 0)
	src, err := NewSource(SourceConfig{
		Signer:       signer,
		TokenURL:     srv.URL,
		RefreshToken: "rt",
		HTTPClient:   srv.Client(),
		Now:          func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("NewSource: %v", err)
	}

	for range 3 {
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://unused", nil)
		if err := src.Apply(req); err != nil {
			t.Fatalf("Apply: %v", err)
		}
		if got := req.Header.Get("Authorization"); got != "Bearer at" {
			t.Fatalf("authz header: %q", got)
		}
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("refresh hits: got %d want 1 (should cache)", got)
	}
}

func TestSource_RefreshesAfterExpiry(t *testing.T) {
	t.Parallel()
	signer := newTestSigner(t)
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if n == 1 {
			_, _ = io.WriteString(w, `{"access_token":"at1","token_type":"bearer","expires_in":100}`)
		} else {
			_, _ = io.WriteString(w, `{"access_token":"at2","token_type":"bearer","expires_in":100}`)
		}
	}))
	defer srv.Close()

	now := time.Unix(1_700_000_000, 0)
	src, err := NewSource(SourceConfig{
		Signer:       signer,
		TokenURL:     srv.URL,
		RefreshToken: "rt",
		HTTPClient:   srv.Client(),
		Now:          func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("NewSource: %v", err)
	}

	// First call triggers refresh.
	tok, err := src.Token(context.Background())
	if err != nil || tok != "at1" {
		t.Fatalf("first token: %q err=%v", tok, err)
	}
	// Advance past the cached TTL (100s - 60s skew = 40s). Jump 2 min.
	now = now.Add(2 * time.Minute)
	tok, err = src.Token(context.Background())
	if err != nil || tok != "at2" {
		t.Fatalf("second token: %q err=%v", tok, err)
	}
	if got := hits.Load(); got != 2 {
		t.Fatalf("refresh hits: got %d want 2", got)
	}
}

func TestNewSource_Validation(t *testing.T) {
	t.Parallel()
	signer := newTestSigner(t)
	cases := []struct {
		name string
		cfg  SourceConfig
	}{
		{"no signer", SourceConfig{TokenURL: "x", RefreshToken: "r"}},
		{"no url", SourceConfig{Signer: signer, RefreshToken: "r"}},
		{"no refresh", SourceConfig{Signer: signer, TokenURL: "x"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if _, err := NewSource(tc.cfg); err == nil {
				t.Fatal("want error")
			}
		})
	}
}
