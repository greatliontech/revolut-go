//go:build sandbox

package revolut_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	revolut "github.com/greatliontech/revolut-go"
	"github.com/greatliontech/revolut-go/merchant"
)

// merchantSandboxConfig mirrors the JSON shape written to
// ~/.config/revolut-go/sandbox/merchant.json by the operator
// (there's no OAuth consent flow for Merchant — the secret is
// copied from the Revolut Business dashboard). Override the path
// with the REVOLUT_MERCHANT_SANDBOX_TOKENS env var.
type merchantSandboxConfig struct {
	Environment string    `json:"environment"`
	PublicKey   string    `json:"public_key"`
	SecretKey   string    `json:"secret_key"`
	ObtainedAt  time.Time `json:"obtained_at"`
}

// loadMerchantSandbox reads the merchant credentials file or
// skips the test when it's missing so `go test -tags sandbox
// ./...` on a fresh checkout doesn't fail, matching the pattern
// in integration_test.go for business.
func loadMerchantSandbox(t *testing.T) merchantSandboxConfig {
	t.Helper()
	path := os.Getenv("REVOLUT_MERCHANT_SANDBOX_TOKENS")
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			t.Skipf("cannot locate home dir: %v", err)
		}
		path = filepath.Join(home, ".config", "revolut-go", "sandbox", "merchant.json")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			t.Skipf("merchant sandbox file missing (%s)", path)
		}
		t.Fatalf("read merchant sandbox: %v", err)
	}
	var cfg merchantSandboxConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("parse merchant sandbox: %v", err)
	}
	if cfg.SecretKey == "" {
		t.Fatalf("merchant sandbox missing secret_key")
	}
	return cfg
}

// merchantClient builds a sandbox Merchant client authenticated
// with the secret key as a Bearer token. The SDK strips any
// Authorization set via method parameters (to prevent callers
// from overriding the transport auth), so the Authenticator is
// the only path that sets the header on the wire.
func merchantClient(t *testing.T) *merchant.Client {
	t.Helper()
	cfg := loadMerchantSandbox(t)
	auth := revolut.AuthenticatorFunc(func(r *http.Request) error {
		r.Header.Set("Authorization", "Bearer "+cfg.SecretKey)
		return nil
	})
	c, err := revolut.NewMerchantClient(auth, revolut.WithEnvironment(revolut.EnvironmentSandbox))
	if err != nil {
		t.Fatalf("NewMerchantClient: %v", err)
	}
	return c
}

// merchantAPIVersion is the Revolut-Api-Version value passed on
// methods whose spec parameter is the Min-2024-09-01 variant
// (customers). Resources whose spec uses the plain Revolut-Api-
// Version (orders, disputes) get merchant.RevolutAPIVersion20251204
// directly.
const merchantAPIVersion = merchant.RevolutAPIVersion20240901Min20251204

func TestSandbox_Merchant_CustomersList(t *testing.T) {
	c := merchantClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	resp, err := c.Customers.GetList(ctx, merchantAPIVersion, nil)
	if err != nil {
		t.Fatalf("GetList: %v", err)
	}
	if resp == nil {
		t.Fatal("nil response")
	}
	// A fresh merchant account may genuinely have zero customers,
	// so the list can be empty — assert the response decoded,
	// not that it's populated.
	t.Logf("merchant customers: %d items", len(resp.Customers))
}

// TestSandbox_Merchant_CustomersListAll exercises the iterator
// path: the spec declares cursor-style pagination on customers,
// so GetListAll yields one item per step until exhausted.
func TestSandbox_Merchant_CustomersListAll(t *testing.T) {
	c := merchantClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	count := 0
	for _, err := range c.Customers.GetListAll(ctx, merchantAPIVersion, nil) {
		if err != nil {
			t.Fatalf("iterator: %v", err)
		}
		count++
		if count >= 10 {
			break // upper bound so the test finishes on huge accounts
		}
	}
	t.Logf("iterated %d customers", count)
}

// TestSandbox_Merchant_OrdersList pins the read path on the
// primary resource. Empty sandbox is fine; the test asserts the
// request/response round-trip, not the content.
func TestSandbox_Merchant_OrdersList(t *testing.T) {
	c := merchantClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	resp, err := c.Orders.GetList(ctx, merchant.RevolutAPIVersion20251204, nil)
	if err != nil {
		t.Fatalf("Orders.GetList: %v", err)
	}
	if resp == nil {
		t.Fatal("nil response")
	}
	t.Logf("merchant orders: %d items", len(resp.Orders))
}

