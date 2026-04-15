package main

import (
	"sort"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
)

// buildConfig bundles the caller's per-package knobs so multiple
// target packages (business, merchant, open-banking, ...) can share
// the generator without string surgery at call sites.
type buildConfig struct {
	PackageName       string
	ResourceAllow     []string
	IncludeDeprecated bool

	// ErrPrefix is prepended to validation error messages emitted by
	// generated methods (e.g. "business", "merchant"). Typically the
	// package name.
	ErrPrefix string

	// DocsBase is the base URL for per-operation godoc links. The
	// operation ID is appended in kebab case.
	DocsBase string
}

// buildSpec walks an openapi3 document and produces a normalized Spec.
func buildSpec(doc *openapi3.T, cfg buildConfig) (*Spec, error) {
	b := &builder{
		cfg:         cfg,
		doc:         doc,
		allow:       stringSet(cfg.ResourceAllow),
		emitted:     map[string]bool{},
		typesByName: map[string]*NamedType{},
	}
	b.collectResourceNames()
	b.buildTypes()
	b.buildResources()
	b.promoteRequestBodyPointers()
	b.buildValidators()
	return b.spec(cfg.PackageName), nil
}

// collectResourceNames pre-computes the set of Go identifiers that
// will be used for per-tag resource structs. buildTypes consults this
// set so a schema whose name collides with a tag gets a `Response`
// suffix. Without this, Go emits a "redeclared in this block" error
// when the spec has e.g. a `Customers` tag and a `Customers` schema.
func (b *builder) collectResourceNames() {
	b.resourceNames = map[string]bool{}
	for _, path := range sortedPaths(b.doc) {
		item := b.doc.Paths.Value(path)
		if item == nil {
			continue
		}
		for _, op := range item.Operations() {
			if op == nil {
				continue
			}
			if len(b.allow) > 0 && len(op.Tags) > 0 && !b.allow[op.Tags[0]] {
				continue
			}
			tag := "Untagged"
			if len(op.Tags) > 0 {
				tag = op.Tags[0]
			}
			b.resourceNames[goTypeName(tag)] = true
		}
	}
}

// goTypeFromSpec resolves a spec schema name to its Go type name,
// avoiding collisions with resource struct names.
func (b *builder) goTypeFromSpec(specName string) string {
	name := goTypeName(specName)
	if b.resourceNames[name] {
		return name + "Response"
	}
	return name
}

// buildValidators runs the recursive required-field walker for every
// operation's request body. Done as a post-pass — after type promotion —
// so the walker observes the final Go types (e.g. *int64 instead of
// int64 for promoted required ints).
func (b *builder) buildValidators() {
	for _, r := range b.resources {
		for _, op := range r.Ops {
			if op.RequestType == "" {
				continue
			}
			t, ok := b.typesByName[op.RequestType]
			if !ok || t.Kind != KindStruct {
				continue
			}
			visited := map[string]bool{op.RequestType: true}
			op.Validate = b.walkRequired(t, "req", op.RequestType, visited)
		}
	}
}

// promoteRequestBodyPointers rewrites required bool / integer fields on
// top-level request-body struct types to pointer types. Without this,
// the zero value (false / 0) is indistinguishable from "user forgot to
// set the field", which is unsafe for a banking SDK: a caller who
// intends to send `false` and one who simply didn't fill the struct
// would produce the same payload. Promotion to *bool / *int64 forces
// an explicit choice, validated as `!= nil` before the HTTP call.
//
// Scope is deliberately narrow: only the top-level request type. Nested
// types may be shared with response bodies, where such promotion would
// change the decoded shape.
func (b *builder) promoteRequestBodyPointers() {
	requestBodies := map[string]bool{}
	for _, r := range b.resources {
		for _, op := range r.Ops {
			if op.RequestType != "" {
				requestBodies[op.RequestType] = true
			}
		}
	}
	for _, t := range b.types {
		if !requestBodies[t.GoName] || t.Kind != KindStruct {
			continue
		}
		for _, f := range t.Fields {
			if !f.Required {
				continue
			}
			switch f.GoType {
			case "bool":
				f.GoType = "*bool"
			case "int", "int32", "int64":
				f.GoType = "*int64"
			}
		}
	}
}

type builder struct {
	cfg         buildConfig
	doc         *openapi3.T
	allow       map[string]bool // empty = allow all
	resources   []*Resource
	types       []*NamedType
	typesByName map[string]*NamedType
	emitted     map[string]bool

	// resourceNames is the set of Go identifiers reserved for
	// generated resource structs (one per OpenAPI tag). Populated
	// before buildTypes so schema names colliding with a resource
	// (e.g. spec tag `Customers` + schema `Customers` as a pagination
	// wrapper) get a `Response` suffix to disambiguate.
	resourceNames map[string]bool
}

func (b *builder) spec(pkg string) *Spec {
	// Stable output: sort resources by name, sort types by name.
	sort.Slice(b.resources, func(i, j int) bool { return b.resources[i].GoName < b.resources[j].GoName })
	sort.Slice(b.types, func(i, j int) bool { return b.types[i].GoName < b.types[j].GoName })
	return &Spec{
		PackageName: pkg,
		ErrPrefix:   b.cfg.ErrPrefix,
		Resources:   b.resources,
		Types:       b.types,
	}
}

// --- types --------------------------------------------------------------

func (b *builder) buildTypes() {
	for _, name := range sortedSchemaNames(b.doc) {
		ref := b.doc.Components.Schemas[name]
		if ref == nil || ref.Value == nil {
			continue
		}
		t := b.typeFromSchema(name, ref.Value)
		if t == nil {
			continue
		}
		b.addType(t)
	}
}

