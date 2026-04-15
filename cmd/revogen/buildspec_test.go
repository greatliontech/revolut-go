package main

import (
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
)

// loadSpecFromString mirrors loadSpec but takes the YAML in memory so
// tests don't need a temp file on disk.
func loadSpecFromString(t *testing.T, src string) *openapi3.T {
	t.Helper()
	cleaned, err := scrubInvalidBounds([]byte(src))
	if err != nil {
		t.Fatalf("scrub: %v", err)
	}
	loader := openapi3.NewLoader()
	loader.IsExternalRefsAllowed = false
	doc, err := loader.LoadFromData(cleaned)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	return doc
}

func buildTest(t *testing.T, src string) *Spec {
	t.Helper()
	doc := loadSpecFromString(t, src)
	spec, err := buildSpec(doc, buildConfig{
		PackageName: "test",
		ErrPrefix:   "test",
	})
	if err != nil {
		t.Fatalf("buildSpec: %v", err)
	}
	return spec
}

func findType(spec *Spec, goName string) *NamedType {
	for _, t := range spec.Types {
		if t.GoName == goName {
			return t
		}
	}
	return nil
}

func findOp(spec *Spec, resource, method string) *Operation {
	for _, r := range spec.Resources {
		if r.GoName != resource {
			continue
		}
		for _, op := range r.Ops {
			if op.GoMethod == method {
				return op
			}
		}
	}
	return nil
}

const minimalSpec = `
openapi: 3.0.0
info: { title: mini, version: "1" }
paths:
  /accounts:
    get:
      tags: [Accounts]
      operationId: listAccounts
      responses:
        '200':
          description: ok
          content:
            application/json:
              schema: { type: array, items: { $ref: '#/components/schemas/Account' } }
  /accounts/{account_id}:
    get:
      tags: [Accounts]
      operationId: getAccount
      parameters:
        - name: account_id
          in: path
          required: true
          schema: { type: string }
      responses:
        '200':
          description: ok
          content:
            application/json:
              schema: { $ref: '#/components/schemas/Account' }
  /transfer:
    post:
      tags: [Transfers]
      operationId: createTransfer
      requestBody:
        required: true
        content:
          application/json:
            schema: { $ref: '#/components/schemas/TransferRequest' }
      responses:
        '200':
          description: ok
          content:
            application/json:
              schema: { $ref: '#/components/schemas/Account' }
components:
  schemas:
    Account:
      type: object
      required: [id, currency]
      properties:
        id: { type: string, format: uuid }
        currency: { type: string, pattern: '^[A-Z]{3}$' }
        state:
          type: string
          enum: [active, inactive]
        created_at: { type: string, format: date-time }
    TransferRequest:
      type: object
      required: [request_id, amount, source_account_id]
      properties:
        request_id: { type: string }
        amount: { type: number }
        source_account_id: { type: string, format: uuid }
        target_account_id: { type: string, format: uuid }
`

func TestBuildSpec_Shape(t *testing.T) {
	spec := buildTest(t, minimalSpec)
	if len(spec.Resources) != 2 {
		t.Fatalf("want 2 resources, got %d", len(spec.Resources))
	}
	acct := findType(spec, "Account")
	if acct == nil {
		t.Fatal("Account type missing")
	}
	if acct.Kind != KindStruct {
		t.Fatalf("Account.Kind = %v", acct.Kind)
	}
	fieldByJSON := map[string]*StructField{}
	for _, f := range acct.Fields {
		fieldByJSON[f.JSONName] = f
	}
	if fieldByJSON["id"].GoType != "string" {
		t.Errorf("id type = %q", fieldByJSON["id"].GoType)
	}
	// Currency is a plain string (no named Currency schema in this
	// fixture); the core.Currency mapping only fires for top-level
	// schemas whose pattern matches ^[A-Z]{3}$, not for inline fields.
	if fieldByJSON["currency"].GoType != "string" {
		t.Errorf("currency type = %q; want string", fieldByJSON["currency"].GoType)
	}
	if fieldByJSON["state"].GoType != "AccountState" {
		t.Errorf("state type = %q; want AccountState (inline enum promoted)", fieldByJSON["state"].GoType)
	}
	// Optional time.Time must be pointer.
	if fieldByJSON["created_at"].GoType != "*time.Time" {
		t.Errorf("created_at type = %q; want *time.Time", fieldByJSON["created_at"].GoType)
	}
	// Synthesised enum type must exist with both values.
	st := findType(spec, "AccountState")
	if st == nil || st.Kind != KindEnum || len(st.EnumValues) != 2 {
		t.Fatalf("AccountState enum missing or malformed: %+v", st)
	}
}

