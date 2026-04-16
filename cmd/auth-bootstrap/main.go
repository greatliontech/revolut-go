// auth-bootstrap completes the one-time Revolut Business OAuth consent
// flow and captures a long-lived refresh token.
//
// It hosts a self-signed HTTPS listener on 127.0.0.1:8787 (configurable),
// prints the consent URL, and — once Revolut redirects the browser back
// with an authorization code — exchanges the code for tokens using a JWT
// client assertion signed with your private key. The resulting tokens are
// written to a JSON file which the SDK's integration tests read.
//
// Run once per sandbox account or when the refresh token is revoked.
package main

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
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

	"github.com/greatliontech/revolut-go/auth/jwt"
)

type environment string

const (
	envSandbox    environment = "sandbox"
	envProduction environment = "production"
)

func tokenURL(env environment) string {
	if env == envProduction {
		return "https://b2b.revolut.com/api/1.0/auth/token"
	}
	return "https://sandbox-b2b.revolut.com/api/1.0/auth/token"
}

func consentHost(env environment) string {
	if env == envProduction {
		return "https://business.revolut.com"
	}
	return "https://sandbox-business.revolut.com"
}

type config struct {
	privateKey string
	clientID   string
	issuer     string
	env        environment
	redirect   string
	addr       string
	tlsCert    string
	tlsKey     string
	output     string
	open       bool
}

// storedTokens is the JSON shape written to the output file. Matches
// what the integration tests will consume.
type storedTokens struct {
	Environment  string    `json:"environment"`
	TokenURL     string    `json:"token_url"`
	ClientID     string    `json:"client_id"`
	Issuer       string    `json:"issuer"`
	PrivateKey   string    `json:"private_key_path"`
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	ExpiresAt    time.Time `json:"expires_at"`
	ObtainedAt   time.Time `json:"obtained_at"`
}

