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
		"our-interaction-id", "Bearer tok", "test/1.0",
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
		"", "Bearer tok", "test/1.0",
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
		"our-interaction", "Bearer tok", "test/1.0",
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
		"", "Bearer", "test/1.0",
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
