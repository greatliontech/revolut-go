package merchant

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestPagination_CtxCancelStops: cancelling ctx between pages aborts
// the iterator with the cancellation error rather than firing
// another round-trip.
func TestPagination_CtxCancelStops(t *testing.T) {
	var hits int32
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"customers":[{"id":"c1","email":"a@b","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"}],"next_page_token":"tok-2"}`)
	}))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	seen := 0
	for item, err := range client.Customers.GetListAll(ctx, RevolutAPIVersion20240901Min20251204, nil) {
		if err != nil {
			if !errors.Is(err, context.Canceled) {
				t.Errorf("want context.Canceled; got %v (item=%+v)", err, item)
			}
			break
		}
		seen++
		cancel() // next iteration of the outer for-loop will trip ctx.Err()
	}
	if seen == 0 {
		t.Error("expected at least one item before cancellation")
	}
	// At most two HTTP calls (the pre-cancel one + 0-or-1 races).
	if got := atomic.LoadInt32(&hits); got > 2 {
		t.Errorf("iterator fired %d requests after cancellation; want ≤2", got)
	}
}

// TestPagination_SameCursorStallGuard: when the server returns the
// same next-page token twice in a row, the iterator stops instead
// of spinning forever.
func TestPagination_SameCursorStallGuard(t *testing.T) {
	var hits int32
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Header().Set("Content-Type", "application/json")
		// Always return the same next_page_token so a non-guarded
		// iterator would loop endlessly.
		_, _ = io.WriteString(w, `{"customers":[{"id":"c","email":"x@y","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"}],"next_page_token":"stuck"}`)
	}))
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	seen := 0
	for _, err := range client.Customers.GetListAll(ctx, RevolutAPIVersion20240901Min20251204, &RetrieveCustomerListParams{PageToken: "stuck"}) {
		if err != nil {
			t.Fatalf("iterator: %v", err)
		}
		seen++
		if seen > 5 {
			t.Fatal("iterator did not honour same-token stall guard")
		}
	}
	if got := atomic.LoadInt32(&hits); got > 1 {
		t.Errorf("expected 1 request before stall guard fires; got %d", got)
	}
}

// TestRawResponse_EmptyBodyGuard: a 204-ish JSON endpoint that
// replies with an empty body now leaves the typed payload at its
// zero value instead of erroring on json.Unmarshal("", ...).
func TestRawResponse_EmptyBodyGuard(t *testing.T) {
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		// no body
	}))
	resp, err := client.Orders.GetList(context.Background(), RevolutAPIVersion20251204, nil)
	if err != nil {
		t.Fatalf("empty 200 should not error: %v", err)
	}
	if resp == nil {
		t.Fatal("nil response for empty 200")
	}
	if resp.Orders != nil && len(resp.Orders) != 0 {
		t.Errorf("empty 200 populated orders: %+v", resp.Orders)
	}
}

// TestMultipart_CustomFilenameAndContentType proves the generated
// <Field>Filename / <Field>ContentType companion fields override
// the multipart part's default header. Uses the Disputes Upload
// endpoint — its spec encoding.contentType is
// application/pdf,image/png,image/jpeg.
func TestMultipart_CustomFilenameAndContentType(t *testing.T) {
	var gotBody []byte
	var gotCT string
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotCT = r.Header.Get("Content-Type")
		b, _ := io.ReadAll(r.Body)
		gotBody = b
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{}`)
	})
	srv := httptest.NewServer(h)
	defer srv.Close()
	_ = srv
	_ = gotCT
	_ = gotBody
	// A direct-struct test of the emitted encodeMultipart is more
	// illuminating than wiring through a real HTTP call: the
	// per-field companion knobs are the thing under test.
	req := DisputeEvidenceCreation{
		File:            strings.NewReader("PDF-1.4\n%EOF"),
		FileFilename:    "my-evidence.pdf",
		FileContentType: "application/pdf",
	}
	body, contentType, err := req.encodeMultipart()
	if err != nil {
		t.Fatalf("encodeMultipart: %v", err)
	}
	if !strings.HasPrefix(contentType, "multipart/form-data") {
		t.Errorf("outer Content-Type=%q", contentType)
	}
	bodyBytes, _ := io.ReadAll(body)
	blob := string(bodyBytes)
	if !strings.Contains(blob, `filename="my-evidence.pdf"`) {
		t.Errorf("caller filename override missing:\n%s", blob)
	}
	if !strings.Contains(blob, "Content-Type: application/pdf") {
		t.Errorf("caller content-type override missing:\n%s", blob)
	}
}

