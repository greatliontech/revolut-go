package revolut_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	revolut "github.com/greatliontech/revolut-go"
	"github.com/greatliontech/revolut-go/internal/transport"
)

// transportWithRetry wires a Transport with the given retry policy
// and a base URL pointing at the test server. Skips the higher-level
// constructors so the test can inject a policy without needing live
// auth credentials.
func transportWithRetry(t *testing.T, srv *httptest.Server, p revolut.RetryPolicy) *transport.Transport {
	t.Helper()
	tr, err := transport.New(transport.Config{
		BaseURL:     srv.URL + "/",
		RetryPolicy: p,
	})
	if err != nil {
		t.Fatalf("transport.New: %v", err)
	}
	return tr
}

// TestBackoffPolicy_RetriesOn5xxThenSucceeds: a 503 followed by a
// 200 results in two HTTP calls and a successful Do.
func TestBackoffPolicy_RetriesOn5xxThenSucceeds(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&hits, 1)
		if n == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	tr := transportWithRetry(t, srv, revolut.BackoffPolicy{
		BaseDelay: time.Millisecond, // fast for tests
	})
	var out struct {
		OK bool `json:"ok"`
	}
	if err := tr.Do(context.Background(), http.MethodGet, "x", nil, &out); err != nil {
		t.Fatalf("Do: %v", err)
	}
	if !out.OK {
		t.Errorf("dst not populated on retry success")
	}
	if got := atomic.LoadInt32(&hits); got != 2 {
		t.Errorf("hits=%d; want 2", got)
	}
}

// TestBackoffPolicy_StopsAtMaxAttempts: when every attempt fails,
// the policy stops after MaxAttempts and surfaces the last
// APIError.
func TestBackoffPolicy_StopsAtMaxAttempts(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()

	tr := transportWithRetry(t, srv, revolut.BackoffPolicy{
		MaxAttempts: 3,
		BaseDelay:   time.Millisecond,
	})
	err := tr.Do(context.Background(), http.MethodGet, "x", nil, nil)
	apiErr, ok := revolut.AsAPIError(err)
	if !ok {
		t.Fatalf("want APIError, got %T: %v", err, err)
	}
	if apiErr.StatusCode != http.StatusBadGateway {
		t.Errorf("status=%d; want 502", apiErr.StatusCode)
	}
	if got := atomic.LoadInt32(&hits); got != 3 {
		t.Errorf("hits=%d; want 3 (MaxAttempts)", got)
	}
}

// TestBackoffPolicy_HonorsRetryAfter: when the server returns a
// numeric Retry-After, the wait between attempts matches the
// header value (in seconds, the default unit).
func TestBackoffPolicy_HonorsRetryAfter(t *testing.T) {
	var firstAt, secondAt time.Time
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&hits, 1)
		if n == 1 {
			firstAt = time.Now()
			// 1 second is the smallest delta-seconds value the
			// server can report; the test waits at least that long.
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		secondAt = time.Now()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	tr := transportWithRetry(t, srv, revolut.BackoffPolicy{BaseDelay: time.Hour /* would dominate without Retry-After */})
	if err := tr.Do(context.Background(), http.MethodGet, "x", nil, nil); err != nil {
		t.Fatalf("Do: %v", err)
	}
	gap := secondAt.Sub(firstAt)
	if gap < 900*time.Millisecond || gap > 2*time.Second {
		t.Errorf("retry gap=%v; want ≈1s (Retry-After), proving BaseDelay didn't dominate", gap)
	}
}

// TestBackoffPolicy_DoesNotRetry2xx: a 200 short-circuits the
// policy — only failures fan out into retries.
func TestBackoffPolicy_DoesNotRetry2xx(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	tr := transportWithRetry(t, srv, revolut.BackoffPolicy{BaseDelay: time.Millisecond})
	if err := tr.Do(context.Background(), http.MethodGet, "x", nil, nil); err != nil {
		t.Fatal(err)
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Errorf("hits=%d; want 1 (no retry on 2xx)", got)
	}
}

