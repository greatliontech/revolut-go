package build

import (
	"testing"

	"github.com/getkin/kin-openapi/openapi3"

	"github.com/greatliontech/revolut-go/cmd/revogen/ir"
)

func newOperationBuilder() *Builder {
	b := newTestBuilder()
	b.doc.Paths = &openapi3.Paths{}
	return b
}

func pathItem() *openapi3.PathItem { return &openapi3.PathItem{} }

func addPath(b *Builder, p string, item *openapi3.PathItem) {
	b.doc.Paths.Set(p, item)
}

func TestOperations_MethodNameFromOperationId(t *testing.T) {
	b := newOperationBuilder()
	item := pathItem()
	item.Get = &openapi3.Operation{
		OperationID: "listAccounts",
		Tags:        []string{"Accounts"},
		Responses:   openapi3.NewResponses(),
	}
	// Attach a no-content response so the Operation is modelable.
	item.Get.Responses.Set("200", &openapi3.ResponseRef{Value: &openapi3.Response{}})
	addPath(b, "/accounts", item)
	b.reserveResourceNames()
	b.buildOperations()

	r := b.resourceByName["Accounts"]
	if r == nil || len(r.Methods) != 1 {
		t.Fatalf("accounts resource: %+v", r)
	}
	if r.Methods[0].Name != "List" {
		t.Errorf("method name: %q", r.Methods[0].Name)
	}
}

func TestOperations_MethodNameStripsTagStem(t *testing.T) {
	b := newOperationBuilder()
	item := pathItem()
	item.Post = &openapi3.Operation{
		OperationID: "validateAccountName",
		Tags:        []string{"Counterparties"},
		Responses:   openapi3.NewResponses(),
	}
	item.Post.Responses.Set("200", &openapi3.ResponseRef{Value: &openapi3.Response{}})
	addPath(b, "/account-name-validation", item)
	b.reserveResourceNames()
	b.buildOperations()

	if r := b.resourceByName["Counterparties"]; r == nil || r.Methods[0].Name != "ValidateAccountName" {
		t.Fatalf("method: %+v", r)
	}
}

func TestOperations_FallbackPathName(t *testing.T) {
	b := newOperationBuilder()
	item := pathItem()
	item.Get = &openapi3.Operation{
		Tags:      []string{"Accounts"},
		Responses: openapi3.NewResponses(),
	}
	item.Get.Responses.Set("200", &openapi3.ResponseRef{Value: &openapi3.Response{}})
	addPath(b, "/accounts/{account_id}", item)
	b.reserveResourceNames()
	b.buildOperations()

	// Tag-stem-stripped path with endsInParam yields bare "Get" on
	// the resource; callers read `Accounts.Get(ctx, id)` which is
	// the Go convention for this shape.
	if r := b.resourceByName["Accounts"]; r == nil || r.Methods[0].Name != "Get" {
		t.Fatalf("method: %+v", r)
	}
}

func TestOperations_PathParam(t *testing.T) {
	b := newOperationBuilder()
	item := pathItem()
	item.Get = &openapi3.Operation{
		OperationID: "getAccount",
		Tags:        []string{"Accounts"},
		Parameters: openapi3.Parameters{
			&openapi3.ParameterRef{Value: &openapi3.Parameter{
				Name:     "account_id",
				In:       "path",
				Required: true,
				Schema:   inline(primSchema("string", "uuid")),
			}},
		},
		Responses: openapi3.NewResponses(),
	}
	item.Get.Responses.Set("200", &openapi3.ResponseRef{Value: &openapi3.Response{}})
	addPath(b, "/accounts/{account_id}", item)
	b.reserveResourceNames()
	b.buildOperations()

	m := b.resourceByName["Accounts"].Methods[0]
	if len(m.PathParams) != 1 || m.PathParams[0].Name != "accountID" {
		t.Errorf("path params: %+v", m.PathParams)
	}
}

