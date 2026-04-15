package transport

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/greatliontech/revolut-go/internal/core"
)

type authFunc func(*http.Request) error

func (f authFunc) Apply(r *http.Request) error { return f(r) }

func newTransport(t *testing.T, srv *httptest.Server, auth core.Authenticator) *Transport {
	t.Helper()
	base := srv.URL + "/api/"
	tr, err := New(Config{
		BaseURL: base,
		Auth:    auth,
		UserAgent: "revolut-go-test",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return tr
}

func TestDo_RelativePathsAgainstBase(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	tr := newTransport(t, srv, nil)

	var out struct {
		OK bool `json:"ok"`
	}
	if err := tr.Do(context.Background(), http.MethodGet, "accounts/42", nil, &out); err != nil {
		t.Fatalf("Do: %v", err)
	}
	if gotPath != "/api/accounts/42" {
		t.Errorf("server saw path %q; want /api/accounts/42", gotPath)
	}
	if !out.OK {
		t.Error("OK false")
	}
}

func TestDo_AppliesAuthAndUserAgent(t *testing.T) {
	var hUA, hAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hUA = r.Header.Get("User-Agent")
		hAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	auth := authFunc(func(r *http.Request) error {
		r.Header.Set("Authorization", "Bearer test-token")
		return nil
	})
	tr := newTransport(t, srv, auth)
	if err := tr.Do(context.Background(), http.MethodGet, "ping", nil, nil); err != nil {
		t.Fatal(err)
	}
	if hUA != "revolut-go-test" {
		t.Errorf("ua: %q", hUA)
	}
	if hAuth != "Bearer test-token" {
		t.Errorf("auth: %q", hAuth)
	}
}

func TestDo_AuthFailurePropagates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("server should not be reached")
	}))
	defer srv.Close()
	boom := errors.New("auth boom")
	tr := newTransport(t, srv, authFunc(func(*http.Request) error { return boom }))
	err := tr.Do(context.Background(), http.MethodGet, "ping", nil, nil)
	if err == nil || !strings.Contains(err.Error(), "auth boom") {
		t.Errorf("want auth error, got %v", err)
	}
}

func TestDo_APIErrorOnNon2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"code":1234,"message":"bad input"}`))
	}))
	defer srv.Close()
	tr := newTransport(t, srv, nil)
	err := tr.Do(context.Background(), http.MethodGet, "x", nil, nil)
	apiErr, ok := core.AsAPIError(err)
	if !ok {
		t.Fatalf("want APIError, got %T: %v", err, err)
	}
	if apiErr.StatusCode != 400 {
		t.Errorf("status: %d", apiErr.StatusCode)
	}
	if apiErr.Code != 1234 || apiErr.Message != "bad input" {
		t.Errorf("code/message: %d %q", apiErr.Code, apiErr.Message)
	}
}

func TestDo_APIErrorWithOpaqueBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("<html>500</html>"))
	}))
	defer srv.Close()
	tr := newTransport(t, srv, nil)
	err := tr.Do(context.Background(), http.MethodGet, "x", nil, nil)
	apiErr, ok := core.AsAPIError(err)
	if !ok {
		t.Fatalf("want APIError, got %T", err)
	}
	if apiErr.StatusCode != 500 {
		t.Errorf("status: %d", apiErr.StatusCode)
	}
	if string(apiErr.Body) != "<html>500</html>" {
		t.Errorf("body not preserved: %q", apiErr.Body)
	}
	// Message/Code should be zero-value when the body isn't JSON.
	if apiErr.Message != "" || apiErr.Code != 0 {
		t.Errorf("non-JSON body polluted Code/Message: %d %q", apiErr.Code, apiErr.Message)
	}
}

func TestDo_ContextCancellation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer srv.Close()
	tr := newTransport(t, srv, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := tr.Do(ctx, http.MethodGet, "x", nil, nil)
	if err == nil {
		t.Fatal("want cancellation error")
	}
}

func TestDoRaw_FormBody(t *testing.T) {
	var gotCT string
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotCT = r.Header.Get("Content-Type")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()
	tr := newTransport(t, srv, nil)
	body, _, err := tr.DoRaw(context.Background(), http.MethodPost, "x", RawRequest{
		FormBody: url.Values{"a": {"1"}, "b": {"two"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "ok" {
		t.Errorf("body: %q", body)
	}
	if gotCT != "application/x-www-form-urlencoded" {
		t.Errorf("content-type: %q", gotCT)
	}
	if !strings.Contains(gotBody, "a=1") || !strings.Contains(gotBody, "b=two") {
		t.Errorf("form body: %q", gotBody)
	}
}

func TestDoRaw_RawBodyAndAccept(t *testing.T) {
	var gotCT, gotAccept string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotCT = r.Header.Get("Content-Type")
		gotAccept = r.Header.Get("Accept")
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/pdf")
		_, _ = w.Write([]byte("%PDF-1.4"))
	}))
	defer srv.Close()
	tr := newTransport(t, srv, nil)
	body, hdr, err := tr.DoRaw(context.Background(), http.MethodPost, "x", RawRequest{
		RawBody:        bytes.NewReader([]byte("raw-blob")),
		RawContentType: "application/octet-stream",
		Accept:         "application/pdf",
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotCT != "application/octet-stream" {
		t.Errorf("ct: %q", gotCT)
	}
	if gotAccept != "application/pdf" {
		t.Errorf("accept: %q", gotAccept)
	}
	if string(gotBody) != "raw-blob" {
		t.Errorf("body: %q", gotBody)
	}
	if string(body) != "%PDF-1.4" {
		t.Errorf("resp body: %q", body)
	}
	if hdr.Get("Content-Type") != "application/pdf" {
		t.Errorf("resp ct: %q", hdr.Get("Content-Type"))
	}
}

func TestDoRaw_RawBodyMissingContentType(t *testing.T) {
	tr := &Transport{} // only needs BaseURL-unaware paths
	_, _, err := tr.DoRaw(context.Background(), http.MethodPost, "x", RawRequest{
		RawBody: bytes.NewReader([]byte("x")),
	})
	if err == nil || !strings.Contains(err.Error(), "RawContentType") {
		t.Errorf("want explicit error about RawContentType; got %v", err)
	}
}

func TestNew_ValidatesBaseURL(t *testing.T) {
	if _, err := New(Config{}); err == nil {
		t.Error("want error for missing BaseURL")
	}
	if _, err := New(Config{BaseURL: "://bad"}); err == nil {
		t.Error("want error for malformed BaseURL")
	}
}