// TestMultipart_DefaultFromSpec: when the caller does not supply a
// filename / content-type, the encoder falls back to the JSON name
// and the spec-declared encoding.contentType.
func TestMultipart_DefaultFromSpec(t *testing.T) {
	req := DisputeEvidenceCreation{
		File: strings.NewReader("x"),
	}
	body, _, err := req.encodeMultipart()
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(body)
	blob := string(b)
	if !strings.Contains(blob, `filename="file"`) {
		t.Errorf("default filename missing:\n%s", blob)
	}
	// First entry of the spec-declared comma-separated list wins.
	if !strings.Contains(blob, "Content-Type: application/pdf") {
		t.Errorf("default content-type (from encoding.contentType first MIME) missing:\n%s", blob)
	}
}

// TestStreamingDownload_ReturnsReadCloser pins the io.ReadCloser
// return path for non-JSON responses. The server streams a PDF
// payload; the generated method must hand back a stream rather
// than buffer the whole body.
func TestStreamingDownload_ReturnsReadCloser(t *testing.T) {
	payload := strings.Repeat("id,amount\n1,42\n", 1024) // CSV
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Accept"); got != "text/csv" {
			t.Errorf("Accept header=%q; want text/csv (from spec)", got)
		}
		w.Header().Set("Content-Type", "text/csv")
		_, _ = io.WriteString(w, payload)
	}))
	stream, err := client.ReportRuns.DownloadReportFile(context.Background(),
		"11111111-1111-1111-1111-111111111111")
	if err != nil {
		t.Fatalf("DownloadReportFile: %v", err)
	}
	if stream == nil {
		t.Fatal("nil stream")
	}
	defer stream.Close()
	got, err := io.ReadAll(stream)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != payload {
		t.Errorf("payload mismatch: got %d bytes, want %d", len(got), len(payload))
	}
}

// TestUnion_DecodeNullReturnsNil pins the decode<Union> behaviour
// for JSON null. A spec that declares a nullable union-typed field
// would otherwise surface the absence as an "unknown tag" protocol
// error instead of a clean nil.
func TestUnion_DecodeNullReturnsNil(t *testing.T) {
	for _, input := range [][]byte{[]byte(`null`), nil, []byte("")} {
		got, err := decodeAuthenticationChallenge(input)
		if err != nil {
			t.Errorf("null input %q errored: %v", input, err)
		}
		if got != nil {
			t.Errorf("null input %q returned non-nil: %+v", input, got)
		}
	}
}

// TestUnion_DecodeUnknownTagErrors still returns an explicit error
// when a non-null body's discriminator is unrecognised, so the
// null-allowance doesn't swallow real protocol mismatches.
func TestUnion_DecodeUnknownTagErrors(t *testing.T) {
	_, err := decodeAuthenticationChallenge([]byte(`{"type":"totally-made-up"}`))
	if err == nil {
		t.Fatal("want unknown-tag error")
	}
	if !strings.Contains(err.Error(), "unknown") {
		t.Errorf("error should mention unknown: %v", err)
	}
}

// TestRedaction_SigningSecret covers the sensitive-field redaction
// emit path: WebhookV2.SigningSecret is marked sensitive by the
// name heuristic, so fmt.Sprintf("%+v") replaces the value with a
// redaction marker. JSON serialisation is untouched.
func TestRedaction_SigningSecret(t *testing.T) {
	w := WebhookV2{
		ID:            "wh-1",
		SigningSecret: "super-secret-value",
	}
	rendered := fmt.Sprintf("%+v", w)
	if strings.Contains(rendered, "super-secret-value") {
		t.Errorf("sensitive value leaked through %%+v: %s", rendered)
	}
	if !strings.Contains(rendered, "[REDACTED]") {
		t.Errorf("expected [REDACTED] marker in %%+v output: %s", rendered)
	}
	// %#v / GoString redacts too.
	goString := fmt.Sprintf("%#v", w)
	if strings.Contains(goString, "super-secret-value") {
		t.Errorf("sensitive value leaked through %%#v: %s", goString)
	}
}
