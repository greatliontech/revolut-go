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

// TestEmit_BodyReceiverEmittedAsValue pins the body-receiver
// normalisation end-to-end: the IR-level test (in build/) checks
// the Type flag, but it doesn't prove the emitter renders the
// signature as `req X` instead of `req *X`. A regression that
// re-pointerised the body in methodParamList would slip past the
// IR-level check but fail here.
func TestEmit_BodyReceiverEmittedAsValue(t *testing.T) {
	spec := &ir.Spec{
		Package:   "sample",
		ErrPrefix: "sample",
		Decls: []*ir.Decl{{
			Name: "Foo",
			Kind: ir.DeclStruct,
			Fields: []*ir.Field{
				{JSONName: "id", GoName: "ID", Type: ir.Prim("string"), Required: true},
			},
		}},
		Resources: []*ir.Resource{{
			Name: "Foos",
			Methods: []*ir.Method{{
				Receiver:  "Foos",
				Name:      "Create",
				BodyParam: &ir.Param{Name: "req", Type: ir.Named("Foo")},
				Returns:   ir.Named("Foo"),
				HTTPCall: ir.HTTPCall{
					Method:   "POST",
					PathExpr: "foos",
					BodyKind: ir.BodyJSON,
					BodyExpr: "req",
					RespKind: ir.RespJSONValue,
					RespType: ir.Named("Foo"),
				},
			}},
		}},
	}
	dir, err := os.MkdirTemp("", "emit-body-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	if err := Spec(spec, dir); err != nil {
		t.Fatalf("Spec: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "gen_foos.go"))
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	if !strings.Contains(src, "req Foo") {
		t.Errorf("expected body param to render as value `req Foo`:\n%s", src)
	}
	if strings.Contains(src, "req *Foo") {
		t.Errorf("body param emitted as pointer — normalisation regressed:\n%s", src)
	}
}

// TestEmit_HeaderParamTypes covers the four header-param shapes the
// generator has to handle: plain string, named string-backed enum,
// numeric, and boolean. Regression test for a vet-caught bug where
// named enums were emitted without a string() conversion and ints
// were compared to "".
// TestEmit_QueryParamsEncoderAndApplyDefaults pins the three
// intertwined emit paths on a Params struct: the encode() helper
// that serialises fields into url.Values, ApplyDefaults that
// fills zero-value fields with the spec's declared defaults, and
// the interaction between DefaultLiteral / DefaultZeroCond.
func TestEmit_QueryParamsEncoderAndApplyDefaults(t *testing.T) {
	d := &ir.Decl{
		Name:               "ListParams",
		Kind:               ir.DeclStruct,
		QueryParamsEncoder: true,
		Fields: []*ir.Field{
			{JSONName: "limit", GoName: "Limit", Type: ir.Prim("int"), DefaultLiteral: "100"},
			{JSONName: "page_token", GoName: "PageToken", Type: ir.Prim("string")},
		},
	}
	w := newFileWriter("sample", []string{"net/url", "strconv"})
	w.header()
	writeDecl(w, d)
	got := w.buf.String()
	assertGofmt(t, "params-encoder", got)

	for _, want := range []string{
		"func (p *ListParams) encode() url.Values",
		`q.Set("limit", strconv.FormatInt(int64(p.Limit), 10))`,
		`q.Set("page_token", p.PageToken)`,
		"func (p *ListParams) ApplyDefaults()",
		"if p.Limit == 0 {",
		"p.Limit = 100",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q\n---source---\n%s", want, got)
		}
	}
}

// TestEmit_FormEncoder pins the form encoder emission. Form
// bodies are common on OAuth token exchanges; the encoder must
// produce url.Values with one key per required field.
func TestEmit_FormEncoder(t *testing.T) {
	d := &ir.Decl{
		Name:        "TokenReq",
		Kind:        ir.DeclStruct,
		FormEncoder: true,
		Fields: []*ir.Field{
			{JSONName: "grant_type", GoName: "GrantType", Type: ir.Prim("string"), Required: true},
			{JSONName: "refresh_token", GoName: "RefreshToken", Type: ir.Prim("string")},
		},
	}
	w := newFileWriter("x", []string{"fmt", "net/url", "strconv", "time"})
	w.header()
	writeDecl(w, d)
	got := w.buf.String()
	assertGofmt(t, "form-encoder", got)
	for _, want := range []string{
		"func (r *TokenReq) encodeForm() url.Values",
		`"grant_type"`,
		`"refresh_token"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q\n---source---\n%s", want, got)
		}
	}
}

// TestEmit_ProbeDecoderForUntaggedUnion pins the emitter path
// for an untagged union where each variant has a required-field
// probe. The decoder reads into a map[string]json.RawMessage
// and dispatches based on which probe keys are present.
func TestEmit_ProbeDecoderForUntaggedUnion(t *testing.T) {
	spec := &ir.Spec{
		Package:   "sample",
		ErrPrefix: "sample",
		Decls: []*ir.Decl{
			{
				Name:         "Event",
				Kind:         ir.DeclInterface,
				MarkerMethod: "isEvent",
				Variants: []ir.Variant{
					{GoName: "Click", Tag: "Click", RequiredProbe: []string{"x", "y"}},
					{GoName: "Scroll", Tag: "Scroll", RequiredProbe: []string{"delta"}},
				},
			},
			{
				Name: "Click",
				Kind: ir.DeclStruct,
				Fields: []*ir.Field{
					{JSONName: "x", GoName: "X", Type: ir.Prim("int"), Required: true},
					{JSONName: "y", GoName: "Y", Type: ir.Prim("int"), Required: true},
				},
				ImplementsUnions: []string{"Event"},
			},
			{
				Name: "Scroll",
				Kind: ir.DeclStruct,
				Fields: []*ir.Field{
					{JSONName: "delta", GoName: "Delta", Type: ir.Prim("int"), Required: true},
				},
				ImplementsUnions: []string{"Event"},
			},
		},
	}
	dir, err := os.MkdirTemp("", "emit-probe-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	if err := Spec(spec, dir); err != nil {
		t.Fatalf("Spec: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "gen_types.go"))
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	assertGofmt(t, "probe-decoder", src)
	for _, want := range []string{
		"type Event interface",
		"isEvent()",
		"func decodeEvent(data []byte) (Event, error)",
		"validate.HasJSONKey(probe",
		"func (Click) isEvent()",
		"func (Scroll) isEvent()",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("missing %q\n---source---\n%s", want, src)
		}
	}
}

// TestEmit_CursorPaginationIterator pins the Seq2 iterator
// emission for cursor-style pagination: the generated ListAll
// method wraps List and advances via the next_page_token field.
func TestEmit_CursorPaginationIterator(t *testing.T) {
	spec := &ir.Spec{
		Package:   "sample",
		ErrPrefix: "sample",
		Decls: []*ir.Decl{
			{Name: "Item", Kind: ir.DeclStruct, Fields: []*ir.Field{
				{JSONName: "id", GoName: "ID", Type: ir.Prim("string"), Required: true},
			}},
			{Name: "ListResp", Kind: ir.DeclStruct, Fields: []*ir.Field{
				{JSONName: "items", GoName: "Items", Type: ir.Slice(ir.Named("Item"))},
				{JSONName: "next_page_token", GoName: "NextPageToken", Type: ir.Prim("string")},
			}},
		},
		Resources: []*ir.Resource{{
			Name: "Stuff",
			Methods: []*ir.Method{{
				Receiver: "Stuff",
				Name:     "List",
				Returns:  ir.Named("ListResp"),
				HTTPCall: ir.HTTPCall{
					Method: "GET", PathExpr: "stuff",
					RespKind: ir.RespJSONValue,
					RespType: ir.Named("ListResp"),
				},
				Pagination: &ir.Pagination{
					Shape:          ir.PaginationCursor,
					ItemType:       ir.Named("Item"),
					ItemsField:     "Items",
					NextTokenField: "NextPageToken",
					NextTokenType:  ir.Prim("string"),
					PageTokenParam: "PageToken",
					PageTokenType:  ir.Prim("string"),
				},
			}},
		}},
	}
	dir, err := os.MkdirTemp("", "emit-pag-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	if err := Spec(spec, dir); err != nil {
		t.Fatalf("Spec: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "gen_stuff.go"))
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	assertGofmt(t, "pagination", src)
	for _, want := range []string{
		"func (s *Stuff) ListAll(",
		"iter.Seq2[Item, error]",
		"s.List(ctx",
		"resp.NextPageToken",
		"p.PageToken",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("missing %q\n---source---\n%s", want, src)
		}
	}
}

func TestEmit_HeaderParamTypes(t *testing.T) {
	spec := &ir.Spec{
		Package:   "sample",
		ErrPrefix: "sample",
		Decls: []*ir.Decl{{
			Name:     "APIVersion",
			Kind:     ir.DeclEnum,
			EnumBase: ir.Prim("string"),
			EnumValues: []ir.EnumValue{
				{GoName: "APIVersionV1", Value: "2024-01-01"},
			},
		}},
		Resources: []*ir.Resource{{
			Name: "Widgets",
			Methods: []*ir.Method{{
				Receiver: "Widgets",
				Name:     "Ping",
				HeaderParams: []ir.Param{
					{Name: "authorization", Type: ir.Prim("string"), WireName: "Authorization"},
					{Name: "apiVersion", Type: ir.Named("APIVersion"), WireName: "X-Api-Version"},
					{Name: "timestamp", Type: ir.Prim("int"), WireName: "X-Timestamp"},
					{Name: "dryRun", Type: ir.Prim("bool"), WireName: "X-Dry-Run"},
				},
				HTTPCall: ir.HTTPCall{Method: "GET", PathExpr: "ping", RespKind: ir.RespNone},
			}},
		}},
	}
	dir, err := os.MkdirTemp("", "emit-headers-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	if err := Spec(spec, dir); err != nil {
		if bad, rerr := os.ReadFile(filepath.Join(dir, "gen_widgets.go.bad")); rerr == nil {
			t.Fatalf("Spec: %v\n---bad source---\n%s", err, bad)
		}
		t.Fatalf("Spec: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "gen_widgets.go"))
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	assertGofmt(t, "gen_widgets", src)
	for _, want := range []string{
		`if authorization != "" {`,
		`r.Headers.Set("Authorization", authorization)`,
		`if apiVersion != "" {`,
		`r.Headers.Set("X-Api-Version", string(apiVersion))`,
		`r.Headers.Set("X-Timestamp", strconv.Itoa(int(timestamp)))`,
		`r.Headers.Set("X-Dry-Run", strconv.FormatBool(dryRun))`,
		`"strconv"`,
	} {
		if !strings.Contains(src, want) {
			t.Errorf("missing %q\n---source---\n%s", want, src)
		}
	}
	// Int/bool params must NOT have an empty-string guard — the bug
	// we're regression-testing emitted `if timestamp != ""` which
	// doesn't compile.
	for _, unwanted := range []string{
		`if timestamp != ""`,
		`if dryRun != ""`,
	} {
		if strings.Contains(src, unwanted) {
			t.Errorf("unexpected %q\n---source---\n%s", unwanted, src)
		}
	}
}
