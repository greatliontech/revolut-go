package revolut_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
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

func TestSandbox_TransferBetweenOwnAccounts(t *testing.T) {
	client := newSandboxClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	accounts, err := client.Accounts.List(ctx)
	if err != nil {
		t.Fatalf("Accounts.List: %v", err)
	}
	src, dst, ok := pickSameCurrencyPair(accounts)
	if !ok {
		t.Skip("sandbox has no two active accounts of the same currency")
	}

	req := business.TransferRequest{
		RequestID:       "revolut-go-test-" + randomHex(8),
		SourceAccountID: src.ID,
		TargetAccountID: dst.ID,
		Amount:          "1",
		Currency:        src.Currency,
		Reference:       "revolut-go integration test",
	}
	got, err := client.Transfers.Create(ctx, req)
	if err != nil {
		t.Fatalf("Transfers.Create (%s -> %s, 1 %s): %v", src.ID, dst.ID, src.Currency, err)
	}
	if got.ID == "" {
		t.Fatal("empty transfer id")
	}
	switch got.State {
	case business.TransactionStateCreated,
		business.TransactionStatePending,
		business.TransactionStateCompleted:
		// All acceptable for same-currency same-business transfer.
	default:
		t.Fatalf("unexpected state %q", got.State)
	}
	t.Logf("transfer id=%s state=%s (%s -> %s, 1 %s)", got.ID, got.State, src.ID, dst.ID, src.Currency)
}

func pickSameCurrencyPair(accounts []business.Account) (src, dst business.Account, ok bool) {
	byCur := map[revolut.Currency][]business.Account{}
	for _, a := range accounts {
		if a.State != business.AccountStateActive {
			continue
		}
		byCur[a.Currency] = append(byCur[a.Currency], a)
	}
	for _, as := range byCur {
		if len(as) >= 2 {
			return as[0], as[1], true
		}
	}
	return business.Account{}, business.Account{}, false
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

func TestSandbox_CounterpartiesList(t *testing.T) {
	client := newSandboxClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	counterparties, err := client.Counterparties.List(ctx, nil)
	if err != nil {
		t.Fatalf("Counterparties.List: %v", err)
	}
	t.Logf("got %d counterparties", len(counterparties))
	// Sandbox may have zero counterparties for a fresh account; we
	// just exercise the network path + decoding here, not data shape.
}

func TestSandbox_TransactionsList(t *testing.T) {
	client := newSandboxClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	transactions, err := client.Transactions.List(ctx, nil)
	if err != nil {
		t.Fatalf("Transactions.List: %v", err)
	}
	t.Logf("got %d transactions (no filter)", len(transactions))
}

func TestSandbox_TransactionsList_WithCount(t *testing.T) {
	client := newSandboxClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	transactions, err := client.Transactions.List(ctx, &business.GetTransactionsParams{Count: 2})
	if err != nil {
		t.Fatalf("Transactions.List(count=2): %v", err)
	}
	if len(transactions) > 2 {
		t.Fatalf("count=2 should cap results at 2; got %d", len(transactions))
	}
	t.Logf("got %d transactions (count=2)", len(transactions))
}

func TestSandbox_TransactionsListAll(t *testing.T) {
	client := newSandboxClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Baseline: full list in one call (server default count=100).
	full, err := client.Transactions.List(ctx, nil)
	if err != nil {
		t.Fatalf("full List: %v", err)
	}
	if len(full) == 0 {
		t.Skip("sandbox returned 0 transactions — nothing to paginate")
	}

	// Iterate with a deliberately small page so the cursor is
	// exercised. All items must be seen, exactly once, regardless of
	// page size. Nanosecond-precise cursor advance guarantees no
	// timestamp-tie data loss (would have been broken with second
	// precision).
	seen := map[string]bool{}
	for tx, err := range client.Transactions.ListAll(ctx, &business.GetTransactionsParams{Count: 2}) {
		if err != nil {
			t.Fatalf("ListAll error at item %d: %v", len(seen), err)
		}
		if seen[tx.ID] {
			t.Fatalf("duplicate transaction id %s — cursor overlapping pages", tx.ID)
		}
		seen[tx.ID] = true
		if len(seen) > len(full)+32 {
			t.Fatalf("iterator emitted %d items, expected <= %d + margin", len(seen), len(full))
		}
	}

	if len(seen) != len(full) {
		t.Fatalf("ListAll saw %d items, full list has %d — pagination dropped items", len(seen), len(full))
	}
	t.Logf("ListAll yielded %d unique transactions (full list: %d, pageSize=2)", len(seen), len(full))
}

func TestSandbox_AccountNameValidation(t *testing.T) {
	client := newSandboxClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	req := business.ValidateAccountNameRequestUk{
		AccountNo:   "12345678",
		SortCode:    "04-00-75",
		CompanyName: "John Smith Co.",
	}
	got, err := client.Counterparties.AccountNameValidation(ctx, req)
	if err != nil {
		t.Fatalf("AccountNameValidation: %v", err)
	}
	if got == nil {
		t.Fatal("nil response")
	}
	// Verify the probe decoder returned a concrete variant that
	// implements the union interface. Variants share structure, so the
	// decoder may select any of them; what matters is that a typed
	// variant came back (not a map).
	switch v := got.(type) {
	case business.ValidateAccountNameResponseUk,
		business.ValidateAccountNameResponseAu,
		business.ValidateAccountNameResponseRo,
		business.ValidateAccountNameResponseEur:
		t.Logf("variant %T: %+v", v, v)
	default:
		t.Fatalf("unknown variant %T", v)
	}
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
