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
	"strconv"
	"strings"
	"time"

	"github.com/greatliontech/revolut-go/internal/core"
)

const defaultUserAgent = "revolut-go"

// DefaultMaxResponseBytes caps the response body the transport will
// decode. Chosen to be large enough for the biggest legitimate
// Revolut response (statement exports, list endpoints at max page
// size) while preventing a malicious or broken server from forcing
// the caller to OOM.
const DefaultMaxResponseBytes int64 = 10 << 20 // 10 MiB

// DefaultHTTPTimeout is the per-request timeout the transport
// applies when the caller doesn't supply an *http.Client. http.
// DefaultClient has no timeout, which is hostile to anything past
// "small dev script" — pick a long-but-finite default so a stuck
// upstream never wedges a goroutine forever.
const DefaultHTTPTimeout = 30 * time.Second

// Transport carries the per-API HTTP configuration: base URL, the auth
// scheme to apply, and the *http.Client to dispatch on.
//
// A zero Transport is not usable; construct via [New].
type Transport struct {
	baseURL          *url.URL
	httpc            *http.Client
	auth             core.Authenticator
	userAgent        string
	hostAliases      map[string]string
	maxResponseBytes int64
	retryAfterUnit   time.Duration
	retry            core.RetryPolicy
}

// Config configures a [Transport]. BaseURL is required.
type Config struct {
	BaseURL    string
	HTTPClient *http.Client
	Auth       core.Authenticator
	UserAgent  string
	// HostAliases lets the caller remap hostnames on absolute-URL
	// requests. Used by the revolut constructors to redirect the
	// spec's per-operation production server overrides (e.g.
	// https://apis.revolut.com) onto their sandbox equivalents
	// when WithEnvironment(EnvironmentSandbox) is in effect.
	// Requests whose URL is already relative (and therefore
	// resolved against BaseURL) are untouched.
	HostAliases map[string]string

	// MaxResponseBytes caps the bytes the transport reads from a
	// response body. Zero uses DefaultMaxResponseBytes. A negative
	// value disables the cap (use at your own risk).
	MaxResponseBytes int64

	// RetryAfterUnit controls the unit of a numeric Retry-After
	// header on non-2xx responses. RFC 7231 says seconds; some
	// Revolut APIs (revolut-x) document the value in milliseconds.
	// Zero defaults to seconds.
	RetryAfterUnit time.Duration

	// RetryPolicy, when non-nil, drives transport-level retries.
	// The policy is consulted after every transport error and
	// after every non-2xx response; ctx cancellation overrides any
	// requested delay.
	//
	// Bodies that arrive as a raw io.Reader are buffered into
	// memory (capped by MaxResponseBytes) before the first attempt
	// so retry can replay them. Buffer-busting uploads should pick
	// a policy that excludes the methods they hit.
	RetryPolicy core.RetryPolicy
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
		// http.DefaultClient has no timeout. Use a private client
		// with DefaultHTTPTimeout so a stuck upstream doesn't
		// wedge the calling goroutine indefinitely.
		hc = &http.Client{Timeout: DefaultHTTPTimeout}
	}
	ua := cfg.UserAgent
	if ua == "" {
		ua = defaultUserAgent
	}
	// Defensive copy: SandboxHostAliases is exposed as an exported
	// package-level var in each generated client so the revolut
	// constructors can pass it in. If a caller mutates the source
	// map after New returns, the transport's view must not shift
	// under live requests.
	var aliases map[string]string
	if len(cfg.HostAliases) > 0 {
		aliases = make(map[string]string, len(cfg.HostAliases))
		for k, v := range cfg.HostAliases {
			aliases[k] = v
		}
	}
	maxBytes := cfg.MaxResponseBytes
	if maxBytes == 0 {
		maxBytes = DefaultMaxResponseBytes
	}
	unit := cfg.RetryAfterUnit
	if unit == 0 {
		unit = time.Second
	}
	return &Transport{
		baseURL:          u,
		httpc:            hc,
		auth:             cfg.Auth,
		userAgent:        ua,
		hostAliases:      aliases,
		maxResponseBytes: maxBytes,
		retryAfterUnit:   unit,
		retry:            cfg.RetryPolicy,
	}, nil
}

