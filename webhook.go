package revolut

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// WebhookVerificationOptions tunes the behaviour of VerifyWebhook.
// The zero value is a sensible default: v1 signatures, 5 minutes
// of past-age tolerance, and 30 seconds of forward clock-skew
// tolerance.
type WebhookVerificationOptions struct {
	// MaxAge rejects webhook events whose Revolut-Request-Timestamp
	// is more than this far in the past. Zero uses DefaultWebhookMaxAge.
	// A negative value disables the check.
	MaxAge time.Duration

	// FutureSkew is the maximum amount of forward clock drift the
	// verifier tolerates — the webhook's timestamp is allowed to sit
	// up to this far in the future of the verifier's clock before
	// being rejected. Revolut stamps on send, so a real delivery's
	// timestamp is always in the past; this knob only absorbs clock
	// drift. Zero uses DefaultWebhookFutureSkew. A negative value
	// disables forward-skew tolerance entirely: any timestamp in the
	// future (even by a millisecond) is rejected.
	FutureSkew time.Duration

	// Now lets tests inject a clock. Nil uses time.Now().UTC().
	Now func() time.Time
}

// DefaultWebhookMaxAge matches Revolut's documented replay window.
const DefaultWebhookMaxAge = 5 * time.Minute

// DefaultWebhookFutureSkew is the forward clock-drift tolerance.
// Keeps the verifier strictly past-oriented while absorbing a
// modest amount of client/server clock mismatch.
const DefaultWebhookFutureSkew = 30 * time.Second

// VerifyWebhook authenticates an incoming webhook delivery using the
// merchant / business / crypto-ramp signing secret. It checks:
//
//   - The Revolut-Signature header's version prefix ("v1=").
//   - The HMAC-SHA256 of "v1.{timestamp}.{body}" against the
//     hex-encoded signature using a constant-time comparison.
//   - That Revolut-Request-Timestamp is within MaxAge of Now.
//
// Returns nil on success. Any failure returns a non-nil error and
// callers MUST reject the delivery.
//
// Parameters match the raw wire values: body is the unparsed request
// body bytes (order-sensitive — do not re-marshal the JSON), header
// values are the literal HTTP header strings.
func VerifyWebhook(secret []byte, body []byte, timestampHeader, signatureHeader string, opts WebhookVerificationOptions) error {
	if len(secret) == 0 {
		return errors.New("revolut: webhook secret is empty")
	}
	if timestampHeader == "" {
		return errors.New("revolut: Revolut-Request-Timestamp header missing")
	}
	if signatureHeader == "" {
		return errors.New("revolut: Revolut-Signature header missing")
	}

	// Signature header can carry multiple v-tagged values separated
	// by commas: "v1=abc,v1=def". Accept any v1 match.
	var v1s []string
	for _, part := range strings.Split(signatureHeader, ",") {
		part = strings.TrimSpace(part)
		if tag, rest, ok := strings.Cut(part, "="); ok && strings.EqualFold(tag, "v1") {
			v1s = append(v1s, rest)
		}
	}
	if len(v1s) == 0 {
		return errors.New("revolut: Revolut-Signature has no v1= entry")
	}

	payload := "v1." + timestampHeader + "."
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(payload))
	mac.Write(body)
	want := mac.Sum(nil)

	for _, candidate := range v1s {
		got, err := hex.DecodeString(strings.TrimSpace(candidate))
		if err != nil {
			continue
		}
		if hmac.Equal(got, want) {
			return checkTimestamp(timestampHeader, opts)
		}
	}
	return errors.New("revolut: webhook signature mismatch")
}

func checkTimestamp(raw string, opts WebhookVerificationOptions) error {
	maxAge := opts.MaxAge
	if maxAge == 0 {
		maxAge = DefaultWebhookMaxAge
	}
	if maxAge < 0 {
		return nil
	}
	ms, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return fmt.Errorf("revolut: Revolut-Request-Timestamp %q not an integer: %w", raw, err)
	}
	// Revolut timestamps are milliseconds since the Unix epoch.
	when := time.UnixMilli(ms).UTC()
	now := time.Now().UTC()
	if opts.Now != nil {
		now = opts.Now()
	}
	// Replay protection: the timestamp must be in the past (Revolut
	// stamps at send-time) within maxAge. A small forward drift is
	// tolerated via FutureSkew; a legitimate delivery's timestamp
	// can't be hours in the future.
	futureSkew := opts.FutureSkew
	switch {
	case futureSkew == 0:
		futureSkew = DefaultWebhookFutureSkew
	case futureSkew < 0:
		// Caller has disabled forward tolerance; treat as zero.
		futureSkew = 0
	}
	delta := now.Sub(when)
	if delta < -futureSkew {
		return fmt.Errorf("revolut: webhook timestamp %s is in the future beyond clock-skew tolerance (%s)", when.Format(time.RFC3339), futureSkew)
	}
	if delta > maxAge {
		return fmt.Errorf("revolut: webhook timestamp %s older than %s window", when.Format(time.RFC3339), maxAge)
	}
	return nil
}
