//go:build sandbox

package revolut_test

import (
	"context"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	revolut "github.com/greatliontech/revolut-go"
	"github.com/greatliontech/revolut-go/openbanking"
)

// obSandboxConfig mirrors the credentials.json shape ob-jwks +
// the cert flow write to ~/.config/revolut-go/sandbox/openbanking.
// Override the directory with REVOLUT_OB_SANDBOX_DIR.
type obSandboxConfig struct {
	ClientID               string `json:"client_id"`
	Kid                    string `json:"kid"`
	SubjectDN              string `json:"subject_dn"`
	OrganizationIdentifier string `json:"organization_identifier"`
	Alg                    string `json:"alg"`
	PrivateKeyFile         string `json:"private_key"`
	SigningCertFile        string `json:"signing_cert"`
	TransportCertFile      string `json:"transport_cert"`

	dir string // populated by loadOBSandbox; resolves the relative file fields
}

// loadOBSandbox reads credentials.json + the cert/key files. Skips
// the test when the directory is missing so a fresh checkout
// doesn't fail under `go test -tags sandbox ./...`.
func loadOBSandbox(t *testing.T) obSandboxConfig {
	t.Helper()
	dir := os.Getenv("REVOLUT_OB_SANDBOX_DIR")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			t.Skipf("cannot locate home dir: %v", err)
		}
		dir = filepath.Join(home, ".config", "revolut-go", "sandbox", "openbanking")
	}
	credPath := filepath.Join(dir, "credentials.json")
	raw, err := os.ReadFile(credPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			t.Skipf("OB sandbox credentials missing (%s)", credPath)
		}
		t.Fatalf("read OB credentials: %v", err)
	}
	var cfg obSandboxConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("parse OB credentials: %v", err)
	}
	if cfg.ClientID == "" || cfg.Kid == "" {
		t.Fatalf("OB credentials missing client_id or kid: %+v", cfg)
	}
	cfg.dir = dir
	return cfg
}

// loadPrivateKey reads the RSA private key matching cfg.PrivateKeyFile.
func (cfg obSandboxConfig) loadPrivateKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(cfg.dir, cfg.PrivateKeyFile))
	if err != nil {
		t.Fatalf("read private key: %v", err)
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		t.Fatalf("private key not PEM")
	}
	switch block.Type {
	case "RSA PRIVATE KEY":
		k, err := x509.ParsePKCS1PrivateKey(block.Bytes)
		if err != nil {
			t.Fatalf("parse PKCS#1 key: %v", err)
		}
		return k
	case "PRIVATE KEY":
		k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			t.Fatalf("parse PKCS#8 key: %v", err)
		}
		rsaKey, ok := k.(*rsa.PrivateKey)
		if !ok {
			t.Fatalf("PKCS#8 key is %T", k)
		}
		return rsaKey
	}
	t.Fatalf("unsupported PEM block %q", block.Type)
	return nil
}

// noopAuth satisfies revolut.Authenticator without setting any
// header — used by the MTLS-only canary so we can call
// GetDistinguishedName before the token-source path is exercised.
type noopAuth struct{}

func (noopAuth) Apply(*http.Request) error { return nil }

// obMTLSClient builds the *http.Client both the OB SDK and the
// token source dispatch through. Sharing one client lets the
// connection pool reuse handshakes between the /token call and
// the API calls.
//
// Trusts the OBIE Pre-Production CA bundle on top of the system
// roots; Revolut's sandbox edge presents a cert signed by that
// chain, which isn't in any system pool by default.
func obMTLSClient(t *testing.T, cfg obSandboxConfig) *http.Client {
	t.Helper()
	cert, err := openbanking.LoadTransportCert(
		filepath.Join(cfg.dir, cfg.TransportCertFile),
		filepath.Join(cfg.dir, cfg.PrivateKeyFile),
	)
	if err != nil {
		t.Fatalf("LoadTransportCert: %v", err)
	}
	bundle, err := os.ReadFile(filepath.Join(cfg.dir, "obie-pp-ca-bundle.pem"))
	if err != nil {
		t.Fatalf("read OBIE PP CA bundle: %v", err)
	}
	return openbanking.MTLSHTTPClient(cert, bundle)
}