// dispatch issues a single HTTP request, optionally replaying it
// via the configured RetryPolicy. The build callback recreates the
// request from scratch on every attempt — required because
// http.Request bodies are typically single-use io.Readers, and
// because Authenticator may want to refresh credentials between
// tries. The caller is still responsible for closing the returned
// response's body.
//
// Cancellation: the parent ctx interrupts the inter-attempt sleep
// and is propagated into every rebuilt request, so a cancelled ctx
// stops retries promptly.
func (t *Transport) dispatch(ctx context.Context, build func() (*http.Request, error)) (*http.Response, error) {
	for attempt := 0; ; attempt++ {
		req, err := build()
		if err != nil {
			return nil, err
		}
		resp, err := t.httpc.Do(req)
		if t.retry == nil {
			return resp, err
		}
		delay, retry := t.retry.Next(attempt+1, resp, err)
		if !retry {
			return resp, err
		}
		// Drain & close the previous response so the connection
		// stays in the pool. A nil resp (transport-level error)
		// has nothing to clean up.
		if resp != nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			// If the policy didn't pick its own delay, fall back
			// to the response's Retry-After header.
			if delay == 0 {
				if hint := t.parseRetryAfter(resp.Header.Get("Retry-After")); hint > 0 {
					delay = hint
				}
			}
		}
		if delay <= 0 {
			continue
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

// Do performs an HTTP request against path (which is joined onto the
// configured base URL).
//
//   - body, when non-nil, is JSON-encoded as the request body.
//   - dst, when non-nil, receives the JSON-decoded response.
//   - On a 2xx response with a nil dst, the body is drained and discarded.
//   - On a non-2xx response, the returned error is *[core.APIError].
func (t *Transport) Do(ctx context.Context, method, path string, body, dst any) error {
	_, err := t.doJSON(ctx, method, path, body, dst)
	return err
}

// DoWithHeaders is Do with the 2xx response's http.Header returned
// alongside the typed payload. Used by generated methods whose spec
// declares response-metadata headers (x-fapi-interaction-id, etc.)
// so the method can populate a per-package ResponseMetadata struct
// without touching global state.
func (t *Transport) DoWithHeaders(ctx context.Context, method, path string, body, dst any) (http.Header, error) {
	return t.doJSON(ctx, method, path, body, dst)
}

func (t *Transport) doJSON(ctx context.Context, method, path string, body, dst any) (http.Header, error) {
	// Marshal once; rebuild the request per attempt so retry can
	// replay the body without consuming it.
	var encoded []byte
	if body != nil {
		var err error
		encoded, err = json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("revolut: encode %s %s body: %w", method, path, err)
		}
	}
	resp, err := t.dispatch(ctx, func() (*http.Request, error) {
		var reader io.Reader
		var contentType string
		if encoded != nil {
			reader = bytes.NewReader(encoded)
			contentType = "application/json"
		}
		return t.newRequestWithBody(ctx, method, path, reader, contentType, "application/json")
	})
	if err != nil {
		return nil, fmt.Errorf("revolut: %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		if dst == nil {
			_, _ = io.Copy(io.Discard, t.limit(resp.Body))
			return resp.Header, nil
		}
		// Read to buffer first so an empty body (expected on 204, and
		// possible on any 2xx with Transfer-Encoding: chunked) is a
		// clean no-op and we don't accidentally call Decode on EOF.
		raw, err := io.ReadAll(t.limit(resp.Body))
		if err != nil {
			return nil, fmt.Errorf("revolut: read %s %s: %w", method, path, err)
		}
		if len(raw) == 0 {
			return resp.Header, nil
		}
		if err := json.Unmarshal(raw, dst); err != nil {
			return nil, fmt.Errorf("revolut: decode %s %s: %w", method, path, err)
		}
		return resp.Header, nil
	}
	return nil, t.decodeError(resp)
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
	build, accept, err := t.rawRequestBuilder(ctx, method, path, r, "application/json")
	if err != nil {
		return nil, nil, err
	}
	_ = accept
	resp, err := t.dispatch(ctx, build)
	if err != nil {
		return nil, nil, fmt.Errorf("revolut: %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, nil, t.decodeError(resp)
	}
	body, err := io.ReadAll(t.limit(resp.Body))
	if err != nil {
		return nil, nil, fmt.Errorf("revolut: read %s %s: %w", method, path, err)
	}
	return body, resp.Header, nil
}

// DoRawStream is like DoRaw but returns the response body as an
// io.ReadCloser instead of buffering the whole thing. Used by
// generated methods whose response is a large non-JSON payload
// (PDF / CSV statements). The caller is responsible for Close().
func (t *Transport) DoRawStream(ctx context.Context, method, path string, r RawRequest) (io.ReadCloser, http.Header, error) {
	build, _, err := t.rawRequestBuilder(ctx, method, path, r, "application/octet-stream")
	if err != nil {
		return nil, nil, err
	}
	resp, err := t.dispatch(ctx, build)
	if err != nil {
		return nil, nil, fmt.Errorf("revolut: %s %s: %w", method, path, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer resp.Body.Close()
		return nil, nil, t.decodeError(resp)
	}
	return &limitReadCloser{rc: resp.Body, r: t.limit(resp.Body)}, resp.Header, nil
}

// rawRequestBuilder validates the RawRequest, materialises any
// io.Reader body into memory (so retry can replay it), and returns
// a builder closure dispatch can call once per attempt.
//
// defaultAccept is used when r.Accept is empty — JSON callers want
// "application/json", streaming callers want
// "application/octet-stream".
func (t *Transport) rawRequestBuilder(ctx context.Context, method, path string, r RawRequest, defaultAccept string) (func() (*http.Request, error), string, error) {
	var bodyBytes []byte
	var contentType string
	switch {
	case r.JSONBody != nil:
		buf, err := json.Marshal(r.JSONBody)
		if err != nil {
			return nil, "", fmt.Errorf("revolut: encode %s %s body: %w", method, path, err)
		}
		bodyBytes = buf
		contentType = "application/json"
	case r.FormBody != nil:
		bodyBytes = []byte(r.FormBody.Encode())
		contentType = "application/x-www-form-urlencoded"
	case r.RawBody != nil:
		if r.RawContentType == "" {
			return nil, "", errors.New("revolut: RawBody set without RawContentType")
		}
		buf, err := io.ReadAll(r.RawBody)
		if err != nil {
			return nil, "", fmt.Errorf("revolut: buffer raw body: %w", err)
		}
		bodyBytes = buf
		contentType = r.RawContentType
	}
	accept := r.Accept
	if accept == "" {
		accept = defaultAccept
	}
	// Defensive copy of caller-supplied headers so a mutation
	// after the call returns (or between retries) doesn't shift
	// the request under us.
	headersCopy := make(http.Header, len(r.Headers))
	for k, vs := range r.Headers {
		switch http.CanonicalHeaderKey(k) {
		case "Content-Type", "Accept", "Authorization", "User-Agent":
			continue
		}
		headersCopy[http.CanonicalHeaderKey(k)] = append([]string(nil), vs...)
	}
	build := func() (*http.Request, error) {
		var reader io.Reader
		if bodyBytes != nil {
			reader = bytes.NewReader(bodyBytes)
		}
		req, err := t.newRequestWithBody(ctx, method, path, reader, contentType, accept)
		if err != nil {
			return nil, err
		}
		for k, vs := range headersCopy {
			req.Header[k] = append([]string(nil), vs...)
		}
		return req, nil
	}
	return build, accept, nil
}

type limitReadCloser struct {
	rc io.ReadCloser
	r  io.Reader
}

func (l *limitReadCloser) Read(p []byte) (int, error) { return l.r.Read(p) }
func (l *limitReadCloser) Close() error               { return l.rc.Close() }

func (t *Transport) limit(r io.Reader) io.Reader {
	if t.maxResponseBytes <= 0 {
		return r
	}
	return &io.LimitedReader{R: r, N: t.maxResponseBytes}
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
		u, err := url.Parse(path)
		if err != nil {
			return nil, fmt.Errorf("revolut: parse path %q: %w", path, err)
		}
		if alt, ok := t.hostAliases[u.Host]; ok {
			u.Host = alt
		}
		return u, nil
	}
	ref, err := url.Parse(path)
	if err != nil {
		return nil, fmt.Errorf("revolut: parse path %q: %w", path, err)
	}
	return t.baseURL.ResolveReference(ref), nil
}

func (t *Transport) decodeError(resp *http.Response) error {
	body, _ := io.ReadAll(t.limit(resp.Body))
	apiErr := &core.APIError{
		StatusCode: resp.StatusCode,
		Body:       body,
		RetryAfter: t.parseRetryAfter(resp.Header.Get("Retry-After")),
	}
	if resp.Request != nil {
		apiErr.Method = resp.Request.Method
		if resp.Request.URL != nil {
			apiErr.URL = resp.Request.URL.String()
		}
	}
	// Surface the server-echoed request correlator if there is one —
	// different Revolut APIs use different casings for the same
	// concept, so check every common spelling.
	for _, name := range []string{"X-Request-Id", "X-Request-ID", "Request-Id", "x-fapi-interaction-id"} {
		if v := resp.Header.Get(name); v != "" {
			apiErr.RequestID = v
			break
		}
	}
	if len(body) > 0 {
		populateErrorFields(apiErr, body)
	}
	return apiErr
}

// populateErrorFields attempts to extract Code / Message / ErrorID
// from the response body without knowing which Revolut API produced
// it — the shapes vary (business has integer code + error_id,
// merchant Error-v2 has string code, revolut-x has uuid error_id +
// timestamp, open-banking uses capitalised keys). Fields the body
// doesn't declare are left zero.
func populateErrorFields(apiErr *core.APIError, body []byte) {
	var aux struct {
		// code: business/open-banking emit an integer, merchant
		// Error-v2 emits a string. json.RawMessage lets us accept
		// either without a type mismatch aborting the whole unmarshal.
		Code    json.RawMessage `json:"code"`
		CodeCap json.RawMessage `json:"Code"`
		Message string          `json:"message"`
		MsgCap  string          `json:"Message"`

		ErrorID    string `json:"error_id"`
		ErrorIDM   string `json:"errorId"`
		ErrorIDCap string `json:"Id"`
	}
	if err := json.Unmarshal(body, &aux); err != nil {
		return
	}
	apiErr.Code = rawCode(aux.Code)
	if apiErr.Code == "" {
		apiErr.Code = rawCode(aux.CodeCap)
	}
	apiErr.Message = aux.Message
	if apiErr.Message == "" {
		apiErr.Message = aux.MsgCap
	}
	apiErr.ErrorID = aux.ErrorID
	if apiErr.ErrorID == "" {
		apiErr.ErrorID = aux.ErrorIDM
	}
	if apiErr.ErrorID == "" {
		apiErr.ErrorID = aux.ErrorIDCap
	}
}

// rawCode turns a json.RawMessage that's either "foo" or 42 into its
// string form, so APIError.Code has a single type regardless of
// which spec produced the error body.
func rawCode(raw json.RawMessage) string {
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" {
		return ""
	}
	// Quoted string: strip quotes.
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		var v string
		if err := json.Unmarshal(raw, &v); err == nil {
			return v
		}
	}
	return s
}

// parseRetryAfter interprets RFC 7231's Retry-After header. The
// header may be either a delta-seconds integer or an HTTP-date;
// both forms are supported. The transport's RetryAfterUnit (seconds
// by default; milliseconds for APIs like revolut-x that document the
// header in ms) scales the integer form. Returns zero on empty or
// malformed input.
func (t *Transport) parseRetryAfter(raw string) time.Duration {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0
	}
	unit := t.retryAfterUnit
	if unit == 0 {
		unit = time.Second
	}
	if n, err := strconv.ParseInt(raw, 10, 64); err == nil && n >= 0 {
		return time.Duration(n) * unit
	}
	if when, err := http.ParseTime(raw); err == nil {
		if d := time.Until(when); d > 0 {
			return d
		}
	}
	return 0
}
