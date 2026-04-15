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
	return w.buf.String()
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
	}
}

// writeQueryParamsEncoder emits encode() url.Values for a generated
// `<Op>Params` struct. Each field renders one key=value pair when
// non-zero; arrays expand into repeated entries.
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
			w.printf("\tfor _, v := range %s {\n", expr)
			w.printf("\t\tq.Add(%q, %s)\n", f.JSONName, conv)
			w.write("\t}\n")
			continue
		}
		conv := queryStringify(f.Type, expr)
		guard := isSet(f.Type, expr)
		w.printf("\tif %s {\n", guard)
		w.printf("\t\tq.Set(%q, %s)\n", f.JSONName, conv)
		w.write("\t}\n")
	}
	w.write("\treturn q\n}\n\n")
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
