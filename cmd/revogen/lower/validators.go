package lower

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/greatliontech/revolut-go/cmd/revogen/ir"
)

// Validators populates Method.Validators by walking each method's
// request body type and emitting a check per required field. The
// walker recurses through:
//
//   - value-type nested structs (req.Foo.Bar == "")
//   - pointer-type nested structs, with the parent's nil check
//     prepended so a nil pointer short-circuits subordinate
//     validators (req.Foo != nil && req.Foo.Bar == "")
//
// Conditional-required anyOf groups (Decl.AnyOfRequiredGroups)
// emit a single "at least one of: (a, b) OR (c)" check on the
// containing struct, with the field names spelled out.
//
// Cycle-guarded via a visited set keyed on Decl name.
func Validators(spec *ir.Spec) {
	declByName := indexDecls(spec.Decls)
	for _, r := range spec.Resources {
		for _, m := range r.Methods {
			if m.BodyParam != nil {
				if rootName := bodyDeclName(m.BodyParam.Type); rootName != "" {
					if root := declByName[rootName]; root != nil && root.Kind == ir.DeclStruct {
						visited := map[string]bool{rootName: true}
						m.Validators = append(m.Validators,
							walkValidators(root, "req", rootName, declByName, visited, spec.ErrPrefix)...)
					}
				}
			}
			if m.OptsParam != nil {
				m.Validators = append(m.Validators,
					optsValidators(m.OptsParam.Type, declByName, spec.ErrPrefix)...)
			}
		}
	}
}

// optsValidators returns the required-field checks for a method's
// opts (query params) struct. When the struct has any required
// field, opts itself is required — we emit a single `opts == nil`
// check first (which short-circuits via early-return in the caller)
// so subsequent field checks can safely dereference opts without a
// redundant nil guard.
func optsValidators(optsType *ir.Type, declByName map[string]*ir.Decl, errPrefix string) []ir.Validator {
	rootName := bodyDeclName(optsType) // shares the pointer-peeling logic
	if rootName == "" {
		return nil
	}
	root := declByName[rootName]
	if root == nil || root.Kind != ir.DeclStruct {
		return nil
	}
	hasRequired := false
	for _, f := range root.Fields {
		if f.Required {
			hasRequired = true
			break
		}
	}
	if !hasRequired {
		return nil
	}
	out := []ir.Validator{{
		Cond:    "opts == nil",
		Message: errPrefix + ": " + rootName + " is required",
	}}
	visited := map[string]bool{rootName: true}
	out = append(out, walkValidators(root, "opts", rootName, declByName, visited, errPrefix)...)
	return out
}

func walkValidators(d *ir.Decl, exprPrefix, jsonPathPrefix string, declByName map[string]*ir.Decl, visited map[string]bool, errPrefix string) []ir.Validator {
	var out []ir.Validator
	if len(d.AnyOfRequiredGroups) > 0 {
		out = append(out, anyOfGroupValidator(d, exprPrefix, jsonPathPrefix, errPrefix)...)
	}
	for _, f := range d.Fields {
		expr := exprPrefix + "." + f.GoName
		jsonPath := jsonPathPrefix + "." + f.JSONName
		if f.Required {
			if cond := unsetCond(f.Type, expr, declByName); cond != "" {
				out = append(out, ir.Validator{
					Cond:    cond,
					Message: errPrefix + ": " + jsonPath + " is required",
				})
			}
		}
		// Value-range / length / pattern checks fire on any set
		// value. Optional fields contribute a "set" guard so
		// unset-equals-unset doesn't falsely trigger.
		out = append(out, constraintValidators(f, expr, jsonPath, errPrefix, declByName)...)
		if !f.Required {
			continue
		}
		nestedName, isPointer := nestedStructName(f.Type)
		if nestedName == "" || visited[nestedName] {
			continue
		}
		nested := declByName[nestedName]
		if nested == nil || nested.Kind != ir.DeclStruct {
			continue
		}
		visited[nestedName] = true
		inner := walkValidators(nested, expr, jsonPath, declByName, visited, errPrefix)
		delete(visited, nestedName)
		if isPointer {
			for _, v := range inner {
				out = append(out, ir.Validator{
					Cond:    expr + " != nil && " + v.Cond,
					Message: v.Message,
				})
			}
		} else {
			out = append(out, inner...)
		}
	}
	return out
}

