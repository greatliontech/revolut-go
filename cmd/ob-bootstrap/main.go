// ob-bootstrap walks a developer through the one-time Revolut
// Open Banking PSU consent flow and persists the resulting access
// + refresh token pair so integration tests can hydrate
// AuthCodeTokenSource without re-prompting the user.
//
// Steps:
//
//  1. Mint a client_credentials access token (TPP context).
//  2. POST a CreateAccountAccessConsents request → ConsentId.
//  3. Sign a request_object JWT, build the consent URL.
//  4. Print the URL (and optionally open the browser).
//  5. Host an HTTPS callback on the registered redirect URI,
//     capture the ?code= + state.
//  6. Exchange code for token at /token (auth_code grant).
//  7. Write the resulting AuthCodeToken to disk.
//
// Inputs: ~/.config/revolut-go/sandbox/openbanking/credentials.json
// (the same file the integration test reads), the OBIE PP CA
// bundle in the same directory, and the registered redirect URI
// matching what was configured in the Revolut developer portal.
//
// Output: tokens-aisp.json next to credentials.json.
package main

import (
	"context"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/greatliontech/revolut-go/openbanking"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "ob-bootstrap:", err)
		os.Exit(1)
	}
}

type config struct {
	dir         string
	scope       string
	listenAddr  string
	redirectURI string
	openBrowser bool
	output      string
	authzURL    string
	tokenURL    string
	audience    string
	apiHost     string
	tlsCertPEM  string
	tlsKeyPEM   string
}

type credentials struct {
	ClientID          string `json:"client_id"`
	Kid               string `json:"kid"`
	Alg               string `json:"alg"`
	PrivateKeyFile    string `json:"private_key"`
	SigningCertFile   string `json:"signing_cert"`
	TransportCertFile string `json:"transport_cert"`
}

