// Package loader reads an OpenAPI spec from disk, applies
// Revolut-specific scrubs that work around published violations,
// and hands the cleaned bytes to kin-openapi. The result is an
// openapi3.T ready for the build stage.
package loader

import (
	"fmt"
	"os"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
)

// Load reads, scrubs, and parses the spec at path.
//
// openapi3.T.Validate is deliberately not called: Revolut's vendored
// specs have many non-fatal violations (prose defaults, format
// mismatches, ...) which the scrubs cannot safely normalize away.
// But some violations do silently degrade into broken generator
// output — undefined $refs produce nil Value pointers that walk the
// pipeline without ever failing. Load does a post-parse scan for
// those and returns a real error, so regenerating against a
// typo'd spec fails the operator instead of emitting a half-wired
// package.
func Load(path string) (*openapi3.T, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	cleaned, err := Scrub(raw)
	if err != nil {
		return nil, fmt.Errorf("preprocess yaml: %w", err)
	}
	loader := openapi3.NewLoader()
	loader.IsExternalRefsAllowed = false
	doc, err := loader.LoadFromData(cleaned)
	if err != nil {
		return nil, err
	}
	if missing := findUnresolvedRefs(doc); len(missing) > 0 {
		return nil, fmt.Errorf("unresolved $ref(s) in spec: %s", strings.Join(missing, ", "))
	}
	return doc, nil
}

// findUnresolvedRefs walks the doc's component schemas and every
// operation's parameters / request bodies / responses looking for
// $refs whose Value is nil — i.e. the ref text is non-empty but the
// loader couldn't resolve it. Returns a deduplicated list of the
// bad ref strings.
func findUnresolvedRefs(doc *openapi3.T) []string {
	seen := map[string]bool{}
	add := func(ref string) {
		if ref == "" || seen[ref] {
			return
		}
		seen[ref] = true
	}
	if doc.Components != nil {
		for _, s := range doc.Components.Schemas {
			if s != nil && s.Ref != "" && s.Value == nil {
				add(s.Ref)
			}
		}
	}
	if doc.Paths != nil {
		for _, item := range doc.Paths.Map() {
			if item == nil {
				continue
			}
			for _, op := range item.Operations() {
				if op == nil {
					continue
				}
				for _, p := range op.Parameters {
					if p != nil && p.Ref != "" && p.Value == nil {
						add(p.Ref)
					}
				}
				if op.RequestBody != nil && op.RequestBody.Ref != "" && op.RequestBody.Value == nil {
					add(op.RequestBody.Ref)
				}
				if op.Responses != nil {
					for _, r := range op.Responses.Map() {
						if r != nil && r.Ref != "" && r.Value == nil {
							add(r.Ref)
						}
					}
				}
			}
		}
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	return out
}