// constraintValidators emits one ir.Validator per spec-declared
// value-range, length, or pattern constraint on f. Optional fields
// get a "present" guard so unset isn't treated as invalid.
func constraintValidators(f *ir.Field, expr, jsonPath, errPrefix string, declByName map[string]*ir.Decl) []ir.Validator {
	if f == nil || f.Type == nil {
		return nil
	}
	var out []ir.Validator
	present := presentGuard(f, expr)
	// String length + pattern. Only apply to plain string / json.Number /
	// string-backed named types whose wire shape is a string.
	switch {
	case isStringLike(f.Type) && !isNamedMapOrStruct(f.Type, declByName):
		base := stringAccess(f.Type, expr)
		if f.MinLength > 0 {
			out = append(out, wrapGuard(ir.Validator{
				Cond:    "len(" + base + ") < " + strconv.FormatUint(f.MinLength, 10),
				Message: fmt.Sprintf("%s: %s must be at least %d characters", errPrefix, jsonPath, f.MinLength),
			}, present))
		}
		if f.MaxLength > 0 {
			out = append(out, wrapGuard(ir.Validator{
				Cond:    "len(" + base + ") > " + strconv.FormatUint(f.MaxLength, 10),
				Message: fmt.Sprintf("%s: %s must be at most %d characters", errPrefix, jsonPath, f.MaxLength),
			}, present))
		}
		if f.Pattern != "" {
			out = append(out, wrapGuard(ir.Validator{
				Cond:    "!mustMatchPattern(" + strconv.Quote(f.Pattern) + ", " + base + ")",
				Message: fmt.Sprintf("%s: %s must match pattern %s", errPrefix, jsonPath, f.Pattern),
			}, present))
		}
	case isNumericLike(f.Type):
		base := numericAccess(f.Type, expr)
		if f.Minimum != nil {
			op := "<"
			if f.ExclusiveMin {
				op = "<="
			}
			out = append(out, wrapGuard(ir.Validator{
				Cond:    base + " " + op + " " + formatNumericLiteral(*f.Minimum, f.Type),
				Message: fmt.Sprintf("%s: %s must be %s %s", errPrefix, jsonPath, boundWord("minimum", f.ExclusiveMin), formatNumericLiteral(*f.Minimum, f.Type)),
			}, numericPresentGuard(f, expr)))
		}
		if f.Maximum != nil {
			op := ">"
			if f.ExclusiveMax {
				op = ">="
			}
			out = append(out, wrapGuard(ir.Validator{
				Cond:    base + " " + op + " " + formatNumericLiteral(*f.Maximum, f.Type),
				Message: fmt.Sprintf("%s: %s must be %s %s", errPrefix, jsonPath, boundWord("maximum", f.ExclusiveMax), formatNumericLiteral(*f.Maximum, f.Type)),
			}, numericPresentGuard(f, expr)))
		}
	case f.Type.IsSlice():
		if f.MinItems > 0 {
			out = append(out, ir.Validator{
				Cond:    "len(" + expr + ") > 0 && uint64(len(" + expr + ")) < " + strconv.FormatUint(f.MinItems, 10),
				Message: fmt.Sprintf("%s: %s must contain at least %d items", errPrefix, jsonPath, f.MinItems),
			})
			if f.Required {
				out = append(out, ir.Validator{
					Cond:    "uint64(len(" + expr + ")) < " + strconv.FormatUint(f.MinItems, 10),
					Message: fmt.Sprintf("%s: %s must contain at least %d items", errPrefix, jsonPath, f.MinItems),
				})
			}
		}
		if f.MaxItems > 0 {
			out = append(out, ir.Validator{
				Cond:    "uint64(len(" + expr + ")) > " + strconv.FormatUint(f.MaxItems, 10),
				Message: fmt.Sprintf("%s: %s must contain at most %d items", errPrefix, jsonPath, f.MaxItems),
			})
		}
	}
	return out
}

// wrapGuard prefixes v.Cond with a "field is set" guard so an
// optional field at its zero value doesn't fail the value check.
// When guard is empty the validator is unchanged (required fields
// fire unconditionally).
func wrapGuard(v ir.Validator, guard string) ir.Validator {
	if guard == "" {
		return v
	}
	v.Cond = guard + " && " + v.Cond
	return v
}