// typeFromSchema returns a NamedType for a component schema, or nil if
// the schema is not renderable as a top-level Go type (e.g. an inline
// array type that we handle as []T at reference sites).
func (b *builder) typeFromSchema(name string, s *openapi3.Schema) *NamedType {
	goName := b.goTypeFromSpec(name)

	// Discriminated union. Must be detected before allOf/object fall-through
	// since Revolut sometimes combines a `discriminator` with an empty-properties
	// object shape.
	if s.Discriminator != nil && len(s.Discriminator.Mapping) > 0 {
		variants := make([]UnionVariant, 0, len(s.Discriminator.Mapping))
		for _, tag := range sortedKeys(s.Discriminator.Mapping) {
			ref := s.Discriminator.Mapping[tag].Ref
			const prefix = "#/components/schemas/"
			if !strings.HasPrefix(ref, prefix) {
				continue
			}
			variantName := strings.TrimPrefix(ref, prefix)
			variants = append(variants, UnionVariant{
				Tag:    tag,
				GoName: goTypeName(variantName),
			})
		}
		if len(variants) > 0 {
			return &NamedType{
				GoName:        goName,
				Kind:          KindUnion,
				Doc:           s.Description,
				UnionVariants: variants,
			}
		}
	}

	// Untagged union: oneOf (or anyOf in a type-composition sense)
	// where every branch is a component $ref. The tag string defaults
	// to the variant's Go name since the spec provides no explicit one.
	if variants := b.unionFromRefBranches(s.OneOf); len(variants) > 0 {
		return &NamedType{
			GoName:        goName,
			Kind:          KindUnion,
			Doc:           s.Description,
			UnionVariants: variants,
		}
	}
	if variants := b.unionFromRefBranches(s.AnyOf); len(variants) > 0 && !isConditionalRequiredAnyOf(s) {
		return &NamedType{
			GoName:        goName,
			Kind:          KindUnion,
			Doc:           s.Description,
			UnionVariants: variants,
		}
	}

	// allOf composition: flatten by merging the object branches.
	if len(s.AllOf) > 0 {
		merged := mergeAllOf(s)
		if merged != nil {
			s = merged
		}
	}

	// Enum — default to string-backed when the base type is unset.
	if len(s.Enum) > 0 {
		base := "string"
		if schemaTypeIs(s, "integer") {
			base = "int"
		}
		values := make([]EnumValue, 0, len(s.Enum))
		for _, v := range s.Enum {
			if sv, ok := v.(string); ok {
				values = append(values, EnumValue{
					GoName: goName + goTypeName(sv),
					Value:  sv,
				})
			}
		}
		return &NamedType{
			GoName:     goName,
			Kind:       KindEnum,
			Doc:        s.Description,
			EnumBase:   base,
			EnumValues: values,
		}
	}

	// Object with properties → struct.
	if schemaTypeIs(s, "object") || len(s.Properties) > 0 {
		return b.structFromSchema(goName, s)
	}

	// Primitive aliases.
	if schemaTypeIs(s, "string") {
		target := "string"
		if isCurrencyPattern(s) {
			target = "core.Currency"
		}
		return &NamedType{GoName: goName, Kind: KindAlias, Doc: s.Description, AliasTarget: target}
	}
	if schemaTypeIs(s, "integer") {
		return &NamedType{GoName: goName, Kind: KindAlias, Doc: s.Description, AliasTarget: "int"}
	}
	if schemaTypeIs(s, "number") {
		return &NamedType{GoName: goName, Kind: KindAlias, Doc: s.Description, AliasTarget: "json.Number"}
	}
	if schemaTypeIs(s, "boolean") {
		return &NamedType{GoName: goName, Kind: KindAlias, Doc: s.Description, AliasTarget: "bool"}
	}

	// Fallback for schemas we can't represent precisely yet
	// (discriminator-only, anyOf/oneOf, pure array wrappers handled
	// at use sites, ...): emit as an alias to `any` so that references
	// resolve and the emitted code still compiles. Callers can pass
	// concrete values; Revolut validates on the wire.
	//
	// Array-typed schemas are still skipped — they are unwrapped at
	// reference sites.
	if schemaTypeIs(s, "array") {
		return nil
	}
	return &NamedType{
		GoName:      goName,
		Kind:        KindAlias,
		Doc:         s.Description,
		AliasTarget: "any",
	}
}

// goTypeExpr renders a Go type expression for a schema reference.
// Component $refs are resolved to their Go name; array-wrapper component
// schemas (e.g. Accounts → array of Account) are flattened to []Element;
// primitives are mapped by (type, format).
func (b *builder) goTypeExpr(ref *openapi3.SchemaRef) string {
	if ref == nil {
		return ""
	}
	if name := schemaRefName(ref); name != "" {
		if target := b.doc.Components.Schemas[name]; target != nil && target.Value != nil {
			if schemaTypeIs(target.Value, "array") {
				if inlineName, ok := b.maybePromoteArrayItemObject(name, target.Value); ok {
					return "[]" + inlineName
				}
				return "[]" + b.goTypeExpr(target.Value.Items)
			}
		}
		return b.goTypeFromSpec(name)
	}
	s := ref.Value
	if s == nil {
		return ""
	}
	switch {
	case schemaTypeIs(s, "string"):
		if isDateTimeFormat(s.Format) {
			return "time.Time"
		}
		if s.Format == "binary" {
			return "io.Reader"
		}
		return "string"
	case schemaTypeIs(s, "integer"):
		return "int"
	case schemaTypeIs(s, "number"):
		return "json.Number"
	case schemaTypeIs(s, "boolean"):
		return "bool"
	case schemaTypeIs(s, "array"):
		inner := b.goTypeExpr(s.Items)
		if inner == "" {
			return ""
		}
		return "[]" + inner
	}
	return ""
}

