// Package build lowers an OpenAPI document into the generator's IR.
//
// The package's single, recursive type-resolution entry point is
// [*Builder.resolveType]. Every path through the OpenAPI type system
// passes through it, so adding a feature (a new format, a new shape
// like nullable maps) is a single-site change. Inline-object
// promotion is also done here: an inline object schema produces a
// synthesized top-level Decl and the resolver returns a Named
// reference to it.
package build

import (
	"sort"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"

	"github.com/greatliontech/revolut-go/cmd/revogen/ir"
	"github.com/greatliontech/revolut-go/cmd/revogen/names"
)

const internalCoreImport = "github.com/greatliontech/revolut-go/internal/core"

// Context carries naming hints for inline type synthesis. When
// resolveType encounters an inline object or an inline object array,
// it uses <Parent><Field> as the generated type name.
type Context struct {
	Parent string // parent Go type name (e.g. "ValidateAccountNameRequestUK")
	Field  string // current property's JSON name (e.g. "individual_name")
}

// resolveType converts an OpenAPI schema reference into an IR Type.
//
// It is the single dispatch point for every type the generator
// emits; callers never construct Type values directly from
// openapi3.SchemaRef.
//
// Behaviour summary:
//
//   - $ref to a component schema → Named(<resolved-go-name>).
//     Array-typed component schemas flatten to []Element; chains of
//     such wrappers collapse to a single []Element so the emitted
//     type matches the wire format.
//   - Primitives dispatch on (type, format) to the Go primitive.
//     Unknown formats fall back to the base type.
//   - Arrays → Slice(element); element resolved recursively.
//   - Inline objects get promoted to a named Decl via promoteInline
//     and Named is returned.
//   - allOf compositions are merged in place before resolution.
//   - nullable: true wraps the result in Pointer, except for types
//     that already express nullability natively (slice, map,
//     pointer).
//
// The receiver is nil-safe for ref == nil (returns nil) and for
// ref.Value == nil when ref.Ref is also empty.
func (b *Builder) resolveType(ref *openapi3.SchemaRef, ctx Context) *ir.Type {
	if ref == nil {
		return nil
	}
	// $ref path takes precedence. Sibling keys on a $ref are disallowed
	// by OpenAPI 3.0; the scrubber already removes them.
	if name := schemaRefName(ref); name != "" {
		return b.resolveNamedRef(name)
	}
	s := ref.Value
	if s == nil {
		return nil
	}
	t := b.resolveInlineSchema(s, ctx)
	if t != nil && s.Nullable {
		t = wrapNullable(t)
	}
	return t
}

// resolveNamedRef resolves a $ref that points into components/schemas.
// If the target is an array wrapper, the wrapper is unwrapped so the
// caller sees []Element rather than a Named that happens to be a
// slice. Chains of such wrappers (array-of-array-ref-to-array-...)
// collapse to a single []Element.
func (b *Builder) resolveNamedRef(specName string) *ir.Type {
	target := b.doc.Components.Schemas[specName]
	if target == nil || target.Value == nil {
		return b.maybePointerForSelfRef(specName, ir.Named(b.resolvedName(specName)))
	}
	v := target.Value
	if isArray(v) {
		elem := b.resolveType(v.Items, Context{Parent: specName, Field: "item"})
		// Collapse one level of double-wrap: Revolut's spec sometimes
		// nests `type: array, items: $ref: ListResponse` where
		// ListResponse is itself `type: array`. The wire format (per
		// examples) is a flat list, so we collapse here.
		if elem != nil && elem.IsSlice() {
			return elem
		}
		return ir.Slice(elem)
	}
	t := b.maybePointerForSelfRef(specName, ir.Named(b.resolvedName(specName)))
	// A component schema declared with `nullable: true` propagates
	// to every reference site. Without this wrap, a nullable string
	// alias like merchant's NextPageToken (`type: string,
	// nullable: true`) used in a response struct can't distinguish
	// "omitted"/"null" from "present empty string". Arrays already
	// carry nil-slice semantics so the wrap applies only to the
	// non-array branch.
	if v.Nullable && t != nil && !t.IsPointer() && t.Kind != ir.KindSlice && t.Kind != ir.KindMap {
		t = ir.Pointer(t)
	}
	return t
}

// maybePointerForSelfRef wraps t in a pointer when specName is the
// schema whose Decl is currently under construction. Breaks direct
// recursive types that would otherwise compile into `type X struct
// { Field X }` — invalid per Go's size rule. Slice/map references
// don't need pointer indirection because they're already indirect.
func (b *Builder) maybePointerForSelfRef(specName string, t *ir.Type) *ir.Type {
	if b.currentBuildSpec == "" || specName != b.currentBuildSpec {
		return t
	}
	if t.IsPointer() || t.IsSlice() || t.Kind == ir.KindMap {
		return t
	}
	return ir.Pointer(t)
}

