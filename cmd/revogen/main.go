// revogen generates the Revolut Go SDK resource files from the
// vendored OpenAPI specifications.
//
// Pipeline:
//
//  1. loader/  — read YAML, scrub Revolut-specific violations, hand
//     the cleaned bytes to kin-openapi.
//  2. build/   — single-entry resolveType walks the openapi3.T and
//     produces an ir.Spec with every Decl, Resource, Method,
//     Callback, and the picked error type.
//  3. lower/   — IR-on-IR passes: union variant wiring,
//     readonly-field stripping for shared schemas, validators
//     emission, name-collision resolution, per-file imports.
//  4. emit/    — mechanical text rendering of the final IR into
//     gen_*.go source files, gofmt-validated.
//
// Run via `task gen` (see Taskfile.yml).
package main

import (
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"

	"github.com/greatliontech/revolut-go/cmd/revogen/build"
	"github.com/greatliontech/revolut-go/cmd/revogen/emit"
	"github.com/greatliontech/revolut-go/cmd/revogen/ir"
	"github.com/greatliontech/revolut-go/cmd/revogen/loader"
	"github.com/greatliontech/revolut-go/cmd/revogen/lower"
)

type flags struct {
	spec              string
	pkg               string
	out               string
	resources         stringList
	includeDeprecated bool
	verbose           bool
	errPrefix         string
	docsBase          string
}

type stringList []string

func (s *stringList) String() string     { return strings.Join(*s, ",") }
func (s *stringList) Set(v string) error { *s = append(*s, strings.Split(v, ",")...); return nil }

func parseFlags() flags {
	var f flags
	flag.StringVar(&f.spec, "spec", "specs/business.yaml", "path to the OpenAPI spec")
	flag.StringVar(&f.pkg, "package", "business", "Go package name for generated files")
	flag.StringVar(&f.out, "out", "business", "directory to emit gen_*.go files into")
	flag.Var(&f.resources, "resource", "resource tag to emit (repeatable; omit to emit all)")
	flag.BoolVar(&f.includeDeprecated, "include-deprecated", false, "emit operations/resources the spec marks as deprecated")
	flag.BoolVar(&f.verbose, "v", false, "verbose output")
	flag.StringVar(&f.errPrefix, "err-prefix", "", "prefix for validation error messages (defaults to the package name)")
	flag.StringVar(&f.docsBase, "docs-base", "", "base URL for generated godoc links (defaults to https://developer.revolut.com/docs/<package>/)")
	flag.Parse()
	if f.errPrefix == "" {
		f.errPrefix = f.pkg
	}
	if f.docsBase == "" {
		f.docsBase = "https://developer.revolut.com/docs/" + f.pkg + "/"
	}
	return f
}

func main() {
	f := parseFlags()
	if err := run(f); err != nil {
		fmt.Fprintln(os.Stderr, "revogen:", err)
		os.Exit(1)
	}
}

func run(f flags) error {
	doc, err := loader.Load(f.spec)
	if err != nil {
		return fmt.Errorf("load spec: %w", err)
	}
	if f.verbose {
		summarize(doc)
	}
	spec, err := build.FromOpenAPI(doc, build.Config{
		Package:           f.pkg,
		ResourceAllow:     f.resources,
		IncludeDeprecated: f.includeDeprecated,
		ErrPrefix:         f.errPrefix,
		DocsBase:          f.docsBase,
	})
	if err != nil {
		return fmt.Errorf("build IR: %w", err)
	}
	lower.RunAll(spec)
	if f.verbose {
		dumpIR(spec)
	}
	if err := emit.Spec(spec, f.out); err != nil {
		return fmt.Errorf("emit: %w", err)
	}
	return nil
}

func dumpIR(spec *ir.Spec) {
	fmt.Fprintf(os.Stderr, "\nnormalized IR:\n")
	fmt.Fprintf(os.Stderr, "  %d resources, %d decls, %d callbacks\n",
		len(spec.Resources), len(spec.Decls), len(spec.Callbacks))
	for _, r := range spec.Resources {
		fmt.Fprintf(os.Stderr, "  resource %s (%d methods)\n", r.Name, len(r.Methods))
		for _, m := range r.Methods {
			fmt.Fprintf(os.Stderr, "    %s\n", m.Name)
		}
	}
}

// summarize prints operation counts grouped by tag to stderr. Used
// to verify the loader sees the spec the way the generator will.
func summarize(doc *openapi3.T) {
	byTag := map[string]int{}
	total := 0
	for _, item := range doc.Paths.Map() {
		for _, op := range item.Operations() {
			tag := "(untagged)"
			if len(op.Tags) > 0 {
				tag = op.Tags[0]
			}
			byTag[tag]++
			total++
		}
	}
	tags := make([]string, 0, len(byTag))
	for t := range byTag {
		tags = append(tags, t)
	}
	sort.Strings(tags)
	fmt.Fprintf(os.Stderr, "loaded %s: %d operations across %d tags\n", doc.Info.Title, total, len(tags))
	for _, t := range tags {
		fmt.Fprintf(os.Stderr, "  %-40s %d ops\n", t, byTag[t])
	}
}
