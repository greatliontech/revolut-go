package build

import (
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
)

// TestClassifyResponseHeaders_KnownSuccessHeader: the two OB
// allowlisted success headers produce metadata fields that the
// method will surface via ResponseMetadata. Retry-After stays
// off the method — it's a transport-level concern.
func TestClassifyResponseHeaders_KnownSuccessHeader(t *testing.T) {
	op := &openapi3.Operation{OperationID: "getX"}
	op.Responses = openapi3.NewResponses()
	op.Responses.Set("200", &openapi3.ResponseRef{Value: &openapi3.Response{
		Headers: openapi3.Headers{
			"x-fapi-interaction-id": {Value: &openapi3.Header{}},
			"Retry-After":           {Value: &openapi3.Header{}},
			"x-jws-signature":       {Value: &openapi3.Header{}},
		},
	}})
	fields, err := classifyResponseHeaders(op, "getX")
	if err != nil {
		t.Fatalf("classify: %v", err)
	}
	names := map[string]bool{}
	for _, f := range fields {
		names[f.GoName] = true
	}
	if !names["InteractionID"] || !names["JWSSignature"] {
		t.Errorf("missing expected fields: %+v", fields)
	}
	if len(fields) != 2 {
		t.Errorf("want 2 (Retry-After stays on error path); got %d: %+v", len(fields), fields)
	}
}

// TestClassifyResponseHeaders_UnknownHeaderFailsFast pins the
// fail-fast allowlist: an undeclared response header blocks the
// whole build, with an error pointing the maintainer at the
// classifier to triage.
func TestClassifyResponseHeaders_UnknownHeaderFailsFast(t *testing.T) {
	op := &openapi3.Operation{OperationID: "getY"}
	op.Responses = openapi3.NewResponses()
	op.Responses.Set("200", &openapi3.ResponseRef{Value: &openapi3.Response{
		Headers: openapi3.Headers{
			"X-Brand-New-Header": {Value: &openapi3.Header{}},
		},
	}})
	_, err := classifyResponseHeaders(op, "getY")
	if err == nil {
		t.Fatal("want fail-fast on unknown header")
	}
	if !strings.Contains(err.Error(), "X-Brand-New-Header") || !strings.Contains(err.Error(), "getY") {
		t.Errorf("error should cite header + op; got %v", err)
	}
	if !strings.Contains(err.Error(), "responses.go") {
		t.Errorf("error should direct maintainer to classifier: %v", err)
	}
}

// TestClassifyResponseHeaders_AllowlistedOnErrorResponseIgnored:
// x-fapi-interaction-id declared only on a 4xx response is
// ignored — the generator surfaces metadata from 2xx only.
func TestClassifyResponseHeaders_AllowlistedOnErrorResponseIgnored(t *testing.T) {
	op := &openapi3.Operation{OperationID: "getZ"}
	op.Responses = openapi3.NewResponses()
	op.Responses.Set("429", &openapi3.ResponseRef{Value: &openapi3.Response{
		Headers: openapi3.Headers{
			"x-fapi-interaction-id": {Value: &openapi3.Header{}},
		},
	}})
	fields, err := classifyResponseHeaders(op, "getZ")
	if err != nil {
		t.Fatalf("classify: %v", err)
	}
	if len(fields) != 0 {
		t.Errorf("error-only header should not appear on ResponseMetadata: %+v", fields)
	}
}
