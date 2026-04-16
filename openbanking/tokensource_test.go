package openbanking

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// stubTransportCert returns a minimally-valid tls.Certificate
// usable as the TransportCert field. Token-source unit tests
// inject their own *http.Client so the cert never goes through a
// real handshake; we only need to satisfy the constructor's
// "TransportCert is set" check.
func stubTransportCert(t *testing.T) tls.Certificate {
	t.Helper()
	return tls.Certificate{Certificate: [][]byte{{0x30, 0x00}}}
}

// fakeTokenServer returns the URL of an httptest server that
// behaves like an /token endpoint. The handler captures the
// inbound form and replies with a fresh token whose lifetime the
// caller controls.
func fakeTokenServer(t *testing.T, lifetime int, capture func(form url.Values)) (string, func()) {
	t.Helper()
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if capture != nil {
			capture(r.PostForm)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "tok-" + r.PostForm.Get("client_assertion")[0:6],
			"token_type":   "Bearer",
			"expires_in":   lifetime,
		})
	})
	srv := httptest.NewServer(h)
	return srv.URL, srv.Close
}

func newSourceForTest(t *testing.T, tokenURL string, opts ...func(*ClientCredentialsConfig)) *ClientCredentialsTokenSource {
	t.Helper()
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	cfg := ClientCredentialsConfig{
		ClientID:      "client-1",
		TokenURL:      tokenURL,
		Kid:           "kid-1",
		PrivateKey:    key,
		TransportCert: stubTransportCert(t),
		HTTPClient:    http.DefaultClient, // stub server is plain HTTP
	}
	for _, opt := range opts {
		opt(&cfg)
	}
	src, err := NewClientCredentialsTokenSource(cfg)
	if err != nil {
		t.Fatalf("NewClientCredentialsTokenSource: %v", err)
	}
	return src
}

// TestTokenSource_ApplySetsBearer pins the Authenticator
// behaviour: Apply mints (or reuses) a token and stamps it as
// `Authorization: Bearer …` on the request.
func TestTokenSource_ApplySetsBearer(t *testing.T) {
	url, close := fakeTokenServer(t, 300, nil)
	defer close()
	src := newSourceForTest(t, url)
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://api.example.com/x", nil)
	if err := src.Apply(req); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	got := req.Header.Get("Authorization")
	if !strings.HasPrefix(got, "Bearer tok-") {
		t.Errorf("Authorization header=%q; want Bearer tok-…", got)
	}
}

// TestTokenSource_CachesUntilExpiry: a second Apply within the
// lifetime hits the cache instead of /token again.
func TestTokenSource_CachesUntilExpiry(t *testing.T) {
	var hits int32
	url, close := fakeTokenServer(t, 300, func(_ url.Values) { atomic.AddInt32(&hits, 1) })
	defer close()
	src := newSourceForTest(t, url)
	for i := 0; i < 5; i++ {
		req, _ := http.NewRequest(http.MethodGet, "https://x/y", nil)
		if err := src.Apply(req); err != nil {
			t.Fatal(err)
		}
	}
	if h := atomic.LoadInt32(&hits); h != 1 {
		t.Errorf("/token hits=%d; want 1 (cache should swallow the rest)", h)
	}
}

// TestTokenSource_RefreshesOnExpiry: with EarlyRefresh + a
// short lifetime we force a second fetch and watch the new
// token replace the cached one.
func TestTokenSource_RefreshesOnExpiry(t *testing.T) {
	var hits int32
	var counter int
	url, close := fakeTokenServer(t, 1, func(_ url.Values) {
		atomic.AddInt32(&hits, 1)
		counter++
	})
	defer close()
	src := newSourceForTest(t, url, func(c *ClientCredentialsConfig) {
		// EarlyRefresh > lifetime makes the cache always stale.
		c.EarlyRefresh = 5 * time.Second
	})
	for i := 0; i < 3; i++ {
		req, _ := http.NewRequest(http.MethodGet, "https://x/y", nil)
		_ = src.Apply(req)
	}
	if h := atomic.LoadInt32(&hits); h != 3 {
		t.Errorf("/token hits=%d; want 3 (EarlyRefresh forces every Apply to refetch)", h)
	}
}

// TestTokenSource_ConcurrentRefreshIsSerialised: many goroutines
// hitting Apply at once on a cold cache must produce one /token
// fetch, not N. Without the mutex the test would record many.
func TestTokenSource_ConcurrentRefreshIsSerialised(t *testing.T) {
	var hits int32
	url, close := fakeTokenServer(t, 300, func(_ url.Values) {
		// Slow the response so concurrent goroutines pile up
		// behind the lock; without serialisation they'd all
		// have already fired their own request.
		time.Sleep(20 * time.Millisecond)
		atomic.AddInt32(&hits, 1)
	})
	defer close()
	src := newSourceForTest(t, url)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req, _ := http.NewRequest(http.MethodGet, "https://x/y", nil)
			_ = src.Apply(req)
		}()
	}
	wg.Wait()
	if h := atomic.LoadInt32(&hits); h != 1 {
		t.Errorf("/token hits=%d; want 1 (mutex must serialise the concurrent first call)", h)
	}
}

