package lower

import (
	"testing"

	"github.com/greatliontech/revolut-go/cmd/revogen/ir"
)

func TestUnions_WireTaggedDispatch(t *testing.T) {
	spec := &ir.Spec{
		Decls: []*ir.Decl{
			{
				Name:          "PaymentMethod",
				Kind:          ir.DeclInterface,
				MarkerMethod:  "isPaymentMethod",
				Discriminator: &ir.Discriminator{PropertyName: "type"},
				Variants: []ir.Variant{
					{GoName: "ApplePay", Tag: "apple_pay"},
					{GoName: "Card", Tag: "card"},
				},
			},
			{Name: "ApplePay", Kind: ir.DeclStruct},
			{Name: "Card", Kind: ir.DeclStruct},
		},
	}
	Unions(spec)

	for _, name := range []string{"ApplePay", "Card"} {
		var d *ir.Decl
		for _, candidate := range spec.Decls {
			if candidate.Name == name {
				d = candidate
				break
			}
		}
		if d == nil {
			t.Fatalf("%s missing", name)
		}
		if len(d.ImplementsUnions) != 1 || d.ImplementsUnions[0] != "PaymentMethod" {
			t.Errorf("%s ImplementsUnions: %v", name, d.ImplementsUnions)
		}
		if d.UnionDispatch == nil {
			t.Fatalf("%s UnionDispatch nil", name)
		}
		if d.UnionDispatch.PropertyName != "type" {
			t.Errorf("%s property: %q", name, d.UnionDispatch.PropertyName)
		}
	}
}

func TestUnions_NestedUnionPropagates(t *testing.T) {
	// Parent interface has a variant that is itself an interface;
	// the nested interface's leaf structs must satisfy both
	// markers.
	spec := &ir.Spec{
		Decls: []*ir.Decl{
			{
				Name:         "Location",
				Kind:         ir.DeclInterface,
				MarkerMethod: "isLocation",
				Variants: []ir.Variant{
					{GoName: "LocationPhysical", Tag: "physical"},
				},
			},
			{
				Name:         "LocationPhysical",
				Kind:         ir.DeclInterface,
				MarkerMethod: "isLocationPhysical",
				Variants: []ir.Variant{
					{GoName: "Shop", Tag: "shop"},
				},
			},
			{Name: "Shop", Kind: ir.DeclStruct},
		},
	}
	Unions(spec)

	var shop, physical *ir.Decl
	for _, d := range spec.Decls {
		switch d.Name {
		case "Shop":
			shop = d
		case "LocationPhysical":
			physical = d
		}
	}
	if shop == nil || physical == nil {
		t.Fatal("setup")
	}
	// Shop must satisfy BOTH Location and LocationPhysical.
	got := map[string]bool{}
	for _, u := range shop.ImplementsUnions {
		got[u] = true
	}
	if !got["Location"] || !got["LocationPhysical"] {
		t.Errorf("Shop.ImplementsUnions: %v", shop.ImplementsUnions)
	}
	// LocationPhysical (an interface) must also carry Location's
	// marker so its interface declaration includes isLocation().
	if len(physical.ImplementsUnions) != 1 || physical.ImplementsUnions[0] != "Location" {
		t.Errorf("LocationPhysical.ImplementsUnions: %v", physical.ImplementsUnions)
	}
}
