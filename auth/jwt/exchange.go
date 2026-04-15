package jwt

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// clientAssertionType is the RFC 7523 assertion type Revolut expects.
const clientAssertionType = "urn:ietf:params:oauth:client-assertion-type:jwt-bearer"

// TokenResponse mirrors the JSON body of Revolut's /auth/token endpoint.
//
// RefreshToken is populated on the authorization_code grant but typically
// empty on refresh_token grants — the original refresh token remains valid
// until explicitly revoked.
type TokenResponse struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	RefreshToken string `json:"refresh_token,omitempty"`
}

// TokenError carries a failing /auth/token response. Revolut emits
// OAuth 2.0-style error JSON on 4xx.
type TokenError struct {
	StatusCode       int
	Code             string `json:"error"`
	Description      string `json:"error_description"`
	Body             []byte `json:"-"`
}

func (e *TokenError) Error() string {
	switch {
	case e.Description != "" && e.Code != "":
		return fmt.Sprintf("jwt: token exchange %d %s: %s", e.StatusCode, e.Code, e.Description)
	case e.Code != "":
		return fmt.Sprintf("jwt: token exchange %d %s", e.StatusCode, e.Code)
	default:
		return fmt.Sprintf("jwt: token exchange http %d", e.StatusCode)
	}
}

// ExchangeCode swaps a one-time authorization code for an access token and
// a long-lived refresh token. Call this once after the browser consent
// flow; store the refresh token.
func ExchangeCode(ctx context.Context, httpc *http.Client, tokenURL string, signer *Signer, code string) (*TokenResponse, error) {
	if code == "" {
		return nil, fmt.Errorf("jwt: code is required")
	}
	assertion, err := signer.Sign()
	if err != nil {
		return nil, err
	}
	form := url.Values{
		"grant_type":            {"authorization_code"},
		"code":                  {code},
		"client_assertion_type": {clientAssertionType},
		"client_assertion":      {assertion},
	}
	return postTokenRequest(ctx, httpc, tokenURL, form)
}

// Refresh uses a refresh token to fetch a new access token. The response's
// RefreshToken field is typically empty; keep using the original.
func Refresh(ctx context.Context, httpc *http.Client, tokenURL string, signer *Signer, refreshToken string) (*TokenResponse, error) {
	if refreshToken == "" {
		return nil, fmt.Errorf("jwt: refreshToken is required")
	}
	assertion, err := signer.Sign()
	if err != nil {
		return nil, err
	}
	form := url.Values{
		"grant_type":            {"refresh_token"},
		"refresh_token":         {refreshToken},
		"client_assertion_type": {clientAssertionType},
		"client_assertion":      {assertion},
	}
	return postTokenRequest(ctx, httpc, tokenURL, form)
}

func postTokenRequest(ctx context.Context, httpc *http.Client, tokenURL string, form url.Values) (*TokenResponse, error) {
	if httpc == nil {
		httpc = http.DefaultClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("jwt: build token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := httpc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("jwt: token request: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("jwt: read token response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		tokErr := &TokenError{StatusCode: resp.StatusCode, Body: body}
		_ = json.Unmarshal(body, tokErr)
		return nil, tokErr
	}
	var tr TokenResponse
	if err := json.Unmarshal(body, &tr); err != nil {
		return nil, fmt.Errorf("jwt: decode token response: %w", err)
	}
	return &tr, nil
}