func TestOperations_QueryParamsStruct(t *testing.T) {
	b := newOperationBuilder()
	item := pathItem()
	item.Get = &openapi3.Operation{
		OperationID: "listTransactions",
		Tags:        []string{"Transactions"},
		Parameters: openapi3.Parameters{
			&openapi3.ParameterRef{Value: &openapi3.Parameter{
				Name:   "count",
				In:     "query",
				Schema: inline(primSchema("integer", "int32")),
			}},
			&openapi3.ParameterRef{Value: &openapi3.Parameter{
				Name:   "from",
				In:     "query",
				Schema: inline(primSchema("string", "date-time")),
			}},
		},
		Responses: openapi3.NewResponses(),
	}
	item.Get.Responses.Set("200", &openapi3.ResponseRef{Value: &openapi3.Response{}})
	addPath(b, "/transactions", item)
	b.reserveResourceNames()
	b.buildOperations()

	m := b.resourceByName["Transactions"].Methods[0]
	if m.OptsParam == nil || m.OptsParam.Type.GoExpr() != "*ListTransactionsParams" {
		t.Fatalf("opts: %+v", m.OptsParam)
	}
	d := b.declByName["ListTransactionsParams"]
	if d == nil || len(d.Fields) != 2 {
		t.Fatalf("params struct: %+v", d)
	}
}

func TestOperations_HeaderParam(t *testing.T) {
	b := newOperationBuilder()
	item := pathItem()
	item.Get = &openapi3.Operation{
		OperationID: "getX",
		Tags:        []string{"X"},
		Parameters: openapi3.Parameters{
			&openapi3.ParameterRef{Value: &openapi3.Parameter{
				Name:   "x-fapi-interaction-id",
				In:     "header",
				Schema: inline(primSchema("string", "uuid")),
			}},
		},
		Responses: openapi3.NewResponses(),
	}
	item.Get.Responses.Set("200", &openapi3.ResponseRef{Value: &openapi3.Response{}})
	addPath(b, "/x", item)
	b.reserveResourceNames()
	b.buildOperations()

	m := b.resourceByName["X"].Methods[0]
	if len(m.HeaderParams) != 1 {
		t.Fatalf("header params: %+v", m.HeaderParams)
	}
}

func TestOperations_JSONRequestAndResponse(t *testing.T) {
	b := newOperationBuilder()
	b.doc.Components.Schemas["Foo"] = inline(&openapi3.Schema{
		Type: &openapi3.Types{"object"},
		Properties: openapi3.Schemas{
			"x": inline(primSchema("string", "")),
		},
	})
	item := pathItem()
	item.Post = &openapi3.Operation{
		OperationID: "createFoo",
		Tags:        []string{"Foos"},
		RequestBody: &openapi3.RequestBodyRef{Value: &openapi3.RequestBody{
			Content: openapi3.Content{
				"application/json": &openapi3.MediaType{Schema: refTo("Foo")},
			},
		}},
		Responses: openapi3.NewResponses(),
	}
	item.Post.Responses.Set("200", &openapi3.ResponseRef{Value: &openapi3.Response{
		Content: openapi3.Content{
			"application/json": &openapi3.MediaType{Schema: refTo("Foo")},
		},
	}})
	addPath(b, "/foos", item)
	b.reserveResourceNames()
	b.buildDecls()
	b.buildOperations()

	m := b.resourceByName["Foos"].Methods[0]
	if m.BodyParam == nil || m.BodyParam.Type.GoExpr() != "Foo" {
		t.Errorf("body param: %+v", m.BodyParam)
	}
	if m.Returns == nil || m.Returns.GoExpr() != "Foo" {
		t.Errorf("returns: %+v", m.Returns)
	}
	if m.HTTPCall.RespKind != ir.RespJSONValue || m.HTTPCall.BodyKind != ir.BodyJSON {
		t.Errorf("kinds: body=%v resp=%v", m.HTTPCall.BodyKind, m.HTTPCall.RespKind)
	}
}

