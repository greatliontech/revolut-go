package emit

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/greatliontech/revolut-go/cmd/revogen/ir"
	"github.com/greatliontech/revolut-go/cmd/revogen/lower"
	"github.com/greatliontech/revolut-go/cmd/revogen/names"
)

// Spec writes every Go file the generator produces for one IR
// Spec into outDir. Three categories of file:
//
//   - gen_types.go: every Decl, plus union helpers and callback
//     decoders.
//   - gen_<resource>.go: one per Resource (lowercased name),
//     containing the resource struct and its methods.
//   - gen_client.go: the Client struct and New constructor.
//
// Per-file imports are computed by lower.FileImports so the
// emitter never has to track them by string-substring scanning.
func Spec(spec *ir.Spec, outDir string) error {
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}
	imports := lower.FileImports(spec)

	if err := writeFile(filepath.Join(outDir, "gen_types.go"), writeTypesAndCallbacks(spec, imports["gen_types.go"])); err != nil {
		return fmt.Errorf("emit types: %w", err)
	}
	for _, r := range spec.Resources {
		fname := "gen_" + names.LowerASCII(r.Name) + ".go"
		src := writeResourceFile(spec, r, imports[fname])
		if err := writeFile(filepath.Join(outDir, fname), src); err != nil {
			return fmt.Errorf("emit %s: %w", r.Name, err)
		}
	}
	if err := writeFile(filepath.Join(outDir, "gen_client.go"), writeClientFile(spec, imports["gen_client.go"])); err != nil {
		return fmt.Errorf("emit client: %w", err)
	}
	return nil
}

// writeTypesAndCallbacks renders gen_types.go: types first, then
// any callback decoders. Sharing the file keeps callback helpers
// adjacent to the types they decode into without a fourth file
// kind.
func writeTypesAndCallbacks(spec *ir.Spec, imports []string) string {
	src := writeTypesFile(spec, imports)
	if len(spec.Callbacks) == 0 {
		return src
	}
	w := newFileWriter(spec.Package, nil)
	w.buf.WriteString(src)
	writeCallbackHelpers(w, spec, spec.Callbacks)
	return w.buf.String()
}

func writeFile(path, src string) error {
	w := newFileWriter("", nil)
	w.buf.WriteString(src)
	return w.flush(path)
}
