package build

import (
	"testing"

	"github.com/getkin/kin-openapi/openapi3"

	"github.com/greatliontech/revolut-go/cmd/revogen/ir"
)

func newTestBuilder() *Builder {
	return &Builder{
		doc:            &openapi3.T{Components: &openapi3.Components{Schemas: openapi3.Schemas{}}},
		declByName:     map[string]*ir.Decl{},
		resourceByName: map[string]*ir.Resource{},
		specNameToGo:   map[string]string{},
		reserved:       map[string]bool{},
	}
}

// refTo builds an openapi3.SchemaRef whose Ref points at the target
// name. Used to test resolveNamedRef paths.
func refTo(name string) *openapi3.SchemaRef {
	return &openapi3.SchemaRef{Ref: "#/components/schemas/" + name}
}

func inline(s *openapi3.Schema) *openapi3.SchemaRef {
	return &openapi3.SchemaRef{Value: s}
}

// primSchema constructs a minimal schema carrying just a type and
// optional format for dispatch tests.
func primSchema(typ, format string) *openapi3.Schema {
	s := &openapi3.Schema{Type: &openapi3.Types{typ}}
	s.Format = format
	return s
}

func TestResolveType_Primitives(t *testing.T) {
	b := newTestBuilder()
	cases := []struct {
		name   string
		schema *openapi3.Schema
		want   string
	}{
		{"string", primSchema("string", ""), "string"},
		{"string-uuid", primSchema("string", "uuid"), "string"},
		{"string-date-time", primSchema("string", "date-time"), "time.Time"},
		{"string-date", primSchema("string", "date"), "time.Time"},
		{"string-binary", primSchema("string", "binary"), "io.Reader"},
		{"string-decimal", primSchema("string", "decimal"), "json.Number"},
		{"integer", primSchema("integer", ""), "int"},
		{"integer-int64", primSchema("integer", "int64"), "int64"},
		{"number", primSchema("number", ""), "json.Number"},
		{"boolean", primSchema("boolean", ""), "bool"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := b.resolveType(inline(c.schema), Context{})
			if got.GoExpr() != c.want {
				t.Errorf("got %q; want %q", got.GoExpr(), c.want)
			}
		})
	}
}

func TestResolveType_CurrencyPattern(t *testing.T) {
	b := newTestBuilder()
	s := primSchema("string", "")
	s.Pattern = "^[A-Z]{3}$"
	got := b.resolveType(inline(s), Context{})
	if got.GoExpr() != "core.Currency" {
		t.Errorf("got %q", got.GoExpr())
	}
	set := map[string]struct{}{}
	got.CollectImports(set)
	if _, ok := set[internalCoreImport]; !ok {
		t.Errorf("missing core import: %v", set)
	}
}

func TestResolveType_Array(t *testing.T) {
	b := newTestBuilder()
	s := &openapi3.Schema{
		Type:  &openapi3.Types{"array"},
		Items: inline(primSchema("string", "")),
	}
	got := b.resolveType(inline(s), Context{})
	if got.GoExpr() != "[]string" {
		t.Errorf("got %q", got.GoExpr())
	}
}

func TestResolveType_Nullable(t *testing.T) {
	b := newTestBuilder()
	s := primSchema("string", "")
	s.Nullable = true
	if got := b.resolveType(inline(s), Context{}); got.GoExpr() != "*string" {
		t.Errorf("got %q", got.GoExpr())
	}
	// Nullable slice stays a slice (slices already admit nil).
	arr := &openapi3.Schema{
		Type:  &openapi3.Types{"array"},
		Items: inline(primSchema("string", "")),
	}
	arr.Nullable = true
	if got := b.resolveType(inline(arr), Context{}); got.GoExpr() != "[]string" {
		t.Errorf("nullable array wrapped unnecessarily: %q", got.GoExpr())
	}
}

