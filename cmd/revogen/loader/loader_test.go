package loader

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLoad_UnresolvedRefFails pins the post-parse scan: a $ref
// pointing at a component that doesn't exist is caught at load time
// instead of silently producing degraded IR that emits broken Go.
func TestLoad_UnresolvedRefFails(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.yaml")
	spec := `
openapi: 3.0.0
info: {title: x, version: "1"}
paths:
  /x:
    get:
      operationId: getX
      responses:
        '200':
          description: ok
          content:
            application/json:
              schema:
                $ref: '#/components/schemas/Missing'
components:
  schemas:
    Present:
      type: object
      properties:
        id: {type: string}
`
	if err := os.WriteFile(path, []byte(spec), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path)
	if err == nil {
		t.Fatal("want unresolved-$ref error")
	}
	if !strings.Contains(err.Error(), "unresolved") && !strings.Contains(err.Error(), "Missing") {
		t.Errorf("error should cite the bad ref; got %v", err)
	}
}

// TestLoad_CleanSpecPasses: a well-formed minimal spec loads without
// raising the unresolved-$ref error.
func TestLoad_CleanSpecPasses(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "clean.yaml")
	spec := `
openapi: 3.0.0
info: {title: x, version: "1"}
paths:
  /x:
    get:
      operationId: getX
      responses:
        '200':
          description: ok
components:
  schemas:
    Empty:
      type: object
`
	if err := os.WriteFile(path, []byte(spec), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err != nil {
		t.Fatalf("Load: %v", err)
	}
}
