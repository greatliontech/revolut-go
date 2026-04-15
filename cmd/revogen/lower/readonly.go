package lower

import (
	"github.com/greatliontech/revolut-go/cmd/revogen/ir"
)

// ReadOnly handles schemas used as both request bodies and
// responses where some fields are marked readOnly. The pass:
//
//  1. identifies Decls referenced by a Method.BodyParam (request
//     bodies);
//  2. of those, the ones that ALSO appear elsewhere as a response
//     return type or struct field — i.e. shared schemas;
//  3. for each such Decl that has any ReadOnly fields, clones it
//     with the readOnly fields stripped, renames the clone to
//     "<Original>Input", and re-points the affected BodyParam.Type
//     at the clone.
//
// Schemas only used as request bodies (not shared) keep their
// readOnly fields with the omitempty tag — server ignores extras.
//
// Revolut splits request/response schemas by naming convention
// (Location vs Location-Creation), so this pass is typically a
// no-op for those specs. It exists for spec authors who do share.
func ReadOnly(spec *ir.Spec) {
	declByName := indexDecls(spec.Decls)
	requestUses := map[string]int{}
	responseUses := map[string]int{}

	collectFromType := func(t *ir.Type, into map[string]int) {
		visited := map[string]bool{}
		var walk func(*ir.Type)
		walk = func(t *ir.Type) {
			if t == nil {
				return
			}
			switch t.Kind {
			case ir.KindNamed:
				if visited[t.Name] {
					return
				}
				visited[t.Name] = true
				into[t.Name]++
				if d, ok := declByName[t.Name]; ok && d.Kind == ir.DeclStruct {
					for _, f := range d.Fields {
						walk(f.Type)
					}
				}
			case ir.KindPointer, ir.KindSlice:
				walk(t.Elem)
			case ir.KindMap:
				walk(t.Key)
				walk(t.Val)
			}
		}
		walk(t)
	}

	for _, r := range spec.Resources {
		for _, m := range r.Methods {
			if m.BodyParam != nil {
				collectFromType(m.BodyParam.Type, requestUses)
			}
			if m.Returns != nil {
				collectFromType(m.Returns, responseUses)
			}
		}
	}

	clones := map[string]string{} // original Go name -> clone name
	for name, n := range requestUses {
		if n == 0 || responseUses[name] == 0 {
			continue
		}
		d := declByName[name]
		if d == nil || d.Kind != ir.DeclStruct {
			continue
		}
		if !hasReadOnly(d) {
			continue
		}
		cloneName := name + "Input"
		clone := cloneStripReadOnly(d, cloneName)
		spec.Decls = append(spec.Decls, clone)
		declByName[cloneName] = clone
		clones[name] = cloneName
	}
	if len(clones) == 0 {
		return
	}

	// Re-point every Method.BodyParam.Type that referenced a cloned
	// Decl. Only top-level body types are switched; nested
	// references inside the cloned struct's own field types stay
	// pointing at the originals (those are response-shaped and the
	// server returns readOnly fields there).
	for _, r := range spec.Resources {
		for _, m := range r.Methods {
			if m.BodyParam == nil {
				continue
			}
			if name, _ := topNamed(m.BodyParam.Type); name != "" {
				if cloneName, ok := clones[name]; ok {
					// Body receivers are always value shape (see
					// build/operations.go applyRequestBody); the clone
					// follows suit.
					m.BodyParam.Type = ir.Named(cloneName)
				}
			}
		}
	}
}

func hasReadOnly(d *ir.Decl) bool {
	for _, f := range d.Fields {
		if f.ReadOnly {
			return true
		}
	}
	return false
}

func cloneStripReadOnly(orig *ir.Decl, newName string) *ir.Decl {
	out := &ir.Decl{
		Name:                newName,
		Doc:                 orig.Doc,
		Kind:                ir.DeclStruct,
		AnyOfRequiredGroups: orig.AnyOfRequiredGroups,
		ImplementsUnions:    orig.ImplementsUnions,
		FormEncoder:         orig.FormEncoder,
		MultipartEncoder:    orig.MultipartEncoder,
		ExtraMap:            orig.ExtraMap,
	}
	for _, f := range orig.Fields {
		if f.ReadOnly {
			continue
		}
		fc := *f
		out.Fields = append(out.Fields, &fc)
	}
	return out
}

func topNamed(t *ir.Type) (string, bool) {
	if t == nil {
		return "", false
	}
	if t.IsPointer() {
		if t.Elem != nil && t.Elem.IsNamed() {
			return t.Elem.Name, true
		}
		return "", false
	}
	if t.IsNamed() {
		return t.Name, false
	}
	return "", false
}