func TestResolveType_NamedRef(t *testing.T) {
	b := newTestBuilder()
	b.doc.Components.Schemas["Account"] = inline(&openapi3.Schema{
		Type:       &openapi3.Types{"object"},
		Properties: openapi3.Schemas{"id": inline(primSchema("string", ""))},
	})
	got := b.resolveType(refTo("Account"), Context{})
	if got.GoExpr() != "Account" {
		t.Errorf("got %q", got.GoExpr())
	}
}

func TestResolveType_NamedArrayRefFlattens(t *testing.T) {
	b := newTestBuilder()
	b.doc.Components.Schemas["Accounts"] = inline(&openapi3.Schema{
		Type:  &openapi3.Types{"array"},
		Items: refTo("Account"),
	})
	b.doc.Components.Schemas["Account"] = inline(&openapi3.Schema{
		Type:       &openapi3.Types{"object"},
		Properties: openapi3.Schemas{"id": inline(primSchema("string", ""))},
	})
	got := b.resolveType(refTo("Accounts"), Context{})
	if got.GoExpr() != "[]Account" {
		t.Errorf("got %q", got.GoExpr())
	}
}

func TestResolveType_DoubleArrayCollapses(t *testing.T) {
	b := newTestBuilder()
	// Outer: inline array of $ref Inner.
	// Inner: array of $ref Item.
	b.doc.Components.Schemas["Inner"] = inline(&openapi3.Schema{
		Type:  &openapi3.Types{"array"},
		Items: refTo("Item"),
	})
	b.doc.Components.Schemas["Item"] = inline(&openapi3.Schema{
		Type:       &openapi3.Types{"object"},
		Properties: openapi3.Schemas{"x": inline(primSchema("string", ""))},
	})
	outer := &openapi3.Schema{
		Type:  &openapi3.Types{"array"},
		Items: refTo("Inner"),
	}
	got := b.resolveType(inline(outer), Context{})
	if got.GoExpr() != "[]Item" {
		t.Errorf("got %q; want []Item (double-wrap collapsed)", got.GoExpr())
	}
}

func TestResolveType_MapOnly(t *testing.T) {
	b := newTestBuilder()
	s := &openapi3.Schema{Type: &openapi3.Types{"object"}}
	has := true
	s.AdditionalProperties.Has = &has
	if got := b.resolveType(inline(s), Context{}); got.GoExpr() != "map[string]any" {
		t.Errorf("got %q", got.GoExpr())
	}
	s2 := &openapi3.Schema{Type: &openapi3.Types{"object"}}
	s2.AdditionalProperties.Schema = inline(primSchema("integer", "int64"))
	if got := b.resolveType(inline(s2), Context{}); got.GoExpr() != "map[string]int64" {
		t.Errorf("got %q", got.GoExpr())
	}
}

func TestResolveType_ResourceCollision(t *testing.T) {
	b := newTestBuilder()
	b.reserved["Customers"] = true // tag Customers is already claimed
	b.doc.Components.Schemas["Customers"] = inline(&openapi3.Schema{
		Type:       &openapi3.Types{"object"},
		Properties: openapi3.Schemas{"x": inline(primSchema("string", ""))},
	})
	got := b.resolveType(refTo("Customers"), Context{})
	if got.GoExpr() != "CustomersResponse" {
		t.Errorf("got %q; want CustomersResponse suffix", got.GoExpr())
	}
}

func TestResolveType_InlineObjectPromoted(t *testing.T) {
	b := newTestBuilder()
	s := &openapi3.Schema{
		Type: &openapi3.Types{"object"},
		Properties: openapi3.Schemas{
			"first_name": inline(primSchema("string", "")),
			"last_name":  inline(primSchema("string", "")),
		},
		Required: []string{"first_name", "last_name"},
	}
	got := b.resolveType(inline(s), Context{Parent: "Request", Field: "individual_name"})
	if got.GoExpr() != "*RequestIndividualName" {
		t.Errorf("got %q", got.GoExpr())
	}
	if _, ok := b.declByName["RequestIndividualName"]; !ok {
		t.Errorf("promoted Decl not registered: %v", b.declByName)
	}
}
