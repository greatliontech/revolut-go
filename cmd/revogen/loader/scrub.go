package loader

import (
	"strconv"

	"gopkg.in/yaml.v3"
)

// Scrub patches Revolut-specific OpenAPI violations on the raw YAML
// bytes and returns the cleaned document. The scrubs are:
//
//   - Non-numeric `maximum`/`minimum` — Revolut overloads these for
//     date-range prose ("maximum: now + 7 days"), which kin-openapi
//     rejects. Dropped; the generator ignores min/max anyway.
//   - Sibling fields on $ref schemas — OpenAPI 3.0 forbids them and
//     kin-openapi refuses the document. The $ref wins.
//   - Parameter-only fields (explode/style/allowReserved/
//     allowEmptyValue) inside schema objects — kin-openapi rejects.
//
// Scrubs inside `example`/`examples` subtrees are suppressed: those
// payloads are user data and may legitimately contain $ref-keyed or
// parameter-looking shapes.
func Scrub(data []byte) ([]byte, error) {
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return nil, err
	}
	walkWithContext(&root, false, func(n *yaml.Node, insideExample bool) {
		if n.Kind != yaml.MappingNode {
			return
		}
		stripNonNumericBounds(n)
		if insideExample {
			return
		}
		stripRefSiblings(n)
		stripUnknownSchemaFields(n)
	})
	return yaml.Marshal(&root)
}

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

var parameterOnlyFields = map[string]bool{
	"explode":         true,
	"style":           true,
	"allowReserved":   true,
	"allowEmptyValue": true,
}

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
	if _, err := strconv.ParseFloat(n.Value, 64); err == nil {
		return true
	}
	return false
}

// walkWithContext traverses the YAML tree depth-first and carries an
// "inside example subtree" flag so schema-level scrubs can suppress
// themselves inside user-authored example payloads.
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
