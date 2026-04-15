package emit

import (
	"go/format"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/greatliontech/revolut-go/cmd/revogen/ir"
)

// assertGofmt fails the test when src isn't valid, gofmt-formatted
// Go. The emitter runs gofmt internally; if a piece of emission
// produces invalid Go, this surfaces the exact line.
func assertGofmt(t *testing.T, name, src string) {
	t.Helper()
	if _, err := format.Source([]byte(src)); err != nil {
		t.Fatalf("%s: gofmt failed: %v\n---source---\n%s", name, err, src)
	}
}

func TestEmit_MinimalSpec(t *testing.T) {
	spec := &ir.Spec{
		Package:   "sample",
		ErrPrefix: "sample",
		Decls: []*ir.Decl{
			{
				Name: "Account",
				Kind: ir.DeclStruct,
				Fields: []*ir.Field{
					{JSONName: "id", GoName: "ID", Type: ir.Prim("string"), Required: true},
					{JSONName: "name", GoName: "Name", Type: ir.Prim("string")},
				},
			},
			{
				Name:     "State",
				Kind:     ir.DeclEnum,
				EnumBase: ir.Prim("string"),
				EnumValues: []ir.EnumValue{
					{GoName: "StateActive", Value: "active"},
					{GoName: "StateInactive", Value: "inactive"},
				},
			},
		},
		Resources: []*ir.Resource{{
			Name: "Accounts",
			Methods: []*ir.Method{{
				Receiver: "Accounts",
				Name:     "List",
				Doc:      []string{"List all accounts."},
				Returns:  ir.Slice(ir.Named("Account")),
				HTTPCall: ir.HTTPCall{
					Method:   "GET",
					PathExpr: "/accounts",
					RespKind: ir.RespJSONList,
					RespType: ir.Slice(ir.Named("Account")),
				},
			}},
		}},
	}

	dir := t.TempDir()
	if err := Spec(spec, dir); err != nil {
		t.Fatalf("Spec: %v", err)
	}
	for _, name := range []string{"gen_types.go", "gen_accounts.go", "gen_client.go"} {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		assertGofmt(t, name, string(b))
	}
}

func TestEmit_StructFields(t *testing.T) {
	d := &ir.Decl{
		Name: "X",
		Kind: ir.DeclStruct,
		Fields: []*ir.Field{
			{JSONName: "id", GoName: "ID", Type: ir.Prim("string"), Required: true},
			{JSONName: "at", GoName: "At", Type: ir.Pointer(ir.Prim("time.Time", "time"))},
		},
	}
	w := newFileWriter("x", []string{"time"})
	w.header()
	writeDecl(w, d)
	got := w.buf.String()
	assertGofmt(t, "struct", got)
	if !strings.Contains(got, `ID string `+"`"+`json:"id"`+"`") {
		t.Errorf("required tag missing omitempty check: %s", got)
	}
	if !strings.Contains(got, `At *time.Time `+"`"+`json:"at,omitempty"`) {
		t.Errorf("optional pointer: %s", got)
	}
}

func TestEmit_EnumConsts(t *testing.T) {
	d := &ir.Decl{
		Name:     "Color",
		Kind:     ir.DeclEnum,
		EnumBase: ir.Prim("string"),
		EnumValues: []ir.EnumValue{
			{GoName: "ColorRed", Value: "red"},
			{GoName: "ColorGreen", Value: "green"},
		},
	}
	w := newFileWriter("x", nil)
	w.header()
	writeDecl(w, d)
	got := w.buf.String()
	assertGofmt(t, "enum", got)
	for _, want := range []string{
		"type Color string",
		`ColorRed Color = "red"`,
		`ColorGreen Color = "green"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

func TestEmit_MapAlias(t *testing.T) {
	d := &ir.Decl{
		Name:        "Labels",
		Kind:        ir.DeclAlias,
		AliasTarget: ir.Map(ir.Prim("string"), ir.Slice(ir.Prim("string"))),
	}
	w := newFileWriter("x", nil)
	w.header()
	writeDecl(w, d)
	got := w.buf.String()
	if !strings.Contains(got, "type Labels = map[string][]string") {
		t.Errorf("alias expr: %s", got)
	}
}

func TestEmit_Union_WireTagged(t *testing.T) {
	spec := &ir.Spec{
		Package: "x",
		Decls: []*ir.Decl{
			{
				Name:          "Shape",
				Kind:          ir.DeclInterface,
				MarkerMethod:  "isShape",
				Discriminator: &ir.Discriminator{PropertyName: "type"},
				Variants: []ir.Variant{
					{GoName: "Circle", Tag: "circle"},
					{GoName: "Square", Tag: "square"},
				},
			},
			{
				Name:             "Circle",
				Kind:             ir.DeclStruct,
				ImplementsUnions: []string{"Shape"},
				UnionDispatch: &ir.UnionLink{
					UnionName:    "Shape",
					PropertyName: "type",
					Value:        "circle",
				},
				Fields: []*ir.Field{
					{JSONName: "radius", GoName: "Radius", Type: ir.Prim("json.Number", "encoding/json"), Required: true},
				},
			},
			{
				Name:             "Square",
				Kind:             ir.DeclStruct,
				ImplementsUnions: []string{"Shape"},
				UnionDispatch: &ir.UnionLink{
					UnionName:    "Shape",
					PropertyName: "type",
					Value:        "square",
				},
				Fields: []*ir.Field{
					{JSONName: "side", GoName: "Side", Type: ir.Prim("json.Number", "encoding/json"), Required: true},
				},
			},
		},
	}
	dir := t.TempDir()
	if err := Spec(spec, dir); err != nil {
		t.Fatalf("Spec: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "gen_types.go"))
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	assertGofmt(t, "gen_types", src)
	for _, want := range []string{
		"type Shape interface {",
		"isShape()",
		`case "circle":`,
		`case "square":`,
		"func (Circle) isShape() {}",
		"func (v Circle) MarshalJSON() ([]byte, error) {",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("missing %q", want)
		}
	}
}
