package build

import (
	"fmt"
	"sort"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"

	"github.com/greatliontech/revolut-go/cmd/revogen/ir"
	"github.com/greatliontech/revolut-go/cmd/revogen/names"
)

// buildDecls walks components/schemas and produces one Decl per
// renderable entry. The resolution is recursive: resolveType may
// register additional Decls (inline object promotion) and
// buildDecls's ordering accounts for that.
//
// The walk is done in sorted schema-name order for deterministic
// output. When two schemas would collide on the same Go name
// (resource-vs-schema collision handled via the `reserved` set at
// resolution time; anything else deferred to lower/names.go), the
// first wins and the second is rejected here.
func (b *Builder) buildDecls() {
	if b.doc.Components == nil {
		return
	}
	for _, specName := range sortedSchemaNames(b.doc) {
		ref := b.doc.Components.Schemas[specName]
		if ref == nil || ref.Value == nil {
			continue
		}
		goName := b.resolvedName(specName)
		if _, exists := b.declByName[goName]; exists {
			// Already registered (typically from an earlier inline
			// promotion under the same name, or a resource collision
			// already resolved).
			continue
		}
		b.currentBuildSpec = specName
		decl := b.declFromSchema(goName, ref.Value)
		b.currentBuildSpec = ""
		if decl == nil {
			continue
		}
		b.registerDecl(goName, decl)
	}
}

// declFromSchema is the top-level dispatcher for component schemas.
// It classifies the schema into one of the Decl kinds and returns a
// ready-to-emit value, or nil for schemas that don't map to a
// top-level Go declaration (e.g. inline array wrappers that are
// unwrapped at the reference site).
func (b *Builder) declFromSchema(goName string, s *openapi3.Schema) *ir.Decl {
	if len(s.AllOf) > 0 {
		if merged := mergeAllOf(s); merged != nil {
			s = merged
		}
	}

	// Unions (discriminated, untagged oneOf/anyOf with named-only
	// branches) classify before the enum / object / alias paths so
	// schemas that carry both a discriminator and an empty type tag
	// don't accidentally fall through to the alias fallback.
	if d := b.unionDeclFromSchema(goName, s); d != nil {
		return d
	}

	// Enums take precedence over primitive aliases: a `type: string,
	// enum: [...]` schema is a named enum, not an alias for string.
	if len(s.Enum) > 0 {
		return b.enumFromSchema(goName, s)
	}

	// Map-shaped objects (additionalProperties only, no fixed
	// properties) are aliases to map[string]T rather than empty
	// structs. This keeps the emitted Go idiomatic and lets callers
	// range over the map directly.
	if isObjectLike(s) && len(s.Properties) == 0 && hasAdditionalProperties(s) {
		return &ir.Decl{
			Name:        goName,
			Kind:        ir.DeclAlias,
			Doc:         s.Description,
			AliasTarget: ir.Map(ir.Prim("string"), b.additionalValueType(s, Context{Parent: goName})),
		}
	}
	// Otherwise: struct with fixed properties (and optionally an
	// ExtraMap catch-all for additionalProperties).
	if isObjectLike(s) {
		return b.structFromSchema(goName, s)
	}

	// Array-typed component schemas are flattened to []Element at
	// the reference site; no top-level declaration is emitted here.
	if schemaTypeIs(s, "array") {
		return nil
	}

	// Remaining cases are primitive-alias schemas: `type: string`
	// with a pattern (e.g. currency), `type: integer`, etc.
	return b.aliasFromSchema(goName, s)
}

