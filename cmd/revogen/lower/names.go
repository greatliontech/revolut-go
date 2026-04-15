package lower

import (
	"fmt"
	"os"
	"strconv"

	"github.com/greatliontech/revolut-go/cmd/revogen/ir"
)

// ResolveNames is the single collision-resolution pass. It walks
// every exported identifier the generator will emit (Decl names,
// method names per resource, enum constants per enum) and renames
// duplicates with deterministic numeric suffixes (V2, V3, ...).
//
// Renames are propagated through any reference held in the IR:
//   - A renamed Decl: every Type with Kind == KindNamed pointing
//     at the old name is updated.
//   - A renamed method: stays inside its Resource, no further
//     references to update.
//   - A renamed enum constant: only referenced by literal Go code
//     in the user's program, not by the IR; nothing else to update.
//
// Warnings about renames are written to stderr so spec authors can
// disambiguate at the source.
func ResolveNames(spec *ir.Spec) {
	resolveDeclNames(spec)
	resolveMethodNames(spec)
	resolveEnumConstantNames(spec)
}

func resolveDeclNames(spec *ir.Spec) {
	rename := map[string]string{}
	used := map[string]bool{}
	for _, d := range spec.Decls {
		if !used[d.Name] {
			used[d.Name] = true
			continue
		}
		newName := suffixed(d.Name, used)
		fmt.Fprintf(os.Stderr, "revogen: type name collision: %s renamed to %s\n", d.Name, newName)
		rename[d.Name] = newName
		d.Name = newName
		used[newName] = true
	}
	if len(rename) == 0 {
		return
	}
	rewriteTypeReferences(spec, rename)
}

func resolveMethodNames(spec *ir.Spec) {
	for _, r := range spec.Resources {
		used := map[string]bool{}
		for _, m := range r.Methods {
			if !used[m.Name] {
				used[m.Name] = true
				continue
			}
			newName := suffixed(m.Name, used)
			fmt.Fprintf(os.Stderr, "revogen: method name collision on %s: %s renamed to %s\n",
				r.Name, m.Name, newName)
			m.Name = newName
			used[newName] = true
		}
	}
}

func resolveEnumConstantNames(spec *ir.Spec) {
	used := map[string]bool{}
	// Const names live in the package's top-level scope so the
	// uniqueness check spans the whole spec, not per-enum.
	for _, d := range spec.Decls {
		if d.Kind != ir.DeclEnum {
			continue
		}
		for i := range d.EnumValues {
			ev := &d.EnumValues[i]
			if !used[ev.GoName] {
				used[ev.GoName] = true
				continue
			}
			newName := suffixed(ev.GoName, used)
			fmt.Fprintf(os.Stderr, "revogen: enum const collision: %s renamed to %s\n", ev.GoName, newName)
			ev.GoName = newName
			used[newName] = true
		}
	}
}

func suffixed(base string, used map[string]bool) string {
	for i := 2; ; i++ {
		candidate := base + "V" + strconv.Itoa(i)
		if !used[candidate] {
			return candidate
		}
	}
}

// rewriteTypeReferences walks every Type tree in the Spec and
// replaces Named references whose name is in `rename`. It also
// rewrites Decl-level fields that hold raw name strings
// (ImplementsUnions, UnionDispatch.UnionName, Variant.GoName).
func rewriteTypeReferences(spec *ir.Spec, rename map[string]string) {
	var walk func(*ir.Type)
	walk = func(t *ir.Type) {
		if t == nil {
			return
		}
		if t.Kind == ir.KindNamed {
			if newName, ok := rename[t.Name]; ok {
				t.Name = newName
			}
		}
		walk(t.Elem)
		walk(t.Key)
		walk(t.Val)
	}
	for _, d := range spec.Decls {
		for _, f := range d.Fields {
			walk(f.Type)
		}
		walk(d.AliasTarget)
		walk(d.ExtraMap)
		for i, name := range d.ImplementsUnions {
			if newName, ok := rename[name]; ok {
				d.ImplementsUnions[i] = newName
			}
		}
		if d.UnionDispatch != nil {
			if newName, ok := rename[d.UnionDispatch.UnionName]; ok {
				d.UnionDispatch.UnionName = newName
			}
		}
		for i := range d.Variants {
			if newName, ok := rename[d.Variants[i].GoName]; ok {
				d.Variants[i].GoName = newName
			}
		}
	}
	for _, r := range spec.Resources {
		for _, m := range r.Methods {
			for i := range m.PathParams {
				walk(m.PathParams[i].Type)
			}
			for i := range m.HeaderParams {
				walk(m.HeaderParams[i].Type)
			}
			if m.BodyParam != nil {
				walk(m.BodyParam.Type)
			}
			if m.OptsParam != nil {
				walk(m.OptsParam.Type)
			}
			walk(m.Returns)
			walk(m.HTTPCall.RespType)
			if m.Pagination != nil {
				walk(m.Pagination.ItemType)
				walk(m.Pagination.NextTokenType)
				walk(m.Pagination.PageTokenType)
			}
		}
	}
	if newName, ok := rename[spec.ErrorType]; ok {
		spec.ErrorType = newName
	}
	for _, cb := range spec.Callbacks {
		walk(cb.Payload)
	}
}
