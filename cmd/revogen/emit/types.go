package emit

import (
	"fmt"
	"sort"
	"strings"

	"github.com/greatliontech/revolut-go/cmd/revogen/ir"
)

// writeTypesFile emits gen_types.go: every Decl plus any helpers
// the union codecs need.
func writeTypesFile(spec *ir.Spec, imports []string) string {
	w := newFileWriter(spec.Package, imports)
	w.header()

	if spec.APIVersion != "" {
		w.printf("// APIVersion is the OpenAPI info.version this client was generated against.\nconst APIVersion = %q\n\n", spec.APIVersion)
	}

	hasUnion := false
	for _, d := range spec.Decls {
		if d.Kind == ir.DeclInterface {
			hasUnion = true
			break
		}
	}
	if hasUnion {
		writeUnionHelpers(w)
	}

	for _, d := range spec.Decls {
		writeDecl(w, d)
	}
	writeResponseMetadata(w, spec)
	writeFormatHelpers(w, spec)
	return w.buf.String()
}

// writeFormatHelpers emits the local format-validation helpers
// path-param validators call. Currently just isUUID — emitted
// only when at least one path param in the spec declares
// `format: uuid`, so packages that never reference it stay flat.
//
// The check is a single RFC 4122 canonical-form match: 8-4-4-4-12
// hex digits with no brace/URN wrapper. Forgiving-but-typical, to
// reject obvious typos without second-guessing server-side
// dialects (UUIDv1/v4/v6 look identical structurally; we don't
// version-discriminate).
func writeFormatHelpers(w *fileWriter, spec *ir.Spec) {
	if !specUsesUUIDValidator(spec) {
		return
	}
	w.write("\n// isUUID reports whether s matches the RFC 4122 canonical form\n")
	w.write("// (8-4-4-4-12 hex digits). Used by generated path-param validators\n")
	w.write("// to reject malformed IDs before issuing the HTTP call.\n")
	w.write("func isUUID(s string) bool {\n")
	w.write("\tif len(s) != 36 {\n\t\treturn false\n\t}\n")
	w.write("\tfor i, r := range s {\n")
	w.write("\t\tswitch i {\n")
	w.write("\t\tcase 8, 13, 18, 23:\n")
	w.write("\t\t\tif r != '-' { return false }\n")
	w.write("\t\tdefault:\n")
	w.write("\t\t\tswitch {\n")
	w.write("\t\t\tcase r >= '0' && r <= '9':\n")
	w.write("\t\t\tcase r >= 'a' && r <= 'f':\n")
	w.write("\t\t\tcase r >= 'A' && r <= 'F':\n")
	w.write("\t\t\tdefault: return false\n")
	w.write("\t\t\t}\n")
	w.write("\t\t}\n")
	w.write("\t}\n")
	w.write("\treturn true\n")
	w.write("}\n")
}

// specUsesUUIDValidator reports whether any method in spec has
// a path param typed `format: uuid`, so writeFormatHelpers knows
// whether to emit the helper.
func specUsesUUIDValidator(spec *ir.Spec) bool {
	for _, r := range spec.Resources {
		for _, m := range r.Methods {
			for _, p := range m.PathParams {
				if p.Format == "uuid" {
					return true
				}
			}
		}
	}
	return false
}

