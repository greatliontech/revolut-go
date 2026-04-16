package build

import (
	"strings"

	"github.com/greatliontech/revolut-go/cmd/revogen/ir"
)

// detectPagination classifies a method's pagination shape by
// inspecting its response and params-struct. Runs after the Decls
// and Methods are otherwise finalized so it can consult the fields
// directly rather than re-parsing schemas.
//
// Shapes:
//
//   - Cursor: response is a struct with `next_page_token` + an
//     array field; params has `page_token` (or equivalent cursor).
//   - TimeWindow: response is []Item whose items carry `created_at`
//     as time.Time; params has `to` or `created_before` typed as
//     time.Time.
//   - Limit: params has `limit` with no cursor or time-window field;
//     response is []Item with no explicit terminator.
//
// The method returns nil when no shape applies; callers leave
// Method.Pagination as nil in that case.
func (b *Builder) detectPagination(m *ir.Method) *ir.Pagination {
	if m.OptsParam == nil || m.Returns == nil {
		return nil
	}
	paramsDecl := b.declFromParamsType(m.OptsParam.Type)
	if paramsDecl == nil {
		return nil
	}
	if p := b.cursorPagination(m, paramsDecl); p != nil {
		return p
	}
	if p := b.timeWindowPagination(m, paramsDecl); p != nil {
		return p
	}
	if p := b.limitPagination(m, paramsDecl); p != nil {
		return p
	}
	return nil
}

// declFromParamsType dereferences the Method.OptsParam.Type (which
// is *<Name>Params) and returns the underlying Decl.
func (b *Builder) declFromParamsType(t *ir.Type) *ir.Decl {
	t = t.Deref()
	if t == nil || !t.IsNamed() {
		return nil
	}
	return b.declByName[t.Name]
}

func (b *Builder) cursorPagination(m *ir.Method, params *ir.Decl) *ir.Pagination {
	if m.Returns.IsSlice() || !m.Returns.IsNamed() {
		return nil
	}
	respDecl := b.declByName[m.Returns.Name]
	if respDecl == nil || respDecl.Kind != ir.DeclStruct {
		return nil
	}
	var nextField, itemsField string
	var nextType, itemType *ir.Type
	for _, f := range respDecl.Fields {
		if f.JSONName == "next_page_token" {
			nextField = f.GoName
			nextType = f.Type
			continue
		}
		if f.Type.IsSlice() && itemsField == "" {
			itemsField = f.GoName
			itemType = f.Type.Elem
		}
	}
	if nextField == "" || itemsField == "" {
		return nil
	}
	for _, f := range params.Fields {
		if f.JSONName == "page_token" {
			return &ir.Pagination{
				Shape:          ir.PaginationCursor,
				ItemType:       itemType,
				ItemsField:     itemsField,
				NextTokenField: nextField,
				NextTokenType:  nextType,
				PageTokenParam: f.GoName,
				PageTokenType:  f.Type,
			}
		}
	}
	return nil
}

func (b *Builder) timeWindowPagination(m *ir.Method, params *ir.Decl) *ir.Pagination {
	if !m.Returns.IsSlice() {
		return nil
	}
	itemType := m.Returns.Elem
	if itemType == nil || !itemType.IsNamed() {
		return nil
	}
	itemDecl := b.declByName[itemType.Name]
	if itemDecl == nil || itemDecl.Kind != ir.DeclStruct {
		return nil
	}
	// Pick the item field the iterator advances off. The `created_at`
	// convention wins when present; otherwise fall back to the sole
	// required non-pointer time-typed field on the item (Expense's
	// `expense_date` fits this — the spec's prose explicitly tells
	// callers to echo that value back as `to`).
	fromItem := ""
	var fallback []string
	for _, f := range itemDecl.Fields {
		if !b.isPlainTimeType(f.Type) {
			continue
		}
		if f.JSONName == "created_at" {
			fromItem = f.GoName
			break
		}
		if f.Required {
			fallback = append(fallback, f.GoName)
		}
	}
	if fromItem == "" && len(fallback) == 1 {
		fromItem = fallback[0]
	}
	if fromItem == "" {
		return nil
	}
	for _, f := range params.Fields {
		if (f.JSONName == "to" || f.JSONName == "created_before") && b.isPlainTimeType(f.Type) {
			return &ir.Pagination{
				Shape:           ir.PaginationTimeWindow,
				ItemType:        itemType,
				AdvanceParam:    f.GoName,
				AdvanceFromItem: fromItem,
			}
		}
	}
	return nil
}

