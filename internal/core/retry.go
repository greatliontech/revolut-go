package core

import (
	"net/http"
	"time"
)

// RetryPolicy decides whether the transport should retry a failed
// request. Implementations are queried after every transport-level
// outcome — both transport errors (resp == nil, err != nil) and
// non-2xx responses (resp != nil, err == nil) — and return the
// delay to wait plus whether to attempt again.
//
// The transport calls Next with attempt=1 after the first failure,
// 2 after the second, and so on. Returning (0, false) stops the
// retry loop and surfaces the last response/error to the caller.
//
// Implementations MUST be safe for concurrent use. The transport
// stamps `attempt` per request, so a single policy instance can
// drive every concurrent call.
type RetryPolicy interface {
	Next(attempt int, resp *http.Response, err error) (delay time.Duration, retry bool)
}

// RetryPolicyFunc adapts a plain function to RetryPolicy.
type RetryPolicyFunc func(attempt int, resp *http.Response, err error) (time.Duration, bool)

// Next implements RetryPolicy.
func (f RetryPolicyFunc) Next(attempt int, resp *http.Response, err error) (time.Duration, bool) {
	return f(attempt, resp, err)
}