// detectPagination inspects an operation's response and params to
// classify its pagination shape. Cursor takes priority over time-window
// since it's unambiguous when present.
func (b *builder) detectPagination(o *Operation) *Pagination {
	if o.ParamsStruct == nil || o.ResponseType == "" {
		return nil
	}

	// Cursor: response is a struct with a NextPageToken field and one
	// array-valued items field; params has a PageToken field.
	if !strings.HasPrefix(o.ResponseType, "[]") {
		if rt := b.typesByName[o.ResponseType]; rt != nil && rt.Kind == KindStruct {
			var nextField, nextType, itemsField, itemType string
			for _, f := range rt.Fields {
				if f.JSONName == "next_page_token" {
					nextField = f.GoName
					nextType = f.GoType
					continue
				}
				if strings.HasPrefix(f.GoType, "[]") && itemsField == "" {
					itemsField = f.GoName
					itemType = strings.TrimPrefix(f.GoType, "[]")
				}
			}
			if nextField != "" && itemsField != "" {
				for _, pf := range o.ParamsStruct.Fields {
					if pf.JSONName == "page_token" {
						return &Pagination{
							Shape:          PaginationCursor,
							ItemType:       itemType,
							ItemsField:     itemsField,
							NextTokenField: nextField,
							NextTokenType:  nextType,
							PageTokenParam: pf.GoName,
							PageTokenType:  pf.GoType,
						}
					}
				}
			}
		}
	}

	// Time-window: response is a []ItemType whose items carry a
	// created_at timestamp, and params has a "to" or "created_before"
	// cursor field typed as time.Time.
	if strings.HasPrefix(o.ResponseType, "[]") {
		itemType := strings.TrimPrefix(o.ResponseType, "[]")
		if it := b.typesByName[itemType]; it != nil && it.Kind == KindStruct {
			var fromItemField string
			for _, f := range it.Fields {
				if f.JSONName == "created_at" && (f.GoType == "time.Time" || f.GoType == "*time.Time") {
					fromItemField = f.GoName
					break
				}
			}
			if fromItemField != "" {
				for _, pf := range o.ParamsStruct.Fields {
					if (pf.JSONName == "to" || pf.JSONName == "created_before") && pf.GoType == "time.Time" {
						return &Pagination{
							Shape:           PaginationTimeWindow,
							ItemType:        itemType,
							AdvanceParam:    pf.GoName,
							AdvanceFromItem: fromItemField,
						}
					}
				}
			}
		}
	}

	return nil
}

// buildParamsStruct lifts an operation's query parameters into a
// dedicated struct so callers can set them ergonomically and leave
// unspecified params as the zero value.
func buildParamsStruct(goName string, params []*QueryParam, summary string) *NamedType {
	fields := make([]*StructField, 0, len(params))
	for _, p := range params {
		fields = append(fields, &StructField{
			JSONName: p.Name,
			GoName:   p.GoName,
			GoType:   p.GoType,
			Doc:      p.Doc,
		})
	}
	doc := summary
	if doc != "" {
		doc = "Query parameters for: " + doc
	}
	return &NamedType{
		GoName: goName,
		Kind:   KindStruct,
		Doc:    doc,
		Fields: fields,
	}
}

// synthesizeInlineStruct builds a NamedType for a schema found inline
// at an operation's response (no $ref in components/schemas). Used for
// paginated container shapes like "{ next_page_token, items[] }".
func (b *builder) synthesizeInlineStruct(goName string, s *openapi3.Schema) *NamedType {
	return b.structFromSchema(goName, s)
}

// structFromSchema produces a KindStruct NamedType from an OpenAPI
// object schema. This is the single entry point for both top-level
// component schemas and inline sub-schemas promoted to named types. It
// honours `readOnly`, `writeOnly`, field-level `deprecated`, and
// `nullable` on each property, and collapses `additionalProperties`
// into a `map[string]T` when there are no fixed `properties`.
func (b *builder) structFromSchema(goName string, s *openapi3.Schema) *NamedType {
	var anyOfGroups [][]string
	if isConditionalRequiredAnyOf(s) {
		for _, br := range s.AnyOf {
			group := append([]string(nil), br.Value.Required...)
			sort.Strings(group)
			anyOfGroups = append(anyOfGroups, group)
		}
	}
	// Map-shaped object: no fixed properties, only additionalProperties.
	if len(s.Properties) == 0 && hasAdditionalProperties(s) {
		inner := additionalPropertyType(b, s)
		return &NamedType{
			GoName:      goName,
			Kind:        KindAlias,
			Doc:         s.Description,
			AliasTarget: "map[string]" + inner,
		}
	}

	required := stringSet(s.Required)
	fields := make([]*StructField, 0, len(s.Properties))
	for _, propName := range sortedKeys(s.Properties) {
		propRef := s.Properties[propName]
		if propRef == nil || propRef.Value == nil {
			continue
		}
		pv := propRef.Value
		isRequired := required[propName]
		goType := b.goTypePromoteEnum(goName, propName, propRef)
		if goType == "" {
			continue
		}
		if goType == "time.Time" && !isRequired {
			goType = "*time.Time"
		}
		if pv.Nullable && !strings.HasPrefix(goType, "*") && !strings.HasPrefix(goType, "[]") && !strings.HasPrefix(goType, "map[") {
			goType = "*" + goType
		}
		fields = append(fields, &StructField{
			JSONName:   propName,
			GoName:     goFieldName(propName),
			GoType:     goType,
			Required:   isRequired,
			Doc:        firstLine(pv.Description),
			ReadOnly:   pv.ReadOnly,
			WriteOnly:  pv.WriteOnly,
			Deprecated: deprecationMessage(pv),
		})
	}
	return &NamedType{
		GoName:              goName,
		Kind:                KindStruct,
		Doc:                 s.Description,
		Fields:              fields,
		AnyOfRequiredGroups: anyOfGroups,
	}
}

