package lower

import "github.com/greatliontech/revolut-go/cmd/revogen/ir"

// PromoteOptionalStructs walks every struct Decl and rewrites
// optional fields whose type resolves to another struct Decl into
// pointer wrappers. Without this, the json:",omitempty" tag the
// emitter writes on those fields is functionally a no-op:
// encoding/json only suppresses empty pointers / slices / maps,
// not empty value-typed structs. So a request-body struct with an
// unset optional sub-struct serialises as
// `"DebtorAccount":{"Identification":"","SchemeName":""}` and the
// API rejects the empty payload.
//
// Promotion rules:
//
//   - Only fields marked Required=false (the JSON spec itself
//     said the field is optional).
//   - Only fields whose type, after stripping any leading pointer
//     (defensive — there shouldn't be one), resolves to a
//     DeclStruct.
//   - The field's existing pointer / slice / map shape is left
//     alone if already non-empty-friendly. Promotion only adds a
//     pointer to a bare named-struct type.
//
// Idempotent: running twice is a no-op (already-pointer fields
// short-circuit). Safe to invoke after Unions / ReadOnly because
// neither pass introduces new value-typed optional struct fields.
func PromoteOptionalStructs(spec *ir.Spec) {
	by := indexDecls(spec.Decls)
	for _, d := range spec.Decls {
		if d.Kind != ir.DeclStruct {
			continue
		}
		for _, f := range d.Fields {
			if f.Required || f.Type == nil {
				continue
			}
			if shouldPromote(f.Type, by) {
				f.Type = ir.Pointer(f.Type)
			}
		}
	}
}

// shouldPromote reports whether t is a bare reference to another
// struct Decl that would benefit from pointer wrapping for
// omitempty support.
func shouldPromote(t *ir.Type, by map[string]*ir.Decl) bool {
	if t == nil {
		return false
	}
	// Already-pointer / slice / map don't need promotion;
	// json:",omitempty" handles them.
	if t.IsPointer() || t.IsSlice() || t.Kind == ir.KindMap {
		return false
	}
	if !t.IsNamed() {
		return false
	}
	d := by[t.Name]
	if d == nil {
		return false
	}
	return d.Kind == ir.DeclStruct
}
