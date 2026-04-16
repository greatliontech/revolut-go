package build

import (
	"sort"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"

	"github.com/greatliontech/revolut-go/cmd/revogen/ir"
	"github.com/greatliontech/revolut-go/cmd/revogen/names"
)

// knownSuccessHeader enumerates every response header the generator
// knows how to surface on a 2xx response. Add a case here only
// after thinking through where the value lands on the generated
// Go API — new headers should not be silently promoted to method
// signatures.
//
// Headers that belong on the error path (Retry-After) are parsed
// unconditionally by the transport and don't appear here.
var knownSuccessHeaders = map[string]ir.MetadataField{
	"x-fapi-interaction-id": {GoName: "InteractionID", WireName: "x-fapi-interaction-id", Doc: "FAPI correlation ID, echoed by the server on every 2xx response."},
	"x-jws-signature":       {GoName: "JWSSignature", WireName: "x-jws-signature", Doc: "Detached JWS over the response body. Populated when the spec declares x-jws-signature on this endpoint."},
}

// errorPathHeaders lists the response headers the transport already
// handles on 4xx/5xx responses (Retry-After → APIError.RetryAfter).
// Listed here so classifyResponseHeaders can recognise them and
// not treat them as unknown.
var errorPathHeaders = map[string]bool{
	"retry-after": true,
}

// classifyResponseHeaders walks an operation's declared response
// headers on 2xx and error responses, returning the metadata fields
// to attach to the Method. Any header absent from the allowlist
// produces an error so the maintainer has to triage it explicitly
// (add to knownSuccessHeaders, errorPathHeaders, or route it
// somewhere new).
//
// The returned slice is sorted by GoName for deterministic ordering
// regardless of map iteration.
func classifyResponseHeaders(op *openapi3.Operation, opID string) ([]ir.MetadataField, error) {
	if op == nil || op.Responses == nil {
		return nil, nil
	}
	seen := map[string]ir.MetadataField{}
	for _, codeStr := range sortedResponseCodes(op.Responses) {
		resp := op.Responses.Value(codeStr)
		if resp == nil || resp.Value == nil {
			continue
		}
		isSuccess := strings.HasPrefix(codeStr, "2")
		for name, hdrRef := range resp.Value.Headers {
			if hdrRef == nil {
				continue
			}
			lower := strings.ToLower(name)
			if errorPathHeaders[lower] {
				continue // already handled by transport
			}
			field, ok := knownSuccessHeaders[lower]
			if !ok {
				// Fallback: synthesize a MetadataField from the
				// spec-declared header name so the SDK surfaces every
				// header without the maintainer having to enumerate
				// them. The curated allowlist above still wins when
				// present — it supplies nicer godoc copy.
				field = ir.MetadataField{
					GoName:   synthHeaderFieldName(name),
					WireName: name,
					Doc:      firstLineOrEmpty(hdrRef.Value),
				}
			}
			if !isSuccess {
				// Allowlisted headers that happen to appear on an
				// error response are ignored — the generator surfaces
				// them only on 2xx. Flag mixed-use via explicit
				// routing if that ever becomes necessary.
				continue
			}
			seen[field.GoName] = field
		}
	}
	if len(seen) == 0 {
		return nil, nil
	}
	out := make([]ir.MetadataField, 0, len(seen))
	for _, f := range seen {
		out = append(out, f)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].GoName < out[j].GoName })
	return out, nil
}

// operationEmitsSignedVariant reports whether the spec declares
// x-jws-signature on any 2xx response for op. Used to toggle the
// `<Name>Signed` raw-bytes method on the affected resource.
func operationEmitsSignedVariant(op *openapi3.Operation) bool {
	if op == nil || op.Responses == nil {
		return false
	}
	for _, codeStr := range sortedResponseCodes(op.Responses) {
		if !strings.HasPrefix(codeStr, "2") {
			continue
		}
		resp := op.Responses.Value(codeStr)
		if resp == nil || resp.Value == nil {
			continue
		}
		for name := range resp.Value.Headers {
			if strings.EqualFold(name, "x-jws-signature") {
				return true
			}
		}
	}
	return false
}

// synthHeaderFieldName converts a wire header name into a Go field
// identifier. "x-request-id" → "XRequestID" after initialism
// normalisation.
func synthHeaderFieldName(wire string) string {
	return names.FieldName(wire)
}

// firstLineOrEmpty extracts the first line of the header's
// description, or "" when absent, so the synthesized field's Doc
// isn't a multi-paragraph blob. openapi3.Header embeds Parameter;
// the description lives on the inner Parameter.
func firstLineOrEmpty(h *openapi3.Header) string {
	if h == nil {
		return ""
	}
	return firstLine(h.Parameter.Description)
}

// sortedResponseCodes returns the response codes of r in stable
// ASCII order. openapi3.Responses.Map() is map-backed, so direct
// iteration is non-deterministic.
func sortedResponseCodes(r *openapi3.Responses) []string {
	m := r.Map()
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