// unionFromRefBranches converts a oneOf/anyOf slice whose every branch
// is a simple component $ref into an ordered list of UnionVariants.
// Returns nil if any branch is inline (then we lack a stable name for
// the variant) or if the slice is empty.
func (b *builder) unionFromRefBranches(branches openapi3.SchemaRefs) []UnionVariant {
	if len(branches) == 0 {
		return nil
	}
	out := make([]UnionVariant, 0, len(branches))
	for _, br := range branches {
		if br == nil {
			return nil
		}
		name := schemaRefName(br)
		if name == "" {
			return nil
		}
		out = append(out, UnionVariant{Tag: name, GoName: b.goTypeFromSpec(name)})
	}
	return out
}

// isConditionalRequiredAnyOf detects OpenAPI's validation-only anyOf
// pattern: every branch is an inline schema with only a `required:`
// list (and no type / properties / refs of its own). These describe
// "at least one of these field groups must be supplied", not a type
// union. Callers use this to decide whether to emit a union or lift
// the constraint into the parent struct's validator.
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

// sanitisePath reduces a spec path template to a set of word-safe
// tokens suitable for feeding into goTypeName — strip curly params
// and leading slash, collapse separators.
func sanitisePath(p string) string {
	p = strings.TrimPrefix(p, "/")
	var out strings.Builder
	skip := false
	for _, r := range p {
		switch {
		case r == '{':
			skip = true
		case r == '}':
			skip = false
		case skip:
			// inside {param}: drop
		default:
			out.WriteRune(r)
		}
	}
	return out.String()
}

// pickRawResponseContent returns the preferred non-JSON response
// MIME type for an operation's content map, or "" when none of the
// known raw types are declared. Preference order: text/csv,
// application/pdf, text/plain, then any other text/* entry.
func pickRawResponseContent(content openapi3.Content) string {
	for _, preferred := range []string{"text/csv", "application/pdf", "text/plain", "application/octet-stream", "*/*"} {
		if _, ok := content[preferred]; ok {
			return preferred
		}
	}
	for mime := range content {
		if strings.HasPrefix(mime, "text/") || strings.HasPrefix(mime, "image/") || strings.HasPrefix(mime, "application/") {
			return mime
		}
	}
	return ""
}

// hasAdditionalProperties reports whether a schema's
// `additionalProperties` is a non-trivial declaration — either a
// typed sub-schema or an explicit `true`. `false` and the default
// behaviour (absent) both yield false.
func hasAdditionalProperties(s *openapi3.Schema) bool {
	if s.AdditionalProperties.Schema != nil {
		return true
	}
	if s.AdditionalProperties.Has != nil && *s.AdditionalProperties.Has {
		return true
	}
	return false
}

// additionalPropertyType returns the Go type expression for the
// value type of a map-shaped schema.
func additionalPropertyType(b *builder, s *openapi3.Schema) string {
	if sub := s.AdditionalProperties.Schema; sub != nil {
		if t := b.resolveRefTypeExpr(sub); t != "" {
			return t
		}
	}
	return "any"
}

// deprecationMessage returns the text for a `// Deprecated:` comment
// if the schema is marked `deprecated: true`. A schema's description
// often doubles as the deprecation reason, but the flag alone is
// enough to surface the annotation.
func deprecationMessage(s *openapi3.Schema) string {
	if !s.Deprecated {
		return ""
	}
	if s.Description != "" {
		return firstLine(s.Description)
	}
	return "retained for backwards compatibility; the API may remove it."
}

// mergeAllOf flattens an allOf composition into a single inline object
// schema. Each branch is either a $ref (resolved into its target
// schema) or an inline object; we union their Properties and Required
// sets. Non-object branches cause the merge to bail so the caller can
// fall back to skipping the schema.
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

// goTypePromoteEnum is goTypeExpr with two normalization passes.
//
// 1. Inline string enums on a struct property (no $ref, not a named
//    component type) are promoted to a derived named enum type
//    (e.g. Account.state → AccountState) so callers get exported
//    constants rather than bare strings.
//
// 2. Inline object schemas on a struct property — the spec form
//    `foo: { type: object, properties: {...} }` — are promoted to a
//    derived named struct type (e.g. `ValidateAccountNameRequestUk`
//    .individual_name → `ValidateAccountNameRequestUkIndividualName`).
//    Without this, goTypeExpr would return "" for the field and the
//    emitted struct would silently lose that property.
//
// Inline object arrays — `foo: { type: array, items: { type: object,
// ... } }` — are similarly unwrapped: the element type is promoted
// and the field emitted as `[]<DerivedName>`.
func (b *builder) goTypePromoteEnum(parentGoName, propName string, ref *openapi3.SchemaRef) string {
	if ref != nil && ref.Ref == "" && ref.Value != nil && len(ref.Value.Enum) > 0 && schemaTypeIs(ref.Value, "string") {
		enumName := parentGoName + goTypeName(propName)
		if b.typesByName[enumName] == nil {
			values := make([]EnumValue, 0, len(ref.Value.Enum))
			for _, v := range ref.Value.Enum {
				sv, ok := v.(string)
				if !ok {
					continue
				}
				values = append(values, EnumValue{
					GoName: enumName + goTypeName(sv),
					Value:  sv,
				})
			}
			b.addType(&NamedType{
				GoName:     enumName,
				Kind:       KindEnum,
				Doc:        ref.Value.Description,
				EnumBase:   "string",
				EnumValues: values,
			})
		}
		return enumName
	}
	if name, ok := b.promoteInlineObject(parentGoName, propName, ref); ok {
		return name
	}
	if name, ok := b.promoteInlineObjectArray(parentGoName, propName, ref); ok {
		return name
	}
	return b.goTypeExpr(ref)
}

