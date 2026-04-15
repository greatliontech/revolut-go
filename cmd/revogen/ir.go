package main

// The intermediate representation revogen emits from. It is the boundary
// between spec parsing (which deals in openapi3.Schema, Parameter, ...)
// and emission (which is text/template). Everything here is already
// resolved into Go-friendly shapes.

// Spec is the full normalized representation of an OpenAPI spec, scoped
// to one Go output package.
type Spec struct {
	PackageName string
	// ErrPrefix is prepended to validation error messages in emitted
	// methods ("<ErrPrefix>: Foo.bar is required"). Set by buildConfig.
	ErrPrefix string
	Resources []*Resource
	Types     []*NamedType
}

// Resource is one grouping of operations that produces a single
// generated file (e.g. "Accounts" → business/gen_accounts.go).
type Resource struct {
	GoName string // e.g. "Accounts"
	Ops    []*Operation
}

// Operation is one HTTP endpoint rendered as a Go method on the
// resource's struct.
type Operation struct {
	GoMethod     string        // Go method name, e.g. "List", "Get", "Create"
	HTTPMethod   string        // "GET", "POST", ...
	PathTemplate string        // raw spec path, "/accounts/{account_id}"
	PathParams   []*PathParam  // path params in the order they appear in the URL
	QueryParams  []*QueryParam // query-string params
	Summary      string        // one-line summary for godoc
	Description  string        // full description for godoc
	DocURL       string        // documentation URL, linked from godoc
	RequestType  string        // Go type of the request body; empty for no body
	ResponseType string        // Go type of the 2xx response; empty for void
	Validate     []FieldCheck  // client-side required-field checks
	ParamsType   string        // Go type name holding query params (empty if none)
	ParamsStruct *NamedType    // the synthesised params struct (or nil)
	Pagination   *Pagination   // how to iterate; nil if the op doesn't paginate
}

// PaginationShape classifies the paging pattern an operation exposes.
type PaginationShape int

const (
	// PaginationCursor: response struct carries a cursor (next_page_token)
	// that the caller sends back as a query param (page_token) to fetch
	// the next page.
	PaginationCursor PaginationShape = iota + 1
	// PaginationTimeWindow: response is a plain array whose items have
	// a created_at timestamp; the caller bounds future requests via a
	// "to" or "created_before" query param set to the last item's time.
	PaginationTimeWindow
)

// Pagination captures the codegen inputs needed to emit an iterator.
// All Go field names are those of the emitted structs, not the spec's
// JSON names.
type Pagination struct {
	Shape PaginationShape

	// ItemType is the element type yielded per iteration, e.g.
	// "Transaction" or "AccountingCategoryResponse".
	ItemType string

	// Cursor shape only:
	ItemsField     string // Go field on response holding the items slice
	NextTokenField string // Go field on response holding the next cursor
	PageTokenParam string // Go field on params that carries the cursor back

	// Time-window shape only:
	AdvanceParam    string // Go field on params to overwrite each page (e.g. "To")
	AdvanceFromItem string // Go field on item to copy into it (e.g. "CreatedAt")
}

// QueryParam describes one ?foo=... URL parameter.
type QueryParam struct {
	Name   string // wire name, e.g. "from"
	GoName string // Go field name, e.g. "From"
	GoType string // Go field type: "string", "int", "bool", "time.Time", or an enum type name
	Doc    string
}

// PathParam describes a single {…} in the path template.
type PathParam struct {
	Name    string // spec name, e.g. "account_id"
	GoName  string // Go parameter name, e.g. "accountID"
	GoType  string // always "string" for now
	Doc     string
}

// FieldCheck describes a required-field validation the emitted method
// runs before performing the HTTP call. Cond is an emit-ready Go
// boolean expression that evaluates to true when the field is unset
// (e.g. `req.RequestID == ""`, `req.Amount == ""`, `req.Recipient == nil`,
// `req.ExpiresAt.IsZero()`, `len(req.Items) == 0`).
type FieldCheck struct {
	Cond    string
	Message string
}

// TypeKind discriminates NamedType.
type TypeKind int

const (
	KindStruct TypeKind = iota
	KindEnum
	KindAlias
	// KindUnion: a schema with `discriminator.mapping` that maps
	// nominal tags to sibling variant schemas. Rendered as a sealed Go
	// interface; variants add a marker method. Revolut's specs do not
	// set `discriminator.propertyName`, so the union has no wire-level
	// discriminator — decoding probes variants by required-field
	// presence in mapping order.
	KindUnion
)

// NamedType is a top-level Go type emitted into the output package.
type NamedType struct {
	GoName     string
	Kind       TypeKind
	Doc        string

	// KindStruct
	Fields []*StructField

	// KindEnum
	EnumBase   string   // Go base type (e.g. "string")
	EnumValues []EnumValue

	// KindAlias
	AliasTarget string // Go type the alias resolves to (e.g. "core.Currency")

	// KindUnion
	UnionVariants []UnionVariant // ordered by discriminator mapping key
}

// UnionVariant is one mapped variant of a KindUnion.
type UnionVariant struct {
	Tag    string // discriminator mapping key (e.g. "UK"), preserved for docs
	GoName string // Go type name of the variant struct (e.g. "ValidateAccountNameRequestUK")
}

// EnumValue is one entry in a string-enum type.
type EnumValue struct {
	GoName     string // exported Go const name, e.g. "AccountStateActive"
	Value      string // wire value, e.g. "active"
	Doc        string
}

// StructField describes one JSON field on an emitted struct.
type StructField struct {
	JSONName  string
	GoName    string
	GoType    string // full Go type expression, ready to emit ("string", "*time.Time", "[]Foo", ...)
	Required  bool   // controls omitempty and validator hints
	Doc       string
}
