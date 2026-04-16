package openbanking

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/url"
)

// BuildAuthorizationURL assembles the consent URL. The signed
// request_object goes into ?request=<jws>, and the FAPI-required
// duplicate parameters (response_type, client_id, redirect_uri,
// scope, state, nonce) appear at URL level too.
//
// The caller is responsible for opening the URL in a browser and
// capturing the redirect at the registered redirect_uri. See
// cmd/ob-bootstrap for a working callback server.
func BuildAuthorizationURL(authzEndpoint string, params AuthorizationURLParams, requestObjectJWT string) (string, error) {
	if authzEndpoint == "" {
		return "", errors.New("openbanking: BuildAuthorizationURL needs an authorization endpoint")
	}
	if requestObjectJWT == "" {
		return "", errors.New("openbanking: BuildAuthorizationURL needs a signed request object")
	}
	u, err := url.Parse(authzEndpoint)
	if err != nil {
		return "", fmt.Errorf("openbanking: parse authorization endpoint: %w", err)
	}
	q := u.Query()
	q.Set("response_type", "code id_token")
	q.Set("client_id", params.ClientID)
	q.Set("redirect_uri", params.RedirectURI)
	q.Set("scope", params.Scope)
	q.Set("state", params.State)
	q.Set("nonce", params.Nonce)
	q.Set("request", requestObjectJWT)
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// AuthorizationURLParams carries the URL-level OAuth parameters
// the FAPI spec requires alongside the signed request object.
// Each value MUST also appear in the request_object JWT — keep
// them in sync.
type AuthorizationURLParams struct {
	ClientID    string
	RedirectURI string
	Scope       string
	State       string
	Nonce       string
}

// RandomState returns a 128-bit URL-safe random token suitable
// for the OAuth `state` and OIDC `nonce` parameters.
func RandomState() (string, error) {
	buf := make([]byte, 16)
	if _, err := io.ReadFull(rand.Reader, buf); err != nil {
		return "", fmt.Errorf("openbanking: random state: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// AuthorizationCallback parses the query string of the redirect
// URL Revolut calls back to. Returns the auth code on success or
// the AS-reported error on failure.
type AuthorizationCallback struct {
	Code      string
	State     string
	IDToken   string
	Error     string
	ErrorDesc string
}

// ParseAuthorizationCallback inspects the redirect URL parameters
// (typically captured from /callback?code=…&state=… or from the
// fragment in implicit-style callbacks).
func ParseAuthorizationCallback(rawQuery string) (AuthorizationCallback, error) {
	q, err := url.ParseQuery(rawQuery)
	if err != nil {
		return AuthorizationCallback{}, fmt.Errorf("openbanking: parse callback: %w", err)
	}
	cb := AuthorizationCallback{
		Code:      q.Get("code"),
		State:     q.Get("state"),
		IDToken:   q.Get("id_token"),
		Error:     q.Get("error"),
		ErrorDesc: q.Get("error_description"),
	}
	if cb.Error != "" {
		return cb, fmt.Errorf("openbanking: authorization failed: %s: %s", cb.Error, cb.ErrorDesc)
	}
	if cb.Code == "" {
		return cb, errors.New("openbanking: callback missing ?code=")
	}
	return cb, nil
}

// SplitCallbackQueryAndFragment splits a full redirect URL into
// its query and fragment parts so the caller can pass either to
// ParseAuthorizationCallback. OBIE response_type=code+id_token
// puts the code in the query and the id_token in the fragment;
// the helper merges both.
func SplitCallbackQueryAndFragment(rawURL string) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", err
	}
	if u.Fragment == "" {
		return u.RawQuery, nil
	}
	if u.RawQuery == "" {
		return u.Fragment, nil
	}
	return u.RawQuery + "&" + u.Fragment, nil
}
