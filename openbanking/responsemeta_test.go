package openbanking

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/greatliontech/revolut-go/internal/transport"
)

func newTestClient(t *testing.T, h http.Handler) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	tr, err := transport.New(transport.Config{BaseURL: srv.URL + "/"})
	if err != nil {
		t.Fatalf("transport.New: %v", err)
	}
	return New(tr)
}

// TestResponseMetadata_CapturesHeaders pins the generator's
// allowlisted-header exposure: a typed OB method that declares
// x-fapi-interaction-id on its 2xx response returns the header
// value alongside the typed payload. Without this test, the
// only thing pinning ResponseMetadata was the golden
// signature — a regression that always returned empty fields
// would slip through.
func TestResponseMetadata_CapturesHeaders(t *testing.T) {
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("x-fapi-interaction-id", "corr-abc-123")
		w.Header().Set("x-jws-signature", "header..sig")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"Data":{"Account":[]}}`))
	}))

	_, meta, err := client.Accounts.List(
		context.Background(),
		"fin-id", "Mon, 10 Sep 2017 19:43:31 UTC", "1.2.3.4",
		"our-interaction-id", "test/1.0",
	)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if meta.InteractionID != "corr-abc-123" {
		t.Errorf("InteractionID=%q; want corr-abc-123", meta.InteractionID)
	}
	if meta.JWSSignature != "header..sig" {
		t.Errorf("JWSSignature=%q; want header..sig", meta.JWSSignature)
	}
}

// TestResponseMetadata_EmptyHeadersStillDecode: missing optional
// headers leave the corresponding field at the empty string; the
// typed payload still decodes.
func TestResponseMetadata_EmptyHeadersStillDecode(t *testing.T) {
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"Data":{"Account":[]}}`))
	}))
	result, meta, err := client.Accounts.List(
		context.Background(),
		"fin", "Mon, 10 Sep 2017 19:43:31 UTC", "1.2.3.4",
		"", "test/1.0",
	)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if result == nil {
		t.Fatal("nil typed payload")
	}
	if meta.InteractionID != "" || meta.JWSSignature != "" {
		t.Errorf("want empty metadata fields on header-less response; got %+v", meta)
	}
}

// TestSignedVariant_PreservesRawBytes pins the x-jws-signature
// companion: Signed[T] carries the untouched response bytes
// alongside the typed payload. A JSON re-marshal is NOT byte-
// identical, so Raw is the only form a detached-JWS verifier
// can hash. Uses GetConsentsConsentIDSigned — a GET with a
// single path param — so the test body doesn't have to
// construct a fully-required request struct.
func TestSignedVariant_PreservesRawBytes(t *testing.T) {
	const wireBody = `{"Data":{"ConsentId":"consent-7","Status":"Authorised","CreationDateTime":"2026-01-01T00:00:00Z","StatusUpdateDateTime":"2026-01-01T00:00:00Z","Initiation":{"InstructionIdentification":"inst","EndToEndIdentification":"e2e","InstructedAmount":{"Amount":"1.00","Currency":"GBP"},"CreditorAccount":{"SchemeName":"UK.OBIE.SortCodeAccountNumber","Identification":"12345678","Name":"N"}}},"Links":{"Self":"https://example.com/consent-7"},"Meta":{}}`
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("x-fapi-interaction-id", "corr-sig-1")
		w.Header().Set("x-jws-signature", "header..sig-blob")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(wireBody))
	}))
	signed, err := client.DomesticPayment.GetConsentsConsentIDSigned(
		context.Background(),
		"consent-7",
		"fin", "Mon, 10 Sep 2017 19:43:31 UTC", "1.2.3.4",
		"our-interaction", "test/1.0",
	)
	if err != nil {
		t.Fatalf("GetConsentsConsentIDSigned: %v", err)
	}
	if signed == nil {
		t.Fatal("nil Signed wrapper")
	}
	if string(signed.Raw) != wireBody {
		t.Errorf("Raw bytes don't match wire body.\n got: %s\nwant: %s", signed.Raw, wireBody)
	}
	if signed.Typed == nil || signed.Typed.Data.ConsentID != "consent-7" {
		t.Errorf("Typed payload not decoded; got %+v", signed.Typed)
	}
	if signed.Metadata.InteractionID != "corr-sig-1" || signed.Metadata.JWSSignature != "header..sig-blob" {
		t.Errorf("Metadata not populated: %+v", signed.Metadata)
	}
}

