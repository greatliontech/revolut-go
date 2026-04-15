package ir

import (
	"reflect"
	"sort"
	"testing"
)

func TestGoExpr(t *testing.T) {
	cases := []struct {
		name string
		t    *Type
		want string
	}{
		{"nil", nil, ""},
		{"prim string", Prim("string"), "string"},
		{"prim time", Prim("time.Time", "time"), "time.Time"},
		{"named", Named("Account"), "Account"},
		{"pointer", Pointer(Named("Foo")), "*Foo"},
		{"slice of named", Slice(Named("Bar")), "[]Bar"},
		{"slice of pointer", Slice(Pointer(Named("Baz"))), "[]*Baz"},
		{"pointer to slice", Pointer(Slice(Prim("string"))), "*[]string"},
		{"map prim->named", Map(Prim("string"), Named("LabelGroup")), "map[string]LabelGroup"},
		{"map prim->pointer", Map(Prim("string"), Pointer(Named("V"))), "map[string]*V"},
		{"raw json.RawMessage", Raw("json.RawMessage", "encoding/json"), "json.RawMessage"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.t.GoExpr(); got != c.want {
				t.Errorf("got %q; want %q", got, c.want)
			}
		})
	}
}

func TestCollectImports(t *testing.T) {
	// Compound shape: map[string]*[]time.Time — the time import must
	// propagate through Map.Val, Pointer, Slice.
	typ := Map(Prim("string"), Pointer(Slice(Prim("time.Time", "time"))))
	set := map[string]struct{}{}
	typ.CollectImports(set)
	got := SortedImports(set)
	want := []string{"time"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v; want %v", got, want)
	}
}

func TestCollectImports_Merges(t *testing.T) {
	// Two subtrees contributing different packages plus a duplicate.
	typ := Slice(Map(
		Prim("string"),
		Raw("json.RawMessage", "encoding/json"),
	))
	also := Pointer(Prim("time.Time", "time"))

	set := map[string]struct{}{}
	typ.CollectImports(set)
	also.CollectImports(set)

	got := SortedImports(set)
	want := []string{"encoding/json", "time"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v; want %v", got, want)
	}
}

func TestCollectImports_NilSafe(t *testing.T) {
	var nilT *Type
	set := map[string]struct{}{}
	nilT.CollectImports(set) // must not panic
	if len(set) != 0 {
		t.Errorf("nil type produced imports: %v", set)
	}
}

func TestHelpers(t *testing.T) {
	p := Pointer(Named("X"))
	if !p.IsPointer() {
		t.Error("IsPointer false")
	}
	if p.Deref().GoExpr() != "X" {
		t.Errorf("Deref: %q", p.Deref().GoExpr())
	}
	n := Named("X")
	if n.Deref().GoExpr() != "X" {
		t.Errorf("non-pointer Deref changed type: %q", n.Deref().GoExpr())
	}
}

func TestIsStdlib(t *testing.T) {
	cases := []struct {
		pkg  string
		want bool
	}{
		{"time", true},
		{"encoding/json", true},
		{"io", true},
		{"github.com/foo/bar", false},
		{"golang.org/x/sync", false},
	}
	for _, c := range cases {
		if got := IsStdlib(c.pkg); got != c.want {
			t.Errorf("IsStdlib(%q) = %v; want %v", c.pkg, got, c.want)
		}
	}
}

func TestSortedImports(t *testing.T) {
	set := map[string]struct{}{"time": {}, "encoding/json": {}, "io": {}}
	got := SortedImports(set)
	want := []string{"encoding/json", "io", "time"}
	if !sort.StringsAreSorted(got) {
		t.Errorf("not sorted: %v", got)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v; want %v", got, want)
	}
}