// TestSandbox_OpenBanking_DistinguishedName is the MTLS-only
// canary. The endpoint returns the DN extracted from the TLS
// client certificate Revolut sees during the handshake, so a
// successful 200 here proves the transport cert + key are
// correctly wired into the *http.Client. No access token, no
// JWKS fetch, no JWS — just MTLS.
func TestSandbox_OpenBanking_DistinguishedName(t *testing.T) {
	cfg := loadOBSandbox(t)
	mtls := obMTLSClient(t, cfg)
	client, err := revolut.NewOpenBankingClient(noopAuth{},
		revolut.WithEnvironment(revolut.EnvironmentSandbox),
		revolut.WithHTTPClient(mtls),
	)
	if err != nil {
		t.Fatalf("NewOpenBankingClient: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	dn, err := client.Applications.GetDistinguishedName(ctx)
	if err != nil {
		t.Fatalf("GetDistinguishedName: %v", err)
	}
	if dn == nil || dn.TLSClientAuthDn == "" {
		t.Fatalf("empty DN response: %+v", dn)
	}
	// Sanity-check Revolut sees our cert: the DN they echo back
	// must contain the CN we provisioned. The full string varies
	// in attribute ordering across implementations, so we check
	// the CN substring rather than equality.
	wantCN := "2kiXQyo0tedjW2somjSgH7"
	if !strings.Contains(dn.TLSClientAuthDn, wantCN) {
		t.Errorf("DN echoed back does not contain CN %q; got %q", wantCN, dn.TLSClientAuthDn)
	}
	t.Logf("transport cert DN as Revolut sees it: %s", dn.TLSClientAuthDn)
}

// TestSandbox_OpenBanking_TokenSourceMintsAccessToken exercises
// just the token-source half: builds a ClientCredentialsTokenSource,
// asks it for a token, asserts a non-empty bearer comes back. If
// this passes, the JWS + JWKS-publish + MTLS-to-/token chain all
// work; failures here pinpoint the auth flow without dragging in
// a real API call.
func TestSandbox_OpenBanking_TokenSourceMintsAccessToken(t *testing.T) {
	cfg := loadOBSandbox(t)
	key := cfg.loadPrivateKey(t)
	cert, err := openbanking.LoadTransportCert(
		filepath.Join(cfg.dir, cfg.TransportCertFile),
		filepath.Join(cfg.dir, cfg.PrivateKeyFile),
	)
	if err != nil {
		t.Fatalf("LoadTransportCert: %v", err)
	}
	bundle, err := os.ReadFile(filepath.Join(cfg.dir, "obie-pp-ca-bundle.pem"))
	if err != nil {
		t.Fatalf("read OBIE PP CA bundle: %v", err)
	}
	src, err := openbanking.NewClientCredentialsTokenSource(openbanking.ClientCredentialsConfig{
		ClientID:      cfg.ClientID,
		TokenURL:      "https://sandbox-oba-auth.revolut.com/token",
		Kid:           cfg.Kid,
		PrivateKey:    key,
		TransportCert: cert,
		Alg:           coalesceAlg(cfg.Alg),
		HTTPClient:    openbanking.MTLSHTTPClient(cert, bundle),
		// No scope here — accept whatever Revolut grants by default.
	})
	if err != nil {
		t.Fatalf("NewClientCredentialsTokenSource: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	tok, err := src.Token(ctx)
	if err != nil {
		var tokErr *openbanking.TokenError
		if errors.As(err, &tokErr) {
			t.Fatalf("Token: %s\nbody: %s", tokErr.Error(), tokErr.Body)
		}
		t.Fatalf("Token: %v", err)
	}
	if tok == "" {
		t.Fatal("empty access token")
	}
	t.Logf("got access token: %d bytes", len(tok))
}

// TestSandbox_OpenBanking_GetApplication closes the loop: token
// source as the SDK's Authenticator, hit a real API call that
// requires a bearer token, assert Revolut returns our app's
// metadata. End-to-end proof that JWS signing + JWKS fetch + MTLS
// + token mint + token acceptance all work.
func TestSandbox_OpenBanking_GetApplication(t *testing.T) {
	cfg := loadOBSandbox(t)
	key := cfg.loadPrivateKey(t)
	cert, err := openbanking.LoadTransportCert(
		filepath.Join(cfg.dir, cfg.TransportCertFile),
		filepath.Join(cfg.dir, cfg.PrivateKeyFile),
	)
	if err != nil {
		t.Fatalf("LoadTransportCert: %v", err)
	}
	bundle, err := os.ReadFile(filepath.Join(cfg.dir, "obie-pp-ca-bundle.pem"))
	if err != nil {
		t.Fatalf("read OBIE PP CA bundle: %v", err)
	}
	mtls := openbanking.MTLSHTTPClient(cert, bundle)
	src, err := openbanking.NewClientCredentialsTokenSource(openbanking.ClientCredentialsConfig{
		ClientID:      cfg.ClientID,
		TokenURL:      "https://sandbox-oba-auth.revolut.com/token",
		Kid:           cfg.Kid,
		PrivateKey:    key,
		TransportCert: cert,
		Alg:           coalesceAlg(cfg.Alg),
		HTTPClient:    mtls,
	})
	if err != nil {
		t.Fatalf("NewClientCredentialsTokenSource: %v", err)
	}
	client, err := revolut.NewOpenBankingClient(src,
		revolut.WithEnvironment(revolut.EnvironmentSandbox),
		revolut.WithHTTPClient(mtls),
	)
	if err != nil {
		t.Fatalf("NewOpenBankingClient: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	app, err := client.Applications.Get(ctx, cfg.ClientID)
	if err != nil {
		t.Fatalf("Applications.Get: %v", err)
	}
	if app == nil {
		t.Fatal("nil application response")
	}
	t.Logf("Applications.Get OK: %+v", app)
}

func coalesceAlg(a string) string {
	if a == "" {
		return openbanking.AlgPS256
	}
	return a
}
