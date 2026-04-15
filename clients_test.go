package revolut_test

import (
	"net/http"
	"testing"

	revolut "github.com/greatliontech/revolut-go"
)

// TestClientConstructors_RejectNilAuth pins the shared contract:
// every public NewXxxClient constructor must error on a nil
// Authenticator rather than produce a misconfigured client that
// fails later with a cryptic HTTP 401.
func TestClientConstructors_RejectNilAuth(t *testing.T) {
	cases := []struct {
		name  string
		build func() (any, error)
	}{
		{"business", func() (any, error) { return revolut.NewBusinessClient(nil) }},
		{"merchant", func() (any, error) { return revolut.NewMerchantClient(nil) }},
		{"open-banking", func() (any, error) { return revolut.NewOpenBankingClient(nil) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.build()
			if err == nil {
				t.Fatalf("want error for nil auth, got %T", got)
			}
		})
	}
}

func TestClientConstructors_AcceptOptions(t *testing.T) {
	auth := revolut.AuthenticatorFunc(func(_ *http.Request) error { return nil })
	if _, err := revolut.NewBusinessClient(auth, revolut.WithEnvironment(revolut.EnvironmentProduction)); err != nil {
		t.Fatalf("business prod: %v", err)
	}
	if _, err := revolut.NewMerchantClient(auth, revolut.WithEnvironment(revolut.EnvironmentSandbox)); err != nil {
		t.Fatalf("merchant sandbox: %v", err)
	}
	if _, err := revolut.NewOpenBankingClient(auth, revolut.WithBaseURL("https://example.com/")); err != nil {
		t.Fatalf("openbanking custom baseURL: %v", err)
	}
}
