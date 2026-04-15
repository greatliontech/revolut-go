package emit

import (
	"github.com/greatliontech/revolut-go/cmd/revogen/ir"
)

// writeClientFile emits gen_client.go: the per-package Client
// struct with one field per resource and a New constructor that
// wires them together.
func writeClientFile(spec *ir.Spec, imports []string) string {
	w := newFileWriter(spec.Package, imports)
	w.header()

	w.write("// Client is the generated Revolut API client. Resource fields\n")
	w.write("// group endpoints by their tag prefix.\n")
	w.write("type Client struct {\n")
	w.write("\ttransport *transport.Transport\n\n")
	for _, r := range spec.Resources {
		w.printf("\t// %s groups the related endpoints.\n", r.Name)
		w.printf("\t%s *%s\n", r.Name, r.Name)
	}
	w.write("}\n\n")

	w.write("// New wraps an HTTP transport in a Client.\n")
	w.write("func New(t *transport.Transport) *Client {\n")
	w.write("\tc := &Client{transport: t}\n")
	for _, r := range spec.Resources {
		w.printf("\tc.%s = &%s{t: t}\n", r.Name, r.Name)
	}
	w.write("\treturn c\n}\n")
	return w.buf.String()
}
