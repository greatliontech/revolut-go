package jwt

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func newTestSigner(t *testing.T) *Signer {
	t.Helper()
	s, err := NewSigner(Config{
		PrivateKey: testKey(t),
		Issuer:     "example.com",
		ClientID:   "cid",
		Now:        func() time.Time { return time.Unix(0, 0) },
	})
	if err != nil {
		t.Fatalf("NewSigner: %v", err)
	}
	return s
}

func TestExchangeCode_Success(t *testing.T) {
	t.Parallel()
	signer := newTestSigner(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method: %s", r.Method)
		}
		if got := r.Header.Get("Content-Type"); got != "application/x-www-form-urlencoded" {
			t.Errorf("content-type: %s", got)
		}
		if err := r.ParseForm(); err != nil {
			t.Fatalf("parse form: %v", err)
		}
		if r.FormValue("grant_type") != "authorization_code" {
			t.Errorf("grant_type: %s", r.FormValue("grant_type"))
		}
		if r.FormValue("code") != "the-code" {
			t.Errorf("code: %s", r.FormValue("code"))
		}
		if r.FormValue("client_assertion_type") != clientAssertionType {
			t.Errorf("assertion_type: %s", r.FormValue("client_assertion_type"))
		}
		if !strings.Contains(r.FormValue("client_assertion"), ".") {
			t.Errorf("client_assertion not a JWT: %s", r.FormValue("client_assertion"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"access_token":"at","token_type":"bearer","expires_in":2399,"refresh_token":"rt"}`)
	}))
	defer srv.Close()

	tr, err := ExchangeCode(context.Background(), srv.Client(), srv.URL, signer, "the-code")
	if err != nil {
		t.Fatalf("ExchangeCode: %v", err)
	}
	if tr.AccessToken != "at" || tr.RefreshToken != "rt" || tr.ExpiresIn != 2399 {
		t.Fatalf("unexpected response: %+v", tr)
	}
}

func TestExchangeCode_EmptyCode(t *testing.T) {
	t.Parallel()
	signer := newTestSigner(t)
	if _, err := ExchangeCode(context.Background(), nil, "http://unused", signer, ""); err == nil {
		t.Fatal("want error for empty code")
	}
}

func TestExchangeCode_ServerError(t *testing.T) {
	t.Parallel()
	signer := newTestSigner(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":"invalid_grant","error_description":"expired code"}`)
	}))
	defer srv.Close()

	_, err := ExchangeCode(context.Background(), srv.Client(), srv.URL, signer, "x")
	if err == nil {
		t.Fatal("want error")
	}
	var tokErr *TokenError
	if !errors.As(err, &tokErr) {
		t.Fatalf("want *TokenError, got %T", err)
	}
	if tokErr.StatusCode != http.StatusBadRequest {
		t.Errorf("status: %d", tokErr.StatusCode)
	}
	if tokErr.Code != "invalid_grant" || tokErr.Description != "expired code" {
		t.Errorf("parsed fields: %+v", tokErr)
	}
}

func TestRefresh_Success(t *testing.T) {
	t.Parallel()
	signer := newTestSigner(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.FormValue("grant_type") != "refresh_token" {
			t.Errorf("grant_type: %s", r.FormValue("grant_type"))
		}
		if r.FormValue("refresh_token") != "rt" {
			t.Errorf("refresh_token: %s", r.FormValue("refresh_token"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"access_token":"at2","token_type":"bearer","expires_in":2399}`)
	}))
	defer srv.Close()

	tr, err := Refresh(context.Background(), srv.Client(), srv.URL, signer, "rt")
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if tr.AccessToken != "at2" || tr.RefreshToken != "" {
		t.Fatalf("unexpected response: %+v", tr)
	}
}

func TestRefresh_EmptyRefreshToken(t *testing.T) {
	t.Parallel()
	signer := newTestSigner(t)
	if _, err := Refresh(context.Background(), nil, "http://unused", signer, ""); err == nil {
		t.Fatal("want error for empty refresh token")
	}
}
