package lower

import (
	"strings"
	"testing"

	"github.com/greatliontech/revolut-go/cmd/revogen/ir"
)

func TestValidators_PlainRequiredFields(t *testing.T) {
	req := &ir.Decl{
		Name: "CreateReq",
		Kind: ir.DeclStruct,
		Fields: []*ir.Field{
			{JSONName: "request_id", GoName: "RequestID", Type: ir.Prim("string"), Required: true},
			{JSONName: "amount", GoName: "Amount", Type: ir.Prim("json.Number", "encoding/json"), Required: true},
			{JSONName: "note", GoName: "Note", Type: ir.Prim("string")},
		},
	}
	method := &ir.Method{
		Receiver: "X",
		Name:     "Do",
		BodyParam: &ir.Param{Name: "req", Type: ir.Named("CreateReq")},
	}
	spec := &ir.Spec{
		ErrPrefix: "business",
		Decls:     []*ir.Decl{req},
		Resources: []*ir.Resource{{Name: "X", Methods: []*ir.Method{method}}},
	}
	Validators(spec)
	if len(method.Validators) != 2 {
		t.Fatalf("validators: %+v", method.Validators)
	}
	conds := []string{method.Validators[0].Cond, method.Validators[1].Cond}
	want := map[string]bool{`req.RequestID == ""`: true, `req.Amount == ""`: true}
	for _, c := range conds {
		if !want[c] {
			t.Errorf("unexpected cond: %q", c)
		}
	}
}

func TestValidators_PointerStructRecursion(t *testing.T) {
	nested := &ir.Decl{
		Name: "Address",
		Kind: ir.DeclStruct,
		Fields: []*ir.Field{
			{JSONName: "country", GoName: "Country", Type: ir.Prim("string"), Required: true},
		},
	}
	req := &ir.Decl{
		Name: "Req",
		Kind: ir.DeclStruct,
		Fields: []*ir.Field{
			{JSONName: "address", GoName: "Address", Type: ir.Pointer(ir.Named("Address")), Required: true},
		},
	}
	method := &ir.Method{
		Receiver:  "X",
		Name:      "Do",
		BodyParam: &ir.Param{Name: "req", Type: ir.Named("Req")},
	}
	spec := &ir.Spec{
		ErrPrefix: "business",
		Decls:     []*ir.Decl{req, nested},
		Resources: []*ir.Resource{{Name: "X", Methods: []*ir.Method{method}}},
	}
	Validators(spec)

	var foundNilCheck, foundCountryGuard bool
	for _, v := range method.Validators {
		if v.Cond == "req.Address == nil" {
			foundNilCheck = true
		}
		if strings.Contains(v.Cond, "req.Address != nil") && strings.Contains(v.Cond, "req.Address.Country") {
			foundCountryGuard = true
		}
	}
	if !foundNilCheck {
		t.Errorf("missing nil check on pointer-struct field; got: %+v", method.Validators)
	}
	if !foundCountryGuard {
		t.Errorf("missing guarded nested check; got: %+v", method.Validators)
	}
}

func TestValidators_AnyOfRequiredGroups(t *testing.T) {
	req := &ir.Decl{
		Name: "Period",
		Kind: ir.DeclStruct,
		Fields: []*ir.Field{
			{JSONName: "start_date", GoName: "StartDate", Type: ir.Prim("string")},
			{JSONName: "end_date", GoName: "EndDate", Type: ir.Prim("string")},
			{JSONName: "end_action", GoName: "EndAction", Type: ir.Prim("string")},
		},
		AnyOfRequiredGroups: [][]string{
			{"start_date"},
			{"end_action", "end_date"},
		},
	}
	method := &ir.Method{
		Receiver:  "X",
		Name:      "Do",
		BodyParam: &ir.Param{Name: "req", Type: ir.Named("Period")},
	}
	spec := &ir.Spec{
		ErrPrefix: "business",
		Decls:     []*ir.Decl{req},
		Resources: []*ir.Resource{{Name: "X", Methods: []*ir.Method{method}}},
	}
	Validators(spec)
	if len(method.Validators) == 0 {
		t.Fatal("no validators emitted")
	}
	msg := method.Validators[0].Message
	// Error message enumerates both groups with AND/OR.
	if !strings.Contains(msg, "start_date") || !strings.Contains(msg, "end_date") || !strings.Contains(msg, " OR ") {
		t.Errorf("message missing group spelling: %q", msg)
	}
}
