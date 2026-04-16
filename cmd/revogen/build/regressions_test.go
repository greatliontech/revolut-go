package build

import (
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"

	"github.com/greatliontech/revolut-go/cmd/revogen/ir"
)

// TestOperations_StripsTransportOwnedHeaders pins the build-time
// strip for every header the transport owns. Emitting them as method
// parameters would force callers to pass placeholders whose value
// never reached the wire.
func TestOperations_StripsTransportOwnedHeaders(t *testing.T) {
	stripped := []string{"Authorization", "Content-Type", "Accept", "User-Agent"}
	kept := []string{"X-Fapi-Financial-Id", "Revolut-Api-Version", "Idempotency-Key"}

	b := newOperationBuilder()
	item := pathItem()
	item.Get = &openapi3.Operation{
		OperationID: "doX",
		Tags:        []string{"X"},
		Responses:   openapi3.NewResponses(),
	}
	item.Get.Responses.Set("200", &openapi3.ResponseRef{Value: &openapi3.Response{}})
	for _, h := range append(append([]string{}, stripped...), kept...) {
		item.Get.Parameters = append(item.Get.Parameters, &openapi3.ParameterRef{
			Value: &openapi3.Parameter{
				In:     "header",
				Name:   h,
				Schema: inline(primSchema("string", "")),
			},
		})
	}
	addPath(b, "/x", item)
	b.reserveResourceNames()
	b.buildOperations()

	m := b.resourceByName["X"].Methods[0]
	got := map[string]bool{}
	for _, p := range m.HeaderParams {
		got[p.WireName] = true
	}
	for _, h := range stripped {
		if got[h] {
			t.Errorf("transport-owned header %q leaked into method params", h)
		}
	}
	for _, h := range kept {
		if !got[h] {
			t.Errorf("non-transport-owned header %q was dropped", h)
		}
	}
}

// TestOperations_MultipartEncodingContentType pins the per-field
// contentType hint from OpenAPI's multipart encoding map. A single
// MIME goes through verbatim; a comma-separated allowed set picks
// the first as the default — callers override via the companion
// <Field>ContentType field on the generated struct.
func TestOperations_MultipartEncodingContentType(t *testing.T) {
	b := newOperationBuilder()
	b.doc.Components.Schemas["Upload"] = inline(&openapi3.Schema{
		Type: &openapi3.Types{"object"},
		Properties: openapi3.Schemas{
			"file":   inline(primSchema("string", "binary")),
			"memo":   inline(primSchema("string", "")),
			"avatar": inline(primSchema("string", "binary")),
		},
	})
	b.buildDecls()

	item := pathItem()
	item.Post = &openapi3.Operation{
		OperationID: "upload",
		Tags:        []string{"X"},
		RequestBody: &openapi3.RequestBodyRef{Value: &openapi3.RequestBody{
			Content: openapi3.Content{
				"multipart/form-data": &openapi3.MediaType{
					Schema: refTo("Upload"),
					Encoding: map[string]*openapi3.Encoding{
						"file":   {ContentType: "application/pdf, image/png, image/jpeg"},
						"avatar": {ContentType: "image/webp"},
					},
				},
			},
		}},
		Responses: openapi3.NewResponses(),
	}
	item.Post.Responses.Set("200", &openapi3.ResponseRef{Value: &openapi3.Response{}})
	addPath(b, "/upload", item)
	b.reserveResourceNames()
	b.buildOperations()

	d := b.declByName["Upload"]
	if d == nil {
		t.Fatal("Upload decl missing")
	}
	byName := map[string]*ir.Field{}
	for _, f := range d.Fields {
		byName[f.JSONName] = f
	}
	if got := byName["file"].MultipartContentType; got != "application/pdf" {
		t.Errorf("file MultipartContentType=%q; want application/pdf (first entry)", got)
	}
	if got := byName["avatar"].MultipartContentType; got != "image/webp" {
		t.Errorf("avatar MultipartContentType=%q", got)
	}
	if got := byName["memo"].MultipartContentType; got != "" {
		t.Errorf("memo (unlisted in encoding) MultipartContentType=%q; want empty", got)
	}
}