// promoteInlineObject synthesizes a named struct type for an inline
// object schema referenced from a parent struct property. Returns the
// derived Go type name or ("", false) if ref is not an inline object.
// Pointer-valued by default so optional inline sub-objects don't force
// callers to materialise an empty struct; required ones are handled
// at the parent level (Required flag on the StructField).
func (b *builder) promoteInlineObject(parentGoName, propName string, ref *openapi3.SchemaRef) (string, bool) {
	if ref == nil || ref.Ref != "" || ref.Value == nil {
		return "", false
	}
	v := ref.Value
	if !schemaTypeIs(v, "object") && len(v.Properties) == 0 {
		return "", false
	}
	// Map-shaped object: type: object with no fixed properties but
	// additionalProperties (typed or `true`). Emit `map[string]T`
	// directly without synthesising a named type.
	if len(v.Properties) == 0 {
		if hasAdditionalProperties(v) {
			return "map[string]" + additionalPropertyType(b, v), true
		}
		return "", false
	}
	name := parentGoName + goTypeName(propName)
	if b.typesByName[name] == nil {
		b.addType(b.synthesizeInlineStruct(name, v))
	}
	return "*" + name, true
}

// promoteInlineObjectArray handles `type: array, items: { inline
// object }`. The items are promoted to a named struct via
// promoteInlineObject and the returned expression is `[]<Name>` (no
// pointer — consistent with []NamedType elsewhere in the generator).
func (b *builder) promoteInlineObjectArray(parentGoName, propName string, ref *openapi3.SchemaRef) (string, bool) {
	if ref == nil || ref.Ref != "" || ref.Value == nil {
		return "", false
	}
	if !schemaTypeIs(ref.Value, "array") {
		return "", false
	}
	items := ref.Value.Items
	if items == nil || items.Ref != "" || items.Value == nil {
		return "", false
	}
	iv := items.Value
	if !schemaTypeIs(iv, "object") || len(iv.Properties) == 0 {
		return "", false
	}
	itemName := parentGoName + goTypeName(propName) + "Item"
	if b.typesByName[itemName] == nil {
		b.addType(b.synthesizeInlineStruct(itemName, iv))
	}
	return "[]" + itemName, true
}

// resolveRefTypeExpr turns a schema reference (as found on a request
// body or response) into a Go type expression. Array-wrapper component
// schemas (e.g. Accounts → array of Account) are flattened to []Element.
//
// Revolut's spec occasionally nests an "array" response schema around an
// already-array named schema (an inline "type: array, items: $ref:
// SomeListResponse" where SomeListResponse is itself "type: array, items:
// $ref: SomeItem"). The wire format, verified against the examples, is
// a flat list of SomeItem. We collapse any chain of array wrappers down
// to a single []Element so the emitted type matches reality.
func (b *builder) resolveRefTypeExpr(ref *openapi3.SchemaRef) string {
	expr := b.resolveRefTypeExprRaw(ref)
	for strings.HasPrefix(expr, "[][]") {
		expr = expr[2:]
	}
	return expr
}

func (b *builder) resolveRefTypeExprRaw(ref *openapi3.SchemaRef) string {
	if ref == nil {
		return ""
	}
	if name := schemaRefName(ref); name != "" {
		if target := b.doc.Components.Schemas[name]; target != nil && target.Value != nil {
			if schemaTypeIs(target.Value, "array") {
				if inlineName, ok := b.maybePromoteArrayItemObject(name, target.Value); ok {
					return "[]" + inlineName
				}
				return "[]" + b.resolveRefTypeExprRaw(target.Value.Items)
			}
		}
		return b.goTypeFromSpec(name)
	}
	if ref.Value != nil && schemaTypeIs(ref.Value, "array") {
		return "[]" + b.resolveRefTypeExprRaw(ref.Value.Items)
	}
	return b.goTypeExpr(ref)
}

// maybePromoteArrayItemObject synthesizes a named type for the items
// of a named array schema when those items are an inline object. Uses
// `<ContainerName>Item` as the generated Go name. Returns (name, true)
// when a type was produced, ("", false) when the items are a $ref or
// primitive and need no synthesis.
func (b *builder) maybePromoteArrayItemObject(containerName string, arr *openapi3.Schema) (string, bool) {
	items := arr.Items
	if items == nil || items.Ref != "" || items.Value == nil {
		return "", false
	}
	iv := items.Value
	if !schemaTypeIs(iv, "object") || len(iv.Properties) == 0 {
		return "", false
	}
	name := goTypeName(containerName) + "Item"
	if b.typesByName[name] == nil {
		b.addType(b.synthesizeInlineStruct(name, iv))
	}
	return name, true
}

func (b *builder) addType(t *NamedType) {
	if b.emitted[t.GoName] {
		return
	}
	b.emitted[t.GoName] = true
	b.types = append(b.types, t)
	b.typesByName[t.GoName] = t
}

// --- resources ----------------------------------------------------------

func (b *builder) buildResources() {
	byTag := map[string][]*Operation{}
	for _, path := range sortedPaths(b.doc) {
		item := b.doc.Paths.Value(path)
		if item == nil {
			continue
		}
		for _, httpMethod := range []string{"GET", "POST", "PUT", "PATCH", "DELETE"} {
			op := getOperation(item, httpMethod)
			if op == nil {
				continue
			}
			tag := "Untagged"
			if len(op.Tags) > 0 {
				tag = op.Tags[0]
			}
			if len(b.allow) > 0 && !b.allow[tag] {
				continue
			}
			if op.Deprecated && !b.cfg.IncludeDeprecated {
				continue
			}
			// Tags ending in "(deprecated)" signal a whole resource
			// being retired; skip unless the caller opts in.
			if !b.cfg.IncludeDeprecated && containsDeprecatedMarker(op.Tags) {
				continue
			}
			built := b.buildOperation(httpMethod, path, op)
			if built == nil {
				continue
			}
			byTag[tag] = append(byTag[tag], built)
		}
	}
	for tag, ops := range byTag {
		b.resources = append(b.resources, &Resource{
			GoName: goTypeName(tag),
			Ops:    ops,
		})
	}
}

