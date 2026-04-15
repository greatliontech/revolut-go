package emit

import (
	"github.com/greatliontech/revolut-go/cmd/revogen/ir"
)

// writeCallbackHelpers appends the typed Decode* functions for
// every callback the spec declares. Emitted into gen_types.go (or
// gen_client.go if simpler) — the user wires their own HTTP
// handler and calls Decode<Name>(body) to deserialize the wire
// payload.
func writeCallbackHelpers(w *fileWriter, callbacks []*ir.Callback) {
	for _, cb := range callbacks {
		writeCallback(w, cb)
	}
}

func writeCallback(w *fileWriter, cb *ir.Callback) {
	w.printf("// %s decodes the JSON body of an incoming %s callback into a typed payload.\n",
		cb.Name, trimDecodePrefix(cb.Name))
	if len(cb.Doc) > 0 {
		w.write("//\n")
		w.docLines(cb.Doc)
	}
	w.printf("func %s(body io.Reader) (%s, error) {\n", cb.Name, returnExprForCallback(cb.Payload))
	w.write("\tdec := json.NewDecoder(body)\n")
	w.printf("\tvar out %s\n", payloadTypeName(cb.Payload))
	w.write("\tif err := dec.Decode(&out); err != nil { return nil, err }\n")
	w.write("\treturn &out, nil\n}\n\n")
}

func returnExprForCallback(t *ir.Type) string {
	if t == nil {
		return "any"
	}
	return "*" + payloadTypeName(t)
}

func payloadTypeName(t *ir.Type) string {
	if t == nil {
		return "any"
	}
	if t.IsPointer() {
		return t.Elem.GoExpr()
	}
	return t.GoExpr()
}

func trimDecodePrefix(name string) string {
	const prefix = "Decode"
	if len(name) > len(prefix) && name[:len(prefix)] == prefix {
		return name[len(prefix):]
	}
	return name
}
