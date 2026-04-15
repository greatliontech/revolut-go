// Package transport implements the shared HTTP transport that every
// Revolut API client uses. It applies an Authenticator to outgoing
// requests, encodes JSON bodies, decodes JSON responses, and turns non-2xx
// responses into [core.APIError].
package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/greatliontech/revolut-go/internal/core"
)

// Default timeout used when the caller does not supply an *http.Client.
const defaultUserAgent = "revolut-go"

// Transport carries the per-API HTTP configuration: base URL, the auth
// scheme to apply, and the *http.Client to dispatch on.
//
// A zero Transport is not usable; construct via [New].
type Transport struct {
	baseURL   *url.URL
	httpc     *http.Client
	auth      core.Authenticator
	userAgent string
}

// Config configures a [Transport]. BaseURL is required.
type Config struct {
	BaseURL    string
	HTTPClient *http.Client
	Auth       core.Authenticator
	UserAgent  string
}

// New builds a Transport from cfg.
func New(cfg Config) (*Transport, error) {
	if cfg.BaseURL == "" {
		return nil, errors.New("transport: BaseURL is required")
	}
	u, err := url.Parse(cfg.BaseURL)
	if err != nil {
		return nil, fmt.Errorf("transport: parse BaseURL: %w", err)
	}
	hc := cfg.HTTPClient
	if hc == nil {
		hc = http.DefaultClient
	}
	ua := cfg.UserAgent
	if ua == "" {
		ua = defaultUserAgent
	}
	return &Transport{
		baseURL:   u,
		httpc:     hc,
		auth:      cfg.Auth,
		userAgent: ua,
	}, nil
}

// Do performs an HTTP request against path (which is joined onto the
// configured base URL).
//
//   - body, when non-nil, is JSON-encoded as the request body.
//   - dst, when non-nil, receives the JSON-decoded response.
//   - On a 2xx response with a nil dst, the body is drained and discarded.
//   - On a non-2xx response, the returned error is *[core.APIError].
func (t *Transport) Do(ctx context.Context, method, path string, body, dst any) error {
	req, err := t.newJSONRequest(ctx, method, path, body)
	if err != nil {
		return err
	}
	resp, err := t.httpc.Do(req)
	if err != nil {
		return fmt.Errorf("revolut: %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		if dst == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			return nil
		}
		// 204 No Content legitimately has an empty body — EOF is
		// expected there. For every other 2xx, EOF on decode means
		// the server closed the connection mid-body and we'd
		// otherwise silently succeed with a zero-valued dst.
		if resp.StatusCode == http.StatusNoContent || resp.ContentLength == 0 {
			_, _ = io.Copy(io.Discard, resp.Body)
			return nil
		}
		if err := json.NewDecoder(resp.Body).Decode(dst); err != nil {
			return fmt.Errorf("revolut: decode %s %s: %w", method, path, err)
		}
		return nil
	}
	return decodeError(resp)
}

// RawRequest describes a non-JSON HTTP request. Exactly one of
// JSONBody, FormBody, or RawBody may be set; the transport picks
// the matching Content-Type automatically. Accept, when non-empty,
// overrides the default `application/json`. Headers, when non-nil,
// are merged into the outgoing request after auth/UA but before
// Content-Type / Accept, so those two can't be accidentally
// overridden.
type RawRequest struct {
	JSONBody       any        // JSON-encoded if non-nil
	FormBody       url.Values // application/x-www-form-urlencoded if non-nil
	RawBody        io.Reader  // raw body bytes; requires RawContentType
	RawContentType string
	Accept         string
	Headers        http.Header
}

// DoRaw performs an HTTP request that may carry a non-JSON body and/or
// expect a non-JSON response. The 2xx response body is returned as
// []byte along with the response headers. Non-2xx errors are surfaced
// as *[core.APIError] just like Do.
func (t *Transport) DoRaw(ctx context.Context, method, path string, r RawRequest) ([]byte, http.Header, error) {
	var reader io.Reader
	var contentType string
	switch {
	case r.JSONBody != nil:
		buf, err := json.Marshal(r.JSONBody)
		if err != nil {
			return nil, nil, fmt.Errorf("revolut: encode %s %s body: %w", method, path, err)
		}
		reader = bytes.NewReader(buf)
		contentType = "application/json"
	case r.FormBody != nil:
		reader = strings.NewReader(r.FormBody.Encode())
		contentType = "application/x-www-form-urlencoded"
	case r.RawBody != nil:
		reader = r.RawBody
		contentType = r.RawContentType
		if contentType == "" {
			return nil, nil, errors.New("revolut: RawBody set without RawContentType")
		}
	}
	accept := r.Accept
	if accept == "" {
		accept = "application/json"
	}
	req, err := t.newRequestWithBody(ctx, method, path, reader, contentType, accept)
	if err != nil {
		return nil, nil, err
	}
	for k, vs := range r.Headers {
		// Don't let the caller override transport-owned headers via
		// the generic Headers field — Authorization comes from
		// auth.Apply, User-Agent from the transport config,
		// Content-Type / Accept are picked by this call's
		// body/response shape.
		switch http.CanonicalHeaderKey(k) {
		case "Content-Type", "Accept", "Authorization", "User-Agent":
			continue
		}
		req.Header[http.CanonicalHeaderKey(k)] = append([]string(nil), vs...)
	}
	resp, err := t.httpc.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("revolut: %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, nil, decodeError(resp)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, nil, fmt.Errorf("revolut: read %s %s: %w", method, path, err)
	}
	return body, resp.Header, nil
}

func (t *Transport) newJSONRequest(ctx context.Context, method, path string, body any) (*http.Request, error) {
	var reader io.Reader
	var contentType string
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("revolut: encode %s %s body: %w", method, path, err)
		}
		reader = bytes.NewReader(buf)
		contentType = "application/json"
	}
	return t.newRequestWithBody(ctx, method, path, reader, contentType, "application/json")
}

func (t *Transport) newRequestWithBody(ctx context.Context, method, path string, body io.Reader, contentType, accept string) (*http.Request, error) {
	u, err := t.resolve(path)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return nil, fmt.Errorf("revolut: build request: %w", err)
	}
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	req.Header.Set("User-Agent", t.userAgent)
	if t.auth != nil {
		if err := t.auth.Apply(req); err != nil {
			return nil, fmt.Errorf("revolut: apply auth: %w", err)
		}
	}
	return req, nil
}

func (t *Transport) resolve(path string) (*url.URL, error) {
	if strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://") {
		return url.Parse(path)
	}
	ref, err := url.Parse(path)
	if err != nil {
		return nil, fmt.Errorf("revolut: parse path %q: %w", path, err)
	}
	return t.baseURL.ResolveReference(ref), nil
}

func decodeError(resp *http.Response) error {
	body, _ := io.ReadAll(resp.Body)
	apiErr := &core.APIError{StatusCode: resp.StatusCode, Body: body}
	if len(body) > 0 {
		var aux struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		}
		if err := json.Unmarshal(body, &aux); err == nil {
			apiErr.Code = aux.Code
			apiErr.Message = aux.Message
		}
	}
	return apiErr
}