func (b *builder) buildOperation(httpMethod, path string, op *openapi3.Operation) *Operation {
	o := &Operation{
		HTTPMethod:   httpMethod,
		PathTemplate: path,
		Summary:      firstLine(op.Summary),
		Description:  op.Description,
	}

	// Path and query params.
	for _, paramRef := range op.Parameters {
		if paramRef == nil || paramRef.Value == nil {
			continue
		}
		p := paramRef.Value
		switch p.In {
		case "path":
			o.PathParams = append(o.PathParams, &PathParam{
				Name:   p.Name,
				GoName: goParamName(p.Name),
				GoType: "string",
				Doc:    firstLine(p.Description),
			})
		case "query":
			goType := b.goTypeExpr(p.Schema)
			if goType == "" {
				goType = "string"
			}
			o.QueryParams = append(o.QueryParams, &QueryParam{
				Name:   p.Name,
				GoName: goFieldName(p.Name),
				GoType: goType,
				Doc:    firstLine(p.Description),
			})
		}
		// header/cookie params are ignored.
	}

	// Request body. Look up MIME types by exact key rather than
	// Content.Get, which falls back to `*/*` when a specific MIME is
	// absent — that would mask non-JSON bodies declared only as `*/*`
	// and accidentally treat them as JSON.
	if op.RequestBody != nil && op.RequestBody.Value != nil {
		content := op.RequestBody.Value.Content
		switch {
		case content["application/json"] != nil:
			o.RequestType = b.resolveRefTypeExpr(content["application/json"].Schema)
		case content["application/x-www-form-urlencoded"] != nil:
			o.RequestContentType = "application/x-www-form-urlencoded"
			o.RequestType = b.resolveRefTypeExpr(content["application/x-www-form-urlencoded"].Schema)
		case content["multipart/form-data"] != nil:
			o.RequestContentType = "multipart/form-data"
			o.RequestType = b.resolveRefTypeExpr(content["multipart/form-data"].Schema)
		case content["application/octet-stream"] != nil:
			o.RequestContentType = "application/octet-stream"
			o.RequestType = "io.Reader"
		}
	}

	// 2xx response. Same precedence: JSON first, then text/* and
	// application/pdf (both returned to the caller as []byte), then
	// octet-stream as an io.ReadCloser is out of scope — callers can
	// reach for Transport.DoRaw directly for that.
	if op.Responses != nil {
		for _, code := range []string{"200", "201", "204"} {
			rr := op.Responses.Value(code)
			if rr == nil || rr.Value == nil {
				continue
			}
			mt := rr.Value.Content["application/json"]
			if mt == nil {
				// Non-JSON responses surface as []byte.
				if alt := pickRawResponseContent(rr.Value.Content); alt != "" {
					o.ResponseType = "[]byte"
					o.ResponseContentType = alt
					break
				}
				continue
			}
			o.ResponseType = b.resolveRefTypeExpr(mt.Schema)
			if o.ResponseType != "" {
				break
			}
			// Inline object (e.g. paginated container with
			// next_page_token + array of items). Synthesize a named
			// struct so the response body is actually exposed.
			if s := mt.Schema; s != nil && s.Ref == "" && s.Value != nil &&
				(schemaTypeIs(s.Value, "object") || len(s.Value.Properties) > 0) {
				name := goTypeName(op.OperationID) + "Response"
				if b.typesByName[name] == nil {
					if synth := b.synthesizeInlineStruct(name, s.Value); synth != nil {
						b.addType(synth)
					}
				}
				o.ResponseType = name
				break
			}
		}
	}

	o.GoMethod = b.methodName(o, op)
	if o.GoMethod == "" {
		return nil
	}
	o.DocURL = b.docURL(op.OperationID)

	// Synthesise a <OperationID>Params struct for operations that have
	// any query parameters. The OperationID is the preferred source,
	// but when a spec leaves it blank we fall back to a name derived
	// from the HTTP method plus a sanitised path so two parameterless
	// operations on the same resource don't both become `Params`.
	if len(o.QueryParams) > 0 {
		base := goTypeName(op.OperationID)
		if base == "" {
			base = goTypeName(httpMethod) + goTypeName(sanitisePath(path))
		}
		o.ParamsType = base + "Params"
		o.ParamsStruct = buildParamsStruct(o.ParamsType, o.QueryParams, op.Summary)
	}

	o.Pagination = b.detectPagination(o)

	return o
}