func TestBuildSpec_MethodNames(t *testing.T) {
	spec := buildTest(t, minimalSpec)
	if findOp(spec, "Accounts", "List") == nil {
		t.Error("Accounts.List missing")
	}
	if findOp(spec, "Accounts", "Get") == nil {
		t.Error("Accounts.Get missing")
	}
	if findOp(spec, "Transfers", "Create") == nil {
		t.Error("Transfers.Create missing")
	}
}

func TestBuildSpec_Validators(t *testing.T) {
	spec := buildTest(t, minimalSpec)
	create := findOp(spec, "Transfers", "Create")
	if create == nil {
		t.Fatal("Transfers.Create missing")
	}
	got := map[string]bool{}
	for _, v := range create.Validate {
		got[v.Cond] = true
	}
	for _, want := range []string{
		`req.RequestID == ""`,
		`req.SourceAccountID == ""`,
		`req.Amount == ""`, // json.Number — string-backed
	} {
		if !got[want] {
			t.Errorf("missing validator: %s; got %v", want, got)
		}
	}
}

const unionSpec = `
openapi: 3.0.0
info: { title: u, version: "1" }
paths:
  /x:
    post:
      tags: [X]
      operationId: doX
      requestBody:
        required: true
        content:
          application/json: { schema: { $ref: '#/components/schemas/Shape' } }
      responses:
        '200':
          description: ok
          content:
            application/json: { schema: { $ref: '#/components/schemas/Shape' } }
components:
  schemas:
    Shape:
      discriminator:
        mapping:
          c: '#/components/schemas/Circle'
          s: '#/components/schemas/Square'
    Circle:
      type: object
      required: [radius]
      properties:
        radius: { type: number }
    Square:
      type: object
      required: [side]
      properties:
        side: { type: number }
`

func TestBuildSpec_DiscriminatorUnion(t *testing.T) {
	spec := buildTest(t, unionSpec)
	shape := findType(spec, "Shape")
	if shape == nil || shape.Kind != KindUnion {
		t.Fatalf("Shape kind = %v", shape)
	}
	if len(shape.UnionVariants) != 2 {
		t.Fatalf("variants: %v", shape.UnionVariants)
	}
}

const conditionalAnyOfSpec = `
openapi: 3.0.0
info: { title: c, version: "1" }
paths:
  /x:
    post:
      tags: [X]
      operationId: doX
      requestBody:
        required: true
        content:
          application/json: { schema: { $ref: '#/components/schemas/Period' } }
      responses:
        '200': { description: ok }
components:
  schemas:
    Period:
      type: object
      properties:
        start_date: { type: string }
        end_date:   { type: string }
        end_action: { type: string }
      anyOf:
        - required: [start_date]
        - required: [end_date, end_action]
`

func TestBuildSpec_AnyOfRequiredGroups(t *testing.T) {
	spec := buildTest(t, conditionalAnyOfSpec)
	period := findType(spec, "Period")
	if period == nil {
		t.Fatal("Period missing")
	}
	if len(period.AnyOfRequiredGroups) != 2 {
		t.Fatalf("anyOfGroups: %v", period.AnyOfRequiredGroups)
	}
	// `POST /x` with tag X collapses to the generic Create method
	// once the tag prefix is stripped from the path.
	op := findOp(spec, "X", "Create")
	if op == nil {
		t.Fatal("X.Create missing")
	}
	found := false
	for _, v := range op.Validate {
		if contains(v.Cond, "req.StartDate") && contains(v.Cond, "req.EndDate") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("no anyOf validator emitted: %v", op.Validate)
	}
}
