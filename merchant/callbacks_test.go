package merchant

import (
	"strings"
	"testing"
)

// TestDecodeSendWebhookEvent_OrderEvent round-trips a realistic
// Revolut webhook payload through the generated Decode helper. The
// merchant spec classifies the callback schema as an editorial
// discriminator (the mapping keys don't match the wire enum), so
// the decoder decodes into a flat struct — this test pins that
// contract and also exercises the Decode helper end-to-end so a
// regression in either the union classifier or the callback
// emitter would flip it.
func TestDecodeSendWebhookEvent_OrderEvent(t *testing.T) {
	const body = `{
		"event": "ORDER_COMPLETED",
		"order_id": "6516e61c-d279-4b53-ac68-f9ae1b4a31e0",
		"merchant_order_ext_ref": "ext-42"
	}`

	out, err := DecodeSendWebhookEvent(strings.NewReader(body))
	if err != nil {
		t.Fatalf("DecodeSendWebhookEvent: %v", err)
	}
	if out == nil {
		t.Fatal("nil payload")
	}
	if got := out.Event; got != "ORDER_COMPLETED" {
		t.Errorf("Event=%q; want ORDER_COMPLETED", got)
	}
	if got := out.OrderID; got != "6516e61c-d279-4b53-ac68-f9ae1b4a31e0" {
		t.Errorf("OrderID=%q", got)
	}
	if got := out.MerchantOrderExtRef; got != "ext-42" {
		t.Errorf("MerchantOrderExtRef=%q", got)
	}
	// Unrelated-variant fields stay zero.
	if out.DisputeID != "" || out.PayoutID != "" || out.SubscriptionID != "" {
		t.Errorf("unexpected populated fields: %+v", out)
	}
}

// TestDecodeSendWebhookEvent_SubscriptionEvent: a different wire
// event fills a different subset of the flat struct, demonstrating
// the editorial-merge shape accepts every variant without a
// decoder-side switch.
func TestDecodeSendWebhookEvent_SubscriptionEvent(t *testing.T) {
	const body = `{
		"event": "SUBSCRIPTION_INITIATED",
		"subscription_id": "sub-123",
		"external_reference": "ext-sub"
	}`

	out, err := DecodeSendWebhookEvent(strings.NewReader(body))
	if err != nil {
		t.Fatalf("DecodeSendWebhookEvent: %v", err)
	}
	if got := out.Event; got != "SUBSCRIPTION_INITIATED" {
		t.Errorf("Event=%q", got)
	}
	if got := out.SubscriptionID; got != "sub-123" {
		t.Errorf("SubscriptionID=%q", got)
	}
	if out.OrderID != "" {
		t.Errorf("OrderID should be empty; got %q", out.OrderID)
	}
}

// TestDecodeSendWebhookEvent_InvalidJSON surfaces a decode error
// unchanged rather than returning a partial payload.
func TestDecodeSendWebhookEvent_InvalidJSON(t *testing.T) {
	_, err := DecodeSendWebhookEvent(strings.NewReader("{not json"))
	if err == nil {
		t.Fatal("want decode error on malformed JSON")
	}
}