// resolveInlineSchema handles every non-$ref shape. allOf is merged
// first so the caller sees a flat object.
func (b *Builder) resolveInlineSchema(s *openapi3.Schema, ctx Context) *ir.Type {
	if len(s.AllOf) > 0 {
		if merged := mergeAllOf(s); merged != nil {
			s = merged
		}
	}

	switch {
	case schemaTypeIs(s, "string"):
		// Inline string enums become named types so callers get
		// exported constants instead of bare strings.
		if len(s.Enum) > 0 {
			return ir.Named(b.promoteInlineEnum(s, ctx))
		}
		return b.resolveStringType(s)
	case schemaTypeIs(s, "integer"):
		return b.resolveIntegerType(s)
	case schemaTypeIs(s, "number"):
		return b.resolveNumberType(s)
	case schemaTypeIs(s, "boolean"):
		return ir.Prim("bool")
	case schemaTypeIs(s, "array"):
		if s.Items == nil {
			return nil
		}
		inner := b.resolveType(s.Items, Context{Parent: ctx.Parent, Field: ctx.Field + "_item"})
		if inner == nil {
			return nil
		}
		// Collapse a [][]X spec shape to []X. Revolut's vendored
		// specs declare `type: array, items: $ref: ListResponse`
		// where ListResponse is itself an array schema; the wire
		// format is a single flat list.
		if inner.IsSlice() {
			return inner
		}
		return ir.Slice(inner)
	case isObjectLike(s):
		return b.resolveObjectType(s, ctx)
	}

	// Schemas with discriminator / oneOf / anyOf but no concrete
	// type declaration are handled by the lower/ unions pass; at
	// this stage we don't synthesize anything for them here.
	return nil
}

func (b *Builder) resolveStringType(s *openapi3.Schema) *ir.Type {
	switch format := strings.ToLower(s.Format); {
	case format == "":
		// no format — fall through to pattern detection below
	case strings.Contains(format, "date-time"), format == "date":
		return ir.Prim("time.Time", "time")
	case format == "binary":
		return ir.Prim("io.Reader", "io")
	case format == "byte":
		// OpenAPI 3.0 base64-encoded binary; Go sees it as a string
		// because the wire format is still a JSON string.
	case format == "decimal":
		return ir.Prim("json.Number", "encoding/json")
	}
	// Pattern-based detection covers schemas that predate the formal
	// format registry: Revolut declares currency and country codes
	// this way.
	if s.Pattern == "^[A-Z]{3}$" {
		return ir.Prim("core.Currency", internalCoreImport)
	}
	return ir.Prim("string")
}

func (b *Builder) resolveIntegerType(s *openapi3.Schema) *ir.Type {
	switch strings.ToLower(s.Format) {
	case "int32":
		return ir.Prim("int32")
	case "int64":
		return ir.Prim("int64")
	}
	return ir.Prim("int")
}

func (b *Builder) resolveNumberType(s *openapi3.Schema) *ir.Type {
	// JSON numbers with arbitrary precision (currency amounts) must
	// stay as strings so we don't lose precision when the spec
	// declares `type: number`.
	return ir.Prim("json.Number", "encoding/json")
}

// resolveObjectType dispatches on the three inline-object shapes:
// map-only (additionalProperties without properties), fixed-
// properties (promoted to named Decl), and mixed (properties +
// additionalProperties, which requires a catch-all Extra field).
func (b *Builder) resolveObjectType(s *openapi3.Schema, ctx Context) *ir.Type {
	hasProps := len(s.Properties) > 0
	hasAdd := hasAdditionalProperties(s)

	switch {
	case !hasProps && hasAdd:
		return ir.Map(ir.Prim("string"), b.additionalValueType(s, ctx))
	case !hasProps:
		// `type: object` with neither properties nor additionalProperties
		// is a free-form JSON object. Fall back to map[string]any.
		return ir.Map(ir.Prim("string"), ir.Prim("any"))
	default:
		// Promote the inline object to a named Decl so it can be
		// referenced by its Go name everywhere.
		return ir.Pointer(ir.Named(b.promoteInline(s, ctx)))
	}
}

// additionalValueType resolves the value type for a map-shaped
// schema. A schema-form AdditionalProperties gives a concrete type;
// the `true` sentinel falls back to `any`.
func (b *Builder) additionalValueType(s *openapi3.Schema, ctx Context) *ir.Type {
	if sub := s.AdditionalProperties.Schema; sub != nil {
		if t := b.resolveType(sub, Context{Parent: ctx.Parent, Field: ctx.Field + "_value"}); t != nil {
			return t
		}
	}
	return ir.Prim("any")
}

