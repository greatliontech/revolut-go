package build

import (
	"testing"

	"github.com/getkin/kin-openapi/openapi3"

	"github.com/greatliontech/revolut-go/cmd/revogen/ir"
)

// namedRef is refTo with the Value field populated from the
// already-seeded components/schemas entry. Necessary for test
// scaffolding because kin-openapi only populates Value during a
// real load; hand-built refs leave it nil, which the flatten
// helper can't dereference.
func namedRef(b *Builder, name string) *openapi3.SchemaRef {
	ref := refTo(name)
	if target := b.doc.Components.Schemas[name]; target != nil {
		ref.Value = target.Value
	}
	return ref
}

// TestUnionDecl_EditorialDiscriminatorFallsBackToFlatten pins the
// classifier: when PropertyName is set but the mapping keys share
// no value with the property's enum, the union is treated as
// editorial and flattened to a struct. Covers merchant's webhook
// payload shape, which declares `event` as the discriminator but
// lists prose labels ("Order or payment event") that never appear
// on the wire.
func TestUnionDecl_EditorialDiscriminatorFallsBackToFlatten(t *testing.T) {
	b := newTestBuilder()
	// Event enum: the wire values.
	seedSchemas(b, "EventType", inline(&openapi3.Schema{
		Type: &openapi3.Types{"string"},
		Enum: []any{"ORDER_COMPLETED", "SUBSCRIPTION_INITIATED"},
	}))
	// Variant A — an order event.
	seedSchemas(b, "OrderEvent", inline(&openapi3.Schema{
		Type: &openapi3.Types{"object"},
		Properties: openapi3.Schemas{
			"event":    namedRef(b, "EventType"),
			"order_id": inline(primSchema("string", "uuid")),
		},
		Required: []string{"event", "order_id"},
	}))
	// Variant B — a subscription event.
	seedSchemas(b, "SubscriptionEvent", inline(&openapi3.Schema{
		Type: &openapi3.Types{"object"},
		Properties: openapi3.Schemas{
			"event":           namedRef(b, "EventType"),
			"subscription_id": inline(primSchema("string", "uuid")),
		},
		Required: []string{"event", "subscription_id"},
	}))
	b.buildDecls()

	// The union schema Merchant declares: propertyName=event,
	// mapping keys are prose labels that aren't in EventType's
	// enum.
	union := &openapi3.Schema{
		Discriminator: &openapi3.Discriminator{
			PropertyName: "event",
			Mapping: map[string]openapi3.MappingRef{
				"Order or payment event": {Ref: "#/components/schemas/OrderEvent"},
				"Subscription event":     {Ref: "#/components/schemas/SubscriptionEvent"},
			},
		},
		OneOf: openapi3.SchemaRefs{refTo("OrderEvent"), refTo("SubscriptionEvent")},
	}

	decl := b.unionDeclFromSchema("Payload", union)
	if decl == nil {
		t.Fatal("expected a Decl, got nil")
	}
	if decl.Kind != ir.DeclStruct {
		t.Fatalf("want DeclStruct (editorial flatten); got Kind=%v", decl.Kind)
	}
	// Flattened fields must include event + both variant IDs.
	got := map[string]bool{}
	for _, f := range decl.Fields {
		got[f.JSONName] = true
	}
	for _, want := range []string{"event", "order_id", "subscription_id"} {
		if !got[want] {
			t.Errorf("missing flattened field %q; have %v", want, got)
		}
	}
}

// TestUnionDecl_WireCompatibleDiscriminatorStaysInterface: when
// mapping keys do appear in the enum values, the union stays a
// wire-tagged interface.
func TestUnionDecl_WireCompatibleDiscriminatorStaysInterface(t *testing.T) {
	b := newTestBuilder()
	seedSchemas(b, "Kind", inline(&openapi3.Schema{
		Type: &openapi3.Types{"string"},
		Enum: []any{"foo", "bar"},
	}))
	seedSchemas(b, "Foo", inline(&openapi3.Schema{
		Type: &openapi3.Types{"object"},
		Properties: openapi3.Schemas{
			"kind": namedRef(b, "Kind"),
			"x":    inline(primSchema("string", "")),
		},
		Required: []string{"kind", "x"},
	}))
	seedSchemas(b, "Bar", inline(&openapi3.Schema{
		Type: &openapi3.Types{"object"},
		Properties: openapi3.Schemas{
			"kind": namedRef(b, "Kind"),
			"y":    inline(primSchema("string", "")),
		},
		Required: []string{"kind", "y"},
	}))
	b.buildDecls()

	union := &openapi3.Schema{
		Discriminator: &openapi3.Discriminator{
			PropertyName: "kind",
			Mapping: map[string]openapi3.MappingRef{
				"foo": {Ref: "#/components/schemas/Foo"},
				"bar": {Ref: "#/components/schemas/Bar"},
			},
		},
		OneOf: openapi3.SchemaRefs{refTo("Foo"), refTo("Bar")},
	}

	decl := b.unionDeclFromSchema("Thing", union)
	if decl == nil || decl.Kind != ir.DeclInterface {
		t.Fatalf("want DeclInterface; got %+v", decl)
	}
	if decl.Discriminator == nil || decl.Discriminator.PropertyName != "kind" {
		t.Errorf("discriminator: %+v", decl.Discriminator)
	}
}