// writeResponseMetadata emits the per-package ResponseMetadata
// struct plus the extractResponseMetadata helper that pulls the
// declared response-header fields off an http.Header. Emitted only
// when at least one method in the spec surfaces metadata, so
// packages without any 2xx-header declarations stay flat.
//
// A Signed[T any] generic wrapper is emitted alongside when any
// method needs a raw-bytes variant for detached-JWS verification.
func writeResponseMetadata(w *fileWriter, spec *ir.Spec) {
	fields := collectMetadataFields(spec)
	if len(fields) == 0 {
		return
	}
	w.write("\n// ResponseMetadata carries the response headers the spec\n")
	w.write("// declares on 2xx responses for any method in this package.\n")
	w.write("// Populated automatically by methods that return it as a\n")
	w.write("// second value; callers read whichever field the relevant\n")
	w.write("// endpoint actually fills.\n")
	w.write("type ResponseMetadata struct {\n")
	for _, f := range fields {
		if f.Doc != "" {
			w.printf("\t// %s\n", f.Doc)
		}
		w.printf("\t%s string\n", f.GoName)
	}
	w.write("}\n\n")

	w.write("func extractResponseMetadata(h http.Header) ResponseMetadata {\n")
	w.write("\treturn ResponseMetadata{\n")
	for _, f := range fields {
		w.printf("\t\t%s: h.Get(%q),\n", f.GoName, f.WireName)
	}
	w.write("\t}\n}\n")

	if specNeedsSigned(spec) {
		w.write("\n// Signed wraps a typed response body with the raw bytes\n")
		w.write("// and per-response metadata the caller needs to verify the\n")
		w.write("// detached x-jws-signature header against the untouched\n")
		w.write("// JSON payload. JSON decoding a typed struct and\n")
		w.write("// re-marshalling is not byte-identical, so the raw field\n")
		w.write("// is the only signature-verifiable form.\n")
		w.write("type Signed[T any] struct {\n")
		w.write("\tTyped    *T\n")
		w.write("\tRaw      []byte\n")
		w.write("\tMetadata ResponseMetadata\n")
		w.write("}\n")
	}
}

// collectMetadataFields flattens every method's ResponseMetadata
// slice into a sorted-unique list, keyed by GoName.
func collectMetadataFields(spec *ir.Spec) []ir.MetadataField {
	seen := map[string]ir.MetadataField{}
	for _, r := range spec.Resources {
		for _, m := range r.Methods {
			for _, f := range m.ResponseMetadata {
				seen[f.GoName] = f
			}
		}
	}
	if len(seen) == 0 {
		return nil
	}
	out := make([]ir.MetadataField, 0, len(seen))
	for _, f := range seen {
		out = append(out, f)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].GoName < out[j].GoName })
	return out
}

func specNeedsSigned(spec *ir.Spec) bool {
	for _, r := range spec.Resources {
		for _, m := range r.Methods {
			if m.EmitSignedVariant {
				return true
			}
		}
	}
	return false
}

// writeUnionHelpers emits the package-level helpers used by
// generated union code: hasJSONKey for probe decoders.
func writeUnionHelpers(w *fileWriter) {
	w.write("// hasJSONKey reports whether key is present on the wire.\n")
	w.write("// Used by generated union probe decoders.\n")
	w.write("func hasJSONKey(m map[string]json.RawMessage, key string) bool {\n")
	w.write("\t_, ok := m[key]\n")
	w.write("\treturn ok\n")
	w.write("}\n\n")
}

// writeDecl dispatches to the kind-specific emitter.
func writeDecl(w *fileWriter, d *ir.Decl) {
	switch d.Kind {
	case ir.DeclAlias:
		writeAlias(w, d)
	case ir.DeclEnum:
		writeEnum(w, d)
	case ir.DeclStruct:
		writeStruct(w, d)
	case ir.DeclInterface:
		writeInterface(w, d)
	}
}

func writeAlias(w *fileWriter, d *ir.Decl) {
	w.docComment(d.Name, d.Doc)
	w.printf("type %s = %s\n\n", d.Name, d.AliasTarget.GoExpr())
}

func writeEnum(w *fileWriter, d *ir.Decl) {
	w.docComment(d.Name, d.Doc)
	w.printf("type %s %s\n\n", d.Name, d.EnumBase.GoExpr())
	if len(d.EnumValues) == 0 {
		return
	}
	w.write("const (\n")
	for _, v := range d.EnumValues {
		if v.Doc != "" {
			w.printf("\t// %s.\n", v.Doc)
		}
		switch x := v.Value.(type) {
		case string:
			w.printf("\t%s %s = %q\n", v.GoName, d.Name, x)
		case int:
			w.printf("\t%s %s = %d\n", v.GoName, d.Name, x)
		case int32:
			w.printf("\t%s %s = %d\n", v.GoName, d.Name, x)
		case int64:
			w.printf("\t%s %s = %d\n", v.GoName, d.Name, x)
		}
	}
	w.write(")\n\n")
}

