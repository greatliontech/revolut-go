package build

import (
	"testing"

	"github.com/greatliontech/revolut-go/cmd/revogen/ir"
)

// TestTimeWindowPagination_NonCreatedAtItemField pins the fallback
// rule: when an item doesn't carry `created_at` but has exactly one
// required non-pointer time-typed field, the detector picks that as
// the advance field. Expenses exercises this — the item's time
// field is `expense_date` and the spec's prose pairs it with `to`.
func TestTimeWindowPagination_NonCreatedAtItemField(t *testing.T) {
	b := newTestBuilder()
	item := &ir.Decl{
		Name: "Expense",
		Kind: ir.DeclStruct,
		Fields: []*ir.Field{
			{JSONName: "id", GoName: "ID", Type: ir.Prim("string"), Required: true},
			{JSONName: "expense_date", GoName: "ExpenseDate", Type: ir.Prim("time.Time", "time"), Required: true},
			// Optional time fields must not mislead the detector.
			{JSONName: "completed_at", GoName: "CompletedAt", Type: ir.Pointer(ir.Prim("time.Time", "time"))},
		},
	}
	params := &ir.Decl{
		Name: "GetExpensesParams",
		Kind: ir.DeclStruct,
		Fields: []*ir.Field{
			{JSONName: "from", GoName: "From", Type: ir.Prim("time.Time", "time")},
			{JSONName: "to", GoName: "To", Type: ir.Prim("time.Time", "time")},
		},
	}
	b.declByName[item.Name] = item
	b.declByName[params.Name] = params
	m := &ir.Method{
		Returns:   ir.Slice(ir.Named("Expense")),
		OptsParam: &ir.Param{Name: "opts", Type: ir.Pointer(ir.Named("GetExpensesParams"))},
	}
	p := b.detectPagination(m)
	if p == nil {
		t.Fatal("expected time-window pagination to be detected")
	}
	if p.Shape != ir.PaginationTimeWindow {
		t.Errorf("shape: %v", p.Shape)
	}
	if p.AdvanceParam != "To" || p.AdvanceFromItem != "ExpenseDate" {
		t.Errorf("advance: param=%q from=%q", p.AdvanceParam, p.AdvanceFromItem)
	}
}

// TestTimeWindowPagination_AmbiguousItemFields: if the item has
// multiple required non-pointer time-typed fields and no
// `created_at`, the detector bails rather than guessing — a wrong
// advance field is worse than no iterator.
func TestTimeWindowPagination_AmbiguousItemFields(t *testing.T) {
	b := newTestBuilder()
	item := &ir.Decl{
		Name: "Ambiguous",
		Kind: ir.DeclStruct,
		Fields: []*ir.Field{
			{JSONName: "opened_at", GoName: "OpenedAt", Type: ir.Prim("time.Time", "time"), Required: true},
			{JSONName: "closed_at", GoName: "ClosedAt", Type: ir.Prim("time.Time", "time"), Required: true},
		},
	}
	params := &ir.Decl{
		Name: "Params",
		Kind: ir.DeclStruct,
		Fields: []*ir.Field{
			{JSONName: "to", GoName: "To", Type: ir.Prim("time.Time", "time")},
		},
	}
	b.declByName[item.Name] = item
	b.declByName[params.Name] = params
	m := &ir.Method{
		Returns:   ir.Slice(ir.Named("Ambiguous")),
		OptsParam: &ir.Param{Name: "opts", Type: ir.Pointer(ir.Named("Params"))},
	}
	if p := b.detectPagination(m); p != nil {
		t.Errorf("expected no detection, got %+v", p)
	}
}

// TestTimeWindowPagination_CreatedAtStillWins: when both
// `created_at` and another required time field are present, the
// `created_at` convention wins and the alternate is ignored.
func TestTimeWindowPagination_CreatedAtStillWins(t *testing.T) {
	b := newTestBuilder()
	item := &ir.Decl{
		Name: "Transaction",
		Kind: ir.DeclStruct,
		Fields: []*ir.Field{
			{JSONName: "created_at", GoName: "CreatedAt", Type: ir.Prim("time.Time", "time"), Required: true},
			{JSONName: "settled_at", GoName: "SettledAt", Type: ir.Prim("time.Time", "time"), Required: true},
		},
	}
	params := &ir.Decl{
		Name: "Params",
		Kind: ir.DeclStruct,
		Fields: []*ir.Field{
			{JSONName: "to", GoName: "To", Type: ir.Prim("time.Time", "time")},
		},
	}
	b.declByName[item.Name] = item
	b.declByName[params.Name] = params
	m := &ir.Method{
		Returns:   ir.Slice(ir.Named("Transaction")),
		OptsParam: &ir.Param{Name: "opts", Type: ir.Pointer(ir.Named("Params"))},
	}
	p := b.detectPagination(m)
	if p == nil || p.AdvanceFromItem != "CreatedAt" {
		t.Errorf("expected CreatedAt to win; got %+v", p)
	}
}
