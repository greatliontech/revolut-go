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
		Receiver:  "X",
		Name:      "Do",
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

// TestValidators_RequiredQueryParams pins the opts validator shape:
// a struct with any required field emits an `opts == nil` check
// first followed by per-field checks without a nil guard, so the
// emitted Go short-circuits on the first failure.
func TestValidators_RequiredQueryParams(t *testing.T) {
	params := &ir.Decl{
		Name: "GetRateParams",
		Kind: ir.DeclStruct,
		Fields: []*ir.Field{
			{JSONName: "from", GoName: "From", Type: ir.Named("Currency"), Required: true},
			{JSONName: "amount", GoName: "Amount", Type: ir.Prim("json.Number", "encoding/json")},
			{JSONName: "to", GoName: "To", Type: ir.Named("Currency"), Required: true},
		},
	}
	currency := &ir.Decl{Name: "Currency", Kind: ir.DeclEnum, EnumBase: ir.Prim("string")}
	method := &ir.Method{
		Receiver:  "ForeignExchange",
		Name:      "GetRate",
		OptsParam: &ir.Param{Name: "opts", Type: ir.Pointer(ir.Named("GetRateParams"))},
	}
	spec := &ir.Spec{
		ErrPrefix: "business",
		Decls:     []*ir.Decl{params, currency},
		Resources: []*ir.Resource{{Name: "ForeignExchange", Methods: []*ir.Method{method}}},
	}
	Validators(spec)
	if len(method.Validators) != 3 {
		t.Fatalf("want 3 validators, got %d: %+v", len(method.Validators), method.Validators)
	}
	if got := method.Validators[0].Cond; got != "opts == nil" {
		t.Errorf("first validator should be opts == nil; got %q", got)
	}
	for _, v := range method.Validators[1:] {
		if strings.Contains(v.Cond, "!= nil") {
			t.Errorf("inner validator has redundant nil guard: %q", v.Cond)
		}
	}
}

// TestValidators_OptionalQueryParamsNoValidators: opts with zero
// required fields gets no validators — leaving the param genuinely
// optional for callers.
// TestValidators_MinMaxLength covers string length constraints,
// including the present-guard on an optional field and the
// unconditional check on a required field.
func TestValidators_MinMaxLength(t *testing.T) {
	req := &ir.Decl{
		Name: "Req",
		Kind: ir.DeclStruct,
		Fields: []*ir.Field{
			{JSONName: "required_name", GoName: "RequiredName", Type: ir.Prim("string"),
				Required: true, MinLength: 3, MaxLength: 10},
			{JSONName: "optional_name", GoName: "OptionalName", Type: ir.Prim("string"),
				MinLength: 3, MaxLength: 10},
		},
	}
	method := &ir.Method{
		Receiver:  "X",
		Name:      "Do",
		BodyParam: &ir.Param{Name: "req", Type: ir.Named("Req")},
	}
	spec := &ir.Spec{
		ErrPrefix: "b",
		Decls:     []*ir.Decl{req},
		Resources: []*ir.Resource{{Name: "X", Methods: []*ir.Method{method}}},
	}
	Validators(spec)
	var minOpt, maxOpt, minReq, maxReq bool
	for _, v := range method.Validators {
		switch {
		case strings.Contains(v.Cond, `req.OptionalName != "" && len(req.OptionalName) < 3`):
			minOpt = true
		case strings.Contains(v.Cond, `req.OptionalName != "" && len(req.OptionalName) > 10`):
			maxOpt = true
		case strings.Contains(v.Cond, `len(req.RequiredName) < 3`) && !strings.Contains(v.Cond, `!= ""`):
			minReq = true
		case strings.Contains(v.Cond, `len(req.RequiredName) > 10`) && !strings.Contains(v.Cond, `!= ""`):
			maxReq = true
		}
	}
	if !minOpt || !maxOpt {
		t.Errorf("optional field missing present-guarded length check: %+v", method.Validators)
	}
	if !minReq || !maxReq {
		t.Errorf("required field missing unconditional length check: %+v", method.Validators)
	}
}

// TestValidators_PatternUsesFlagAndHelperCall: a pattern constraint
// produces a Cond that invokes validate.MatchPattern and tags the
// Uses bitfield so imports.go adds the validate package.
func TestValidators_PatternUsesFlagAndHelperCall(t *testing.T) {
	req := &ir.Decl{
		Name: "Req",
		Kind: ir.DeclStruct,
		Fields: []*ir.Field{
			{JSONName: "currency", GoName: "Currency", Type: ir.Prim("string"),
				Required: true, Pattern: "^[A-Z]{3}$"},
		},
	}
	method := &ir.Method{
		Receiver:  "X",
		Name:      "Do",
		BodyParam: &ir.Param{Name: "req", Type: ir.Named("Req")},
	}
	spec := &ir.Spec{
		ErrPrefix: "b",
		Decls:     []*ir.Decl{req},
		Resources: []*ir.Resource{{Name: "X", Methods: []*ir.Method{method}}},
	}
	Validators(spec)
	var hit bool
	for _, v := range method.Validators {
		if strings.Contains(v.Cond, "validate.MatchPattern") && v.Uses.Has(ir.UsesPattern) {
			hit = true
		}
	}
	if !hit {
		t.Errorf("pattern validator missing or Uses bit unset: %+v", method.Validators)
	}
}

