package build

import (
	"sort"

	"github.com/getkin/kin-openapi/openapi3"

	"github.com/greatliontech/revolut-go/cmd/revogen/ir"
	"github.com/greatliontech/revolut-go/cmd/revogen/names"
)

// buildCallbacks walks operation-level `callbacks:` blocks and emits
// a typed decoder per distinct callback name. The generator does
// not synthesize an HTTP server — users wire their own endpoint and
// call Decode<CallbackName>(body) to get a typed payload.
//
// Detection: each callback entry maps a runtime expression (e.g.
// "{$request.body#/url}") to a PathItem. We pick the first POST
// operation in that PathItem's item and take its JSON request body
// schema as the payload type.
func (b *Builder) buildCallbacks() {
	if b.doc.Paths == nil {
		return
	}
	seen := map[string]bool{}
	for _, pathName := range sortedPaths(b.doc) {
		item := b.doc.Paths.Value(pathName)
		if item == nil {
			continue
		}
		for _, op := range item.Operations() {
			if op == nil {
				continue
			}
			for _, cbName := range sortedCallbackNames(op.Callbacks) {
				if seen[cbName] {
					continue
				}
				cb := b.callbackFor(op.Callbacks[cbName])
				if cb == nil {
					continue
				}
				cb.Name = "Decode" + names.TypeName(cbName)
				b.callbacks = append(b.callbacks, cb)
				seen[cbName] = true
			}
		}
	}
}

func (b *Builder) callbackFor(ref *openapi3.CallbackRef) *ir.Callback {
	if ref == nil || ref.Value == nil {
		return nil
	}
	for _, ep := range ref.Value.Map() {
		if ep == nil {
			continue
		}
		for _, op := range ep.Operations() {
			if op == nil || op.RequestBody == nil || op.RequestBody.Value == nil {
				continue
			}
			mt := op.RequestBody.Value.Content["application/json"]
			if mt == nil || mt.Schema == nil {
				continue
			}
			payload := b.resolveType(mt.Schema, Context{Parent: "Callback", Field: "payload"})
			if payload == nil {
				continue
			}
			return &ir.Callback{Payload: payload}
		}
	}
	return nil
}

func sortedCallbackNames(m openapi3.Callbacks) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
