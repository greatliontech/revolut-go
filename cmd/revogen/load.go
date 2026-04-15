package main

import (
	"fmt"
	"os"
	"strconv"

	"github.com/getkin/kin-openapi/openapi3"
	"gopkg.in/yaml.v3"
)

// loadSpec reads a spec from disk, scrubs known Revolut-specific
// violations, then parses and validates via kin-openapi.
//
// The scrubbing pass fixes:
//
//   - Non-numeric maximum/minimum: Revolut overloads these in the
//     Business spec for date-range annotations ("maximum: now + 7 days"),
//     which kin-openapi rejects because the OpenAPI schema requires
//     numeric bounds. Stripping them is safe — we ignore maximum/minimum
//     entirely during code generation.
//   - Sibling fields on $ref schemas: in OpenAPI 3.0 a schema object
//     with $ref cannot have sibling fields, but Revolut's Business spec
//     has several (e.g. a description next to a $ref). We keep the
//     $ref and drop the siblings.
func loadSpec(path string) (*openapi3.T, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	cleaned, err := scrubInvalidBounds(raw)
	if err != nil {
		return nil, fmt.Errorf("preprocess yaml: %w", err)
	}

	loader := openapi3.NewLoader()
	loader.IsExternalRefsAllowed = false
	doc, err := loader.LoadFromData(cleaned)
	if err != nil {
		return nil, err
	}
	// We deliberately skip doc.Validate. Revolut's published specs have
	// several non-fatal violations (default values expressed as prose,
	// format mismatches, ...) which would block loading with no benefit
	// for code generation. Our emitted Go code compiling is the real
	// validation signal we care about.
	return doc, nil
}

// scrubInvalidBounds patches Revolut-specific spec violations. Operates
// on a parsed YAML node tree, then re-serialises.
//
// Each scrub pass runs with a context flag that tells it whether the
// current mapping is inside an `example(s)` subtree. Example payloads
// are arbitrary user data: stripping "$ref siblings" or
// "parameter-only fields" from them risks mangling the documented
// wire shape. Schema-level scrubs therefore only fire outside
// example subtrees.
func scrubInvalidBounds(data []byte) ([]byte, error) {
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return nil, err
	}
	walkWithContext(&root, false, func(n *yaml.Node, insideExample bool) {
		if n.Kind != yaml.MappingNode {
			return
		}
		// Non-numeric maximum/minimum is a schema-only concern, safe
		// to strip globally (these keys don't appear in examples or
		// parameter-adjacent objects).
		stripNonNumericBounds(n)
		if insideExample {
			return
		}
		stripRefSiblings(n)
		stripUnknownSchemaFields(n)
	})
	return yaml.Marshal(&root)
}

// stripNonNumericBounds removes any "maximum:" or "minimum:" entry whose
// scalar value is not parseable as a number.
func stripNonNumericBounds(n *yaml.Node) {
	out := n.Content[:0]
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i], n.Content[i+1]
		if (k.Value == "maximum" || k.Value == "minimum") && v.Kind == yaml.ScalarNode && !isNumericScalar(v) {
			continue
		}
		out = append(out, k, v)
	}
	n.Content = out
}

// parameterOnlyFields are keys that only belong on Parameter objects but
// that Revolut sometimes pastes into Schema objects. kin-openapi rejects
// the whole document when it sees them inside a schema, so we drop them.
var parameterOnlyFields = map[string]bool{
	"explode":        true,
	"style":          true,
	"allowReserved":  true,
	"allowEmptyValue": true,
}

// stripUnknownSchemaFields removes parameter-only keys from any mapping
// that looks like a schema (has 'type', 'items', 'properties', or $ref).
func stripUnknownSchemaFields(n *yaml.Node) {
	looksLikeSchema := false
	for i := 0; i+1 < len(n.Content); i += 2 {
		switch n.Content[i].Value {
		case "type", "items", "properties", "$ref", "allOf", "oneOf", "anyOf":
			looksLikeSchema = true
		}
	}
	if !looksLikeSchema {
		return
	}
	out := n.Content[:0]
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i], n.Content[i+1]
		if parameterOnlyFields[k.Value] {
			continue
		}
		out = append(out, k, v)
	}
	n.Content = out
}

// stripRefSiblings drops every key other than $ref when a $ref is present
// in the mapping. OpenAPI 3.0 forbids sibling fields on a $ref schema.
func stripRefSiblings(n *yaml.Node) {
	hasRef := false
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == "$ref" {
			hasRef = true
			break
		}
	}
	if !hasRef {
		return
	}
	out := n.Content[:0]
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i], n.Content[i+1]
		if k.Value == "$ref" {
			out = append(out, k, v)
		}
	}
	n.Content = out
}

func isNumericScalar(n *yaml.Node) bool {
	if n.Kind != yaml.ScalarNode {
		return false
	}
	if n.Tag == "!!int" || n.Tag == "!!float" {
		return true
	}
	// yaml.v3 leaves Tag empty for auto-detected scalars; verify by parsing.
	if _, err := strconv.ParseFloat(n.Value, 64); err == nil {
		return true
	}
	return false
}

// walk invokes fn on every node in the tree, depth-first.
func walk(n *yaml.Node, fn func(*yaml.Node)) {
	if n == nil {
		return
	}
	fn(n)
	for _, child := range n.Content {
		walk(child, fn)
	}
}

// walkWithContext is walk with an "inside example subtree" flag
// propagated through children. The flag turns on whenever the current
// mapping has a key named `example` or `examples` and the walker
// descends into that key's value; it stays on for the whole subtree.
func walkWithContext(n *yaml.Node, insideExample bool, fn func(*yaml.Node, bool)) {
	if n == nil {
		return
	}
	fn(n, insideExample)
	switch n.Kind {
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			k, v := n.Content[i], n.Content[i+1]
			nextInside := insideExample
			if !nextInside && (k.Value == "example" || k.Value == "examples") {
				nextInside = true
			}
			walkWithContext(v, nextInside, fn)
		}
	default:
		for _, child := range n.Content {
			walkWithContext(child, insideExample, fn)
		}
	}
}