// presentGuard returns the "set" condition for an optional field,
// matching the inverse of unsetCond. Required fields return "".
func presentGuard(f *ir.Field, expr string) string {
	if f.Required {
		return ""
	}
	if f.Type.IsPointer() {
		return expr + " != nil"
	}
	if f.Type.IsSlice() {
		return "len(" + expr + ") > 0"
	}
	if isStringLike(f.Type) {
		return expr + ` != ""`
	}
	if isNumericLike(f.Type) {
		return expr + ` != 0`
	}
	return ""
}

// numericPresentGuard returns a guard that only fires when a
// numeric optional field is "set". For json.Number (a string
// underneath), that means the string is non-empty; for real
// numeric types, zero is treated as unset — a caller that
// explicitly wants 0 can still submit it, the server applies the
// bound check.
func numericPresentGuard(f *ir.Field, expr string) string {
	if f.Required {
		return ""
	}
	if f.Type.IsPointer() {
		return expr + " != nil"
	}
	if f.Type.Kind == ir.KindPrim && f.Type.Name == "json.Number" {
		return expr + ` != ""`
	}
	return expr + " != 0"
}

// stringAccess yields the Go expression for the string payload of a
// string-like field. Pointer-wrapped values are dereferenced; named
// string-backed types are cast back to string so len() works.
func stringAccess(t *ir.Type, expr string) string {
	if t.IsPointer() {
		return "*" + expr
	}
	if t.IsNamed() || t.Kind == ir.KindPrim && t.Name == "json.Number" || t.Kind == ir.KindPrim && t.Name == "core.Currency" {
		return "string(" + expr + ")"
	}
	return expr
}

// numericAccess yields the Go expression for the numeric payload.
// json.Number requires a conversion helper (parseNumberForValidation)
// injected at the resource level so the bound check works.
func numericAccess(t *ir.Type, expr string) string {
	if t.IsPointer() {
		return "*" + expr
	}
	if t.Kind == ir.KindPrim && t.Name == "json.Number" {
		return "parseNumberForValidation(" + expr + ")"
	}
	return expr
}

func isStringLike(t *ir.Type) bool {
	if t == nil {
		return false
	}
	if t.IsPointer() {
		return isStringLike(t.Elem)
	}
	if t.Kind == ir.KindPrim {
		switch t.Name {
		case "string", "json.Number", "core.Currency":
			return true
		}
		return false
	}
	return t.IsNamed()
}

// isNamedMapOrStruct peels named types back to their underlying
// Decl kind. Structs and map-typed aliases aren't string-like even
// though t.IsNamed() is true.
func isNamedMapOrStruct(t *ir.Type, declByName map[string]*ir.Decl) bool {
	if t == nil || declByName == nil {
		return false
	}
	for t.IsPointer() {
		t = t.Elem
	}
	if !t.IsNamed() {
		return false
	}
	d := declByName[t.Name]
	if d == nil {
		return false
	}
	switch d.Kind {
	case ir.DeclStruct:
		return true
	case ir.DeclAlias:
		// Alias-to-map → not string-like. Alias-to-string → still OK.
		if d.AliasTarget != nil {
			if d.AliasTarget.Kind == ir.KindMap {
				return true
			}
		}
	}
	return false
}

func isNumericLike(t *ir.Type) bool {
	if t == nil {
		return false
	}
	if t.IsPointer() {
		return isNumericLike(t.Elem)
	}
	if t.Kind == ir.KindPrim {
		switch t.Name {
		case "int", "int32", "int64", "float32", "float64", "json.Number":
			return true
		}
	}
	return false
}

func formatNumericLiteral(v float64, t *ir.Type) string {
	if t == nil {
		return strconv.FormatFloat(v, 'g', -1, 64)
	}
	target := t
	for target.IsPointer() {
		target = target.Elem
	}
	if target.Kind == ir.KindPrim {
		switch target.Name {
		case "int", "int32", "int64":
			return strconv.FormatInt(int64(v), 10)
		case "float32", "float64":
			return strconv.FormatFloat(v, 'g', -1, 64)
		case "json.Number":
			return strconv.FormatFloat(v, 'g', -1, 64)
		}
	}
	return strconv.FormatFloat(v, 'g', -1, 64)
}