func writeStruct(w *fileWriter, d *ir.Decl) {
	w.docComment(d.Name, d.Doc)
	w.printf("type %s struct {\n", d.Name)
	for i, f := range d.Fields {
		if i > 0 {
			w.write("\n")
		}
		writeFieldDoc(w, f)
		w.printf("\t%s %s `json:%q`\n", f.GoName, f.Type.GoExpr(), jsonTag(f))
	}
	if d.ExtraMap != nil {
		if len(d.Fields) > 0 {
			w.write("\n")
		}
		w.write("\t// Extra captures any additional JSON properties not listed above.\n")
		w.printf("\tExtra map[string]%s `json:\"-\"`\n", d.ExtraMap.GoExpr())
	}
	w.write("}\n\n")
	for _, union := range d.ImplementsUnions {
		w.printf("func (%s) is%s() {}\n\n", d.Name, union)
	}
	if d.UnionDispatch != nil {
		writeWireTaggedMarshal(w, d)
	}
	if d.ExtraMap != nil {
		writeExtraMapCodec(w, d)
	}
	if d.FormEncoder {
		writeFormEncoder(w, d)
	}
	if d.MultipartEncoder {
		writeMultipartEncoder(w, d)
	}
	if d.QueryParamsEncoder {
		writeQueryParamsEncoder(w, d)
		writeApplyDefaults(w, d)
	}
}

// writeApplyDefaults emits ApplyDefaults on a Params struct when
// at least one field carries a machine-readable default. Opt-in —
// the caller decides when to apply defaults, since some endpoints
// have server-side defaults that the caller may want to defer to
// by leaving the field unset. Each assignment guards on the zero
// value so an explicitly-set value isn't overwritten.
func writeApplyDefaults(w *fileWriter, d *ir.Decl) {
	hasDefault := false
	for _, f := range d.Fields {
		if f.DefaultLiteral != "" {
			hasDefault = true
			break
		}
	}
	if !hasDefault {
		return
	}
	w.printf("// ApplyDefaults sets the spec-declared default value on any\n")
	w.printf("// field of %s that is still at its Go zero value. Call\n", d.Name)
	w.printf("// before encode() if you want the SDK to pre-fill defaults\n")
	w.write("// rather than letting the server apply them. A nil receiver\n")
	w.write("// is a no-op.\n")
	w.printf("func (p *%s) ApplyDefaults() {\n", d.Name)
	w.write("\tif p == nil {\n\t\treturn\n\t}\n")
	for _, f := range d.Fields {
		if f.DefaultLiteral == "" {
			continue
		}
		cond := defaultZeroCond(f)
		if cond == "" {
			continue
		}
		w.printf("\tif %s {\n\t\tp.%s = %s\n\t}\n", cond, f.GoName, f.DefaultLiteral)
	}
	w.write("}\n")
}

// defaultZeroCond returns a Go expression that is true when the
// field is still at its Go zero value. Returns "" for field kinds
// where auto-fill is unsafe (bool, pointer) so the caller of
// writeApplyDefaults skips them.
func defaultZeroCond(f *ir.Field) string {
	t := f.Type
	if t == nil || t.IsPointer() {
		return ""
	}
	if t.Kind == ir.KindPrim {
		switch t.Name {
		case "string", "json.Number", "core.Currency":
			return "p." + f.GoName + ` == ""`
		case "int", "int32", "int64":
			return "p." + f.GoName + " == 0"
		case "float32", "float64":
			return "p." + f.GoName + " == 0"
		}
	}
	if t.Kind == ir.KindNamed {
		// Named types are assumed string-backed enums here — the
		// DefaultLiteral produced for them wraps the value in a
		// cast, so the zero value is the empty string.
		return "p." + f.GoName + ` == ""`
	}
	return ""
}

