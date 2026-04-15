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
	writeHostAliases(w, spec)
	return w.buf.String()
}

// writeHostAliases emits a package-level SandboxHostAliases map the
// root-level revolut constructors consult when
// WithEnvironment(EnvironmentSandbox) is active. Keys are the
// production hosts the spec embeds verbatim in the path argument
// of server-override endpoints; values are their sandbox
// equivalents. Always emitted so callers can reference the symbol
// even when a particular spec has no overrides.
func writeHostAliases(w *fileWriter, spec *ir.Spec) {
	w.write("\n// SandboxHostAliases maps every production host the spec embeds in\n")
	w.write("// per-operation server-override endpoints to its sandbox counterpart.\n")
	w.write("// The revolut package applies this map to the transport when\n")
	w.write("// WithEnvironment(EnvironmentSandbox) is in effect so absolute-URL\n")
	w.write("// requests targeting production hosts are rewritten to sandbox.\n")
	if len(spec.HostAliases) == 0 {
		w.write("var SandboxHostAliases = map[string]string{}\n")
		return
	}
	w.write("var SandboxHostAliases = map[string]string{\n")
	for _, a := range spec.HostAliases {
		w.printf("\t%q: %q,\n", a.Production, a.Sandbox)
	}
	w.write("}\n")
}
