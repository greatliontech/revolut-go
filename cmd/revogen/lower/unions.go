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

	// markerImpls collects the full set of union markers each
	// struct must implement. Computed via transitive closure so a
	// nested union (interface-of-interfaces) propagates its parents'
	// markers down to the leaf structs that ultimately satisfy it.
	markerImpls := map[string][]string{}
	dispatch := map[string]*ir.UnionLink{}

	var propagate func(unionName string, parentMarkers []string, parentDisc *ir.Discriminator, parentTag string)
	propagate = func(unionName string, parentMarkers []string, parentDisc *ir.Discriminator, parentTag string) {
		union := declByName[unionName]
		if union == nil || union.Kind != ir.DeclInterface {
			return
		}
		// Carry parent markers + this union's marker through to
		// each variant.
		myMarkers := append([]string(nil), parentMarkers...)
		myMarkers = append(myMarkers, union.Name)
		for _, v := range union.Variants {
			variant := declByName[v.GoName]
			if variant == nil {
				continue
			}
			switch variant.Kind {
			case ir.DeclStruct:
				for _, m := range myMarkers {
					markerImpls[v.GoName] = appendUnique(markerImpls[v.GoName], m)
				}
				// Wire-tag dispatch only fires for the immediate
				// parent that's tagged; the spec describes the wire
				// at that level only.
				if union.Discriminator != nil {
					dispatch[v.GoName] = &ir.UnionLink{
						UnionName:    union.Name,
						PropertyName: union.Discriminator.PropertyName,
						Value:        v.Tag,
					}
				}
			case ir.DeclInterface:
				// Nested union — interface inherits all markers
				// itself, then we recurse so its leaves do too.
				for _, m := range myMarkers {
					variant.ImplementsUnions = appendUnique(variant.ImplementsUnions, m)
				}
				propagate(v.GoName, myMarkers, parentDisc, parentTag)
			}
		}
	}

	// Seed the closure from every top-level interface (one whose
	// name doesn't already appear as a variant of another union).
	isVariant := map[string]bool{}
	for _, d := range spec.Decls {
		if d.Kind != ir.DeclInterface {
			continue
		}
		for _, v := range d.Variants {
			isVariant[v.GoName] = true
		}
	}
	for _, d := range spec.Decls {
		if d.Kind != ir.DeclInterface || isVariant[d.Name] {
			continue
		}
		propagate(d.Name, nil, nil, "")
	}

	for name, markers := range markerImpls {
		decl := declByName[name]
		if decl == nil {
			continue
		}
		for _, m := range markers {
			decl.ImplementsUnions = appendUnique(decl.ImplementsUnions, m)
		}
	}
	for name, link := range dispatch {
		if d := declByName[name]; d != nil {
			d.UnionDispatch = link
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
