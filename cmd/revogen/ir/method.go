package ir

// BodyKind discriminates how a Method's request body is serialized
// onto the wire.
type BodyKind int

const (
	BodyNone      BodyKind = iota
	BodyJSON               // application/json, json.Marshal of BodyParam
	BodyForm               // application/x-www-form-urlencoded, via encodeForm()
	BodyMultipart          // multipart/form-data, via encodeMultipart()
	BodyRawStream          // raw io.Reader body with explicit content-type
)

// RespKind discriminates how a Method's response body is decoded.
type RespKind int

const (
	RespNone        RespKind = iota
	RespJSONValue            // *T = json.Decode
	RespJSONList             // []T = json.Decode
	RespUnionProbe           // union interface, probe decoder
	RespUnionTagged          // union interface, wire-tag decoder
	RespRawBytes             // []byte, via transport.DoRaw
)

// Param is one positional parameter on a generated Go method.
type Param struct {
	Name string // Go identifier
	Type *Type
	Doc  string

	// WireName is the original spec-side name (e.g. snake_case
	// "account_id") preserved for error messages and URL templates
	// where the wire form must round-trip exactly. Empty when the
	// param has no spec-side counterpart (e.g. ctx).
	WireName string
}

// Method is one Go method on a Resource.
type Method struct {
	Receiver string
	Name     string
	Doc      []string // godoc lines, already trimmed

	PathParams   []Param
	HeaderParams []Param
	BodyParam    *Param
	OptsParam    *Param // the `opts *<Op>Params` pointer when query params exist

	Returns *Type // nil for error-only methods

	Validators []Validator
	HTTPCall   HTTPCall
	Pagination *Pagination // nil when the operation doesn't paginate

	// ResponseMetadata lists the fields the generator surfaces on a
	// per-method ResponseMetadata return when the spec declares
	// allowlisted response headers on 2xx responses. Empty for
	// methods whose spec declares no such headers, which keeps the
	// common `(T, error)` shape for four of the five current
	// packages. Populated in sorted-by-GoName order so emit is
	// deterministic.
	ResponseMetadata []MetadataField

	// EmitSignedVariant is true when the operation's 2xx response
	// carries x-jws-signature. Triggers the emit of a `<Name>Signed`
	// companion method that returns raw bytes + metadata, so callers
	// can run detached-JWS verification against the untouched body.
	EmitSignedVariant bool

	// Godoc-only metadata.
	Scopes     []string // security scopes, e.g. ["READ", "WRITE"]
	DocURL     string
	Deprecated string // non-empty when the operation is deprecated

	// ServerOverride, when non-empty, is the full absolute URL the
	// method uses instead of the client's base URL. Sourced from an
	// operation-level `servers:` entry.
	ServerOverride string
}

// MetadataField names one column of the per-package
// ResponseMetadata struct.
type MetadataField struct {
	GoName   string // Go field name, e.g. "InteractionID"
	WireName string // HTTP header name, e.g. "x-fapi-interaction-id"
	Doc      string // single-line godoc for the field
}

// Validator is a pre-flight required-field check emitted before the
// HTTP call. Cond is a Go boolean expression that is true when the
// field is considered unset.
type Validator struct {
	Cond    string
	Message string
}

// HTTPCall fully describes the transport call the method issues so
// the emitter can wire it up mechanically.
type HTTPCall struct {
	Method   string // "GET", "POST", ...
	PathExpr string // Go expression building the URL path (or full URL when ServerOverride is set)

	BodyKind BodyKind
	BodyExpr string // Go expression for the body value

	RespKind RespKind
	RespType *Type  // target type for JSON decode; nil for RespNone / RespRawBytes
	Accept   string // Accept header override; empty = application/json
}

// PaginationShape classifies how the iterator advances.
type PaginationShape int

const (
	// PaginationCursor: response carries a next_page_token the caller
	// echoes back as page_token.
	PaginationCursor PaginationShape = iota + 1
	// PaginationTimeWindow: response is []T with a created_at
	// timestamp; caller advances a "to"/"created_before" query param.
	PaginationTimeWindow
	// PaginationLimit: limit/offset or page/per-page numeric paging.
	PaginationLimit
)

// Pagination describes the iterator emitted as <Method>All.
type Pagination struct {
	Shape    PaginationShape
	ItemType *Type

	// Cursor
	ItemsField     string
	NextTokenField string
	NextTokenType  *Type
	PageTokenParam string
	PageTokenType  *Type

	// TimeWindow
	AdvanceParam    string
	AdvanceFromItem string

	// Limit
	PageSizeParam string
	PageParam     string
}

// Callback describes a typed decoder for an OpenAPI callback
// (typically a webhook payload the server POSTs to the user).
type Callback struct {
	Name    string // Go identifier, e.g. "DecodeWebhookEvent"
	Payload *Type  // payload type
	Doc     []string
}
