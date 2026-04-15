// Package loader reads an OpenAPI spec from disk, applies
// Revolut-specific scrubs that work around published violations,
// and hands the cleaned bytes to kin-openapi. The result is an
// openapi3.T ready for the build stage.
package loader

import (
	"fmt"
	"os"

	"github.com/getkin/kin-openapi/openapi3"
)

// Load reads, scrubs, and parses the spec at path. doc.Validate is
// deliberately not called: Revolut's vendored specs have many
// non-fatal violations (prose defaults, format mismatches, ...)
// which the scrubs cannot safely normalize away. The signal that a
// spec is usable is that the generator's emitted Go code compiles.
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
	return loader.LoadFromData(cleaned)
}
