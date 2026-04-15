package core

import (
	"errors"
	"fmt"
	"time"
)

// APIError is returned when a Revolut endpoint responds with a non-2xx
// status. It carries the HTTP status code along with any structured fields
// the API populated (code, message) and the raw response body for cases
// where the structured fields are insufficient.
//
// RetryAfter is parsed from the Retry-After response header when the
// server supplies it (typical for 429 Too Many Requests and 503
// Service Unavailable). Zero when the header is absent, malformed, or
// the response came with no retry hint.
type APIError struct {
	StatusCode int
	Code       int
	Message    string
	Body       []byte
	RetryAfter time.Duration
}

func (e *APIError) Error() string {
	switch {
	case e.Message != "" && e.Code != 0:
		return fmt.Sprintf("revolut: %d %s (code %d)", e.StatusCode, e.Message, e.Code)
	case e.Message != "":
		return fmt.Sprintf("revolut: %d %s", e.StatusCode, e.Message)
	default:
		return fmt.Sprintf("revolut: http %d", e.StatusCode)
	}
}

// AsAPIError unwraps err into an *APIError if possible.
func AsAPIError(err error) (*APIError, bool) {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr, true
	}
	return nil, false
}
