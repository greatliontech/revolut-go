package lower

import (
	"sort"
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
			if m.BodyParam == nil {
				continue
			}
			rootName := bodyDeclName(m.BodyParam.Type)
			if rootName == "" {
				continue
			}
			root := declByName[rootName]
			if root == nil || root.Kind != ir.DeclStruct {
				continue
			}
			visited := map[string]bool{rootName: true}
			m.Validators = walkValidators(root, "req", rootName, declByName, visited, spec.ErrPrefix)
		}
	}
}

func walkValidators(d *ir.Decl, exprPrefix, jsonPathPrefix string, declByName map[string]*ir.Decl, visited map[string]bool, errPrefix string) []ir.Validator {
	var out []ir.Validator
	if len(d.AnyOfRequiredGroups) > 0 {
		out = append(out, anyOfGroupValidator(d, exprPrefix, jsonPathPrefix, errPrefix)...)
	}
	for _, f := range d.Fields {
		if !f.Required {
			continue
		}
		expr := exprPrefix + "." + f.GoName
		jsonPath := jsonPathPrefix + "." + f.JSONName
		cond := unsetCond(f.Type, expr, declByName)
		if cond != "" {
			out = append(out, ir.Validator{
				Cond:    cond,
				Message: errPrefix + ": " + jsonPath + " is required",
			})
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