// promoteInlineEnum synthesizes an enum Decl from an inline string
// enum schema, returning the derived Go name (<Parent><Field>).
// Idempotent on the derived name.
func (b *Builder) promoteInlineEnum(s *openapi3.Schema, ctx Context) string {
	parent := ctx.Parent
	if parent == "" {
		parent = "Anonymous"
	}
	derived := names.TypeName(parent) + names.TypeName(ctx.Field)
	if _, ok := b.declByName[derived]; ok {
		return derived
	}
	values := make([]ir.EnumValue, 0, len(s.Enum))
	for _, v := range s.Enum {
		sv, ok := v.(string)
		if !ok {
			continue
		}
		values = append(values, ir.EnumValue{
			GoName: derived + names.TypeName(sv),
			Value:  sv,
		})
	}
	d := &ir.Decl{
		Name:       derived,
		Kind:       ir.DeclEnum,
		Doc:        s.Description,
		EnumBase:   ir.Prim("string"),
		EnumValues: values,
	}
	b.declByName[derived] = d
	b.declOrder = append(b.declOrder, derived)
	return derived
}

// promoteInline synthesizes a top-level struct Decl for an inline
// object schema. The generated name is <Parent><Field> (e.g.
// ValidateAccountNameRequestUKIndividualName). Idempotent: repeated
// calls with the same derived name reuse the existing Decl.
//
// Field resolution inside the synthesized struct recurses through
// resolveType, so deeply-nested inline objects all get their own
// named Decls by the same rule.
func (b *Builder) promoteInline(s *openapi3.Schema, ctx Context) string {
	parent := ctx.Parent
	if parent == "" {
		parent = "Anonymous"
	}
	derived := names.TypeName(parent) + names.TypeName(ctx.Field)
	if _, ok := b.declByName[derived]; ok {
		return derived
	}
	// Seed the map before recursing so a cycle doesn't infinite-loop.
	placeholder := &ir.Decl{Name: derived, Kind: ir.DeclStruct}
	b.declByName[derived] = placeholder
	filled := b.structFromSchema(derived, s)
	*placeholder = *filled
	b.declOrder = append(b.declOrder, derived)
	return derived
}

// wrapNullable turns a bare type into its nullable form. Slices,
// maps, and pointers already admit nil; other types get wrapped in a
// pointer.
func wrapNullable(t *ir.Type) *ir.Type {
	switch t.Kind {
	case ir.KindPointer, ir.KindSlice, ir.KindMap:
		return t
	}
	return ir.Pointer(t)
}

// -- helpers on *openapi3.Schema ----------------------------------------

func schemaTypeIs(s *openapi3.Schema, kind string) bool {
	if s == nil || s.Type == nil {
		return false
	}
	return s.Type.Is(kind)
}

func isArray(s *openapi3.Schema) bool { return schemaTypeIs(s, "array") }

// isObjectLike reports whether a schema should be resolved as an
// object, covering explicit `type: object` and the implicit case
// where `properties` is set without a type tag (permissive specs).
func isObjectLike(s *openapi3.Schema) bool {
	if schemaTypeIs(s, "object") {
		return true
	}
	return len(s.Properties) > 0 || hasAdditionalProperties(s)
}

func hasAdditionalProperties(s *openapi3.Schema) bool {
	if s.AdditionalProperties.Schema != nil {
		return true
	}
	if s.AdditionalProperties.Has != nil && *s.AdditionalProperties.Has {
		return true
	}
	return false
}

func schemaRefName(ref *openapi3.SchemaRef) string {
	if ref == nil || ref.Ref == "" {
		return ""
	}
	const prefix = "#/components/schemas/"
	if strings.HasPrefix(ref.Ref, prefix) {
		return strings.TrimPrefix(ref.Ref, prefix)
	}
	return ""
}

// mergeAllOf flattens an allOf composition into a single inline
// object schema by merging Properties and Required across branches.
// Branches that can't be treated as objects cause the merge to bail
// (return nil), in which case the caller falls back to the original
// schema — which will typically fail resolution too, but fails
// visibly rather than silently producing wrong output.
func mergeAllOf(s *openapi3.Schema) *openapi3.Schema {
	out := &openapi3.Schema{
		Type:       &openapi3.Types{"object"},
		Properties: openapi3.Schemas{},
	}
	requiredSet := map[string]bool{}
	descriptions := []string{}
	if s.Description != "" {
		descriptions = append(descriptions, s.Description)
	}

	for _, branch := range s.AllOf {
		if branch == nil {
			continue
		}
		target := branch.Value
		if target == nil {
			return nil
		}
		if len(target.AllOf) > 0 {
			target = mergeAllOf(target)
			if target == nil {
				return nil
			}
		}
		if len(target.Properties) == 0 && target.Type != nil && !target.Type.Is("object") {
			return nil
		}
		for name, propRef := range target.Properties {
			out.Properties[name] = propRef
		}
		for _, r := range target.Required {
			requiredSet[r] = true
		}
		if target.Description != "" {
			descriptions = append(descriptions, target.Description)
		}
	}
	for r := range requiredSet {
		out.Required = append(out.Required, r)
	}
	sort.Strings(out.Required)
	if len(descriptions) > 0 {
		out.Description = strings.Join(descriptions, "\n\n")
	}
	return out
}
