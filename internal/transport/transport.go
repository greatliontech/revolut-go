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
	req, err := t.newRequest(ctx, method, path, body)
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
		if err := json.NewDecoder(resp.Body).Decode(dst); err != nil && !errors.Is(err, io.EOF) {
			return fmt.Errorf("revolut: decode %s %s: %w", method, path, err)
		}
		return nil
	}
	return decodeError(resp)
}

func (t *Transport) newRequest(ctx context.Context, method, path string, body any) (*http.Request, error) {
	u, err := t.resolve(path)
	if err != nil {
		return nil, err
	}
	var reader io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("revolut: encode %s %s body: %w", method, path, err)
		}
		reader = bytes.NewReader(buf)
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), reader)
	if err != nil {
		return nil, fmt.Errorf("revolut: build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
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
