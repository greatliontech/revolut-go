package main

import (
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// updateGolden rewrites the expected-signature files instead of
// diffing. Run with `go test ./cmd/revogen -update` after an
// intentional API change. The CI path leaves the flag off, so any
// unintentional signature shift fails the test.
var updateGolden = flag.Bool("update", false, "rewrite api golden files")

// generatedPackages names every committed client package the
// generator produces. A new one added here requires a matching
// testdata/golden/<name>.txt after `-update`.
var generatedPackages = []struct {
	name string
	dir  string
}{
	{"business", "../../business"},
	{"cryptoramp", "../../cryptoramp"},
	{"merchant", "../../merchant"},
	{"openbanking", "../../openbanking"},
	{"revolutx", "../../revolutx"},
}

// TestAPIGolden pins the public surface of every generated
// package. It reads the gen_*.go files, extracts one line per
// exported type / func / enum constant, sorts, and compares
// against testdata/golden/<pkg>.txt. Run with -update to refresh
// the golden after an intentional change.
//
// Scope: the snapshot is signatures only — type names + field
// lists + method sigs + enum const values. Doc comments and
// function bodies are excluded so formatting tweaks don't churn
// the golden.
func TestAPIGolden(t *testing.T) {
	for _, p := range generatedPackages {
		t.Run(p.name, func(t *testing.T) {
			got, err := extractAPI(p.dir)
			if err != nil {
				t.Fatalf("extractAPI: %v", err)
			}
			goldenPath := filepath.Join("testdata", "golden", p.name+".txt")
			if *updateGolden {
				if err := os.MkdirAll(filepath.Dir(goldenPath), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(goldenPath, []byte(got), 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(goldenPath)
			if err != nil {
				t.Fatalf("read golden: %v (run `go test ./cmd/revogen -update` to create)", err)
			}
			if got != string(want) {
				t.Fatalf("API surface drifted for %s.\nRun `go test ./cmd/revogen -update` after reviewing the change.\n\n--- first diff ---\n%s",
					p.name, firstDiff(string(want), got))
			}
		})
	}
}

// extractAPI parses every gen_*.go file in dir and returns a
// sorted, newline-joined signature list. Each line is one of:
//
//	type   <name>   <kind> <body>
//	func   <recv?>  <name> <params> <return>
//	const  <name>   <type?> = <value>
//
// Receivers and parameters are rendered via go/printer so
// formatting is stable across Go versions.
func extractAPI(dir string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	var files []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasPrefix(e.Name(), "gen_") && strings.HasSuffix(e.Name(), ".go") {
			files = append(files, filepath.Join(dir, e.Name()))
		}
	}
	sort.Strings(files)

	fset := token.NewFileSet()
	var lines []string
	for _, path := range files {
		f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			return "", fmt.Errorf("%s: %w", path, err)
		}
		for _, decl := range f.Decls {
			switch d := decl.(type) {
			case *ast.GenDecl:
				lines = append(lines, genDeclLines(fset, d)...)
			case *ast.FuncDecl:
				if d.Name == nil || !d.Name.IsExported() {
					continue
				}
				lines = append(lines, formatFuncDecl(fset, d))
			}
		}
	}
	sort.Strings(lines)
	// Trailing newline so the golden file has a clean EOF.
	return strings.Join(lines, "\n") + "\n", nil
}

// genDeclLines expands an ast.GenDecl (type/const/var) into one
// signature line per exported spec. Non-exported specs are
// skipped. Vars are included — exported package-level vars are
// part of the public API surface (e.g. SandboxHostAliases) and
// regressions that rename, retype, or drop them would otherwise
// slip past the golden.
func genDeclLines(fset *token.FileSet, d *ast.GenDecl) []string {
	var out []string
	for _, spec := range d.Specs {
		switch s := spec.(type) {
		case *ast.TypeSpec:
			if !s.Name.IsExported() {
				continue
			}
			out = append(out, formatTypeSpec(fset, s))
		case *ast.ValueSpec:
			if d.Tok != token.CONST && d.Tok != token.VAR {
				continue
			}
			for i, name := range s.Names {
				if !name.IsExported() {
					continue
				}
				out = append(out, formatValueSpec(fset, d.Tok, name, s, i))
			}
		}
	}
	return out
}

func formatTypeSpec(fset *token.FileSet, s *ast.TypeSpec) string {
	var body strings.Builder
	body.WriteString("type ")
	body.WriteString(s.Name.Name)
	if s.TypeParams != nil {
		body.WriteString(printNode(fset, s.TypeParams))
	}
	body.WriteString(" ")
	body.WriteString(printNode(fset, s.Type))
	return normaliseWhitespace(body.String())
}

func formatFuncDecl(fset *token.FileSet, d *ast.FuncDecl) string {
	var b strings.Builder
	b.WriteString("func ")
	if d.Recv != nil && len(d.Recv.List) > 0 {
		b.WriteString("(")
		b.WriteString(printNode(fset, d.Recv.List[0].Type))
		b.WriteString(") ")
	}
	b.WriteString(d.Name.Name)
	b.WriteString(printNode(fset, d.Type.Params))
	if d.Type.Results != nil {
		b.WriteString(" ")
		b.WriteString(printNode(fset, d.Type.Results))
	}
	return normaliseWhitespace(b.String())
}

func formatValueSpec(fset *token.FileSet, tok token.Token, name *ast.Ident, s *ast.ValueSpec, idx int) string {
	var b strings.Builder
	if tok == token.VAR {
		b.WriteString("var ")
	} else {
		b.WriteString("const ")
	}
	b.WriteString(name.Name)
	if s.Type != nil {
		b.WriteString(" ")
		b.WriteString(printNode(fset, s.Type))
	}
	if idx < len(s.Values) {
		b.WriteString(" = ")
		b.WriteString(printNode(fset, s.Values[idx]))
	}
	return normaliseWhitespace(b.String())
}

func printNode(fset *token.FileSet, n ast.Node) string {
	if n == nil {
		return ""
	}
	var b strings.Builder
	cfg := printer.Config{Mode: printer.UseSpaces | printer.TabIndent, Tabwidth: 4}
	_ = cfg.Fprint(&b, fset, n)
	return b.String()
}

// normaliseWhitespace collapses multi-line struct / interface
// bodies into a single line so the golden stays diff-friendly.
// Comments inside bodies are stripped; field order is preserved.
func normaliseWhitespace(s string) string {
	// Drop `//` and `/* */` comments to stop doc-text churn.
	s = stripComments(s)
	fields := strings.Fields(s)
	return strings.Join(fields, " ")
}

func stripComments(s string) string {
	var out strings.Builder
	for i := 0; i < len(s); {
		if i+1 < len(s) && s[i] == '/' && s[i+1] == '/' {
			for i < len(s) && s[i] != '\n' {
				i++
			}
			continue
		}
		if i+1 < len(s) && s[i] == '/' && s[i+1] == '*' {
			i += 2
			for i+1 < len(s) && !(s[i] == '*' && s[i+1] == '/') {
				i++
			}
			if i+1 < len(s) {
				i += 2
			}
			continue
		}
		out.WriteByte(s[i])
		i++
	}
	return out.String()
}

func firstDiff(want, got string) string {
	wl := strings.Split(want, "\n")
	gl := strings.Split(got, "\n")
	n := len(wl)
	if len(gl) < n {
		n = len(gl)
	}
	for i := 0; i < n; i++ {
		if wl[i] != gl[i] {
			return fmt.Sprintf("line %d:\n- %s\n+ %s", i+1, wl[i], gl[i])
		}
	}
	if len(wl) != len(gl) {
		return fmt.Sprintf("line count: want=%d got=%d", len(wl), len(gl))
	}
	return "(no line-level diff — check trailing whitespace)"
}
