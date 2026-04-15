package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestEndToEnd_MinimalSpec exercises loadSpec (via scrub + kin-openapi),
// buildSpec, and emit on a minimal in-memory OpenAPI document. emit
// runs gofmt (go/format.Source) on every file before writing, so a
// success here means the emitted source is syntactically valid and
// import lists are consistent.
func TestEndToEnd_MinimalSpec(t *testing.T) {
	pkg := "minitest"
	doc := loadSpecFromString(t, minimalSpec)
	spec, err := buildSpec(doc, buildConfig{
		PackageName: pkg,
		ErrPrefix:   pkg,
		DocsBase:    "https://example.com/docs/",
	})
	if err != nil {
		t.Fatalf("buildSpec: %v", err)
	}
	pkgDir := filepath.Join(t.TempDir(), pkg)
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := emit(spec, pkgDir); err != nil {
		t.Fatalf("emit: %v", err)
	}

	// Sanity-check a few expected files and symbols.
	types, err := os.ReadFile(filepath.Join(pkgDir, "gen_types.go"))
	if err != nil {
		t.Fatalf("read gen_types.go: %v", err)
	}
	for _, want := range []string{
		"type Account struct",
		"type AccountState string", // inline enum promoted to named type
		"type TransferRequest struct",
	} {
		if !strings.Contains(string(types), want) {
			t.Errorf("gen_types.go missing %q", want)
		}
	}
	transfers, err := os.ReadFile(filepath.Join(pkgDir, "gen_transfers.go"))
	if err != nil {
		t.Fatalf("read gen_transfers.go: %v", err)
	}
	if !strings.Contains(string(transfers), "minitest: TransferRequest.request_id is required") {
		t.Errorf("validator missing in gen_transfers.go")
	}
}
