package lower

import (
	"sort"

	"github.com/greatliontech/revolut-go/cmd/revogen/ir"
	"github.com/greatliontech/revolut-go/cmd/revogen/names"
)

// FileImports computes per-file import sets for the emit phase.
// Returns a map keyed by gen-file basename:
//
//   - "gen_types.go": every Decl's referenced packages plus the
//     transport package (always needed for Client wiring lives in
//     gen_client.go, not here).
//   - "gen_<resource>.go" for every Resource: imports the methods
//     in that resource collectively reference, plus the always-on
//     "context" / "net/http" / transport set.
//   - "gen_client.go": just transport.
//
// Stdlib and third-party imports are bucketed; emit groups them
// with a blank line in between.
func FileImports(spec *ir.Spec) map[string][]string {
	out := map[string][]string{}

	const transportPkg = "github.com/greatliontech/revolut-go/internal/transport"
	const validatePkg = "github.com/greatliontech/revolut-go/internal/validate"

	// gen_types.go
	typesSet := map[string]struct{}{}
	for _, d := range spec.Decls {
		collectDeclImports(d, typesSet)
		// Encoders pull in additional stdlib packages that don't
		// surface via Type.CollectImports.
		if d.FormEncoder {
			typesSet["fmt"] = struct{}{}
			typesSet["net/url"] = struct{}{}
			typesSet["strconv"] = struct{}{}
			typesSet["time"] = struct{}{}
		}
		if d.MultipartEncoder {
			typesSet["bytes"] = struct{}{}
			typesSet["fmt"] = struct{}{}
			typesSet["io"] = struct{}{}
			typesSet["mime/multipart"] = struct{}{}
			typesSet["net/textproto"] = struct{}{}
			typesSet["strconv"] = struct{}{}
			typesSet["time"] = struct{}{}
		}
		if d.Kind == ir.DeclInterface && d.Discriminator == nil && len(d.Variants) > 0 {
			// Probe decoder uses encoding/json + json.RawMessage
			// and the shared validate.HasJSONKey helper.
			typesSet["encoding/json"] = struct{}{}
			typesSet[validatePkg] = struct{}{}
		}
		if d.Kind == ir.DeclInterface && d.Discriminator != nil {
			// Wire-tagged unions need encoding/json for MarshalJSON
			// dispatch and json.RawMessage for unmarshal.
			typesSet["encoding/json"] = struct{}{}
		}
		if d.ExtraMap != nil {
			typesSet["encoding/json"] = struct{}{}
		}
		if d.QueryParamsEncoder {
			typesSet["net/url"] = struct{}{}
			// Field-type-driven additions: strconv for ints/bools,
			// time for time.Time, fmt only as a fallback for shapes
			// the explicit cases miss.
			for _, f := range d.Fields {
				addQueryFieldImports(f.Type, typesSet)
				// style=form, explode=false joins slice values with
				// strings.Join so the wire shape matches the spec.
				if f.ExplodeFalse {
					typesSet["strings"] = struct{}{}
				}
			}
		}
		// Any struct with at least one sensitive field carries a
		// String/GoString method that fmt.Sprintf's.
		if d.Kind == ir.DeclStruct {
			for _, f := range d.Fields {
				if f.Sensitive {
					typesSet["fmt"] = struct{}{}
					break
				}
			}
		}
	}
	// ResponseMetadata + extractResponseMetadata live in gen_types.go
	// and need net/http. Emitted once per package when any method
	// surfaces metadata; gate the import on the same condition.
	if specUsesResponseMetadata(spec) {
		typesSet["net/http"] = struct{}{}
	}
	out["gen_types.go"] = ir.SortedImports(typesSet)

	// gen_<resource>.go: per-resource set.
	for _, r := range spec.Resources {
		set := map[string]struct{}{
			"context":  {},
			"net/http": {},
			transportPkg: {},
		}
		needsErrors := false
		needsURL := false
		needsJSON := false
		needsIter := false
		needsValidate := false
		for _, m := range r.Methods {
			if len(m.PathParams) > 0 {
				// renderPathExpr emits url.PathEscape(...) for each
				// path param.
				needsURL = true
				needsErrors = true
				// Any UUID-formatted path param calls validate.IsUUID.
				for _, p := range m.PathParams {
					if p.Format == "uuid" {
						needsValidate = true
					}
				}
			}
			if len(m.Validators) > 0 {
				needsErrors = true
				for _, v := range m.Validators {
					if v.Uses.Has(ir.UsesPattern) || v.Uses.Has(ir.UsesNumberAsFloat) {
						needsValidate = true
					}
				}
			}
			for _, hp := range m.HeaderParams {
				if hp.Required {
					needsErrors = true
					break
				}
			}
			collectMethodImports(m, set)
			if m.Pagination != nil {
				needsIter = true
			}
			if m.HTTPCall.BodyKind == ir.BodyMultipart || m.HTTPCall.BodyKind == ir.BodyForm || m.HTTPCall.BodyKind == ir.BodyRawStream {
				needsJSON = true // typed JSON responses on raw bodies still decode through json.Unmarshal
			}
			if len(m.HeaderParams) > 0 && m.HTTPCall.RespKind != ir.RespNone && m.HTTPCall.RespKind != ir.RespRawBytes {
				// Header-carrying methods route through DoRaw; JSON
				// responses decode via encoding/json on the returned
				// byte slice.
				needsJSON = true
			}
			for _, hp := range m.HeaderParams {
				if pkg := headerSetImport(hp.Type); pkg != "" {
					set[pkg] = struct{}{}
				}
			}
			if m.HTTPCall.RespKind == ir.RespUnionTagged || m.HTTPCall.RespKind == ir.RespUnionProbe {
				needsJSON = true
			}
		}
		if needsErrors {
			set["errors"] = struct{}{}
		}
		if needsURL {
			set["net/url"] = struct{}{}
		}
		if needsJSON {
			set["encoding/json"] = struct{}{}
		}
		if needsIter {
			set["iter"] = struct{}{}
		}
		if needsValidate {
			set[validatePkg] = struct{}{}
		}
		out["gen_"+lowerASCII(r.Name)+".go"] = ir.SortedImports(set)
	}

	// gen_client.go
	out["gen_client.go"] = []string{transportPkg}

	return out
}

