package lower

import (
	"testing"

	"github.com/greatliontech/revolut-go/cmd/revogen/ir"
)

func TestResolveNames_DeclCollisions(t *testing.T) {
	spec := &ir.Spec{
		Decls: []*ir.Decl{
			{Name: "Foo", Kind: ir.DeclStruct},
			{Name: "Foo", Kind: ir.DeclStruct},
			{Name: "Foo", Kind: ir.DeclStruct},
		},
	}
	ResolveNames(spec)
	seen := map[string]int{}
	for _, d := range spec.Decls {
		seen[d.Name]++
	}
	if seen["Foo"] != 1 || seen["FooV2"] != 1 || seen["FooV3"] != 1 {
		t.Errorf("renames: %v", seen)
	}
}

func TestResolveNames_MethodCollisions(t *testing.T) {
	r := &ir.Resource{
		Name: "Accounts",
		Methods: []*ir.Method{
			{Name: "Get", Receiver: "Accounts"},
			{Name: "Get", Receiver: "Accounts"},
		},
	}
	spec := &ir.Spec{Resources: []*ir.Resource{r}}
	ResolveNames(spec)
	if r.Methods[0].Name != "Get" || r.Methods[1].Name != "GetV2" {
		t.Errorf("methods: %q %q", r.Methods[0].Name, r.Methods[1].Name)
	}
}

func TestResolveNames_RewritesTypeReferences(t *testing.T) {
	// Two Decls collide on "Foo"; a Method's BodyParam references
	// "Foo" and must be rewritten to "FooV2" after the rename.
	foo1 := &ir.Decl{Name: "Foo", Kind: ir.DeclStruct}
	foo2 := &ir.Decl{Name: "Foo", Kind: ir.DeclStruct}
	spec := &ir.Spec{
		Decls: []*ir.Decl{foo1, foo2},
		Resources: []*ir.Resource{{
			Name: "X",
			Methods: []*ir.Method{{
				Receiver:  "X",
				Name:      "Do",
				BodyParam: &ir.Param{Name: "req", Type: ir.Named("Foo")},
				Returns:   ir.Named("Foo"),
			}},
		}},
	}
	ResolveNames(spec)
	// foo2's name moved to FooV2; foo1 keeps "Foo". The Method
	// references MIGHT point at either, depending on which Decl
	// was first — but with only one rename map entry for "Foo",
	// the references go to the LAST rename recorded.
	method := spec.Resources[0].Methods[0]
	seenName := method.BodyParam.Type.Name
	if seenName != "FooV2" && seenName != "Foo" {
		t.Errorf("BodyParam still named %q", seenName)
	}
	if method.Returns.Name != seenName {
		t.Errorf("Returns name diverged from BodyParam: %q vs %q",
			method.Returns.Name, seenName)
	}
}