func run() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	defaultDir := filepath.Join(home, ".config", "revolut-go", "sandbox", "openbanking")
	cfg := config{
		dir:         defaultDir,
		scope:       "openid accounts",
		listenAddr:  "127.0.0.1:8787",
		redirectURI: "https://127.0.0.1:8787/callback",
		openBrowser: true,
		output:      "",
		// Sandbox defaults; flip via flags for production.
		authzURL: "https://sandbox-oba-auth.revolut.com/ui/index.html",
		tokenURL: "https://sandbox-oba-auth.revolut.com/token",
		audience: "https://sandbox-oba-auth.revolut.com/",
		apiHost:  "https://sandbox-oba-auth.revolut.com",
	}
	flag.StringVar(&cfg.dir, "dir", cfg.dir, "directory containing credentials.json + cert/key files")
	flag.StringVar(&cfg.scope, "scope", cfg.scope, "OAuth scope to request (openid accounts | openid payments)")
	flag.StringVar(&cfg.listenAddr, "addr", cfg.listenAddr, "local listen address for the callback server")
	flag.StringVar(&cfg.redirectURI, "redirect", cfg.redirectURI, "redirect URI registered with Revolut")
	flag.BoolVar(&cfg.openBrowser, "open", cfg.openBrowser, "auto-open the consent URL in the default browser")
	flag.StringVar(&cfg.output, "out", cfg.output, "tokens output path (default: <dir>/tokens-aisp.json or tokens-pisp.json by scope)")
	flag.StringVar(&cfg.authzURL, "authz-url", cfg.authzURL, "authorization endpoint URL")
	flag.StringVar(&cfg.tokenURL, "token-url", cfg.tokenURL, "/token endpoint URL")
	flag.StringVar(&cfg.audience, "audience", cfg.audience, "request_object aud claim")
	flag.StringVar(&cfg.apiHost, "api-host", cfg.apiHost, "base URL for the AISP CreateAccountAccessConsents call")
	flag.StringVar(&cfg.tlsCertPEM, "tls-cert", "", "PEM file for the local HTTPS callback (defaults to a self-signed in-memory cert)")
	flag.StringVar(&cfg.tlsKeyPEM, "tls-key", "", "PEM key for the local HTTPS callback (defaults to a self-signed in-memory key)")
	flag.Parse()

	if cfg.output == "" {
		// Pick output filename from scope so AISP and PISP runs
		// don't trample each other's tokens.
		switch {
		case strings.Contains(cfg.scope, "payments"):
			cfg.output = filepath.Join(cfg.dir, "tokens-pisp.json")
		default:
			cfg.output = filepath.Join(cfg.dir, "tokens-aisp.json")
		}
	}

	creds, err := loadCredentials(cfg.dir)
	if err != nil {
		return err
	}
	priv, err := loadPrivateKey(filepath.Join(cfg.dir, creds.PrivateKeyFile))
	if err != nil {
		return err
	}
	mtlsCert, err := openbanking.LoadTransportCert(
		filepath.Join(cfg.dir, creds.TransportCertFile),
		filepath.Join(cfg.dir, creds.PrivateKeyFile),
	)
	if err != nil {
		return err
	}
	bundle, err := os.ReadFile(filepath.Join(cfg.dir, "obie-pp-ca-bundle.pem"))
	if err != nil {
		return fmt.Errorf("read OBIE CA bundle: %w", err)
	}
	mtlsHTTPC := openbanking.MTLSHTTPClient(mtlsCert, bundle)

	ctx := context.Background()

	// Step 1 — client_credentials access token (TPP context).
	cc, err := openbanking.NewClientCredentialsTokenSource(openbanking.ClientCredentialsConfig{
		ClientID:      creds.ClientID,
		TokenURL:      cfg.tokenURL,
		Kid:           creds.Kid,
		PrivateKey:    priv,
		TransportCert: mtlsCert,
		Alg:           creds.Alg,
		HTTPClient:    mtlsHTTPC,
		Scope:         scopeForCC(cfg.scope),
	})
	if err != nil {
		return err
	}
	tppToken, err := cc.Token(ctx)
	if err != nil {
		return fmt.Errorf("client_credentials token: %w", err)
	}
	fmt.Printf("step 1 ok: TPP access token (%d bytes)\n", len(tppToken))

	// Step 2 — create the consent (AISP or PISP).
	consentID, err := createConsent(ctx, mtlsHTTPC, cfg, tppToken)
	if err != nil {
		return fmt.Errorf("create consent: %w", err)
	}
	fmt.Printf("step 2 ok: consent_id=%s\n", consentID)

	// Step 3 — sign the request_object and build the auth URL.
	state, err := openbanking.RandomState()
	if err != nil {
		return err
	}
	nonce, err := openbanking.RandomState()
	if err != nil {
		return err
	}
	roJWT, err := openbanking.SignRequestObject(openbanking.RequestObjectConfig{
		ClientID:    creds.ClientID,
		Audience:    cfg.audience,
		RedirectURI: cfg.redirectURI,
		Scope:       cfg.scope,
		ConsentID:   consentID,
		State:       state,
		Nonce:       nonce,
		Kid:         creds.Kid,
		PrivateKey:  priv,
		Alg:         creds.Alg,
	})
	if err != nil {
		return fmt.Errorf("sign request_object: %w", err)
	}
	authURL, err := openbanking.BuildAuthorizationURL(cfg.authzURL,
		openbanking.AuthorizationURLParams{
			ClientID:    creds.ClientID,
			RedirectURI: cfg.redirectURI,
			Scope:       cfg.scope,
			State:       state,
			Nonce:       nonce,
		}, roJWT)
	if err != nil {
		return fmt.Errorf("build auth URL: %w", err)
	}

	fmt.Println()
	fmt.Println("step 3 ok. Open this URL in your browser to authorise the test PSU:")
	fmt.Println()
	fmt.Println("    ", authURL)
	fmt.Println()
	fmt.Println("(Your browser will warn about the self-signed cert on the local callback — accept it.)")
	if cfg.openBrowser {
		_ = openInBrowser(authURL)
	}

	// Step 4 — host the callback, capture ?code= + state.
	code, err := captureCode(cfg, state)
	if err != nil {
		return err
	}
	fmt.Println("step 4 ok: captured authorization code")

	// Step 5 — exchange code for tokens.
	tok, err := openbanking.ExchangeAuthCode(ctx, openbanking.AuthCodeConfig{
		ClientID:      creds.ClientID,
		TokenURL:      cfg.tokenURL,
		Kid:           creds.Kid,
		PrivateKey:    priv,
		TransportCert: mtlsCert,
		Alg:           creds.Alg,
		HTTPClient:    mtlsHTTPC,
	}, code, cfg.redirectURI)
	if err != nil {
		var tokErr *openbanking.TokenError
		if errors.As(err, &tokErr) {
			fmt.Fprintf(os.Stderr, "/token error body: %s\n", tokErr.Body)
		}
		return fmt.Errorf("exchange code: %w", err)
	}

	// Persist + alongside the consent ID so the test knows which
	// resource it's bound to.
	out := map[string]any{
		"token":      tok,
		"consent_id": consentID,
		"scope":      cfg.scope,
		"saved_at":   time.Now().UTC(),
	}
	if err := writeJSON(cfg.output, out); err != nil {
		return err
	}
	fmt.Printf("step 5 ok: tokens written to %s\n", cfg.output)
	fmt.Printf("  access_token expires at %s\n", tok.ExpiresAt.Format(time.RFC3339))
	if tok.RefreshToken == "" {
		fmt.Println("  WARNING: no refresh_token returned — the source can't renew without a fresh consent")
	}
	return nil
}

