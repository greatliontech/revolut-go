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
	"net/url"
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
	tan         string // OBIE trust anchor name (TAN claim); empty = environment-derived

	// PISP-only — sandbox-friendly defaults; flip with flags.
	pispAmount                 string
	pispCurrency               string
	pispCreditorIdentification string
	pispCreditorName           string
	pispInstructionID          string
	pispEndToEndID             string
	pispRef                    string
}

type credentials struct {
	ClientID          string `json:"client_id"`
	Kid               string `json:"kid"`
	Alg               string `json:"alg"`
	JwksURL           string `json:"jwks_url"`
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
		// Sandbox defaults. Two distinct hosts:
		//   sandbox-oba-auth.revolut.com — TPP-facing, MTLS required
		//     (token, register, account-access-consents, all API calls)
		//   sandbox-oba.revolut.com — PSU-facing browser UI for the
		//     consent flow, no MTLS (browsers don't have client certs)
		// Production mirrors the same split: oba-auth.revolut.com vs
		// oba.revolut.com. Discoverable from /.well-known/openid-configuration
		// when fetched WITH MTLS against the auth host.
		authzURL: "https://sandbox-oba.revolut.com/ui/index.html",
		tokenURL: "https://sandbox-oba-auth.revolut.com/token",
		audience: "https://sandbox-oba-auth.revolut.com",
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
	flag.StringVar(&cfg.tan, "tan", "", "OBIE TAN claim override (default: openbankingtest.org.uk for sandbox, openbanking.org.uk otherwise)")
	// PISP-specific flags. Defaults are sandbox-safe values that
	// move 1.00 GBP to a fictional creditor — Revolut's sandbox
	// accepts arbitrary creditor accounts under the
	// UK.OBIE.SortCodeAccountNumber scheme.
	flag.StringVar(&cfg.pispAmount, "pisp-amount", "1.00", "PISP InstructedAmount.Amount")
	flag.StringVar(&cfg.pispCurrency, "pisp-currency", "GBP", "PISP InstructedAmount.Currency")
	flag.StringVar(&cfg.pispCreditorIdentification, "pisp-creditor-id", "11280001234567", "PISP CreditorAccount.Identification (sort+account)")
	flag.StringVar(&cfg.pispCreditorName, "pisp-creditor-name", "Sandbox Creditor", "PISP CreditorAccount.Name")
	flag.StringVar(&cfg.pispInstructionID, "pisp-instr-id", "", "PISP InstructionIdentification (default: random)")
	flag.StringVar(&cfg.pispEndToEndID, "pisp-e2e-id", "", "PISP EndToEndIdentification (default: random)")
	flag.StringVar(&cfg.pispRef, "pisp-ref", "ob-bootstrap-test", "PISP RemittanceInformation reference + unstructured")
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
	if cfg.pispInstructionID == "" {
		cfg.pispInstructionID = "INSTR-" + randomToken(8)
	}
	if cfg.pispEndToEndID == "" {
		cfg.pispEndToEndID = "E2E-" + randomToken(8)
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

	// PISP consent bodies need a JWS signature in
	// x-jws-signature; build a Signer up front. The JWS `iss`
	// claim must be the signing cert's full subject DN — that's
	// what Revolut binds the assertion to (and what
	// Applications.Get reports as tls_client_auth_dn).
	signingCertDER, err := os.ReadFile(filepath.Join(cfg.dir, creds.SigningCertFile))
	if err != nil {
		return fmt.Errorf("read signing cert: %w", err)
	}
	signingCert, err := x509.ParseCertificate(signingCertDER)
	if err != nil {
		return fmt.Errorf("parse signing cert: %w", err)
	}
	// Revolut's verifier requires the JWS TAN claim to match the
	// DNS host of the JWKS we publish — NOT openbanking.org.uk
	// or any OBIE-directory anchor. Default: parse the host out
	// of the credentials' JwksURL when set, else require -tan.
	signerOpts := openbanking.SignerOptions{Alg: creds.Alg}
	switch {
	case cfg.tan != "":
		signerOpts.TrustAnchor = cfg.tan
	case creds.JwksURL != "":
		if u, perr := url.Parse(creds.JwksURL); perr == nil && u.Host != "" {
			signerOpts.TrustAnchor = u.Host
		}
	}
	if signerOpts.TrustAnchor == "" {
		return errors.New("ob-bootstrap: -tan is required (set it to the DNS host of your published JWKS, e.g. greatliontech.github.io) or add jwks_url to credentials.json")
	}
	signer, err := openbanking.NewSigner(priv, creds.Kid,
		signingCert.Subject.String(),
		signerOpts,
	)
	if err != nil {
		return fmt.Errorf("init signer: %w", err)
	}

	// Step 2 — create the consent (AISP or PISP).
	consentID, err := createConsent(ctx, mtlsHTTPC, cfg, creds, signer, tppToken)
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
// requested scope and returns the ConsentId Revolut assigned.
//
// AISP: a plain JSON body listing Permissions; bearer-only auth.
// PISP: a payment-initiation body that MUST be signed with the
// TPP's signing key — Revolut requires the x-jws-signature header
// on every POST under the payments scope, computed via the
// detached b64=false JWS the openbanking.Signer produces.
func createConsent(ctx context.Context, httc *http.Client, cfg config, creds *credentials, signer *openbanking.Signer, accessToken string) (string, error) {
	if strings.Contains(cfg.scope, "payments") {
		return createPISPConsent(ctx, httc, cfg, signer, accessToken)
	}
	return createAISPConsent(ctx, httc, cfg, accessToken)
}

func createAISPConsent(ctx context.Context, httc *http.Client, cfg config, accessToken string) (string, error) {
	body := strings.NewReader(`{
        "Data":{"Permissions":[
            "ReadAccountsBasic","ReadAccountsDetail",
            "ReadBalances",
            "ReadTransactionsBasic","ReadTransactionsDetail","ReadTransactionsCredits","ReadTransactionsDebits"
        ]},
        "Risk":{}
    }`)
	return postConsent(ctx, httc, cfg, "/account-access-consents", body, accessToken, "")
}

// createPISPConsent builds a minimal domestic payment consent
// body with sandbox-friendly defaults: a small GBP amount to a
// fictional creditor account. The body is signed with the
// openbanking.Signer to produce the x-jws-signature header
// Revolut requires on the POST.
func createPISPConsent(ctx context.Context, httc *http.Client, cfg config, signer *openbanking.Signer, accessToken string) (string, error) {
	body := []byte(fmt.Sprintf(`{
        "Data":{
            "Initiation":{
                "InstructionIdentification":"%s",
                "EndToEndIdentification":"%s",
                "InstructedAmount":{"Amount":"%s","Currency":"%s"},
                "CreditorAccount":{
                    "SchemeName":"UK.OBIE.SortCodeAccountNumber",
                    "Identification":"%s",
                    "Name":"%s"
                },
                "RemittanceInformation":{"Reference":"%s","Unstructured":"%s"}
            }
        },
        "Risk":{}
    }`,
		cfg.pispInstructionID,
		cfg.pispEndToEndID,
		cfg.pispAmount, cfg.pispCurrency,
		cfg.pispCreditorIdentification, cfg.pispCreditorName,
		cfg.pispRef, cfg.pispRef,
	))
	jws, err := signer.Sign(body)
	if err != nil {
		return "", fmt.Errorf("sign PISP consent body: %w", err)
	}
	return postConsent(ctx, httc, cfg, "/domestic-payment-consents", strings.NewReader(string(body)), accessToken, jws)
}

// postConsent is the shared HTTP path for AISP and PISP consent
// creation. PISP additionally needs the x-jws-signature header.
func postConsent(ctx context.Context, httc *http.Client, cfg config, path string, body *strings.Reader, accessToken, jwsHeader string) (string, error) {
	endpoint := strings.TrimRight(cfg.apiHost, "/") + path
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, body)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("x-fapi-financial-id", "001580000103UAvAAM")
	if jwsHeader != "" {
		req.Header.Set("x-jws-signature", jwsHeader)
		// Idempotency-Key is required on PISP POSTs; a fresh
		// random per call is the spec-canonical approach.
		req.Header.Set("x-idempotency-key", randomToken(16))
	}
	resp, err := httc.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	respBody, _ := readAll(resp)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("POST %s %d: %s", path, resp.StatusCode, respBody)
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