// writeQueryParamsEncoder emits encode() url.Values for a generated
// `<Op>Params` struct. Each field renders one key=value pair when
// non-zero; arrays expand into repeated entries (style=form
// explode=true), or a single comma-joined entry when the spec pins
// explode=false via Field.ExplodeFalse. Fields with a DefaultLiteral
// are populated on a nil pointer when left at their zero value, so
// callers that don't set a knob still get the spec-documented
// default on the wire.
func writeQueryParamsEncoder(w *fileWriter, d *ir.Decl) {
	w.printf("// encode serializes %s into a URL query.\n", d.Name)
	w.printf("func (p *%s) encode() url.Values {\n", d.Name)
	w.write("\tif p == nil { return nil }\n")
	w.write("\tq := url.Values{}\n")
	for _, f := range d.Fields {
		expr := "p." + f.GoName
		if f.Type.IsSlice() {
			inner := f.Type.Elem
			conv := queryStringify(inner, "v")
			if f.ExplodeFalse {
				// Single comma-joined entry — the server rejects
				// repeated keys when the spec declares
				// style=form, explode=false.
				w.printf("\tif len(%s) > 0 {\n", expr)
				w.write("\t\tparts := make([]string, 0, len(" + expr + "))\n")
				w.printf("\t\tfor _, v := range %s {\n", expr)
				w.printf("\t\t\tparts = append(parts, %s)\n", conv)
				w.write("\t\t}\n")
				w.printf("\t\tq.Set(%q, strings.Join(parts, \",\"))\n", f.JSONName)
				w.write("\t}\n")
				continue
			}
			w.printf("\tfor _, v := range %s {\n", expr)
			w.printf("\t\tq.Add(%q, %s)\n", f.JSONName, conv)
			w.write("\t}\n")
			continue
		}
		conv := queryStringify(f.Type, expr)
		// Required fields are emitted unconditionally: the zero
		// value may be a legitimate wire value (e.g. a required
		// time.Time whose zero encodes to "0001-01-01T00:00:00Z"
		// is better sent and rejected than silently dropped so the
		// caller has no idea why the server complains).
		if f.Required {
			w.printf("\tq.Set(%q, %s)\n", f.JSONName, conv)
			continue
		}
		guard := isSet(f.Type, expr)
		if guard == "" {
			w.printf("\tq.Set(%q, %s)\n", f.JSONName, conv)
			continue
		}
		w.printf("\tif %s {\n", guard)
		w.printf("\t\tq.Set(%q, %s)\n", f.JSONName, conv)
		if f.DefaultLiteral != "" {
			// Backfill the server default when the caller left the
			// field unset; keeps docs and wire in sync.
			w.write("\t} else {\n")
			w.printf("\t\tq.Set(%q, %s)\n", f.JSONName, defaultLiteralStringExpr(f))
		}
		w.write("\t}\n")
	}
	w.write("\treturn q\n}\n\n")
}

// defaultLiteralStringExpr renders the Go expression that converts
// Field.DefaultLiteral into the string written onto the wire. The
// literal itself is typed (an int, a named enum, etc.); we reuse
// queryStringify's conversion rules so every default lands in the
// same form a caller-supplied value would.
func defaultLiteralStringExpr(f *ir.Field) string {
	return queryStringify(f.Type, "("+f.DefaultLiteral+")")
}

// queryStringify renders a Go expression that converts a typed
// scalar to the string written into url.Values. Mirrors
// formStringify but uses time.RFC3339Nano for time.Time so cursor
// pagination's nanosecond precision survives.
func queryStringify(t *ir.Type, expr string) string {
	if t == nil {
		return expr
	}
	if t.IsPointer() {
		return queryStringify(t.Elem, "*"+expr)
	}
	if t.Kind == ir.KindPrim {
		switch t.Name {
		case "string":
			return expr
		case "bool":
			return "strconv.FormatBool(" + expr + ")"
		case "int", "int32", "int64":
			return "strconv.FormatInt(int64(" + expr + "), 10)"
		case "json.Number":
			return "string(" + expr + ")"
		case "time.Time":
			return expr + ".UTC().Format(time.RFC3339Nano)"
		}
	}
	if t.IsNamed() {
		return "string(" + expr + ")"
	}
	return "fmt.Sprint(" + expr + ")"
}

func writeInterface(w *fileWriter, d *ir.Decl) {
	w.docComment(d.Name, d.Doc)
	if len(d.Variants) > 0 {
		w.write("// Variants:\n")
		for _, v := range d.Variants {
			w.printf("//   - %s → %s\n", v.Tag, v.GoName)
		}
	}
	w.printf("type %s interface {\n", d.Name)
	w.printf("\t%s()\n", d.MarkerMethod)
	// A nested union (this interface is itself listed as a variant
	// of one or more parent unions) carries the parent's marker
	// method too — interfaces "implement" other interfaces by
	// declaring the same methods.
	for _, parent := range d.ImplementsUnions {
		w.printf("\tis%s()\n", parent)
	}
	w.write("}\n\n")
	if d.Discriminator != nil {
		writeWireTaggedDecoder(w, d)
	} else {
		writeProbeDecoder(w, d)
	}
}