// TestOperations_InlineBodyIsValueShape pins the normalisation rule:
// inline-object request bodies (which resolveType returns as *T via
// promoteInline) get their outer pointer stripped so every generated
// method signature takes `req T`, never `req *T`.
func TestOperations_InlineBodyIsValueShape(t *testing.T) {
	b := newOperationBuilder()
	item := pathItem()
	item.Post = &openapi3.Operation{
		OperationID: "createFoo",
		Tags:        []string{"Foos"},
		RequestBody: &openapi3.RequestBodyRef{Value: &openapi3.RequestBody{
			Content: openapi3.Content{
				"application/json": &openapi3.MediaType{Schema: inline(&openapi3.Schema{
					Type: &openapi3.Types{"object"},
					Properties: openapi3.Schemas{
						"x": inline(primSchema("string", "")),
					},
				})},
			},
		}},
		Responses: openapi3.NewResponses(),
	}
	item.Post.Responses.Set("200", &openapi3.ResponseRef{Value: &openapi3.Response{}})
	addPath(b, "/foos", item)
	b.reserveResourceNames()
	b.buildDecls()
	b.buildOperations()

	m := b.resourceByName["Foos"].Methods[0]
	if m.BodyParam == nil {
		t.Fatal("nil body param")
	}
	if got := m.BodyParam.Type.GoExpr(); got[0] == '*' {
		t.Errorf("body type still a pointer: %q", got)
	}
}

func TestOperations_RawBytesResponse(t *testing.T) {
	b := newOperationBuilder()
	item := pathItem()
	item.Get = &openapi3.Operation{
		OperationID: "getPDF",
		Tags:        []string{"X"},
		Responses:   openapi3.NewResponses(),
	}
	item.Get.Responses.Set("200", &openapi3.ResponseRef{Value: &openapi3.Response{
		Content: openapi3.Content{
			"application/pdf": &openapi3.MediaType{Schema: inline(primSchema("string", "binary"))},
		},
	}})
	addPath(b, "/x/pdf", item)
	b.reserveResourceNames()
	b.buildOperations()

	m := b.resourceByName["X"].Methods[0]
	if m.HTTPCall.RespKind != ir.RespRawBytes || m.Returns.GoExpr() != "[]byte" {
		t.Errorf("pdf resp: %+v", m.HTTPCall)
	}
	if m.HTTPCall.Accept != "application/pdf" {
		t.Errorf("accept: %q", m.HTTPCall.Accept)
	}
}

func TestOperations_204NoContent(t *testing.T) {
	b := newOperationBuilder()
	item := pathItem()
	item.Delete = &openapi3.Operation{
		OperationID: "deleteX",
		Tags:        []string{"X"},
		Responses:   openapi3.NewResponses(),
	}
	item.Delete.Responses.Set("204", &openapi3.ResponseRef{Value: &openapi3.Response{}})
	addPath(b, "/x/{id}", item)
	b.reserveResourceNames()
	b.buildOperations()

	m := b.resourceByName["X"].Methods[0]
	if m.Returns != nil {
		t.Errorf("expected nil Returns for 204; got %s", m.Returns.GoExpr())
	}
	if m.HTTPCall.RespKind != ir.RespNone {
		t.Errorf("resp kind: %v", m.HTTPCall.RespKind)
	}
}