// walkRequired produces one FieldCheck per required field on a struct
// type and, recursively, per required field of any required value-type
// nested struct. Cycle-guarded via visited type names.
func (b *builder) walkRequired(t *NamedType, exprPrefix, jsonPathPrefix string, visited map[string]bool) []FieldCheck {
	if t == nil || t.Kind != KindStruct {
		return nil
	}
	var out []FieldCheck
	// Emit the "at least one required group" check from an
	// OpenAPI conditional-required anyOf, if this struct has one.
	// See isConditionalRequiredAnyOf for the pattern.
	if len(t.AnyOfRequiredGroups) > 0 {
		jsonByName := map[string]*StructField{}
		for _, f := range t.Fields {
			jsonByName[f.JSONName] = f
		}
		var groupExprs []string
		for _, group := range t.AnyOfRequiredGroups {
			parts := make([]string, 0, len(group))
			for _, jsonName := range group {
				f := jsonByName[jsonName]
				if f == nil {
					continue
				}
				cond := unsetCond(f.GoType, exprPrefix+"."+f.GoName, b.typesByName)
				if cond == "" {
					continue
				}
				parts = append(parts, "!("+cond+")")
			}
			if len(parts) == 0 {
				continue
			}
			groupExprs = append(groupExprs, "("+strings.Join(parts, " && ")+")")
		}
		if len(groupExprs) > 0 {
			out = append(out, FieldCheck{
				Cond:    "!(" + strings.Join(groupExprs, " || ") + ")",
				Message: b.cfg.ErrPrefix + ": " + jsonPathPrefix + " requires one of the anyOf groups to be satisfied",
			})
		}
	}
	for _, f := range t.Fields {
		if !f.Required {
			continue
		}
		expr := exprPrefix + "." + f.GoName
		jsonPath := jsonPathPrefix + "." + f.JSONName
		cond := unsetCond(f.GoType, expr, b.typesByName)
		if cond != "" {
			out = append(out, FieldCheck{
				Cond:    cond,
				Message: b.cfg.ErrPrefix + ": " + jsonPath + " is required",
			})
		}
		// Recurse into nested struct types so that required fields at
		// greater depths are validated too.
		//
		// Two shapes to handle:
		//   - value-type struct (req.Foo.Bar == ""): reached when
		//     f.GoType is the bare struct name.
		//   - pointer-to-struct (req.Foo != nil && req.Foo.Bar == ""):
		//     reached when f.GoType is "*StructName". The generated
		//     nested checks must be guarded by the nil check already
		//     emitted via cond above, so we prepend it to each
		//     descendant's Cond so a nil pointer short-circuits.
		nestedName := strings.TrimPrefix(f.GoType, "*")
		nested := b.typesByName[nestedName]
		if nested == nil || nested.Kind != KindStruct || visited[nestedName] {
			continue
		}
		visited[nestedName] = true
		inner := b.walkRequired(nested, expr, jsonPath, visited)
		delete(visited, nestedName)
		if strings.HasPrefix(f.GoType, "*") {
			for _, c := range inner {
				out = append(out, FieldCheck{
					Cond:    expr + " != nil && " + c.Cond,
					Message: c.Message,
				})
			}
		} else {
			out = append(out, inner...)
		}
	}
	return out
}

// unsetCond returns a Go boolean expression that is true when `expr` of
// type `goType` is considered "unset" for validation purposes. Returns
// "" when the type has no meaningful unset shape (e.g. a nested struct
// whose presence is encoded by its own required-field checks at
// recursion time, not at this level).
func unsetCond(goType, expr string, named map[string]*NamedType) string {
	// Pointers and slices share nil semantics.
	if strings.HasPrefix(goType, "*") {
		return expr + " == nil"
	}
	if strings.HasPrefix(goType, "[]") {
		return "len(" + expr + ") == 0"
	}
	switch goType {
	case "string", "json.Number", "core.Currency":
		return expr + ` == ""`
	case "time.Time":
		return expr + ".IsZero()"
	case "int", "int64", "int32":
		// Ambiguous with zero — request-body promotion turns these
		// into *int64 before this runs, so reaching here means the
		// field lives on a non-promoted type (e.g. nested struct).
		// Fall back to a zero check.
		return expr + " == 0"
	case "bool":
		// Same reasoning as integers.
		return ""
	}
	if t, ok := named[goType]; ok {
		switch t.Kind {
		case KindEnum:
			if t.EnumBase == "string" {
				return expr + ` == ""`
			}
			return expr + " == 0"
		case KindAlias:
			switch t.AliasTarget {
			case "string", "core.Currency":
				return expr + ` == ""`
			case "int":
				return expr + " == 0"
			case "bool":
				return ""
			case "json.Number":
				return expr + ` == ""`
			case "any":
				return expr + " == nil"
			}
		case KindUnion:
			return expr + " == nil"
		case KindStruct:
			// Value-type nested struct: no top-level unset check.
			// Its required subfields are validated by recursion.
			return ""
		}
	}
	return ""
}

// methodName derives the Go method name for an operation.
//
// Intuition: the method name is a verb ("Get", "List", "Create",
// "Update", "Delete") followed by the sub-resource the operation
// targets. We derive the sub-resource by taking the last non-parameter
// path segment, singularising when the URL's final token is a path
// parameter (so /foo/{id} reads as "Get(Foo)"). Segments that match
// the resource tag — the prefix every endpoint shares — are dropped
// so, e.g., /accounts → Accounts.List rather than Accounts.ListAccounts.
//
// For arrays-listings without a path parameter we prefer "List..." over
// "Get..." so callers read naturally.
func (b *builder) methodName(o *Operation, op *openapi3.Operation) string {
	segs := nonParamSegments(o.PathTemplate)
	// Drop leading segments covered by the resource tag. Handles
	// tag="Accounting" vs path "/accounting/x", multi-word
	// tags like "CardInvitations" vs "/card-invitations/x", and the
	// hyphen-embedded variant "/accounting-categories".
	segs = stripTagPrefix(segs, op.Tags)
	endsInParam := strings.HasSuffix(strings.TrimRight(o.PathTemplate, "/"), "}")

	var suffix string
	if len(segs) > 0 {
		last := segs[len(segs)-1]
		if endsInParam {
			suffix = goTypeName(singularise(last))
		} else {
			suffix = goTypeName(last)
		}
	}

	switch o.HTTPMethod {
	case "GET":
		if endsInParam {
			if suffix == "" {
				return "Get"
			}
			return "Get" + suffix
		}
		if suffix == "" {
			return "List"
		}
		if strings.HasPrefix(o.ResponseType, "[]") {
			return "List" + suffix
		}
		return "Get" + suffix
	case "POST":
		if suffix == "" {
			return "Create"
		}
		if endsInParam {
			return "Create" + suffix
		}
		last := segs[len(segs)-1]
		if looksSingular(last) {
			// Singular trailing segment reads as an imperative verb:
			// /cards/{id}/freeze → Freeze, /pay → Pay.
			return goTypeName(last)
		}
		// Plural trailing segment means we're creating into a
		// collection: /accounting/categories → CreateCategory.
		return "Create" + goTypeName(singularise(last))
	case "PUT", "PATCH":
		if suffix == "" {
			return "Update"
		}
		return "Update" + suffix
	case "DELETE":
		if suffix == "" {
			return "Delete"
		}
		return "Delete" + suffix
	}
	return ""
}

