package revolut

import (
	"errors"
	"math"
	"net"
	"net/http"
	"time"

	"github.com/greatliontech/revolut-go/internal/core"
)

// RetryPolicy decides whether the transport should retry a failed
// request. See [core.RetryPolicy] for the contract.
type RetryPolicy = core.RetryPolicy

// RetryPolicyFunc adapts a plain function to RetryPolicy.
type RetryPolicyFunc = core.RetryPolicyFunc

// WithRetry installs a retry policy on the transport. The policy is
// consulted after every transport-level failure (network error or
// non-2xx response); ctx cancellation always wins. Pass nil — or
// don't pass the option at all — for no retries.
//
// To replay request bodies the transport buffers io.Reader payloads
// before the first attempt; [BackoffPolicy] caps that with a
// configurable RetryableMethods set so callers can keep large
// uploads out of the retry path.
func WithRetry(p RetryPolicy) Option {
	return func(o *clientOptions) { o.retry = p }
}

// BackoffPolicy is the standard retry policy: exponential backoff
// with jitter, capped attempt count, and respect for the server's
// Retry-After header. Zero-value fields take the documented
// defaults so a caller can write `BackoffPolicy{}` and get a
// sensible production policy.
type BackoffPolicy struct {
	// MaxAttempts is the total number of attempts (initial +
	// retries). Zero defaults to 4 (initial + 3 retries).
	MaxAttempts int

	// BaseDelay is the first backoff duration. Subsequent retries
	// double this up to MaxDelay. Zero defaults to 250ms.
	BaseDelay time.Duration

	// MaxDelay caps the per-retry wait. Zero defaults to 30s.
	MaxDelay time.Duration

	// RetryableStatuses lists HTTP statuses that trigger a retry.
	// Nil defaults to {408, 429, 500, 502, 503, 504} — the
	// canonical idempotent-friendly set.
	RetryableStatuses []int

	// RetryableMethods, when non-nil, restricts retries to the
	// listed HTTP methods. Nil retries every method (caller's
	// choice — POST retries are unsafe without
	// idempotency keys; the OBIE / Revolut Business POST surfaces
	// always require x-idempotency-key, so retrying them is
	// generally safe).
	RetryableMethods []string

	// HonorRetryAfter, when true, uses the server's Retry-After
	// header value (in place of BaseDelay backoff) when present.
	// Default true.
	HonorRetryAfter *bool
}

// Next implements RetryPolicy.
func (p BackoffPolicy) Next(attempt int, resp *http.Response, err error) (time.Duration, bool) {
	maxAttempts := p.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = 4
	}
	if attempt >= maxAttempts {
		return 0, false
	}
	if !p.shouldRetry(resp, err) {
		return 0, false
	}
	if p.honorRetryAfter() && resp != nil && resp.Header.Get("Retry-After") != "" {
		// Returning 0 hands control to the transport's
		// Retry-After parser, which knows the per-API unit.
		return 0, true
	}
	return p.backoff(attempt), true
}

func (p BackoffPolicy) honorRetryAfter() bool {
	if p.HonorRetryAfter == nil {
		return true
	}
	return *p.HonorRetryAfter
}

func (p BackoffPolicy) shouldRetry(resp *http.Response, err error) bool {
	// Transport-level errors: retry only when they're transient
	// (timeouts, connection drops). errors.Is(err, syscall.X) is
	// avoided because it pulls in syscall on every supported
	// platform; net.Error.Timeout() and the io.EOF check below
	// are sufficient for the common cases.
	if err != nil {
		var nerr net.Error
		if errors.As(err, &nerr) && nerr.Timeout() {
			return true
		}
		// Connection reset / refused / pipe broken all surface as
		// generic OpError or io.EOF mid-read; retrying is the
		// idiomatic response.
		return true
	}
	if resp == nil {
		return false
	}
	if !p.methodAllowed(resp.Request) {
		return false
	}
	for _, code := range p.statuses() {
		if resp.StatusCode == code {
			return true
		}
	}
	return false
}

func (p BackoffPolicy) statuses() []int {
	if p.RetryableStatuses != nil {
		return p.RetryableStatuses
	}
	return []int{
		http.StatusRequestTimeout,
		http.StatusTooManyRequests,
		http.StatusInternalServerError,
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
		http.StatusGatewayTimeout,
	}
}

func (p BackoffPolicy) methodAllowed(req *http.Request) bool {
	if p.RetryableMethods == nil || req == nil {
		return true
	}
	for _, m := range p.RetryableMethods {
		if m == req.Method {
			return true
		}
	}
	return false
}

func (p BackoffPolicy) backoff(attempt int) time.Duration {
	base := p.BaseDelay
	if base <= 0 {
		base = 250 * time.Millisecond
	}
	cap := p.MaxDelay
	if cap <= 0 {
		cap = 30 * time.Second
	}
	// 2^(attempt-1) * base, capped at cap.
	d := time.Duration(math.Pow(2, float64(attempt-1))) * base
	if d > cap || d <= 0 {
		d = cap
	}
	return d
}
