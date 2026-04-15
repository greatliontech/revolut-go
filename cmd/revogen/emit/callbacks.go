package emit

import (
	"github.com/greatliontech/revolut-go/cmd/revogen/ir"
)

// writeCallbackHelpers appends the typed Decode* functions for
// every callback the spec declares. Emitted into gen_types.go —
// the user wires their own HTTP handler and calls Decode<Name>(body)
// to deserialize the wire payload.
func writeCallbackHelpers(w *fileWriter, spec *ir.Spec, callbacks []*ir.Callback) {
	for _, cb := range callbacks {
		writeCallback(w, spec, cb)
	}
}

// writeCallback emits the Decode* helper. Three shapes are handled:
//
//   - named struct payload: json.Decoder into *T, return *T.
//   - named interface (union) payload: route through the union's
//     wire-tagged or probe decoder so the caller gets a concrete
//     variant out of the typed interface.
//   - anything else (slice/primitive): decode into the raw value,
//     return it by value.
//
// The union path is the interesting one — without it the decoder
// would try json.Decode into a bare interface, which always fails.
func writeCallback(w *fileWriter, spec *ir.Spec, cb *ir.Callback) {
	w.printf("// %s decodes the JSON body of an incoming %s callback into a typed payload.\n",
		cb.Name, trimDecodePrefix(cb.Name))
	if len(cb.Doc) > 0 {
		w.write("//\n")
		w.docLines(cb.Doc)
	}
	payloadExpr, returnExpr, dispatch := callbackShapes(spec, cb.Payload)
	w.printf("func %s(body io.Reader) (%s, error) {\n", cb.Name, returnExpr)
	switch dispatch {
	case dispatchUnion:
		// Union decoders take json.RawMessage; buffer the body so
		// the generated decode<Name> can inspect the tag or probe
		// the required fields.
		w.write("\traw, err := io.ReadAll(body)\n")
		w.write("\tif err != nil { return nil, err }\n")
		w.printf("\treturn decode%s(raw)\n", payloadExpr)
	case dispatchValue:
		w.printf("\tvar out %s\n", payloadExpr)
		w.write("\tif err := json.NewDecoder(body).Decode(&out); err != nil { return nil, err }\n")
		w.write("\treturn out, nil\n")
	default: // dispatchPointer
		w.printf("\tvar out %s\n", payloadExpr)
		w.write("\tif err := json.NewDecoder(body).Decode(&out); err != nil { return nil, err }\n")
		w.write("\treturn &out, nil\n")
	}
	w.write("}\n\n")
}

type callbackDispatch int

const (
	dispatchPointer callbackDispatch = iota // named struct / alias
	dispatchUnion                           // named interface (union)
	dispatchValue                           // slice / primitive / map
)

// callbackShapes picks the Go payload type, return type, and
// decoder strategy for a callback payload.
func callbackShapes(spec *ir.Spec, t *ir.Type) (payload, ret string, d callbackDispatch) {
	if t == nil {
		return "any", "any", dispatchValue
	}
	inner := t
	if inner.IsPointer() {
		inner = inner.Elem
	}
	if inner.IsNamed() {
		name := inner.Name
		decl := findDecl(spec, name)
		if decl != nil && decl.Kind == ir.DeclInterface {
			return name, name, dispatchUnion
		}
		return name, "*" + name, dispatchPointer
	}
	expr := inner.GoExpr()
	return expr, expr, dispatchValue
}

func findDecl(spec *ir.Spec, name string) *ir.Decl {
	for _, d := range spec.Decls {
		if d != nil && d.Name == name {
			return d
		}
	}
	return nil
}

func trimDecodePrefix(name string) string {
	const prefix = "Decode"
	if len(name) > len(prefix) && name[:len(prefix)] == prefix {
		return name[len(prefix):]
	}
	return name
}
