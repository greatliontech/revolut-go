package build

import (
	"testing"

	"github.com/getkin/kin-openapi/openapi3"

	"github.com/greatliontech/revolut-go/cmd/revogen/ir"
)

func seedSchemas(b *Builder, pairs ...any) {
	if len(pairs)%2 != 0 {
		panic("seedSchemas needs name/schema pairs")
	}
	for i := 0; i < len(pairs); i += 2 {
		name := pairs[i].(string)
		ref := pairs[i+1].(*openapi3.SchemaRef)
		b.doc.Components.Schemas[name] = ref
	}
}

func TestBuildDecls_StructFields(t *testing.T) {
	b := newTestBuilder()
	seedSchemas(b, "Account", inline(&openapi3.Schema{
		Type:     &openapi3.Types{"object"},
		Required: []string{"id"},
		Properties: openapi3.Schemas{
			"id":         inline(primSchema("string", "uuid")),
			"created_at": inline(primSchema("string", "date-time")),
		},
	}))
	b.buildDecls()
	d := b.declByName["Account"]
	if d == nil || d.Kind != ir.DeclStruct {
		t.Fatalf("Account decl: %+v", d)
	}
	if len(d.Fields) != 2 {
		t.Fatalf("fields: %+v", d.Fields)
	}
	byJSON := map[string]*ir.Field{}
	for _, f := range d.Fields {
		byJSON[f.JSONName] = f
	}
	if byJSON["id"].Type.GoExpr() != "string" || !byJSON["id"].Required {
		t.Errorf("id: %+v", byJSON["id"])
	}
	// Optional time.Time becomes *time.Time.
	if byJSON["created_at"].Type.GoExpr() != "*time.Time" {
		t.Errorf("created_at: %s", byJSON["created_at"].Type.GoExpr())
	}
}

func TestBuildDecls_Enum(t *testing.T) {
	b := newTestBuilder()
	seedSchemas(b, "State", inline(&openapi3.Schema{
		Type: &openapi3.Types{"string"},
		Enum: []any{"active", "inactive"},
	}))
	b.buildDecls()
	d := b.declByName["State"]
	if d == nil || d.Kind != ir.DeclEnum {
		t.Fatalf("State decl: %+v", d)
	}
	if len(d.EnumValues) != 2 {
		t.Fatalf("values: %+v", d.EnumValues)
	}
	if d.EnumValues[0].GoName != "StateActive" {
		t.Errorf("const name: %s", d.EnumValues[0].GoName)
	}
}

func TestBuildDecls_Alias_Currency(t *testing.T) {
	b := newTestBuilder()
	seedSchemas(b, "Currency", inline(&openapi3.Schema{
		Type:    &openapi3.Types{"string"},
		Pattern: "^[A-Z]{3}$",
	}))
	b.buildDecls()
	d := b.declByName["Currency"]
	if d == nil || d.Kind != ir.DeclAlias {
		t.Fatalf("Currency decl: %+v", d)
	}
	if d.AliasTarget.GoExpr() != "core.Currency" {
		t.Errorf("alias target: %s", d.AliasTarget.GoExpr())
	}
}

func TestBuildDecls_AdditionalPropertiesOnly(t *testing.T) {
	b := newTestBuilder()
	s := &openapi3.Schema{Type: &openapi3.Types{"object"}}
	s.AdditionalProperties.Schema = inline(primSchema("string", ""))
	seedSchemas(b, "LabelGroup", inline(s))
	b.buildDecls()
	d := b.declByName["LabelGroup"]
	// With only additionalProperties, the Decl is an alias to map[string]T.
	if d == nil || d.Kind != ir.DeclAlias {
		t.Fatalf("decl: %+v", d)
	}
	if d.AliasTarget.GoExpr() != "map[string]string" {
		t.Errorf("alias target: %s", d.AliasTarget.GoExpr())
	}
}