// TestSandbox_Merchant_GetUnknownOrder_Returns404 pins the error
// path: a UUID that doesn't map to any order produces an
// APIError the caller can unwrap. Uses a valid-format UUID to
// get past the generator's UUID pre-flight validator.
func TestSandbox_Merchant_GetUnknownOrder_Returns404(t *testing.T) {
	c := merchantClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err := c.Orders.Get(ctx, "00000000-0000-4000-8000-000000000000",
		merchant.RevolutAPIVersion20251204)
	if err == nil {
		t.Fatal("expected error for unknown order id")
	}
	apiErr, ok := revolut.AsAPIError(err)
	if !ok {
		t.Fatalf("want *APIError, got %T: %v", err, err)
	}
	if apiErr.StatusCode != http.StatusNotFound {
		t.Errorf("status=%d; want 404", apiErr.StatusCode)
	}
}

// TestSandbox_Merchant_GetMalformedOrder_LocalValidation pins
// the UUID pre-flight check: a malformed order_id fails the
// local validator and never hits the network.
func TestSandbox_Merchant_GetMalformedOrder_LocalValidation(t *testing.T) {
	c := merchantClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := c.Orders.Get(ctx, "not-a-uuid",
		merchant.RevolutAPIVersion20251204)
	if err == nil {
		t.Fatal("want local validation error")
	}
	if _, isAPI := revolut.AsAPIError(err); isAPI {
		t.Errorf("malformed UUID should fail locally, not round-trip: %v", err)
	}
}

// TestSandbox_Merchant_CustomerLifecycle exercises
// Create → Get → Delete on the Customers resource. Runs every
// time, so each invocation seeds one customer and removes it at
// the end; email uses a timestamp + random suffix so parallel
// runs don't collide.
func TestSandbox_Merchant_CustomerLifecycle(t *testing.T) {
	c := merchantClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Unique lowercase email per run — the server lowercases
	// what it stores, so matching on value requires already-lower.
	email := merchant.Email("sdktest+" + time.Now().UTC().Format("20060102t150405.000000000") + "@example.com")

	created, err := c.Customers.Create(ctx, merchantAPIVersion, merchant.CustomerCreationV2{
		Email:    email,
		FullName: "SDK Test Customer",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created == nil || created.ID == "" {
		t.Fatalf("nil id on created customer: %+v", created)
	}
	t.Cleanup(func() {
		// Best-effort cleanup — swallow error on teardown so a
		// sandbox hiccup doesn't mask the real test failure.
		deleteCtx, deleteCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer deleteCancel()
		if err := c.Customers.Delete(deleteCtx, string(created.ID), merchantAPIVersion); err != nil {
			t.Logf("cleanup delete: %v", err)
		}
	})

	got, err := c.Customers.Get(ctx, string(created.ID), merchantAPIVersion)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Email != email {
		t.Errorf("Get: email=%q; want %q", got.Email, email)
	}
	if got.FullName != "SDK Test Customer" {
		t.Errorf("Get: FullName=%q", got.FullName)
	}
}

// TestSandbox_Merchant_DisputesList probes the Disputes
// resource. The spec declares /api/disputes but at least as of
// 2026-04 the sandbox returns 404 for it — a sandbox parity gap,
// not a generator bug. The test skips on 404 so the suite stays
// green while keeping the coverage for the day the sandbox adds
// the endpoint.
func TestSandbox_Merchant_DisputesList(t *testing.T) {
	c := merchantClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	resp, err := c.Disputes.GetList(ctx, merchant.RevolutAPIVersion20251204, nil)
	if err != nil {
		if apiErr, ok := revolut.AsAPIError(err); ok && apiErr.StatusCode == http.StatusNotFound {
			t.Skipf("sandbox parity gap: /api/disputes returns 404")
		}
		t.Fatalf("Disputes.GetList: %v", err)
	}
	t.Logf("disputes: %d", len(resp))
}

// TestSandbox_Merchant_LocationsList exercises the Locations
// resource. Unlike Customers/Orders/Webhooks it doesn't take the
// Revolut-Api-Version header (spec declares no version on
// Locations), so it separately proves the generator handles
// resources with and without version headers uniformly.
func TestSandbox_Merchant_LocationsList(t *testing.T) {
	c := merchantClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	resp, err := c.Locations.GetList(ctx, nil)
	if err != nil {
		t.Fatalf("Locations.GetList: %v", err)
	}
	t.Logf("locations: %d", len(resp))
}

// TestSandbox_Merchant_WebhooksList exercises a different
// resource (Webhooks) to confirm auth + version-header plumbing
// works uniformly across resources, not just Customers / Orders.
func TestSandbox_Merchant_WebhooksList(t *testing.T) {
	c := merchantClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	resp, err := c.Webhooks.GetList(ctx, merchant.RevolutAPIVersion20240901Min20251204)
	if err != nil {
		t.Fatalf("Webhooks.GetList: %v", err)
	}
	if resp == nil {
		t.Fatal("nil webhooks response")
	}
	t.Logf("webhooks: %d", len(resp.Webhooks))
}