// TestTokenSource_PostFormShape pins what reaches /token: the
// grant type, client_id, assertion type, the assertion JWT
// itself, and (when configured) the scope.
func TestTokenSource_PostFormShape(t *testing.T) {
	var captured url.Values
	url, close := fakeTokenServer(t, 300, func(form url.Values) {
		captured = form
	})
	defer close()
	src := newSourceForTest(t, url, func(c *ClientCredentialsConfig) {
		c.Scope = "accounts payments"
	})
	req, _ := http.NewRequest(http.MethodGet, "https://x/y", nil)
	if err := src.Apply(req); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"grant_type":            "client_credentials",
		"client_id":             "client-1",
		"client_assertion_type": "urn:ietf:params:oauth:client-assertion-type:jwt-bearer",
		"scope":                 "accounts payments",
	}
	for k, v := range want {
		if captured.Get(k) != v {
			t.Errorf("form[%q]=%q; want %q", k, captured.Get(k), v)
		}
	}
	if captured.Get("client_assertion") == "" {
		t.Errorf("client_assertion JWT missing from form")
	}
}

// TestTokenSource_ASErrorSurfacesAsTokenError: a 4xx /token
// response surfaces as *TokenError carrying the response body
// verbatim.
func TestTokenSource_ASErrorSurfacesAsTokenError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":"invalid_client","error_description":"bad assertion"}`)
	}))
	defer srv.Close()
	src := newSourceForTest(t, srv.URL)
	req, _ := http.NewRequest(http.MethodGet, "https://x/y", nil)
	err := src.Apply(req)
	var tokErr *TokenError
	if !errors.As(err, &tokErr) {
		t.Fatalf("want *TokenError, got %T: %v", err, err)
	}
	if tokErr.StatusCode != 401 {
		t.Errorf("status=%d; want 401", tokErr.StatusCode)
	}
	if !strings.Contains(string(tokErr.Body), "invalid_client") {
		t.Errorf("body not preserved: %s", tokErr.Body)
	}
}

// TestNewClientCredentialsTokenSource_RejectsBadConfig pins the
// constructor preconditions. Each missing field surfaces a
// dedicated error — easier to debug than a downstream "/token
// invalid request".
func TestNewClientCredentialsTokenSource_RejectsBadConfig(t *testing.T) {
	good, _ := rsa.GenerateKey(rand.Reader, 2048)
	stub := stubTransportCert(t)
	cases := []struct {
		name string
		mut  func(*ClientCredentialsConfig)
		want string
	}{
		{"no client", func(c *ClientCredentialsConfig) { c.ClientID = "" }, "ClientID"},
		{"no url", func(c *ClientCredentialsConfig) { c.TokenURL = "" }, "TokenURL"},
		{"no kid", func(c *ClientCredentialsConfig) { c.Kid = "" }, "Kid"},
		{"no key", func(c *ClientCredentialsConfig) { c.PrivateKey = nil }, "PrivateKey"},
		{"no cert", func(c *ClientCredentialsConfig) { c.TransportCert = tls.Certificate{} }, "TransportCert"},
		{"negative refresh", func(c *ClientCredentialsConfig) { c.EarlyRefresh = -1 }, "EarlyRefresh"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := ClientCredentialsConfig{
				ClientID:      "c",
				TokenURL:      "https://x/token",
				Kid:           "k",
				PrivateKey:    good,
				TransportCert: stub,
			}
			tc.mut(&cfg)
			_, err := NewClientCredentialsTokenSource(cfg)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err=%v; want substring %q", err, tc.want)
			}
		})
	}
}

// TestTokenSource_StringRedacts: fmt-printing the source must not
// leak the cached token.
func TestTokenSource_StringRedacts(t *testing.T) {
	url, close := fakeTokenServer(t, 300, nil)
	defer close()
	src := newSourceForTest(t, url)
	req, _ := http.NewRequest(http.MethodGet, "https://x/y", nil)
	_ = src.Apply(req)
	s := src.String()
	if !strings.Contains(s, "[REDACTED]") {
		t.Errorf("String() missing redaction marker: %s", s)
	}
	// The cached token is "tok-…" — must not appear verbatim.
	if strings.Contains(s, "tok-") {
		t.Errorf("token leaked through String(): %s", s)
	}
}