// writeWireTaggedMarshal emits MarshalJSON on a variant struct so
// the discriminator property appears alongside the variant's
// fields. Only emitted for variants of wire-tagged unions.
func writeWireTaggedMarshal(w *fileWriter, d *ir.Decl) {
	link := d.UnionDispatch
	w.printf("// MarshalJSON injects the %q tag (\"%s\") on the wire so the\n", link.PropertyName, link.Value)
	w.printf("// server can dispatch this %s variant.\n", link.UnionName)
	w.printf("func (v %s) MarshalJSON() ([]byte, error) {\n", d.Name)
	w.printf("\ttype alias %s\n", d.Name)
	w.write("\treturn json.Marshal(struct {\n")
	w.printf("\t\tT string `json:%q`\n", link.PropertyName)
	w.write("\t\talias\n")
	w.write("\t}{\n")
	w.printf("\t\tT:     %q,\n", link.Value)
	w.write("\t\talias: alias(v),\n")
	w.write("\t})\n")
	w.write("}\n\n")
}

// writeWireTaggedDecoder emits decode<Union>(data) helper for a
// wire-tagged union. Reads the discriminator property, looks up
// the matching variant, and decodes into it.
func writeWireTaggedDecoder(w *fileWriter, d *ir.Decl) {
	link := d.Discriminator
	w.printf("// decode%s reads the %q discriminator and decodes into the\n", d.Name, link.PropertyName)
	w.write("// matching variant.\n")
	w.printf("func decode%s(data []byte) (%s, error) {\n", d.Name, d.Name)
	w.write("\tvar tag struct {\n")
	w.printf("\t\tT string `json:%q`\n", link.PropertyName)
	w.write("\t}\n")
	w.write("\tif err := json.Unmarshal(data, &tag); err != nil {\n")
	w.write("\t\treturn nil, err\n")
	w.write("\t}\n")
	w.write("\tswitch tag.T {\n")
	for _, v := range d.Variants {
		w.printf("\tcase %q:\n", v.Tag)
		w.printf("\t\tvar out %s\n", v.GoName)
		w.write("\t\tif err := json.Unmarshal(data, &out); err != nil {\n")
		w.write("\t\t\treturn nil, err\n")
		w.write("\t\t}\n")
		w.write("\t\treturn out, nil\n")
	}
	w.write("\t}\n")
	w.printf("\treturn nil, fmt.Errorf(\"unknown %s tag: %%q\", tag.T)\n", d.Name)
	w.write("}\n\n")
}

// writeProbeDecoder emits decode<Union>(data) for an untagged
// union: probes variants in order by required-field presence.
func writeProbeDecoder(w *fileWriter, d *ir.Decl) {
	w.printf("// decode%s tries each variant in order, picking the first\n", d.Name)
	w.write("// whose required fields are all present on the wire.\n")
	w.printf("func decode%s(data []byte) (%s, error) {\n", d.Name, d.Name)
	w.write("\tvar probe map[string]json.RawMessage\n")
	w.write("\tif err := json.Unmarshal(data, &probe); err != nil {\n")
	w.write("\t\treturn nil, err\n")
	w.write("\t}\n")
	for _, v := range d.Variants {
		if len(v.RequiredProbe) == 0 {
			continue
		}
		conds := make([]string, 0, len(v.RequiredProbe))
		for _, k := range v.RequiredProbe {
			conds = append(conds, fmt.Sprintf("hasJSONKey(probe, %q)", k))
		}
		w.printf("\tif %s {\n", strings.Join(conds, " && "))
		w.printf("\t\tvar out %s\n", v.GoName)
		w.write("\t\tif err := json.Unmarshal(data, &out); err != nil {\n")
		w.write("\t\t\treturn nil, err\n")
		w.write("\t\t}\n")
		w.write("\t\treturn out, nil\n")
		w.write("\t}\n")
	}
	if len(d.Variants) > 0 {
		w.printf("\tvar out %s\n", d.Variants[0].GoName)
		w.write("\tif err := json.Unmarshal(data, &out); err != nil {\n")
		w.write("\t\treturn nil, err\n")
		w.write("\t}\n")
		w.write("\treturn out, nil\n")
	} else {
		w.write("\treturn nil, nil\n")
	}
	w.write("}\n\n")
}

