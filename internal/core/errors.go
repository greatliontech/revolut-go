package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// APIError is returned when a Revolut endpoint responds with a non-2xx
// status. It carries enough request/response context to be pasted into
// a support ticket: the HTTP method and URL that were called, the
// status code, any structured fields the API populated (code, message,
// error_id), any request-id echoed by the server, and the raw response
// body for cases where the structured fields are insufficient.
//
// RetryAfter is parsed from the Retry-After response header when the
// server supplies it (typical for 429 Too Many Requests and 503
// Service Unavailable). Zero when the header is absent, malformed, or
// the response came with no retry hint.
type APIError struct {
	Method     string
	URL        string
	StatusCode int
	// Code is the error code returned by the API. Revolut's specs
	// declare it as either an integer (business, open-banking) or a
	// short string identifier (merchant Error-v2); the transport
	// captures both forms as a string so callers have a uniform
	// field to compare against.
	Code string
	// Message is the human-readable message the server returned, if any.
	Message string
	// ErrorID is the server-assigned error identifier used for
	// support tickets. Populated from the response body's error_id,
	// errorId, or Id field depending on the spec.
	ErrorID string
	// RequestID is the request correlator echoed by the server
	// (x-request-id or similar) when one is present.
	RequestID string
	// Body is the raw response body, capped at the transport's
	// configured MaxResponseBytes.
	Body       []byte
	RetryAfter time.Duration
}

func (e *APIError) Error() string {
	var head string
	if e.Method != "" && e.URL != "" {
		head = fmt.Sprintf("revolut: %s %s: %d", e.Method, e.URL, e.StatusCode)
	} else {
		head = fmt.Sprintf("revolut: %d", e.StatusCode)
	}
	var parts []string
	if e.Message != "" {
		parts = append(parts, e.Message)
	}
	if e.Code != "" {
		parts = append(parts, fmt.Sprintf("code=%s", e.Code))
	}
	if e.ErrorID != "" {
		parts = append(parts, fmt.Sprintf("error_id=%s", e.ErrorID))
	}
	if e.RequestID != "" {
		parts = append(parts, fmt.Sprintf("request_id=%s", e.RequestID))
	}
	if len(parts) == 0 {
		return head
	}
	out := head + " "
	for i, p := range parts {
		if i > 0 {
			out += ", "
		}
		out += p
	}
	return out
}

// Decode attempts to unmarshal e.Body into out, so callers that know
// the per-endpoint error schema can access the typed fields without
// re-parsing the body themselves.
func (e *APIError) Decode(out any) error {
	if len(e.Body) == 0 {
		return errors.New("revolut: APIError has no body to decode")
	}
	return json.Unmarshal(e.Body, out)
}

// AsAPIError unwraps err into an *APIError if possible.
func AsAPIError(err error) (*APIError, bool) {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr, true
	}
	return nil, false
}
