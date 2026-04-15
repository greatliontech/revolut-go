package revolut_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/greatliontech/revolut-go"
	"github.com/greatliontech/revolut-go/auth/jwt"
	"github.com/greatliontech/revolut-go/business"
)

// sandboxConfig mirrors the JSON written by cmd/auth-bootstrap.
type sandboxConfig struct {
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

// loadSandboxConfig returns the sandbox tokens file, or skips the test
// when the file is missing so `go test ./...` works on a fresh checkout.
//
// Override the path with the REVOLUT_SANDBOX_TOKENS env var.
func loadSandboxConfig(t *testing.T) sandboxConfig {
	t.Helper()
	path := os.Getenv("REVOLUT_SANDBOX_TOKENS")
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			t.Skipf("cannot locate home dir: %v", err)
		}
		path = filepath.Join(home, ".config", "revolut-go", "sandbox", "tokens.json")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			t.Skipf("sandbox tokens missing (%s) -- run cmd/auth-bootstrap to enable integration tests", path)
		}
		t.Fatalf("read tokens: %v", err)
	}
	var cfg sandboxConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("parse tokens: %v", err)
	}
	if cfg.RefreshToken == "" || cfg.PrivateKey == "" || cfg.ClientID == "" {
		t.Fatalf("tokens file %s is missing required fields", path)
	}
	return cfg
}

func newSandboxClient(t *testing.T) *business.Client {
	t.Helper()
	cfg := loadSandboxConfig(t)

	key, err := jwt.LoadPrivateKeyFile(cfg.PrivateKey)
	if err != nil {
		t.Fatalf("load private key: %v", err)
	}
	signer, err := jwt.NewSigner(jwt.Config{
		PrivateKey: key,
		Issuer:     cfg.Issuer,
		ClientID:   cfg.ClientID,
	})
	if err != nil {
		t.Fatalf("build signer: %v", err)
	}
	src, err := jwt.NewSource(jwt.SourceConfig{
		Signer:       signer,
		TokenURL:     cfg.TokenURL,
		RefreshToken: cfg.RefreshToken,
	})
	if err != nil {
		t.Fatalf("build token source: %v", err)
	}

	env := revolut.EnvironmentSandbox
	if cfg.Environment == "production" {
		env = revolut.EnvironmentProduction
	}
	client, err := revolut.NewBusinessClient(src, revolut.WithEnvironment(env))
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	return client
}

func TestSandbox_AccountsList(t *testing.T) {
	client := newSandboxClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	accounts, err := client.Accounts.List(ctx)
	if err != nil {
		t.Fatalf("Accounts.List: %v", err)
	}
	if len(accounts) == 0 {
		t.Fatal("expected at least one sandbox account")
	}
	for i, a := range accounts {
		if a.ID == "" {
			t.Errorf("account %d has empty id", i)
		}
		if a.Currency == "" {
			t.Errorf("account %d (%s) has empty currency", i, a.ID)
		}
	}
	t.Logf("got %d accounts", len(accounts))
}

func TestSandbox_AccountsGet(t *testing.T) {
	client := newSandboxClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	list, err := client.Accounts.List(ctx)
	if err != nil {
		t.Fatalf("Accounts.List: %v", err)
	}
	if len(list) == 0 {
		t.Skip("no sandbox accounts to fetch")
	}

	want := list[0]
	got, err := client.Accounts.Get(ctx, want.ID)
	if err != nil {
		t.Fatalf("Accounts.Get(%q): %v", want.ID, err)
	}
	if got.ID != want.ID {
		t.Errorf("id mismatch: got %s want %s", got.ID, want.ID)
	}
	if got.Currency != want.Currency {
		t.Errorf("currency mismatch: got %s want %s", got.Currency, want.Currency)
	}
}
