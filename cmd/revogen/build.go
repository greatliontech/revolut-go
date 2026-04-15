package main

import (
	"fmt"
	"sort"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
)

// buildSpec walks an openapi3 document and produces a normalized Spec.
//
// For this first pass we intentionally cover a narrow slice: struct,
// enum, and string-alias schemas under components/schemas; operations
// with zero or one JSON-body request and a single JSON 2xx response.
// Unsupported shapes (allOf, oneOf, query params, ...) cause the
// operation or type to be skipped with a warning — not an error —
// so the generator can still emit a partial package while we expand
// coverage.
func buildSpec(doc *openapi3.T, packageName string, resourceAllow []string) (*Spec, error) {
	b := &builder{
		doc:      doc,
		pkg:      packageName,
		allow:    stringSet(resourceAllow),
		emitted:  map[string]bool{},
		typesByName: map[string]*NamedType{},
	}
	b.buildTypes()
	b.buildResources()
	return b.spec(packageName), nil
}

type builder struct {
	doc         *openapi3.T
	pkg         string
	allow       map[string]bool // empty = allow all
	resources   []*Resource
	types       []*NamedType
	typesByName map[string]*NamedType
	emitted     map[string]bool
}

func (b *builder) spec(pkg string) *Spec {
	// Stable output: sort resources by name, sort types by name.
	sort.Slice(b.resources, func(i, j int) bool { return b.resources[i].GoName < b.resources[j].GoName })
	sort.Slice(b.types, func(i, j int) bool { return b.types[i].GoName < b.types[j].GoName })
	return &Spec{PackageName: pkg, Resources: b.resources, Types: b.types}
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
	goName := goTypeName(name)

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
		required := stringSet(s.Required)
		fields := make([]*StructField, 0, len(s.Properties))
		for _, propName := range sortedKeys(s.Properties) {
			propRef := s.Properties[propName]
			if propRef == nil {
				continue
			}
			isRequired := required[propName]
			goType := b.goTypePromoteEnum(goName, propName, propRef)
			if goType == "" {
				continue
			}
			// Optional time.Time becomes *time.Time so "unset" is
			// distinguishable from the zero time.
			if goType == "time.Time" && !isRequired {
				goType = "*time.Time"
			}
			fields = append(fields, &StructField{
				JSONName: propName,
				GoName:   goFieldName(propName),
				GoType:   goType,
				Required: isRequired,
				Doc:      firstLine(propRef.Value.Description),
			})
		}
		return &NamedType{
			GoName: goName,
			Kind:   KindStruct,
			Doc:    s.Description,
			Fields: fields,
		}
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

	// Unsupported for now: allOf/anyOf/oneOf, pure array wrappers
	// (handled at use sites).
	return nil
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
				return "[]" + b.goTypeExpr(target.Value.Items)
			}
		}
		return goTypeName(name)
	}
	s := ref.Value
	if s == nil {
		return ""
	}
	switch {
	case schemaTypeIs(s, "string"):
		if s.Format == "date-time" {
			return "time.Time"
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

// goTypePromoteEnum is goTypeExpr with a normalization pass: when a
// struct property's schema has an inline string enum (no $ref, not a
// named component type), we synthesize a named enum type derived from
// the parent struct + field name (e.g. Account.state → AccountState)
// so callers get exported enum constants rather than bare strings.
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
	return b.goTypeExpr(ref)
}

// resolveRefTypeExpr turns a schema reference (as found on a request
// body or response) into a Go type expression. Array-wrapper component
// schemas (e.g. Accounts → array of Account) are flattened to []Element.
func (b *builder) resolveRefTypeExpr(ref *openapi3.SchemaRef) string {
	if ref == nil {
		return ""
	}
	// Named ref: if it points at an array schema, unwrap it.
	if name := schemaRefName(ref); name != "" {
		if target := b.doc.Components.Schemas[name]; target != nil && target.Value != nil {
			if schemaTypeIs(target.Value, "array") {
				return "[]" + b.resolveRefTypeExpr(target.Value.Items)
			}
		}
		return goTypeName(name)
	}
	// Inline schema.
	if ref.Value != nil && schemaTypeIs(ref.Value, "array") {
		return "[]" + b.resolveRefTypeExpr(ref.Value.Items)
	}
	return b.goTypeExpr(ref)
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

	// Path params.
	for _, paramRef := range op.Parameters {
		if paramRef == nil || paramRef.Value == nil {
			continue
		}
		p := paramRef.Value
		if p.In != "path" {
			// Query/header params are not yet supported; skip silently.
			continue
		}
		o.PathParams = append(o.PathParams, &PathParam{
			Name:   p.Name,
			GoName: goParamName(p.Name),
			GoType: "string",
			Doc:    firstLine(p.Description),
		})
	}

	// Request body.
	if op.RequestBody != nil && op.RequestBody.Value != nil {
		if mt := op.RequestBody.Value.Content.Get("application/json"); mt != nil {
			o.RequestType = b.resolveRefTypeExpr(mt.Schema)
		}
	}

	// 2xx response.
	if op.Responses != nil {
		for _, code := range []string{"200", "201"} {
			if rr := op.Responses.Value(code); rr != nil && rr.Value != nil {
				if mt := rr.Value.Content.Get("application/json"); mt != nil {
					o.ResponseType = b.resolveRefTypeExpr(mt.Schema)
					if o.ResponseType != "" {
						break
					}
				}
			}
		}
	}

	o.GoMethod = b.methodName(o, op)
	if o.GoMethod == "" {
		return nil
	}
	o.DocURL = docURL(op.OperationID)

	// Validation hints: required string-valued fields on the request
	// body. Non-string required fields (nested structs, arrays, time,
	// ...) aren't validatable with a simple zero check, so we skip
	// them — callers can rely on Revolut to reject malformed bodies.
	if o.RequestType != "" {
		if t, ok := b.typesByName[o.RequestType]; ok && t.Kind == KindStruct {
			for _, f := range t.Fields {
				if !f.Required || !isEmptyCheckable(f.GoType, b.typesByName) {
					continue
				}
				o.Validate = append(o.Validate, FieldCheck{
					GoExpr:  "req." + f.GoName,
					Message: fmt.Sprintf("business: %s.%s is required", o.RequestType, f.GoName),
				})
			}
		}
	}

	return o
}

// methodName derives the Go method name for an operation. Heuristics
// are kept deliberately small and deterministic so that output diffs
// are easy to reason about.
//
// Rules:
//   - GET path ending in {param}: "Get".
//   - GET with no params ending in a segment matching the resource
//     plural: "List".
//   - GET on any other sub-resource: "List"+Suffix if the response is
//     an array, else "Get"+Suffix, where Suffix is the PascalCase of
//     the last non-param segment.
//   - POST where the last segment matches the resource singular:
//     "Create"; otherwise PascalCase of the last segment.
//   - PUT/PATCH ending in {param}: "Update"; on sub-resource,
//     "Update"+Suffix.
//   - DELETE: similar Delete / Delete+Suffix pattern.
func (b *builder) methodName(o *Operation, op *openapi3.Operation) string {
	segs := nonParamSegments(o.PathTemplate)
	lastNonParam := ""
	if n := len(segs); n > 0 {
		lastNonParam = segs[n-1]
	}
	endsInParam := strings.HasSuffix(strings.TrimRight(o.PathTemplate, "/"), "}")

	switch o.HTTPMethod {
	case "GET":
		if endsInParam {
			return "Get"
		}
		if matchesResourcePlural(lastNonParam, op.Tags) {
			return "List"
		}
		verb := "Get"
		if strings.HasPrefix(o.ResponseType, "[]") {
			verb = "List"
		}
		return verb + goTypeName(lastNonParam)
	case "POST":
		if matchesResourceSingular(lastNonParam, op.Tags) {
			return "Create"
		}
		if matchesResourcePlural(lastNonParam, op.Tags) {
			return "Create"
		}
		return goTypeName(lastNonParam)
	case "PUT", "PATCH":
		if endsInParam {
			return "Update"
		}
		return "Update" + goTypeName(lastNonParam)
	case "DELETE":
		if endsInParam {
			return "Delete"
		}
		return "Delete" + goTypeName(lastNonParam)
	}
	return ""
}

// --- helpers ------------------------------------------------------------

// isEmptyCheckable reports whether a field's Go type can be compared to
// the empty string to detect an unset value. True for plain strings,
// string-backed enums and aliases, and json.Number (also a string).
func isEmptyCheckable(goType string, named map[string]*NamedType) bool {
	switch goType {
	case "string", "json.Number":
		return true
	}
	if strings.HasPrefix(goType, "[]") || strings.HasPrefix(goType, "*") || strings.Contains(goType, ".") {
		// []T, *T always: not a plain string.
		// For foreign-package refs (core.Currency) we need to check.
		if goType == "core.Currency" {
			return true
		}
		return false
	}
	if t, ok := named[goType]; ok {
		switch t.Kind {
		case KindEnum:
			return t.EnumBase == "string"
		case KindAlias:
			return t.AliasTarget == "string" || t.AliasTarget == "core.Currency"
		}
	}
	return false
}

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

func matchesResourceSingular(seg string, tags []string) bool {
	if len(tags) == 0 {
		return false
	}
	s := strings.ToLower(tags[0])
	s = strings.TrimSuffix(s, "s") // crude singularisation
	return strings.EqualFold(seg, s)
}

func matchesResourcePlural(seg string, tags []string) bool {
	if len(tags) == 0 {
		return false
	}
	return strings.EqualFold(seg, tags[0])
}

func docURL(opID string) string {
	if opID == "" {
		return ""
	}
	return "https://developer.revolut.com/docs/business/" + camelToKebab(opID)
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