func (b *Builder) enumFromSchema(goName string, s *openapi3.Schema) *ir.Decl {
	base := ir.Prim("string")
	if schemaTypeIs(s, "integer") {
		base = ir.Prim("int64")
	}
	values := make([]ir.EnumValue, 0, len(s.Enum))
	// Dedup by wire value. Some upstream specs list the same string
	// twice (open-banking's ObbalanceType1code repeats InterimAvailable
	// and InterimBooked); without this guard the generator emits two
	// constants with identical values and the second is unreachable
	// by a switch — worse, the collision resolver V2-suffixes the
	// name and buries the duplication.
	seen := map[string]bool{}
	for _, v := range s.Enum {
		ev := ir.EnumValue{}
		var key string
		switch x := v.(type) {
		case string:
			ev.Value = x
			ev.GoName = goName + names.TypeName(x)
			key = "s:" + x
		case int, int32, int64:
			ev.Value = x
			ev.GoName = fmt.Sprintf("%s%v", goName, x)
			key = fmt.Sprintf("i:%v", x)
		case float64:
			// OpenAPI YAML parser often produces float64 for numeric enums.
			if x == float64(int64(x)) {
				ev.Value = int64(x)
				ev.GoName = fmt.Sprintf("%s%d", goName, int64(x))
				key = fmt.Sprintf("i:%d", int64(x))
			} else {
				continue // skip non-integer numeric enums; no ergonomic mapping
			}
		default:
			continue
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		values = append(values, ev)
	}
	return &ir.Decl{
		Name:       goName,
		Kind:       ir.DeclEnum,
		Doc:        s.Description,
		EnumBase:   base,
		EnumValues: values,
	}
}

// aliasFromSchema produces a DeclAlias for primitive-shaped top-level
// schemas. Pattern/format-based detection routes a currency-shaped
// string to core.Currency; everything else uses the underlying
// primitive.
func (b *Builder) aliasFromSchema(goName string, s *openapi3.Schema) *ir.Decl {
	target := b.resolveInlineSchema(s, Context{Parent: goName, Field: ""})
	if target == nil {
		return nil
	}
	return &ir.Decl{
		Name:        goName,
		Kind:        ir.DeclAlias,
		Doc:         s.Description,
		AliasTarget: target,
	}
}

// structFromSchema is used both for top-level component schemas and
// for inline object promotion (via resolveType). It must be
// idempotent on the `name` key: promoteInline seeds declByName with
// a placeholder before calling structFromSchema to break cycles.
func (b *Builder) structFromSchema(goName string, s *openapi3.Schema) *ir.Decl {
	if len(s.AllOf) > 0 {
		if merged := mergeAllOf(s); merged != nil {
			s = merged
		}
	}
	decl := &ir.Decl{
		Name: goName,
		Kind: ir.DeclStruct,
		Doc:  s.Description,
	}

	// Conditional-required anyOf: every branch is only a `required:`
	// list. Lift each group into AnyOfRequiredGroups for
	// lower/validators to consume.
	if isConditionalRequiredAnyOf(s) {
		for _, br := range s.AnyOf {
			group := append([]string(nil), br.Value.Required...)
			sort.Strings(group)
			decl.AnyOfRequiredGroups = append(decl.AnyOfRequiredGroups, group)
		}
	}

	requiredSet := stringSet(s.Required)
	for _, propName := range sortedKeys(s.Properties) {
		propRef := s.Properties[propName]
		if propRef == nil || propRef.Value == nil {
			continue
		}
		pv := propRef.Value
		isRequired := requiredSet[propName]
		fieldType := b.resolveType(propRef, Context{Parent: goName, Field: propName})
		if fieldType == nil {
			continue
		}
		// Optional non-pointer, non-slice, non-map primitives that
		// carry timestamps upgrade to pointer so "unset" is
		// distinguishable from the zero time.
		if !isRequired && fieldType.GoExpr() == "time.Time" {
			fieldType = ir.Pointer(fieldType)
		}
		decl.Fields = append(decl.Fields, &ir.Field{
			JSONName:       propName,
			GoName:         names.FieldName(propName),
			Type:           fieldType,
			Required:       isRequired,
			ReadOnly:       pv.ReadOnly,
			WriteOnly:      pv.WriteOnly,
			Deprecated:     deprecationMessage(pv),
			Doc:            firstLine(pv.Description),
			DefaultDoc:     defaultDoc(pv),
			DefaultLiteral: defaultLiteral(pv, fieldType),
		})
	}

	// additionalProperties combined with fixed properties: emit a
	// catch-all Extra field. The emitter will synthesize a custom
	// MarshalJSON / UnmarshalJSON that splits and merges the map.
	if len(s.Properties) > 0 && hasAdditionalProperties(s) {
		decl.ExtraMap = b.additionalValueType(s, Context{Parent: goName, Field: "extra"})
	}

	return decl
}

func (b *Builder) registerDecl(goName string, decl *ir.Decl) {
	b.declByName[goName] = decl
	b.declOrder = append(b.declOrder, goName)
}

// -- helpers -----------------------------------------------------------

func sortedSchemaNames(doc *openapi3.T) []string {
	if doc.Components == nil {
		return nil
	}
	out := make([]string, 0, len(doc.Components.Schemas))
	for name := range doc.Components.Schemas {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func stringSet(values []string) map[string]bool {
	out := map[string]bool{}
	for _, v := range values {
		out[v] = true
	}
	return out
}

func firstLine(s string) string {
	if i := strings.Index(s, "\n"); i >= 0 {
		return s[:i]
	}
	return s
}

// deprecationMessage returns the text for the field's `// Deprecated:`
// godoc line, or "" if the field is not deprecated. The schema's
// description (if any) is reused as the explanation; otherwise a
// generic placeholder keeps the godoc convention intact.
func deprecationMessage(s *openapi3.Schema) string {
	if !s.Deprecated {
		return ""
	}
	if s.Description != "" {
		return firstLine(s.Description)
	}
	return "retained for backwards compatibility; the API may remove it."
}

// defaultDoc formats the spec-level `default:` value for inclusion
// in a field's godoc. Machine-readable defaults are rendered
// inline; prose defaults (e.g. "the current time") survive as a
// textual note.
func defaultDoc(s *openapi3.Schema) string {
	if s.Default == nil {
		return ""
	}
	switch v := s.Default.(type) {
	case string:
		if v == "" {
			return ""
		}
		return fmt.Sprintf("Default: %q.", v)
	case bool, int, int32, int64, float32, float64:
		return fmt.Sprintf("Default: %v.", v)
	case []any:
		return "" // array defaults are rare and awkward to render
	}
	return ""
}

// defaultLiteral returns a Go expression evaluating to the spec's
// declared default, type-appropriate for the field it'll be
// assigned to. Bool defaults are skipped because false is
// indistinguishable from "unset" without a pointer. Returns "" for
// prose / missing / unsafe defaults.
//
// The field's Go type decides the literal shape:
//
//   - plain int primitives: 100
//   - json.Number (Revolut's arbitrary-precision ints): json.Number("100")
//   - named string-backed enums: T("value")
//   - plain string: "value"
func defaultLiteral(s *openapi3.Schema, fieldType *ir.Type) string {
	if s.Default == nil {
		return ""
	}
	raw := ""
	switch v := s.Default.(type) {
	case string:
		if v == "" {
			return ""
		}
		// A string default that contains whitespace is almost
		// always prose ("the date-time at which the request is
		// made", "Default label according to card's type") rather
		// than a wire value. Assigning prose as a default would
		// send garbage to the server; DefaultDoc still carries the
		// text for godoc, just not for ApplyDefaults.
		if strings.ContainsAny(v, " \t\n") {
			return ""
		}
		lit := fmt.Sprintf("%q", v)
		if fieldType != nil && fieldType.Kind == ir.KindNamed {
			return fieldType.GoExpr() + "(" + lit + ")"
		}
		return lit
	case int:
		raw = fmt.Sprintf("%d", v)
	case int32:
		raw = fmt.Sprintf("%d", v)
	case int64:
		raw = fmt.Sprintf("%d", v)
	case float64:
		// kin-openapi decodes YAML integers as float64; recover the
		// integer form when the value is whole so Go's type system
		// doesn't complain about assigning a float to an int field.
		if v == float64(int64(v)) {
			raw = fmt.Sprintf("%d", int64(v))
		} else {
			raw = fmt.Sprintf("%v", v)
		}
	default:
		return ""
	}
	// Numeric literals need wrapping when the target field type
	// isn't a raw Go numeric — json.Number is a string alias, and
	// named numeric types would need a cast.
	if fieldType != nil && fieldType.Kind == ir.KindPrim && fieldType.Name == "json.Number" {
		return fmt.Sprintf(`json.Number(%q)`, raw)
	}
	if fieldType != nil && fieldType.Kind == ir.KindNamed {
		return fieldType.GoExpr() + "(" + raw + ")"
	}
	return raw
}

// isConditionalRequiredAnyOf detects OpenAPI's validation-only anyOf
// pattern: every branch is an inline schema whose only key is
// `required:`. The parent struct is valid when at least one branch's
// field group is fully set; lower/validators emits the check.
func isConditionalRequiredAnyOf(s *openapi3.Schema) bool {
	if len(s.AnyOf) == 0 {
		return false
	}
	for _, br := range s.AnyOf {
		if br == nil || br.Ref != "" || br.Value == nil {
			return false
		}
		v := br.Value
		if len(v.Required) == 0 {
			return false
		}
		if v.Type != nil || len(v.Properties) > 0 || len(v.AllOf) > 0 ||
			len(v.OneOf) > 0 || len(v.AnyOf) > 0 || v.Items != nil {
			return false
		}
	}
	return true
}
