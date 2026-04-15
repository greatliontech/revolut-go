package loader

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestScrub(t *testing.T) {
	cases := []struct {
		name     string
		in       string
		mustDrop []string
		mustKeep []string
	}{
		{
			name: "non-numeric maximum stripped",
			in: `schemas:
  X:
    type: string
    maximum: "now + 7 days"
    minimum: 5
`,
			mustDrop: []string{"now + 7 days"},
			mustKeep: []string{"minimum: 5"},
		},
		{
			name: "ref siblings dropped outside examples",
			in: `schemas:
  X:
    description: keep me
    $ref: '#/components/schemas/Y'
`,
			mustDrop: []string{"keep me"},
			mustKeep: []string{"$ref:"},
		},
		{
			name: "ref siblings preserved inside examples",
			in: `examples:
  foo:
    summary: my summary
    $ref: '#/components/examples/bar'
`,
			mustKeep: []string{"summary: my summary", "$ref:"},
		},
		{
			name: "explode dropped from schema",
			in: `schemas:
  X:
    type: array
    explode: false
    items:
      type: string
`,
			mustDrop: []string{"explode:"},
			mustKeep: []string{"type: array", "items:"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, err := Scrub([]byte(c.in))
			if err != nil {
				t.Fatalf("Scrub: %v", err)
			}
			s := string(out)
			for _, want := range c.mustKeep {
				if !strings.Contains(s, want) {
					t.Errorf("dropped unexpectedly: %q\n in:\n%s", want, s)
				}
			}
			for _, unwant := range c.mustDrop {
				if strings.Contains(s, unwant) {
					t.Errorf("failed to drop: %q\n in:\n%s", unwant, s)
				}
			}
		})
	}
}

// Sanity: yaml.v3 is reachable from the test compile.
var _ = yaml.Node{}