// writeExtraMapCodec emits MarshalJSON / UnmarshalJSON on a struct
// with an Extra catch-all map. Marshal merges Extra into the JSON
// object; Unmarshal pops known fields off the input map and stores
// the rest in Extra.
func writeExtraMapCodec(w *fileWriter, d *ir.Decl) {
	w.printf("// MarshalJSON serializes %s with the Extra map merged into\n", d.Name)
	w.write("// the same JSON object.\n")
	w.printf("func (v %s) MarshalJSON() ([]byte, error) {\n", d.Name)
	w.printf("\ttype alias %s\n", d.Name)
	w.write("\tbase, err := json.Marshal(alias(v))\n")
	w.write("\tif err != nil { return nil, err }\n")
	w.write("\tvar out map[string]json.RawMessage\n")
	w.write("\tif err := json.Unmarshal(base, &out); err != nil { return nil, err }\n")
	w.write("\tdelete(out, \"-\")\n")
	w.write("\tfor k, val := range v.Extra {\n")
	w.write("\t\traw, err := json.Marshal(val)\n")
	w.write("\t\tif err != nil { return nil, err }\n")
	w.write("\t\tout[k] = raw\n")
	w.write("\t}\n")
	w.write("\treturn json.Marshal(out)\n")
	w.write("}\n\n")

	w.printf("// UnmarshalJSON deserializes %s, splitting unknown JSON\n", d.Name)
	w.write("// keys into Extra.\n")
	w.printf("func (v *%s) UnmarshalJSON(data []byte) error {\n", d.Name)
	w.printf("\ttype alias %s\n", d.Name)
	w.write("\tvar tmp alias\n")
	w.write("\tif err := json.Unmarshal(data, &tmp); err != nil { return err }\n")
	w.write("\tvar raw map[string]json.RawMessage\n")
	w.write("\tif err := json.Unmarshal(data, &raw); err != nil { return err }\n")
	for _, f := range d.Fields {
		w.printf("\tdelete(raw, %q)\n", f.JSONName)
	}
	w.printf("\textra := make(map[string]%s, len(raw))\n", d.ExtraMap.GoExpr())
	w.write("\tfor k, val := range raw {\n")
	w.printf("\t\tvar v %s\n", d.ExtraMap.GoExpr())
	w.write("\t\tif err := json.Unmarshal(val, &v); err != nil { return err }\n")
	w.write("\t\textra[k] = v\n")
	w.write("\t}\n")
	w.write("\t*v = " + d.Name + "(tmp)\n")
	w.write("\tv.Extra = extra\n")
	w.write("\treturn nil\n")
	w.write("}\n\n")
}

// writeFormEncoder emits encodeForm() returning url.Values.
func writeFormEncoder(w *fileWriter, d *ir.Decl) {
	w.printf("// encodeForm builds the %s URL-encoded form body.\n", d.Name)
	w.printf("func (r *%s) encodeForm() url.Values {\n", d.Name)
	w.write("\tv := url.Values{}\n")
	w.write("\tif r == nil { return v }\n")
	for _, f := range d.Fields {
		if f.Type.GoExpr() == "io.Reader" {
			continue
		}
		expr := "r." + f.GoName
		w.printf("\tif %s {\n", isSet(f.Type, expr))
		w.printf("\t\tv.Set(%q, %s)\n", f.JSONName, formStringify(f.Type, expr))
		w.write("\t}\n")
	}
	w.write("\treturn v\n}\n\n")
}