// TestValidators_NumericBound pins min/max on a real numeric type
// plus the json.Number coercion helper tag.
func TestValidators_NumericBound(t *testing.T) {
	mn, mx := 1.0, 100.0
	req := &ir.Decl{
		Name: "Req",
		Kind: ir.DeclStruct,
		Fields: []*ir.Field{
			{JSONName: "limit", GoName: "Limit", Type: ir.Prim("int"),
				Required: true, Minimum: &mn, Maximum: &mx},
			{JSONName: "amount", GoName: "Amount", Type: ir.Prim("json.Number", "encoding/json"),
				Required: true, Minimum: &mn},
		},
	}
	method := &ir.Method{
		Receiver:  "X",
		Name:      "Do",
		BodyParam: &ir.Param{Name: "req", Type: ir.Named("Req")},
	}
	spec := &ir.Spec{
		ErrPrefix: "b",
		Decls:     []*ir.Decl{req},
		Resources: []*ir.Resource{{Name: "X", Methods: []*ir.Method{method}}},
	}
	Validators(spec)
	var limitMin, limitMax, amtMin bool
	for _, v := range method.Validators {
		switch {
		case strings.Contains(v.Cond, "req.Limit < 1"):
			limitMin = true
		case strings.Contains(v.Cond, "req.Limit > 100"):
			limitMax = true
		case strings.Contains(v.Cond, "validate.NumberAsFloat(req.Amount)"):
			amtMin = true
			if !v.Uses.Has(ir.UsesNumberAsFloat) {
				t.Error("json.Number bound missing UsesNumberAsFloat flag")
			}
		}
	}
	if !limitMin || !limitMax {
		t.Errorf("int bounds missing: %+v", method.Validators)
	}
	if !amtMin {
		t.Errorf("json.Number bound missing: %+v", method.Validators)
	}
}

// TestValidators_MinMaxItems covers slice length constraints.
func TestValidators_MinMaxItems(t *testing.T) {
	req := &ir.Decl{
		Name: "Req",
		Kind: ir.DeclStruct,
		Fields: []*ir.Field{
			{JSONName: "tags", GoName: "Tags", Type: ir.Slice(ir.Prim("string")),
				Required: true, MinItems: 1, MaxItems: 3},
		},
	}
	method := &ir.Method{
		Receiver:  "X",
		Name:      "Do",
		BodyParam: &ir.Param{Name: "req", Type: ir.Named("Req")},
	}
	spec := &ir.Spec{
		ErrPrefix: "b",
		Decls:     []*ir.Decl{req},
		Resources: []*ir.Resource{{Name: "X", Methods: []*ir.Method{method}}},
	}
	Validators(spec)
	var minHit, maxHit bool
	for _, v := range method.Validators {
		if strings.Contains(v.Cond, "uint64(len(req.Tags)) < 1") && !strings.Contains(v.Cond, "len(req.Tags) > 0") {
			minHit = true
		}
		if strings.Contains(v.Cond, "uint64(len(req.Tags)) > 3") {
			maxHit = true
		}
	}
	if !minHit {
		t.Errorf("required minItems check missing: %+v", method.Validators)
	}
	if !maxHit {
		t.Errorf("maxItems check missing: %+v", method.Validators)
	}
}

// TestValidators_AnyOfWithNamedEnum regression-locks the declByName
// forwarding fix: a named-string field listed in an anyOf group
// used to be silently dropped because unsetCond received a nil
// declByName and couldn't resolve Named -> string-empty check.
func TestValidators_AnyOfWithNamedEnum(t *testing.T) {
	enum := &ir.Decl{
		Name:     "Provider",
		Kind:     ir.DeclEnum,
		EnumBase: ir.Prim("string"),
	}
	req := &ir.Decl{
		Name: "Req",
		Kind: ir.DeclStruct,
		Fields: []*ir.Field{
			{JSONName: "provider", GoName: "Provider", Type: ir.Named("Provider")},
			{JSONName: "raw_iban", GoName: "RawIBAN", Type: ir.Prim("string")},
		},
		AnyOfRequiredGroups: [][]string{{"provider"}, {"raw_iban"}},
	}
	method := &ir.Method{
		Receiver:  "X",
		Name:      "Do",
		BodyParam: &ir.Param{Name: "req", Type: ir.Named("Req")},
	}
	spec := &ir.Spec{
		ErrPrefix: "b",
		Decls:     []*ir.Decl{enum, req},
		Resources: []*ir.Resource{{Name: "X", Methods: []*ir.Method{method}}},
	}
	Validators(spec)
	if len(method.Validators) == 0 {
		t.Fatal("no validators emitted")
	}
	// The anyOf OR-check must list BOTH the enum and the string branch;
	// a regressed implementation would omit the enum entirely.
	cond := method.Validators[0].Cond
	if !strings.Contains(cond, "req.Provider") || !strings.Contains(cond, "req.RawIBAN") {
		t.Errorf("anyOf cond missing one branch: %q", cond)
	}
}

func TestValidators_OptionalQueryParamsNoValidators(t *testing.T) {
	params := &ir.Decl{
		Name: "ListParams",
		Kind: ir.DeclStruct,
		Fields: []*ir.Field{
			{JSONName: "limit", GoName: "Limit", Type: ir.Prim("int")},
		},
	}
	method := &ir.Method{
		Receiver:  "X",
		Name:      "List",
		OptsParam: &ir.Param{Name: "opts", Type: ir.Pointer(ir.Named("ListParams"))},
	}
	spec := &ir.Spec{
		ErrPrefix: "business",
		Decls:     []*ir.Decl{params},
		Resources: []*ir.Resource{{Name: "X", Methods: []*ir.Method{method}}},
	}
	Validators(spec)
	if len(method.Validators) != 0 {
		t.Errorf("expected no validators for all-optional opts; got %+v", method.Validators)
	}
}