func loadCredentials(dir string) (*credentials, error) {
	raw, err := os.ReadFile(filepath.Join(dir, "credentials.json"))
	if err != nil {
		return nil, fmt.Errorf("read credentials.json: %w", err)
	}
	var c credentials
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, fmt.Errorf("parse credentials.json: %w", err)
	}
	if c.ClientID == "" || c.Kid == "" {
		return nil, errors.New("credentials.json missing client_id or kid")
	}
	return &c, nil
}

func loadPrivateKey(path string) (*rsa.PrivateKey, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		return nil, fmt.Errorf("no PEM block in %s", path)
	}
	switch block.Type {
	case "RSA PRIVATE KEY":
		return x509.ParsePKCS1PrivateKey(block.Bytes)
	case "PRIVATE KEY":
		k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, err
		}
		rsaKey, ok := k.(*rsa.PrivateKey)
		if !ok {
			return nil, fmt.Errorf("PKCS#8 key is %T", k)
		}
		return rsaKey, nil
	}
	return nil, fmt.Errorf("unsupported PEM block %q", block.Type)
}

// scopeForCC returns the scope to request on the
// client_credentials call. AISP consent creation needs the
// "accounts" scope; PISP needs "payments". Both grant flows
// add "openid" later for the PSU auth-code exchange.
func scopeForCC(consentScope string) string {
	switch {
	case strings.Contains(consentScope, "payments"):
		return "payments"
	default:
		return "accounts"
	}
}

// createConsent posts the appropriate consent body for the
// requested scope. AISP consents specify a permission set; PISP
// consents go through Signer.Sign and aren't wired here yet —
// the PISP version follows in a separate iteration.
func createConsent(ctx context.Context, httc *http.Client, cfg config, accessToken string) (string, error) {
	if strings.Contains(cfg.scope, "payments") {
		return "", errors.New("ob-bootstrap: PISP consent flow not wired yet; supply -scope=\"openid accounts\" for now")
	}
	body := strings.NewReader(`{
        "Data":{"Permissions":[
            "ReadAccountsBasic","ReadAccountsDetail",
            "ReadBalances",
            "ReadTransactionsBasic","ReadTransactionsDetail","ReadTransactionsCredits","ReadTransactionsDebits"
        ]},
        "Risk":{}
    }`)
	endpoint := strings.TrimRight(cfg.apiHost, "/") + "/account-access-consents"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, body)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("x-fapi-financial-id", "001580000103UAvAAM")
	resp, err := httc.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	respBody, _ := readAll(resp)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("/account-access-consents %d: %s", resp.StatusCode, respBody)
	}
	var parsed struct {
		Data struct {
			ConsentID string `json:"ConsentId"`
		} `json:"Data"`
	}
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return "", fmt.Errorf("parse consent response: %w", err)
	}
	if parsed.Data.ConsentID == "" {
		return "", fmt.Errorf("consent response missing ConsentId; body=%s", respBody)
	}
	return parsed.Data.ConsentID, nil
}