func TestOperations_FormAndMultipart(t *testing.T) {
	b := newOperationBuilder()
	b.doc.Components.Schemas["FormReq"] = inline(&openapi3.Schema{
		Type:       &openapi3.Types{"object"},
		Properties: openapi3.Schemas{"a": inline(primSchema("string", ""))},
	})
	b.doc.Components.Schemas["MultiReq"] = inline(&openapi3.Schema{
		Type:       &openapi3.Types{"object"},
		Properties: openapi3.Schemas{"file": inline(primSchema("string", "binary"))},
	})
	b.buildDecls()

	formItem := pathItem()
	formItem.Post = &openapi3.Operation{
		OperationID: "postForm",
		Tags:        []string{"X"},
		RequestBody: &openapi3.RequestBodyRef{Value: &openapi3.RequestBody{
			Content: openapi3.Content{
				"application/x-www-form-urlencoded": &openapi3.MediaType{Schema: refTo("FormReq")},
			},
		}},
		Responses: openapi3.NewResponses(),
	}
	formItem.Post.Responses.Set("200", &openapi3.ResponseRef{Value: &openapi3.Response{}})
	addPath(b, "/form", formItem)

	multiItem := pathItem()
	multiItem.Post = &openapi3.Operation{
		OperationID: "postMulti",
		Tags:        []string{"X"},
		RequestBody: &openapi3.RequestBodyRef{Value: &openapi3.RequestBody{
			Content: openapi3.Content{
				"multipart/form-data": &openapi3.MediaType{Schema: refTo("MultiReq")},
			},
		}},
		Responses: openapi3.NewResponses(),
	}
	multiItem.Post.Responses.Set("200", &openapi3.ResponseRef{Value: &openapi3.Response{}})
	addPath(b, "/multi", multiItem)

	b.reserveResourceNames()
	b.buildOperations()

	// "postForm" and "postMulti" have a verb synonym for Create, so
	// the derived names are CreateForm / CreateMulti.
	var formM, multiM *ir.Method
	for _, m := range b.resourceByName["X"].Methods {
		switch m.Name {
		case "CreateForm":
			formM = m
		case "CreateMulti":
			multiM = m
		}
	}
	if formM == nil || formM.HTTPCall.BodyKind != ir.BodyForm {
		t.Errorf("form method: %+v", formM)
	}
	if multiM == nil || multiM.HTTPCall.BodyKind != ir.BodyMultipart {
		t.Errorf("multi method: %+v", multiM)
	}
	if d := b.declByName["FormReq"]; d == nil || !d.FormEncoder {
		t.Errorf("FormReq encoder flag: %+v", d)
	}
	if d := b.declByName["MultiReq"]; d == nil || !d.MultipartEncoder {
		t.Errorf("MultiReq encoder flag: %+v", d)
	}
}

func TestOperations_ServerOverride(t *testing.T) {
	b := newOperationBuilder()
	item := pathItem()
	srv := openapi3.Servers{{URL: "https://alt.example.com"}}
	item.Get = &openapi3.Operation{
		OperationID: "getX",
		Tags:        []string{"X"},
		Servers:     &srv,
		Responses:   openapi3.NewResponses(),
	}
	item.Get.Responses.Set("200", &openapi3.ResponseRef{Value: &openapi3.Response{}})
	addPath(b, "/x", item)
	b.reserveResourceNames()
	b.buildOperations()

	m := b.resourceByName["X"].Methods[0]
	if m.ServerOverride != "https://alt.example.com" {
		t.Errorf("override: %q", m.ServerOverride)
	}
}

func TestOperations_SecurityScopes(t *testing.T) {
	b := newOperationBuilder()
	item := pathItem()
	sec := openapi3.SecurityRequirements{{"AccessToken": {"READ", "WRITE"}}}
	item.Get = &openapi3.Operation{
		OperationID: "getX",
		Tags:        []string{"X"},
		Security:    &sec,
		Responses:   openapi3.NewResponses(),
	}
	item.Get.Responses.Set("200", &openapi3.ResponseRef{Value: &openapi3.Response{}})
	addPath(b, "/x", item)
	b.reserveResourceNames()
	b.buildOperations()

	m := b.resourceByName["X"].Methods[0]
	if len(m.Scopes) != 2 {
		t.Errorf("scopes: %+v", m.Scopes)
	}
}