// TestApplications_GetDistinguishedName exercises a simple OB
// method that doesn't take FAPI headers (the Applications
// resource targets the pre-onboarding /distinguished-name
// endpoint). This proves the generator's "no metadata headers"
// branch — a method with zero declared response headers stays
// on the original (T, error) shape even inside the openbanking
// package where most methods DO carry ResponseMetadata.
func TestApplications_GetDistinguishedName(t *testing.T) {
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"tls_client_auth_dn":"CN=tpp.example.com"}`))
	}))
	resp, err := client.Applications.GetDistinguishedName(context.Background())
	if err != nil {
		t.Fatalf("GetDistinguishedName: %v", err)
	}
	if resp.TLSClientAuthDn != "CN=tpp.example.com" {
		t.Errorf("DN=%q", resp.TLSClientAuthDn)
	}
}

// TestTransactions_GetAccountsAccountIDTransactions pins a
// typical OB read flow: path param + seven header params +
// typed return with ResponseMetadata.
func TestTransactions_GetAccountsAccountIDTransactions(t *testing.T) {
	var gotHeaders http.Header
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeaders = r.Header.Clone()
		w.Header().Set("x-fapi-interaction-id", "txn-corr")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"Data":{"Transaction":[]},"Links":{"Self":"s"},"Meta":{}}`))
	}))
	_, meta, err := client.Transactions.GetAccountsAccountIDTransactions(
		context.Background(),
		"11111111-1111-1111-1111-111111111111",
		"fin-id", "Mon, 10 Sep 2017 19:43:31 UTC", "1.2.3.4",
		"interaction-42", "test/1.0",
		nil,
	)
	if err != nil {
		t.Fatalf("GetAccountsAccountIDTransactions: %v", err)
	}
	if meta.InteractionID != "txn-corr" {
		t.Errorf("InteractionID=%q", meta.InteractionID)
	}
	for _, want := range []string{"x-fapi-financial-id", "x-fapi-interaction-id"} {
		if gotHeaders.Get(want) == "" {
			t.Errorf("wire header %q missing", want)
		}
	}
	// Authorization is transport-owned: the SDK does not emit it
	// as a method parameter. Callers wire auth via the Authenticator;
	// this test uses no auth, so the header must be absent on the wire.
	if got := gotHeaders.Get("Authorization"); got != "" {
		t.Errorf("unexpected Authorization header on the wire: %q", got)
	}
}

// TestAccounts_EmptyPath_RejectedLocally: an empty AccountID
// fails the required-param validator before any HTTP call. The
// Open Banking spec doesn't declare `format: uuid` on any path
// param (they're plain strings), so the empty check is the only
// local validator on path params — but it must still fire.
func TestAccounts_EmptyPath_RejectedLocally(t *testing.T) {
	var hits int
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
	}))
	_, _, err := client.Accounts.GetAccountID(
		context.Background(),
		"",
		"fin", "Mon, 10 Sep 2017 19:43:31 UTC", "1.2.3.4",
		"", "test/1.0",
	)
	if err == nil || !strings.Contains(err.Error(), "is required") {
		t.Errorf("want required-path error, got %v", err)
	}
	if hits != 0 {
		t.Error("request fired despite empty path param")
	}
}

// TestSignedVariant_RawBytesAreVerbatim: weird whitespace + key
// order that a JSON re-marshal would normalise away must survive
// the round-trip unchanged. Without this guarantee, any JWS
// verification downstream would fail.
func TestSignedVariant_RawBytesAreVerbatim(t *testing.T) {
	const wireBody = `{  "Data"  :  {"ConsentId":"c","Status":"Authorised","CreationDateTime":"2026-01-01T00:00:00Z","StatusUpdateDateTime":"2026-01-01T00:00:00Z","Initiation":{"InstructionIdentification":"i","EndToEndIdentification":"e","InstructedAmount":{"Amount":"1","Currency":"GBP"},"CreditorAccount":{"SchemeName":"s","Identification":"x","Name":"n"}}},"Links":{"Self":"u"},"Meta":{}   }`
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("x-jws-signature", "sig")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(wireBody))
	}))
	signed, err := client.DomesticPayment.GetConsentsConsentIDSigned(
		context.Background(),
		"c",
		"fin", "Mon, 10 Sep 2017 19:43:31 UTC", "1.2.3.4",
		"", "test/1.0",
	)
	if err != nil {
		t.Fatalf("GetConsentsConsentIDSigned: %v", err)
	}
	if string(signed.Raw) != wireBody {
		t.Errorf("raw bytes normalised away; verification would fail.\n got: %q\nwant: %q",
			string(signed.Raw), wireBody)
	}
	if !strings.Contains(string(signed.Raw), "  ") {
		t.Errorf("raw should retain whitespace; got %q", string(signed.Raw))
	}
}
