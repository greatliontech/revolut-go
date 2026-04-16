package revolut_test

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
	"testing"
	"time"

	revolut "github.com/greatliontech/revolut-go"
)

func sign(secret []byte, ts string, body []byte) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte("v1." + ts + "."))
	mac.Write(body)
	return "v1=" + hex.EncodeToString(mac.Sum(nil))
}

func TestVerifyWebhook_Accepts(t *testing.T) {
	secret := []byte("s3cret")
	body := []byte(`{"event":"ORDER_COMPLETED"}`)
	ts := strconv.FormatInt(time.Now().UnixMilli(), 10)
	sig := sign(secret, ts, body)
	if err := revolut.VerifyWebhook(secret, body, ts, sig, revolut.WebhookVerificationOptions{}); err != nil {
		t.Fatalf("VerifyWebhook: %v", err)
	}
}

func TestVerifyWebhook_RejectsTamperedBody(t *testing.T) {
	secret := []byte("s3cret")
	body := []byte(`{"event":"ORDER_COMPLETED"}`)
	ts := strconv.FormatInt(time.Now().UnixMilli(), 10)
	sig := sign(secret, ts, body)
	tampered := []byte(`{"event":"ORDER_CANCELLED"}`)
	if err := revolut.VerifyWebhook(secret, tampered, ts, sig, revolut.WebhookVerificationOptions{}); err == nil {
		t.Fatal("want signature mismatch")
	}
}

func TestVerifyWebhook_RejectsExpired(t *testing.T) {
	secret := []byte("s3cret")
	body := []byte(`{}`)
	ts := strconv.FormatInt(time.Now().Add(-time.Hour).UnixMilli(), 10)
	sig := sign(secret, ts, body)
	err := revolut.VerifyWebhook(secret, body, ts, sig, revolut.WebhookVerificationOptions{})
	if err == nil || !strings.Contains(err.Error(), "window") {
		t.Errorf("want expired error, got %v", err)
	}
}

func TestVerifyWebhook_AcceptsMultipleV1Entries(t *testing.T) {
	secret := []byte("s3cret")
	body := []byte(`{}`)
	ts := strconv.FormatInt(time.Now().UnixMilli(), 10)
	correct := sign(secret, ts, body)
	combined := "v1=aa11bb22," + correct // old secret + current secret
	if err := revolut.VerifyWebhook(secret, body, ts, combined, revolut.WebhookVerificationOptions{}); err != nil {
		t.Fatalf("VerifyWebhook with multi-entry header: %v", err)
	}
}

// TestVerifyWebhook_RejectsFutureBeyondSkew: a timestamp minutes
// in the future is rejected by the forward-skew tolerance.
func TestVerifyWebhook_RejectsFutureBeyondSkew(t *testing.T) {
	secret := []byte("s3cret")
	body := []byte(`{}`)
	ts := strconv.FormatInt(time.Now().Add(10*time.Minute).UnixMilli(), 10)
	sig := sign(secret, ts, body)
	err := revolut.VerifyWebhook(secret, body, ts, sig, revolut.WebhookVerificationOptions{})
	if err == nil || !strings.Contains(err.Error(), "future") {
		t.Fatalf("want future-skew error, got %v", err)
	}
}

// TestVerifyWebhook_SmallFutureDriftAccepted: a timestamp a few
// seconds in the future is allowed under the default skew window so
// a well-synchronised-but-imperfect clock doesn't reject valid
// deliveries.
func TestVerifyWebhook_SmallFutureDriftAccepted(t *testing.T) {
	secret := []byte("s3cret")
	body := []byte(`{}`)
	ts := strconv.FormatInt(time.Now().Add(5*time.Second).UnixMilli(), 10)
	sig := sign(secret, ts, body)
	if err := revolut.VerifyWebhook(secret, body, ts, sig, revolut.WebhookVerificationOptions{}); err != nil {
		t.Errorf("tiny future drift should pass default skew, got %v", err)
	}
}

// TestVerifyWebhook_FutureSkewDisabled: setting a negative
// FutureSkew turns the forward-drift tolerance off entirely; any
// future timestamp is rejected.
func TestVerifyWebhook_FutureSkewDisabled(t *testing.T) {
	secret := []byte("s3cret")
	body := []byte(`{}`)
	ts := strconv.FormatInt(time.Now().Add(time.Second).UnixMilli(), 10)
	sig := sign(secret, ts, body)
	err := revolut.VerifyWebhook(secret, body, ts, sig, revolut.WebhookVerificationOptions{FutureSkew: -1})
	if err == nil {
		t.Fatal("want rejection with negative FutureSkew")
	}
}

func TestVerifyWebhook_MissingHeaders(t *testing.T) {
	if err := revolut.VerifyWebhook([]byte("s"), []byte("x"), "", "v1=xx", revolut.WebhookVerificationOptions{}); err == nil {
		t.Error("want error on missing timestamp header")
	}
	if err := revolut.VerifyWebhook([]byte("s"), []byte("x"), "1", "", revolut.WebhookVerificationOptions{}); err == nil {
		t.Error("want error on missing signature header")
	}
	if err := revolut.VerifyWebhook(nil, []byte("x"), "1", "v1=xx", revolut.WebhookVerificationOptions{}); err == nil {
		t.Error("want error on empty secret")
	}
}
