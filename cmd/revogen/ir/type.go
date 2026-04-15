// Package ir defines the intermediate representation that the revogen
// generator's build, lower, and emit stages pass between each other.
//
// The package deliberately has no knowledge of OpenAPI (no
// kin-openapi imports) and no knowledge of text rendering. It is the
// shared vocabulary that every other stage agrees on.
package ir

import (
	"sort"
	"strings"
)

// Kind discriminates Type.
type Kind int

const (
	// KindNamed references a top-level Decl by name, e.g. `Account`.
	KindNamed Kind = iota
	// KindPrim is a primitive Go type: "string", "int64", "bool",
	// "float64", "time.Time", "json.Number", "io.Reader", "[]byte",
	// or a fully-qualified external type like "core.Currency".
	// Imports names the packages the Name expression pulls in.
	KindPrim
	// KindPointer is "*"+Elem.GoExpr().
	KindPointer
	// KindSlice is "[]"+Elem.GoExpr().
	KindSlice
	// KindMap is "map["+Key.GoExpr()+"]"+Val.GoExpr().
	KindMap
	// KindRaw carries a literal Go expression with explicit imports,
	// used sparingly for shapes the other kinds can't express
	// (e.g. json.RawMessage).
	KindRaw
)

// Type is a Go type tree.
//
// Construct via the helper functions (Named, Prim, Pointer, Slice,
// Map, Raw) rather than struct literals so Imports populate
// consistently.
type Type struct {
	Kind    Kind
	Name    string
	Elem    *Type
	Key     *Type
	Val     *Type
	Imports []string
}

// Named references a top-level Decl by name.
func Named(name string) *Type { return &Type{Kind: KindNamed, Name: name} }

// Prim constructs a primitive type. Each additional argument names a
// package the Go expression pulls in.
func Prim(name string, imports ...string) *Type {
	return &Type{Kind: KindPrim, Name: name, Imports: append([]string(nil), imports...)}
}

// Pointer returns *Elem.
func Pointer(elem *Type) *Type { return &Type{Kind: KindPointer, Elem: elem} }

// Slice returns []Elem.
func Slice(elem *Type) *Type { return &Type{Kind: KindSlice, Elem: elem} }

// Map returns map[Key]Val.
func Map(key, val *Type) *Type { return &Type{Kind: KindMap, Key: key, Val: val} }

// Raw wraps a literal Go type expression. Callers declare required
// imports explicitly.
func Raw(expr string, imports ...string) *Type {
	return &Type{Kind: KindRaw, Name: expr, Imports: append([]string(nil), imports...)}
}

// GoExpr returns the Go type expression ready to emit. A nil
// receiver returns the empty string so emit sites can be terse.
func (t *Type) GoExpr() string {
	if t == nil {
		return ""
	}
	switch t.Kind {
	case KindNamed, KindPrim, KindRaw:
		return t.Name
	case KindPointer:
		return "*" + t.Elem.GoExpr()
	case KindSlice:
		return "[]" + t.Elem.GoExpr()
	case KindMap:
		return "map[" + t.Key.GoExpr() + "]" + t.Val.GoExpr()
	}
	return ""
}

// CollectImports records every package this type references into dst.
// Callers pass an initialised set; the receiver is nil-safe.
func (t *Type) CollectImports(dst map[string]struct{}) {
	if t == nil {
		return
	}
	for _, imp := range t.Imports {
		if imp != "" {
			dst[imp] = struct{}{}
		}
	}
	t.Elem.CollectImports(dst)
	t.Key.CollectImports(dst)
	t.Val.CollectImports(dst)
}

// IsPointer reports whether t is a pointer at the top level.
func (t *Type) IsPointer() bool { return t != nil && t.Kind == KindPointer }

// IsSlice reports whether t is a slice at the top level.
func (t *Type) IsSlice() bool { return t != nil && t.Kind == KindSlice }

// IsNamed reports whether t is a reference to a top-level Decl.
func (t *Type) IsNamed() bool { return t != nil && t.Kind == KindNamed }

// Deref returns t's inner type when t is a pointer, else t itself.
func (t *Type) Deref() *Type {
	if t.IsPointer() {
		return t.Elem
	}
	return t
}

// SortedImports returns the set's keys in stable order.
func SortedImports(set map[string]struct{}) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// IsStdlib classifies imports by the absence of a dot — the Go
// convention for distinguishing the standard library from
// third-party packages. Emit uses this to group imports.
func IsStdlib(pkg string) bool {
	return !strings.Contains(pkg, ".")
}