func main() {
	cfg := parseFlags()
	if err := run(cfg); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func parseFlags() config {
	home, _ := os.UserHomeDir()
	sandboxDir := filepath.Join(home, ".config", "revolut-go", "sandbox")
	cfg := config{
		env: envSandbox,
	}
	flag.StringVar(&cfg.privateKey, "private-key", filepath.Join(sandboxDir, "private.pem"), "path to the RSA private key PEM")
	flag.StringVar(&cfg.clientID, "client-id", "", "Revolut client ID (from the developer portal)")
	flag.StringVar(&cfg.issuer, "issuer", "127.0.0.1", "JWT iss claim. Revolut derives this from the host of your registered redirect URI; override if yours differs")
	env := flag.String("env", "sandbox", "sandbox or production")
	flag.StringVar(&cfg.redirect, "redirect", "https://127.0.0.1:8787/callback", "OAuth redirect URI registered with Revolut")
	flag.StringVar(&cfg.addr, "addr", "127.0.0.1:8787", "host:port for the local HTTPS callback server")
	flag.StringVar(&cfg.tlsCert, "tls-cert", filepath.Join(sandboxDir, "localhost.crt"), "TLS cert for the callback server (generated if missing)")
	flag.StringVar(&cfg.tlsKey, "tls-key", filepath.Join(sandboxDir, "localhost.key"), "TLS key for the callback server (generated if missing)")
	flag.StringVar(&cfg.output, "output", filepath.Join(sandboxDir, "tokens.json"), "path to write the resulting tokens to")
	flag.BoolVar(&cfg.open, "open", true, "try to open the consent URL in the default browser")
	flag.Parse()
	cfg.env = environment(*env)
	return cfg
}

func run(cfg config) error {
	if cfg.clientID == "" {
		return errors.New("--client-id is required")
	}
	if cfg.env != envSandbox && cfg.env != envProduction {
		return fmt.Errorf("--env must be sandbox or production (got %q)", cfg.env)
	}

	key, err := jwt.LoadPrivateKeyFile(cfg.privateKey)
	if err != nil {
		return fmt.Errorf("load private key: %w", err)
	}
	signer, err := jwt.NewSigner(jwt.Config{
		PrivateKey: key,
		Issuer:     cfg.issuer,
		ClientID:   cfg.clientID,
	})
	if err != nil {
		return fmt.Errorf("build signer: %w", err)
	}

	if err := ensureTLSCert(cfg.tlsCert, cfg.tlsKey); err != nil {
		return fmt.Errorf("ensure TLS cert: %w", err)
	}

	cert, err := tls.LoadX509KeyPair(cfg.tlsCert, cfg.tlsKey)
	if err != nil {
		return fmt.Errorf("load TLS keypair: %w", err)
	}

	ln, err := net.Listen("tcp", cfg.addr)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	defer ln.Close()

	// RFC 6749 §10.12: the client MUST generate a random state value
	// and verify it on callback to prevent CSRF. 128 bits of crypto
	// randomness is plenty.
	stateRaw := make([]byte, 16)
	if _, err := rand.Read(stateRaw); err != nil {
		return fmt.Errorf("generate state: %w", err)
	}
	expectedState := base64.RawURLEncoding.EncodeToString(stateRaw)
	consentURL := buildConsentURL(cfg, expectedState)
	fmt.Printf("Listening on https://%s\n", cfg.addr)
	fmt.Println()
	fmt.Println("Open this URL in your browser to authorise the app:")
	fmt.Println()
	fmt.Println("   ", consentURL)
	fmt.Println()
	fmt.Println("(Your browser will warn about the self-signed cert for the callback — accept it.)")
	fmt.Println()

	if cfg.open {
		_ = openBrowser(consentURL)
	}

	codeCh := make(chan string, 1)
	errCh := make(chan error, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if errCode := q.Get("error"); errCode != "" {
			desc := q.Get("error_description")
			http.Error(w, fmt.Sprintf("authorization failed: %s %s", errCode, desc), http.StatusBadRequest)
			errCh <- fmt.Errorf("authorization failed: %s: %s", errCode, desc)
			return
		}
		code := q.Get("code")
		if code == "" {
			http.Error(w, "missing code", http.StatusBadRequest)
			errCh <- errors.New("callback had no ?code=")
			return
		}
		gotState := q.Get("state")
		if subtle.ConstantTimeCompare([]byte(gotState), []byte(expectedState)) != 1 {
			http.Error(w, "state mismatch", http.StatusBadRequest)
			errCh <- errors.New("callback ?state= did not match the locally-generated value; CSRF possible")
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(successHTML))
		codeCh <- code
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

	var code string
	select {
	case code = <-codeCh:
	case err := <-errCh:
		_ = srv.Shutdown(context.Background())
		return err
	case <-sigCh:
		_ = srv.Shutdown(context.Background())
		return errors.New("interrupted")
	}

	// Give the response a moment to flush to the browser.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)

	fmt.Println("Got authorization code, exchanging for tokens...")

	ctx, cancelExchange := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancelExchange()
	tr, err := jwt.ExchangeCode(ctx, http.DefaultClient, tokenURL(cfg.env), signer, code)
	if err != nil {
		var tokErr *jwt.TokenError
		if errors.As(err, &tokErr) && len(tokErr.Body) > 0 {
			fmt.Fprintf(os.Stderr, "token endpoint response body: %s\n", tokErr.Body)
		}
		return fmt.Errorf("exchange: %w", err)
	}
	if tr.RefreshToken == "" {
		return errors.New("token response had no refresh_token")
	}

	now := time.Now().UTC()
	out := storedTokens{
		Environment:  string(cfg.env),
		TokenURL:     tokenURL(cfg.env),
		ClientID:     cfg.clientID,
		Issuer:       cfg.issuer,
		PrivateKey:   cfg.privateKey,
		AccessToken:  tr.AccessToken,
		RefreshToken: tr.RefreshToken,
		ExpiresAt:    now.Add(time.Duration(tr.ExpiresIn) * time.Second),
		ObtainedAt:   now,
	}
	if err := writeJSON(cfg.output, out); err != nil {
		return fmt.Errorf("write output: %w", err)
	}
	fmt.Printf("\nTokens written to %s\n", cfg.output)
	fmt.Printf("access_token expires at %s (in %ds)\n", out.ExpiresAt.Format(time.RFC3339), tr.ExpiresIn)
	return nil
}

func buildConsentURL(cfg config, state string) string {
	q := url.Values{
		"client_id":     {cfg.clientID},
		"redirect_uri":  {cfg.redirect},
		"response_type": {"code"},
		"state":         {state},
	}
	return consentHost(cfg.env) + "/app-confirm?" + q.Encode()
}

func writeJSON(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o600)
}

func openBrowser(u string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", u)
	case "windows":
		cmd = exec.Command("cmd", "/c", "start", strings.ReplaceAll(u, "&", "^&"))
	default:
		cmd = exec.Command("xdg-open", u)
	}
	return cmd.Start()
}

const successHTML = `<!doctype html>
<html><head><meta charset="utf-8"><title>revolut-go auth-bootstrap</title>
<style>body{font-family:system-ui,sans-serif;max-width:42rem;margin:6rem auto;padding:2rem;color:#222;}
h1{color:#0b7a2f;margin:0 0 1rem;} p{line-height:1.5;}</style></head>
<body><h1>Authorization captured.</h1>
<p>You can close this tab. The CLI is exchanging the code for tokens now.</p>
</body></html>`