// isDateTimeFormat matches both the OpenAPI-standard "date-time" and
// Revolut's loose variants ("date-time or date", "date") which all
// carry ISO-8601 values that we want exposed as time.Time.
func isDateTimeFormat(format string) bool {
	f := strings.ToLower(format)
	if f == "" {
		return false
	}
	return strings.Contains(f, "date-time") || f == "date"
}

// containsDeprecatedMarker reports whether any tag on an operation
// mentions deprecation (Revolut labels whole resources this way,
// e.g. "Webhooks (v1) (deprecated)").
func containsDeprecatedMarker(tags []string) bool {
	for _, t := range tags {
		if strings.Contains(strings.ToLower(t), "deprecated") {
			return true
		}
	}
	return false
}

// stripTagPrefix removes any leading URL segments that are redundant
// with the resource tag. The output is the "meaningful" tail of the
// path from which we derive method names.
//
// Handles four variants:
//
//  1. Tag == leading segment (case-insensitive).
//     Tag "Accounting", path "/accounting/...".
//  2. Tag singularised == leading segment.
//     Tag "Transfers", path "/transfer".
//  3. Multi-word tag joined with '-' matches leading segment.
//     Tag "CardInvitations", path "/card-invitations/...".
//  4. Tag (or its tokens) appear as a hyphen prefix embedded in the
//     leading segment.
//     Tag "Accounting", path "/accounting-categories".
func stripTagPrefix(segs []string, tags []string) []string {
	if len(segs) == 0 || len(tags) == 0 {
		return segs
	}
	tagTokens := splitWords(strings.ToLower(tags[0]))
	if len(tagTokens) == 0 {
		return segs
	}
	tagJoined := strings.Join(tagTokens, "-")
	tagSingular := singularise(tagJoined)
	firstToken := tagTokens[0]
	firstSingular := singularise(firstToken)

	s0 := strings.ToLower(segs[0])
	switch s0 {
	case tagJoined, tagSingular, firstToken, firstSingular:
		return segs[1:]
	}
	for _, prefix := range []string{tagJoined + "-", tagSingular + "-", firstToken + "-", firstSingular + "-"} {
		if prefix != "-" && strings.HasPrefix(s0, prefix) {
			remainder := s0[len(prefix):]
			out := make([]string, 0, len(segs))
			out = append(out, remainder)
			out = append(out, segs[1:]...)
			return out
		}
	}
	return segs
}

// singularise is a conservative English-plural-to-singular converter.
// It handles enough for typical REST collection/item naming without
// pulling in a full pluraliser.
func singularise(s string) string {
	switch {
	case strings.HasSuffix(s, "ies") && len(s) > 3:
		return s[:len(s)-3] + "y"
	case strings.HasSuffix(s, "ses"), strings.HasSuffix(s, "xes"), strings.HasSuffix(s, "zes"):
		return s[:len(s)-2]
	case strings.HasSuffix(s, "s") && !strings.HasSuffix(s, "ss"):
		return s[:len(s)-1]
	}
	return s
}

func looksSingular(s string) bool {
	return singularise(s) == s
}

// --- helpers ------------------------------------------------------------

func schemaTypeIs(s *openapi3.Schema, kind string) bool {
	if s == nil || s.Type == nil {
		return false
	}
	return s.Type.Is(kind)
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

func isCurrencyPattern(s *openapi3.Schema) bool {
	return s.Pattern == "^[A-Z]{3}$"
}

func getOperation(item *openapi3.PathItem, method string) *openapi3.Operation {
	switch method {
	case "GET":
		return item.Get
	case "POST":
		return item.Post
	case "PUT":
		return item.Put
	case "PATCH":
		return item.Patch
	case "DELETE":
		return item.Delete
	}
	return nil
}

func nonParamSegments(path string) []string {
	out := []string{}
	for _, s := range strings.Split(strings.TrimPrefix(path, "/"), "/") {
		if s == "" || strings.HasPrefix(s, "{") {
			continue
		}
		out = append(out, s)
	}
	return out
}

func (b *builder) docURL(opID string) string {
	if opID == "" || b.cfg.DocsBase == "" {
		return ""
	}
	return b.cfg.DocsBase + camelToKebab(opID)
}

func stringSet(values []string) map[string]bool {
	out := map[string]bool{}
	for _, v := range values {
		out[v] = true
	}
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

func sortedSchemaNames(doc *openapi3.T) []string {
	out := make([]string, 0, len(doc.Components.Schemas))
	for name := range doc.Components.Schemas {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func sortedPaths(doc *openapi3.T) []string {
	m := doc.Paths.Map()
	out := make([]string, 0, len(m))
	for p := range m {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

func firstLine(s string) string {
	if i := strings.Index(s, "\n"); i >= 0 {
		return s[:i]
	}
	return s
}