// isPlainTimeType reports whether t is an unwrapped time.Time.
// Accepts named aliases whose alias target is time.Time — Revolut's
// specs often define per-field timestamp aliases like
// `CardInvitationCreatedAt: { type: string, format: date-time }`
// which resolve to `type CardInvitationCreatedAt = time.Time` in Go.
// Without alias unwrap, the pagination detector would reject any
// endpoint whose item timestamps are aliased (the majority in
// business), leaving paginated resources without iterators.
//
// Iterator emission still uses the aliased type at the assignment
// site (`p.CreatedBefore = resp[...].CreatedAt`), which Go type-
// checks because both sides resolve to time.Time.
func (b *Builder) isPlainTimeType(t *ir.Type) bool {
	if t == nil {
		return false
	}
	if t.Kind == ir.KindPrim && t.Name == "time.Time" {
		return true
	}
	if t.IsNamed() {
		if d, ok := b.declByName[t.Name]; ok && d.Kind == ir.DeclAlias {
			return b.isPlainTimeType(d.AliasTarget)
		}
	}
	return false
}

// limitPagination detects params with a bare `limit` field (no
// cursor, no time advance). The iterator walks pages by inferring
// "no more pages" from an empty response — the same signal as
// time-window, but without a timestamp to echo back.
func (b *Builder) limitPagination(m *ir.Method, params *ir.Decl) *ir.Pagination {
	if !m.Returns.IsSlice() {
		return nil
	}
	itemType := m.Returns.Elem
	if itemType == nil {
		return nil
	}
	hasLimit := false
	var pageField string
	var limitField string
	for _, f := range params.Fields {
		switch f.JSONName {
		case "limit", "per_page", "page_size":
			// Only int-typed limit fields support the
			// "exhaust on a short page" heuristic. A
			// json.Number-shaped limit doesn't compare against
			// `int64(len(resp))` cleanly, so we skip pagination
			// for that case rather than emit broken iterator
			// code.
			if isIntType(f.Type) {
				hasLimit = true
				limitField = f.JSONName
			}
		case "page", "offset":
			pageField = f.GoName
		}
	}
	if !hasLimit {
		return nil
	}
	return &ir.Pagination{
		Shape:         ir.PaginationLimit,
		ItemType:      itemType,
		PageSizeParam: limitField,
		PageParam:     pageField,
	}
}

// isIntType reports whether t is one of Go's integer primitives,
// optionally pointer-wrapped.
func isIntType(t *ir.Type) bool {
	if t == nil {
		return false
	}
	if t.IsPointer() {
		t = t.Elem
	}
	if t == nil || t.Kind != ir.KindPrim {
		return false
	}
	switch t.Name {
	case "int", "int32", "int64":
		return true
	}
	return false
}

// isTimeType tests whether a Type expresses a time value (plain or
// pointer-wrapped).
func isTimeType(t *ir.Type) bool {
	if t == nil {
		return false
	}
	if t.IsPointer() {
		t = t.Elem
	}
	return t != nil && strings.HasSuffix(t.GoExpr(), "time.Time")
}

// finalizePagination is the buildOperations hook that annotates
// every Method with its pagination shape. Called from FromOpenAPI
// after types and operations are registered.
func (b *Builder) finalizePagination() {
	for _, r := range b.resourceByName {
		for _, m := range r.Methods {
			if p := b.detectPagination(m); p != nil {
				m.Pagination = p
			}
		}
	}
}