// headerSetImport reports the stdlib package emit's writeHeaderSet
// needs for a header param of the given type, or "". Dispatches on
// ir.Shape so the result stays in sync with writeHeaderSet's
// case table without a parallel switch.
func headerSetImport(t *ir.Type) string {
	switch t.Shape() {
	case ir.ShapeString, ir.ShapeNamedString:
		return ""
	case ir.ShapeInt, ir.ShapeBool:
		return "strconv"
	case ir.ShapeOther:
		return "fmt"
	}
	return "fmt"
}

// specUsesResponseMetadata reports whether any method in spec
// surfaces the generated ResponseMetadata struct. Mirrors
// emit.collectMetadataFields' non-empty check so the import list
// never lies about what gen_types.go references.
func specUsesResponseMetadata(spec *ir.Spec) bool {
	for _, r := range spec.Resources {
		for _, m := range r.Methods {
			if len(m.ResponseMetadata) > 0 {
				return true
			}
		}
	}
	return false
}

// addQueryFieldImports examines a query-param field's Go type and
// records the stdlib packages its serialiser needs. Dispatches on
// ir.Shape so the result stays in sync with emit.queryStringify.
func addQueryFieldImports(t *ir.Type, set map[string]struct{}) {
	if t == nil {
		return
	}
	switch t.Shape() {
	case ir.ShapeSlice, ir.ShapePointer:
		addQueryFieldImports(t.Elem, set)
	case ir.ShapeInt, ir.ShapeBool:
		set["strconv"] = struct{}{}
	case ir.ShapeTime:
		set["time"] = struct{}{}
	case ir.ShapeString, ir.ShapeJSONNumber, ir.ShapeCurrency, ir.ShapeNamedString:
		// stringify is direct (or a string cast); no stdlib helpers.
	default:
		// Map / raw / other: fall back to fmt.Sprint.
		set["fmt"] = struct{}{}
	}
}

func collectDeclImports(d *ir.Decl, set map[string]struct{}) {
	for _, f := range d.Fields {
		f.Type.CollectImports(set)
	}
	d.AliasTarget.CollectImports(set)
	d.ExtraMap.CollectImports(set)
	d.EnumBase.CollectImports(set)
}

func collectMethodImports(m *ir.Method, set map[string]struct{}) {
	for _, p := range m.PathParams {
		p.Type.CollectImports(set)
	}
	for _, p := range m.HeaderParams {
		p.Type.CollectImports(set)
	}
	if m.BodyParam != nil {
		m.BodyParam.Type.CollectImports(set)
	}
	if m.OptsParam != nil {
		m.OptsParam.Type.CollectImports(set)
	}
	m.Returns.CollectImports(set)
	m.HTTPCall.RespType.CollectImports(set)
	if m.Pagination != nil {
		m.Pagination.ItemType.CollectImports(set)
		m.Pagination.NextTokenType.CollectImports(set)
		m.Pagination.PageTokenType.CollectImports(set)
	}
}

// lowerASCII forwards to names.LowerASCII; kept as a thin wrapper
// here to avoid touching every call site while package
// dependencies settle.
func lowerASCII(s string) string { return names.LowerASCII(s) }

// Group splits a sorted import list into stdlib and third-party
// buckets so the emitter can render them with a blank line between.
func Group(imports []string) (stdlib, third []string) {
	for _, p := range imports {
		if ir.IsStdlib(p) {
			stdlib = append(stdlib, p)
		} else {
			third = append(third, p)
		}
	}
	sort.Strings(stdlib)
	sort.Strings(third)
	return stdlib, third
}
