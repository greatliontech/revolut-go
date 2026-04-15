// revogen generates the Revolut Go SDK resource files from the vendored
// OpenAPI specifications.
//
// Pipeline:
//
//  1. Load and $ref-resolve the spec via kin-openapi.
//  2. Build a normalized IR: operations grouped into resources by tag,
//     schemas flattened to a deterministic list of Go-friendly type
//     definitions.
//  3. Render Go source via text/template and go/format, writing one
//     gen_<resource>.go file per resource plus a shared gen_types.go
//     for types referenced by more than one resource.
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
)

type flags struct {
	spec      string
	pkg       string
	out       string
	resources stringList
	verbose   bool
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
	flag.BoolVar(&f.verbose, "v", false, "verbose output")
	flag.Parse()
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
	doc, err := loadSpec(f.spec)
	if err != nil {
		return fmt.Errorf("load spec: %w", err)
	}
	if f.verbose {
		summarize(doc)
	}
	spec, err := buildSpec(doc, f.pkg, f.resources)
	if err != nil {
		return fmt.Errorf("build IR: %w", err)
	}
	if f.verbose {
		dumpIR(spec)
	}
	if err := emit(spec, f.out); err != nil {
		return fmt.Errorf("emit: %w", err)
	}
	return nil
}

func dumpIR(spec *Spec) {
	fmt.Fprintf(os.Stderr, "\nnormalized IR:\n")
	fmt.Fprintf(os.Stderr, "  %d resources, %d named types\n", len(spec.Resources), len(spec.Types))
	for _, r := range spec.Resources {
		fmt.Fprintf(os.Stderr, "  resource %s (%d ops)\n", r.GoName, len(r.Ops))
		for _, op := range r.Ops {
			fmt.Fprintf(os.Stderr, "    %s %-7s %-40s req=%-20s resp=%s\n",
				op.GoMethod, op.HTTPMethod, op.PathTemplate, op.RequestType, op.ResponseType)
		}
	}
}

// summarize prints operation counts grouped by tag to stderr. Used to
// verify the loader sees the spec the way the generator will.
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