// writeMultipartEncoder emits encodeMultipart() returning the
// reader, content-type, and any error.
func writeMultipartEncoder(w *fileWriter, d *ir.Decl) {
	w.printf("// encodeMultipart builds the %s multipart/form-data body.\n", d.Name)
	w.printf("func (r *%s) encodeMultipart() (io.Reader, string, error) {\n", d.Name)
	w.write("\tvar body bytes.Buffer\n")
	w.write("\tw := multipart.NewWriter(&body)\n")
	w.write("\tif r != nil {\n")
	for _, f := range d.Fields {
		expr := "r." + f.GoName
		if f.Type.GoExpr() == "io.Reader" {
			w.printf("\t\tif %s != nil {\n", expr)
			w.printf("\t\t\tpart, err := w.CreateFormFile(%q, %q)\n", f.JSONName, f.JSONName)
			w.write("\t\t\tif err != nil { return nil, \"\", err }\n")
			w.printf("\t\t\tif _, err := io.Copy(part, %s); err != nil { return nil, \"\", err }\n", expr)
			w.write("\t\t}\n")
			continue
		}
		w.printf("\t\tif %s {\n", isSet(f.Type, expr))
		w.printf("\t\t\tif err := w.WriteField(%q, %s); err != nil { return nil, \"\", err }\n", f.JSONName, formStringify(f.Type, expr))
		w.write("\t\t}\n")
	}
	w.write("\t}\n")
	w.write("\tif err := w.Close(); err != nil { return nil, \"\", err }\n")
	w.write("\treturn &body, w.FormDataContentType(), nil\n")
	w.write("}\n\n")
}

// isSet renders a Go boolean expression that's true when expr of
// type t is considered set (the inverse of unsetCond, used for
// "include in form/multipart" decisions).
func isSet(t *ir.Type, expr string) string {
	if t == nil {
		return "true"
	}
	if t.IsPointer() {
		return expr + " != nil"
	}
	if t.IsSlice() {
		return "len(" + expr + ") > 0"
	}
	if t.Kind == ir.KindMap {
		return "len(" + expr + ") > 0"
	}
	if t.Kind == ir.KindPrim {
		switch t.Name {
		case "string", "json.Number", "core.Currency":
			return expr + ` != ""`
		case "time.Time":
			return "!" + expr + ".IsZero()"
		case "int", "int32", "int64", "float32", "float64":
			return expr + " != 0"
		case "bool":
			return expr
		}
		return "true"
	}
	if t.IsNamed() {
		// Treat alias / enum (string-backed) the same as string.
		return expr + ` != ""`
	}
	return "true"
}

// formStringify renders the Go expression that converts a typed
// field value to the string written into url.Values / multipart.
func formStringify(t *ir.Type, expr string) string {
	if t == nil {
		return expr
	}
	if t.IsPointer() {
		return formStringify(t.Elem, "*"+expr)
	}
	if t.Kind == ir.KindPrim {
		switch t.Name {
		case "string":
			return expr
		case "bool":
			return "strconv.FormatBool(" + expr + ")"
		case "int", "int32", "int64":
			return "strconv.FormatInt(int64(" + expr + "), 10)"
		case "json.Number":
			return "string(" + expr + ")"
		case "time.Time":
			return expr + ".UTC().Format(time.RFC3339Nano)"
		}
	}
	if t.IsNamed() {
		return "string(" + expr + ")"
	}
	// Fallback uses fmt.Sprint so unmodelled types still serialise.
	return "fmt.Sprint(" + expr + ")"
}

// jsonTag composes the json struct tag for a field. Required
// fields render without `omitempty`; optional fields with it.
func jsonTag(f *ir.Field) string {
	if f.Required {
		return f.JSONName
	}
	return f.JSONName + ",omitempty"
}

// writeFieldDoc emits the godoc + readOnly/writeOnly/deprecated
// annotations for a struct field.
func writeFieldDoc(w *fileWriter, f *ir.Field) {
	if f.Doc != "" {
		w.printf("\t// %s\n", f.Doc)
	}
	switch {
	case f.ReadOnly && f.WriteOnly:
		w.write("\t//\n\t// Read/write-only flags conflict in the spec; treating as advisory.\n")
	case f.ReadOnly:
		w.write("\t//\n\t// Read-only: populated by the server; any value sent by the client is ignored.\n")
	case f.WriteOnly:
		w.write("\t//\n\t// Write-only: accepted on requests but never echoed in responses.\n")
	}
	if f.DefaultDoc != "" {
		w.write("\t//\n")
		w.printf("\t// %s\n", f.DefaultDoc)
	}
	if f.Deprecated != "" {
		w.write("\t//\n")
		w.printf("\t// Deprecated: %s\n", f.Deprecated)
	}
}

// sortDeclsForEmit returns Decls in stable emit order. Currently
// the Spec already sorts them; this exists so future ordering
// changes are localised.
func sortDeclsForEmit(decls []*ir.Decl) []*ir.Decl {
	out := append([]*ir.Decl(nil), decls...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