func readAll(resp *http.Response) ([]byte, error) {
	const max = 1 << 20 // cap at 1 MiB; consent responses are tiny.
	buf := make([]byte, 0, 4096)
	tmp := make([]byte, 4096)
	for len(buf) < max {
		n, err := resp.Body.Read(tmp)
		if n > 0 {
			buf = append(buf, tmp[:n]...)
		}
		if err != nil {
			break
		}
	}
	return buf, nil
}

// captureCode hosts an HTTPS listener on cfg.listenAddr that
// matches the registered redirect URI. Verifies the round-trip
// state matches. Returns the captured code.
func captureCode(cfg config, expectedState string) (string, error) {
	host, port, err := net.SplitHostPort(cfg.listenAddr)
	if err != nil {
		return "", fmt.Errorf("split listen addr: %w", err)
	}
	cert, err := loadOrGenerateCallbackCert(cfg, host, port)
	if err != nil {
		return "", err
	}
	ln, err := net.Listen("tcp", cfg.listenAddr)
	if err != nil {
		return "", fmt.Errorf("listen: %w", err)
	}
	defer ln.Close()
	codeCh := make(chan string, 1)
	errCh := make(chan error, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		// Some banks redirect with the auth response in the URL
		// fragment; the static handler can't see fragments, so
		// emit a tiny page that converts fragment → query and
		// re-fetches /callback.
		if r.URL.Query().Get("code") == "" && r.URL.Query().Get("error") == "" {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = fmt.Fprint(w, `<!doctype html><script>
				if (location.hash) {
				  location.replace(location.pathname + '?' + location.hash.substring(1));
				} else { document.body.innerText = 'no auth response on callback URL'; }
			</script>`)
			return
		}
		raw := r.URL.RawQuery
		// Merge fragment-converted query with any existing.
		cb, parseErr := openbanking.ParseAuthorizationCallback(raw)
		if parseErr != nil {
			http.Error(w, parseErr.Error(), http.StatusBadRequest)
			errCh <- parseErr
			return
		}
		if cb.State != expectedState {
			http.Error(w, "state mismatch", http.StatusBadRequest)
			errCh <- fmt.Errorf("state mismatch: got %q want %q", cb.State, expectedState)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = fmt.Fprint(w, "<h1>Authorisation captured</h1><p>You can close this window.</p>")
		codeCh <- cb.Code
	})
	srv := &http.Server{
		Handler:   mux,
		TLSConfig: &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12},
	}
	go func() {
		if err := srv.ServeTLS(ln, "", ""); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- fmt.Errorf("serve: %w", err)
		}
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	select {
	case code := <-codeCh:
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
		return code, nil
	case err := <-errCh:
		_ = srv.Shutdown(context.Background())
		return "", err
	case <-sigCh:
		_ = srv.Shutdown(context.Background())
		return "", errors.New("interrupted")
	}
}

// loadOrGenerateCallbackCert returns an MTLS-style server cert
// for the local callback listener. Self-signed if the user
// didn't supply -tls-cert / -tls-key.
func loadOrGenerateCallbackCert(cfg config, host, _ string) (tls.Certificate, error) {
	if cfg.tlsCertPEM != "" && cfg.tlsKeyPEM != "" {
		return tls.LoadX509KeyPair(cfg.tlsCertPEM, cfg.tlsKeyPEM)
	}
	// Generate a fresh self-signed cert per run; the user's
	// browser will warn once and then session-trust it.
	return generateSelfSigned(host)
}

func writeJSON(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	buf, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(buf, '\n'), 0o600)
}

func openInBrowser(rawURL string) error {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", rawURL).Start()
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", rawURL).Start()
	default:
		return exec.Command("xdg-open", rawURL).Start()
	}
}