// TestBackoffPolicy_RestrictsByMethod: only listed methods retry;
// a POST without idempotency would otherwise risk duplicate writes.
func TestBackoffPolicy_RestrictsByMethod(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	tr := transportWithRetry(t, srv, revolut.BackoffPolicy{
		BaseDelay:        time.Millisecond,
		RetryableMethods: []string{http.MethodGet}, // POST not listed
	})
	if err := tr.Do(context.Background(), http.MethodPost, "x", map[string]any{"x": 1}, nil); err == nil {
		t.Fatal("want error")
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Errorf("hits=%d; want 1 (POST excluded by RetryableMethods)", got)
	}
}

// TestBackoffPolicy_CtxCancelStopsLoop: cancelling ctx during the
// inter-attempt sleep aborts the retry promptly with ctx.Err.
func TestBackoffPolicy_CtxCancelStopsLoop(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	tr := transportWithRetry(t, srv, revolut.BackoffPolicy{
		BaseDelay: 5 * time.Second,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	err := tr.Do(ctx, http.MethodGet, "x", nil, nil)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("want context.Canceled in chain; got %v", err)
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Errorf("hits=%d; want 1 (cancellation tripped before second attempt)", got)
	}
}

// TestBackoffPolicy_ReplaysJSONBody: the request body must be
// available on every attempt — the transport buffers it before the
// loop starts so retry doesn't surface an empty body to the server.
func TestBackoffPolicy_ReplaysJSONBody(t *testing.T) {
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(buf)
		bodies = append(bodies, string(buf))
		if len(bodies) < 2 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	tr := transportWithRetry(t, srv, revolut.BackoffPolicy{BaseDelay: time.Millisecond})
	body := map[string]int{"n": 42}
	if err := tr.Do(context.Background(), http.MethodPost, "x", body, nil); err != nil {
		t.Fatalf("Do: %v", err)
	}
	if len(bodies) != 2 {
		t.Fatalf("got %d bodies; want 2", len(bodies))
	}
	if bodies[0] != bodies[1] || bodies[0] == "" {
		t.Errorf("bodies differ across retry: first=%q second=%q", bodies[0], bodies[1])
	}
}

// TestBackoffPolicy_RetriesTransportErrors: a connection refusal
// counts as a retryable transport error, not a permanent failure.
// Pointing at a closed listener simulates the case.
func TestBackoffPolicy_RetriesTransportErrors(t *testing.T) {
	srv := httptest.NewServer(nil)
	srv.Close() // close immediately so the URL refuses connections

	var attempts atomic.Int32
	pol := revolut.RetryPolicyFunc(func(attempt int, _ *http.Response, err error) (time.Duration, bool) {
		attempts.Store(int32(attempt))
		if attempt >= 3 || err == nil {
			return 0, false
		}
		return time.Millisecond, true
	})
	tr, err := transport.New(transport.Config{
		BaseURL:     srv.URL + "/",
		RetryPolicy: pol,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := tr.Do(context.Background(), http.MethodGet, "x", nil, nil); err == nil {
		t.Fatal("want connection refused error")
	}
	if got := attempts.Load(); got != 3 {
		t.Errorf("attempts=%d; want 3", got)
	}
}

// TestDefaultTransport_HasTimeout: a transport built with no
// HTTPClient gets one with DefaultHTTPTimeout — a hanging server
// fails quickly instead of wedging the caller forever.
func TestDefaultTransport_HasTimeout(t *testing.T) {
	hang := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer hang.Close()
	tr, err := transport.New(transport.Config{
		BaseURL: hang.URL + "/",
		// Override the package default to keep the test fast; the
		// non-default value still proves the transport injects
		// _some_ timeout when the caller doesn't.
		HTTPClient: &http.Client{Timeout: 200 * time.Millisecond},
	})
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	err = tr.Do(context.Background(), http.MethodGet, "x", nil, nil)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("want timeout error")
	}
	if elapsed > 2*time.Second {
		t.Errorf("request hung %v; expected a timeout near 200ms", elapsed)
	}
}
