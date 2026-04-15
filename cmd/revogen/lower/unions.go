// Package lower runs IR-on-IR transformations between build and
// emit. Each public function takes a *ir.Spec and mutates it in
// place, with no openapi3 dependency.
package lower

import "github.com/greatliontech/revolut-go/cmd/revogen/ir"

// Unions wires every union-variant struct back to the union(s) it
// belongs to: ImplementsUnions for the marker method, and (for
// wire-tagged unions) UnionDispatch so MarshalJSON injects the
// discriminator value on the wire.
//
// Pre-conditions, established by the build phase:
//   - DeclInterface entries carry MarkerMethod and Variants.
//   - When the union is wire-tagged, Discriminator.PropertyName is
//     set on the interface Decl and Variant.Tag is the wire value.
//
// Post-conditions:
//   - Every variant struct's ImplementsUnions includes the
//     interface name.
//   - Wire-tagged variants have UnionDispatch set with the
//     interface name, propertyName, and the wire value.
func Unions(spec *ir.Spec) {
	declByName := indexDecls(spec.Decls)
	for _, d := range spec.Decls {
		if d.Kind != ir.DeclInterface {
			continue
		}
		for _, v := range d.Variants {
			variant := declByName[v.GoName]
			if variant == nil || variant.Kind != ir.DeclStruct {
				continue
			}
			variant.ImplementsUnions = appendUnique(variant.ImplementsUnions, d.Name)
			if d.Discriminator != nil {
				variant.UnionDispatch = &ir.UnionLink{
					UnionName:    d.Name,
					PropertyName: d.Discriminator.PropertyName,
					Value:        v.Tag,
				}
			}
		}
	}
}

func indexDecls(decls []*ir.Decl) map[string]*ir.Decl {
	out := make(map[string]*ir.Decl, len(decls))
	for _, d := range decls {
		out[d.Name] = d
	}
	return out
}

func appendUnique(s []string, v string) []string {
	for _, x := range s {
		if x == v {
			return s
		}
	}
	return append(s, v)
}