func boundWord(base string, exclusive bool) string {
	if exclusive {
		return "strictly " + base[:len(base)-4]
	}
	return "at " + base
}

// anyOfGroupValidator produces "at least one of (group A) OR
// (group B)" checks. The error message lists the JSON field names
// of every group so the failure is actionable.
func anyOfGroupValidator(d *ir.Decl, exprPrefix, jsonPath, errPrefix string) []ir.Validator {
	jsonByName := map[string]*ir.Field{}
	for _, f := range d.Fields {
		jsonByName[f.JSONName] = f
	}
	var groupExprs []string
	var groupLabels []string
	for _, group := range d.AnyOfRequiredGroups {
		var conds []string
		var labels []string
		for _, jsonName := range group {
			f := jsonByName[jsonName]
			if f == nil {
				continue
			}
			cond := unsetCond(f.Type, exprPrefix+"."+f.GoName, nil)
			if cond == "" {
				continue
			}
			conds = append(conds, "!("+cond+")")
			labels = append(labels, jsonName)
		}
		if len(conds) == 0 {
			continue
		}
		sort.Strings(labels)
		groupExprs = append(groupExprs, "("+strings.Join(conds, " && ")+")")
		groupLabels = append(groupLabels, "("+strings.Join(labels, " AND ")+")")
	}
	if len(groupExprs) == 0 {
		return nil
	}
	return []ir.Validator{{
		Cond:    "!(" + strings.Join(groupExprs, " || ") + ")",
		Message: errPrefix + ": " + jsonPath + " requires one of: " + strings.Join(groupLabels, " OR "),
	}}
}

// nestedStructName extracts the Decl name a field type points at
// when it's a struct (named or pointer-to-named). Returns ("",
// false) for non-struct shapes (slices, maps, primitives).
func nestedStructName(t *ir.Type) (string, bool) {
	if t == nil {
		return "", false
	}
	if t.IsPointer() {
		if t.Elem != nil && t.Elem.IsNamed() {
			return t.Elem.Name, true
		}
		return "", false
	}
	if t.IsNamed() {
		return t.Name, false
	}
	return "", false
}

func bodyDeclName(t *ir.Type) string {
	for t != nil && t.IsPointer() {
		t = t.Elem
	}
	if t == nil || !t.IsNamed() {
		return ""
	}
	return t.Name
}

// unsetCond returns a Go boolean expression that is true when expr
// of type t is unset. Mirrors the table from the design plan:
//
//   string / json.Number / core.Currency / string-enum / string-alias
//                                                        → expr == ""
//   time.Time                                            → expr.IsZero()
//   int / int32 / int64                                  → expr == 0
//   bool                                                 → "" (skip)
//   pointer / interface / union (named interface)        → expr == nil
//   slice                                                → len(expr) == 0
//   map                                                  → len(expr) == 0
//   nested struct (value)                                → "" (recurse)
//
// Returns "" when no meaningful unset check applies; the walker
// skips emitting a validator in that case.
func unsetCond(t *ir.Type, expr string, declByName map[string]*ir.Decl) string {
	if t == nil {
		return ""
	}
	if t.IsPointer() {
		return expr + " == nil"
	}
	if t.IsSlice() {
		return "len(" + expr + ") == 0"
	}
	if t.Kind == ir.KindMap {
		return "len(" + expr + ") == 0"
	}
	if t.Kind == ir.KindRaw {
		return expr + " == nil"
	}
	if t.Kind == ir.KindPrim {
		switch t.Name {
		case "string", "json.Number", "core.Currency":
			return expr + ` == ""`
		case "time.Time":
			return expr + ".IsZero()"
		case "int", "int32", "int64":
			return expr + " == 0"
		case "bool":
			return ""
		case "any":
			return expr + " == nil"
		}
		return ""
	}
	if t.IsNamed() && declByName != nil {
		decl := declByName[t.Name]
		if decl == nil {
			return ""
		}
		switch decl.Kind {
		case ir.DeclEnum:
			if decl.EnumBase != nil && decl.EnumBase.Name == "string" {
				return expr + ` == ""`
			}
			return expr + " == 0"
		case ir.DeclAlias:
			return unsetCond(decl.AliasTarget, expr, declByName)
		case ir.DeclInterface:
			return expr + " == nil"
		case ir.DeclStruct:
			return ""
		}
	}
	return ""
}