// TestOperations_ExplodeFalse pins the query-param array serialisation
// hint when the spec declares style=form, explode=false.
func TestOperations_ExplodeFalse(t *testing.T) {
	falseVal := false
	trueVal := true
	b := newOperationBuilder()
	item := pathItem()
	item.Get = &openapi3.Operation{
		OperationID: "listThings",
		Tags:        []string{"X"},
		Responses:   openapi3.NewResponses(),
		Parameters: openapi3.Parameters{
			{Value: &openapi3.Parameter{
				In:   "query",
				Name: "comma_joined",
				Schema: inline(&openapi3.Schema{
					Type:  &openapi3.Types{"array"},
					Items: inline(primSchema("string", "")),
				}),
				Explode: &falseVal,
			}},
			{Value: &openapi3.Parameter{
				In:   "query",
				Name: "repeated",
				Schema: inline(&openapi3.Schema{
					Type:  &openapi3.Types{"array"},
					Items: inline(primSchema("string", "")),
				}),
				Explode: &trueVal,
			}},
			{Value: &openapi3.Parameter{
				In:   "query",
				Name: "default_explode",
				Schema: inline(&openapi3.Schema{
					Type:  &openapi3.Types{"array"},
					Items: inline(primSchema("string", "")),
				}),
			}},
		},
	}
	item.Get.Responses.Set("200", &openapi3.ResponseRef{Value: &openapi3.Response{}})
	addPath(b, "/things", item)
	b.reserveResourceNames()
	b.buildOperations()

	m := b.resourceByName["X"].Methods[0]
	if m.OptsParam == nil {
		t.Fatal("OptsParam missing")
	}
	paramsName := strings.TrimPrefix(m.OptsParam.Type.GoExpr(), "*")
	d := b.declByName[paramsName]
	if d == nil {
		t.Fatalf("params decl %q missing", paramsName)
	}
	flags := map[string]bool{}
	for _, f := range d.Fields {
		flags[f.JSONName] = f.ExplodeFalse
	}
	if !flags["comma_joined"] {
		t.Error("explode=false param did not set ExplodeFalse")
	}
	if flags["repeated"] {
		t.Error("explode=true param set ExplodeFalse")
	}
	if flags["default_explode"] {
		t.Error("unset explode (default=true) set ExplodeFalse")
	}
}

// TestOperations_DescriptionPreserved covers the docLines expansion:
// beyond the summary's first line, every non-blank description line
// ends up in Method.Doc so wire-format caveats survive into godoc.
func TestOperations_DescriptionPreserved(t *testing.T) {
	b := newOperationBuilder()
	item := pathItem()
	item.Get = &openapi3.Operation{
		OperationID: "getX",
		Tags:        []string{"X"},
		Summary:     "Short summary",
		Description: "Line one describing the endpoint.\nLine two with caveats.",
		Responses:   openapi3.NewResponses(),
	}
	item.Get.Responses.Set("200", &openapi3.ResponseRef{Value: &openapi3.Response{}})
	addPath(b, "/x", item)
	b.reserveResourceNames()
	b.buildOperations()

	m := b.resourceByName["X"].Methods[0]
	if len(m.Doc) < 3 {
		t.Fatalf("doc lines missing; got %d: %q", len(m.Doc), m.Doc)
	}
	if m.Doc[0] != "Short summary" {
		t.Errorf("Doc[0]=%q; want Short summary", m.Doc[0])
	}
	joined := strings.Join(m.Doc, "\n")
	if !strings.Contains(joined, "caveats") {
		t.Errorf("description lines not preserved: %q", joined)
	}
}

// TestOperations_ExternalDocsPreferred covers pickDocURL: when the
// spec declares externalDocs on the operation it takes precedence
// over the synthesized docs-base + operation-id link.
func TestOperations_ExternalDocsPreferred(t *testing.T) {
	b := newOperationBuilder()
	b.cfg.DocsBase = "https://generated.example.com/"
	item := pathItem()
	item.Get = &openapi3.Operation{
		OperationID:  "getX",
		Tags:         []string{"X"},
		Responses:    openapi3.NewResponses(),
		ExternalDocs: &openapi3.ExternalDocs{URL: "https://spec.example.com/getX"},
	}
	item.Get.Responses.Set("200", &openapi3.ResponseRef{Value: &openapi3.Response{}})
	addPath(b, "/x", item)
	b.reserveResourceNames()
	b.buildOperations()

	m := b.resourceByName["X"].Methods[0]
	if m.DocURL != "https://spec.example.com/getX" {
		t.Errorf("DocURL=%q; want externalDocs URL", m.DocURL)
	}
}