func TestBuildDecls_PropertiesPlusAdditional(t *testing.T) {
	b := newTestBuilder()
	s := &openapi3.Schema{
		Type:     &openapi3.Types{"object"},
		Required: []string{"id"},
		Properties: openapi3.Schemas{
			"id": inline(primSchema("string", "")),
		},
	}
	s.AdditionalProperties.Schema = inline(primSchema("string", ""))
	seedSchemas(b, "Labeled", inline(s))
	b.buildDecls()
	d := b.declByName["Labeled"]
	if d == nil || d.Kind != ir.DeclStruct {
		t.Fatalf("Labeled decl: %+v", d)
	}
	if d.ExtraMap == nil || d.ExtraMap.GoExpr() != "string" {
		t.Errorf("ExtraMap: %+v", d.ExtraMap)
	}
}

func TestBuildDecls_ConditionalAnyOf(t *testing.T) {
	b := newTestBuilder()
	s := &openapi3.Schema{
		Type: &openapi3.Types{"object"},
		Properties: openapi3.Schemas{
			"start_date": inline(primSchema("string", "date")),
			"end_date":   inline(primSchema("string", "date")),
		},
		AnyOf: openapi3.SchemaRefs{
			inline(&openapi3.Schema{Required: []string{"start_date"}}),
			inline(&openapi3.Schema{Required: []string{"end_date"}}),
		},
	}
	seedSchemas(b, "Period", inline(s))
	b.buildDecls()
	d := b.declByName["Period"]
	if d == nil || len(d.AnyOfRequiredGroups) != 2 {
		t.Fatalf("anyOfGroups: %+v", d)
	}
}

// TestBuildDecls_EnumDedupsByValue pins the fix for the
// open-banking ObbalanceType1code duplicate-enum-value spec bug.
// The spec lists "InterimAvailable" twice; without dedup the
// generator would emit two constants with identical wire values,
// the second of which is unreachable by a switch statement.
func TestBuildDecls_EnumDedupsByValue(t *testing.T) {
	b := newTestBuilder()
	seedSchemas(b, "Kind", inline(&openapi3.Schema{
		Type: &openapi3.Types{"string"},
		Enum: []any{"foo", "bar", "foo", "baz", "bar"},
	}))
	b.buildDecls()
	d := b.declByName["Kind"]
	if d == nil {
		t.Fatal("no Kind decl")
	}
	if len(d.EnumValues) != 3 {
		t.Fatalf("want 3 deduped values; got %d: %+v", len(d.EnumValues), d.EnumValues)
	}
	seen := map[string]bool{}
	for _, ev := range d.EnumValues {
		s := ev.Value.(string)
		if seen[s] {
			t.Errorf("duplicate value survived dedup: %q", s)
		}
		seen[s] = true
	}
}

func TestBuildDecls_ReadOnlyField(t *testing.T) {
	b := newTestBuilder()
	prop := inline(primSchema("string", "uuid"))
	prop.Value.ReadOnly = true
	seedSchemas(b, "LabelResponse", inline(&openapi3.Schema{
		Type:       &openapi3.Types{"object"},
		Properties: openapi3.Schemas{"id": prop},
	}))
	b.buildDecls()
	d := b.declByName["LabelResponse"]
	if d == nil || !d.Fields[0].ReadOnly {
		t.Errorf("readOnly not lifted: %+v", d)
	}
}

func TestBuildDecls_DefaultDoc(t *testing.T) {
	b := newTestBuilder()
	prop := inline(primSchema("string", ""))
	prop.Value.Default = "active"
	seedSchemas(b, "X", inline(&openapi3.Schema{
		Type:       &openapi3.Types{"object"},
		Properties: openapi3.Schemas{"state": prop},
	}))
	b.buildDecls()
	d := b.declByName["X"]
	if d.Fields[0].DefaultDoc != `Default: "active".` {
		t.Errorf("default doc: %q", d.Fields[0].DefaultDoc)
	}
}

func TestBuildDecls_ProseDefaultIgnored(t *testing.T) {
	// A prose default (string) still renders; an array default does not.
	b := newTestBuilder()
	prop := inline(primSchema("string", ""))
	prop.Value.Default = []any{"a", "b"}
	seedSchemas(b, "Y", inline(&openapi3.Schema{
		Type:       &openapi3.Types{"object"},
		Properties: openapi3.Schemas{"x": prop},
	}))
	b.buildDecls()
	if got := b.declByName["Y"].Fields[0].DefaultDoc; got != "" {
		t.Errorf("array default produced doc: %q", got)
	}
}
