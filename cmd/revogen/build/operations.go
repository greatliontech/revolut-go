package build

import (
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"

	"github.com/greatliontech/revolut-go/cmd/revogen/ir"
	"github.com/greatliontech/revolut-go/cmd/revogen/names"
)

// methodsOrder lists the HTTP verbs the generator emits, in the
// order they appear per path. Controls iteration only; each verb's
// presence on a PathItem is still gated by the spec.
var methodsOrder = []string{
	http.MethodGet,
	http.MethodPost,
	http.MethodPut,
	http.MethodPatch,
	http.MethodDelete,
}

// buildOperations walks every spec operation and turns it into an
// ir.Method on the matching resource. One spec operation produces
// one Method at this stage; per-variant fan-out for unions and any
// other multi-method lowering are separate passes.
//
// The iteration is sorted (paths then verbs) so emission is
// deterministic across runs.
func (b *Builder) buildOperations() {
	if b.doc.Paths == nil {
		return
	}
	allow := map[string]bool{}
	for _, t := range b.cfg.ResourceAllow {
		allow[t] = true
	}
	for _, path := range sortedPaths(b.doc) {
		item := b.doc.Paths.Value(path)
		if item == nil {
			continue
		}
		for _, verb := range methodsOrder {
			op := pickOperation(item, verb)
			if op == nil {
				continue
			}
			if !b.cfg.IncludeDeprecated && op.Deprecated {
				continue
			}
			tag := "Untagged"
			if len(op.Tags) > 0 {
				tag = op.Tags[0]
			}
			if len(allow) > 0 && !allow[tag] {
				continue
			}
			if !b.cfg.IncludeDeprecated && tagLooksDeprecated(op.Tags) {
				continue
			}
			m := b.methodFromOperation(verb, path, item, op, tag)
			if m == nil {
				continue
			}
			resource := b.ensureResource(tag)
			resource.Methods = append(resource.Methods, m)
		}
	}
}

// methodFromOperation synthesizes one Method from an operation.
// Returns nil for operations the generator can't model (e.g. an
// unrecognised request content type).
func (b *Builder) methodFromOperation(verb, path string, item *openapi3.PathItem, op *openapi3.Operation, tag string) *ir.Method {
	receiver := names.TypeName(tag)
	m := &ir.Method{
		Receiver:       receiver,
		Name:           b.deriveMethodName(verb, path, op, tag),
		Doc:            docLines(op),
		DocURL:         pickDocURL(b.docURL(op.OperationID), op.ExternalDocs),
		Deprecated:     deprecationReason(op),
		ServerOverride: b.pickServerOverride(op, item),
	}
	// Set the HTTP-call envelope before parameter handling so the
	// query-params struct's synthesized name has a verb + path to
	// fall back to when operationId is absent.
	m.HTTPCall.Method = verb
	m.HTTPCall.PathExpr = path
	b.applyParameters(m, item, op, verb, path)
	b.applyRequestBody(m, op)
	b.applyResponse(m, op)
	b.applySecurityScopes(m, op)
	b.applyResponseMetadata(m, op)
	return m
}

// applyResponseMetadata classifies the operation's declared
// response headers via the allowlist in responses.go, attaches
// the metadata fields to the method, and flags whether the method
// needs a Signed raw-bytes variant. Exits the whole build on an
// unknown header — silent drift would change the public API shape
// under callers without them noticing.
func (b *Builder) applyResponseMetadata(m *ir.Method, op *openapi3.Operation) {
	fields, err := classifyResponseHeaders(op, op.OperationID)
	if err != nil {
		if b.buildErr == nil {
			b.buildErr = err
		}
		return
	}
	m.ResponseMetadata = fields
	m.EmitSignedVariant = operationEmitsSignedVariant(op)
}

// applyParameters lifts path, query, and header parameters off the
// operation (merged with any path-level common parameters). Query
// parameters are gathered into a synthesized `<Op>Params` struct
// that the emitter renders next to the method.
//
// verb and path are forwarded so the synthesized struct's name can
// fall back to a (verb, path)-derived form when the operation has
// no operationId.
func (b *Builder) applyParameters(m *ir.Method, item *openapi3.PathItem, op *openapi3.Operation, verb, path string) {
	type queryParam struct {
		wireName       string
		goName         string
		typ            *ir.Type
		doc            string
		required       bool
		defaultDoc     string
		defaultLiteral string
		explodeFalse   bool
		schema         *openapi3.Schema
	}
	var queries []queryParam
	for _, paramRef := range concatParameters(item.Parameters, op.Parameters) {
		if paramRef == nil || paramRef.Value == nil {
			continue
		}
		p := paramRef.Value
		switch p.In {
		case "path":
			format := ""
			if p.Schema != nil && p.Schema.Value != nil {
				format = strings.ToLower(p.Schema.Value.Format)
			}
			m.PathParams = append(m.PathParams, ir.Param{
				Name:     names.ParamName(p.Name),
				Type:     ir.Prim("string"),
				Doc:      firstLine(p.Description),
				WireName: p.Name,
				Format:   format,
			})
		case "query":
			typ := b.resolveType(p.Schema, Context{Parent: m.Receiver, Field: p.Name})
			if typ == nil {
				typ = ir.Prim("string")
			}
			q := queryParam{
				wireName:     p.Name,
				goName:       names.FieldName(p.Name),
				typ:          typ,
				doc:          firstLine(p.Description),
				required:     p.Required,
				explodeFalse: isExplodeFalse(p),
			}
			if p.Schema != nil && p.Schema.Value != nil {
				q.defaultDoc = defaultDoc(p.Schema.Value)
				q.defaultLiteral = defaultLiteral(p.Schema.Value, typ)
				q.schema = p.Schema.Value
			}
			queries = append(queries, q)
		case "header":
			// Authorization is owned by the transport: the
			// Authenticator sets it, and the transport actively
			// strips any caller-supplied value to prevent leaks.
			// Emitting it as a method param forces callers to pass
			// a placeholder string that never reaches the wire,
			// which is confusing and also misleads users into
			// hard-coding fake tokens. Skip at build time.
			if isTransportOwnedHeader(p.Name) {
				continue
			}
			typ := b.resolveSharedParamEnum(paramRef)
			if typ == nil {
				typ = b.resolveType(p.Schema, Context{Parent: m.Receiver, Field: p.Name})
			}
			if typ == nil {
				typ = ir.Prim("string")
			}
			m.HeaderParams = append(m.HeaderParams, ir.Param{
				Name:     names.ParamName(p.Name),
				Type:     typ,
				Doc:      firstLine(p.Description),
				WireName: p.Name,
				Required: p.Required,
			})
		}
		// cookie params are ignored; no vendored spec uses them.
	}
	if len(queries) == 0 {
		return
	}
	paramsName := b.synthParamsName(op.OperationID, verb, path)
	paramsDecl := &ir.Decl{
		Name:               paramsName,
		Kind:               ir.DeclStruct,
		Doc:                "Query parameters for: " + firstLine(op.Summary),
		QueryParamsEncoder: true,
	}
	for _, q := range queries {
		f := &ir.Field{
			JSONName:       q.wireName,
			GoName:         q.goName,
			Type:           q.typ,
			Doc:            q.doc,
			Required:       q.required,
			DefaultDoc:     q.defaultDoc,
			DefaultLiteral: q.defaultLiteral,
			ExplodeFalse:   q.explodeFalse,
		}
		if q.schema != nil {
			applySchemaConstraints(f, q.schema)
		}
		paramsDecl.Fields = append(paramsDecl.Fields, f)
	}
	b.registerDecl(paramsName, paramsDecl)
	m.OptsParam = &ir.Param{Name: "opts", Type: ir.Pointer(ir.Named(paramsName))}
}

// synthParamsName returns a stable Go name for the generated Params
// struct. Operations with an operationId use it verbatim; the
// fallback derives a name from the verb and path. Names that
// collide with previously registered Decls get a numeric suffix so
// each operation owns a distinct struct.
func (b *Builder) synthParamsName(opID, verb, path string) string {
	var base string
	if opID != "" {
		base = names.TypeName(opID) + "Params"
	} else {
		var pathPart string
		for _, seg := range nonParamSegments(path) {
			pathPart += names.TypeName(seg)
		}
		if pathPart == "" {
			pathPart = "Root"
		}
		base = names.TypeName(strings.ToLower(verb)) + pathPart + "Params"
	}
	if _, taken := b.declByName[base]; !taken {
		return base
	}
	for i := 2; ; i++ {
		candidate := base + strconv.Itoa(i)
		if _, taken := b.declByName[candidate]; !taken {
			return candidate
		}
	}
}

// applyRequestBody inspects the operation's request body, picks a
// content type, and populates the Method's body hints. Precedence:
// application/json > application/x-www-form-urlencoded >
// multipart/form-data > application/octet-stream.
//
// Request-body types are always normalised to value shape ("req T",
// never "req *T"): no nil-deref risk in emitted validators, no
// zero-value ambiguity for callers, and resolveType's inline-object
// promotion (which returns *T for synthesized names) vs $ref path
// (which returns T) no longer leaks into the public signature.
func (b *Builder) applyRequestBody(m *ir.Method, op *openapi3.Operation) {
	if op.RequestBody == nil || op.RequestBody.Value == nil {
		return
	}
	content := op.RequestBody.Value.Content
	if content == nil {
		return
	}
	// Direct lookups (Content.Get falls back to */*, which would
	// silently treat non-JSON bodies as JSON).
	switch {
	case content["application/json"] != nil:
		typ := b.resolveType(content["application/json"].Schema, Context{Parent: m.Receiver, Field: "body"})
		if typ != nil {
			m.BodyParam = &ir.Param{Name: "req", Type: stripOuterPointer(typ)}
			m.HTTPCall.BodyKind = ir.BodyJSON
			m.HTTPCall.BodyExpr = "req"
		}
	case content["application/x-www-form-urlencoded"] != nil:
		typ := b.resolveType(content["application/x-www-form-urlencoded"].Schema, Context{Parent: m.Receiver, Field: "body"})
		if typ != nil {
			typ = stripOuterPointer(typ)
			m.BodyParam = &ir.Param{Name: "req", Type: typ}
			m.HTTPCall.BodyKind = ir.BodyForm
			m.HTTPCall.BodyExpr = "req.encodeForm()"
			b.flagEncoderOnTarget(typ, func(d *ir.Decl) { d.FormEncoder = true })
		}
	case content["multipart/form-data"] != nil:
		mt := content["multipart/form-data"]
		typ := b.resolveType(mt.Schema, Context{Parent: m.Receiver, Field: "body"})
		if typ != nil {
			typ = stripOuterPointer(typ)
			m.BodyParam = &ir.Param{Name: "req", Type: typ}
			m.HTTPCall.BodyKind = ir.BodyMultipart
			m.HTTPCall.BodyExpr = "req"
			b.flagEncoderOnTarget(typ, func(d *ir.Decl) {
				d.MultipartEncoder = true
				// Thread the per-property encoding.contentType into the
				// matching Field so the emitter can stamp it onto the
				// Content-Type of the generated part.
				if mt.Encoding != nil {
					for _, f := range d.Fields {
						enc, ok := mt.Encoding[f.JSONName]
						if !ok || enc == nil || enc.ContentType == "" {
							continue
						}
						// Spec lists comma-separated MIME options
						// (application/pdf, image/png). Pick the first
						// as the default; callers can override via
						// <Field>ContentType at runtime.
						if comma := strings.Index(enc.ContentType, ","); comma >= 0 {
							f.MultipartContentType = strings.TrimSpace(enc.ContentType[:comma])
						} else {
							f.MultipartContentType = enc.ContentType
						}
					}
				}
			})
		}
	case content["application/octet-stream"] != nil:
		m.BodyParam = &ir.Param{Name: "body", Type: ir.Prim("io.Reader", "io")}
		m.HTTPCall.BodyKind = ir.BodyRawStream
		m.HTTPCall.BodyExpr = "body"
		m.HTTPCall.BodyContentType = "application/octet-stream"
	default:
		// text/* bodies — csv, plain, xml, etc. — are sent as
		// raw io.Reader with the spec-declared Content-Type.
		// OB ships /file-payment-consents/.../file (text/csv),
		// /draft-payments (text/csv) and /register (text/plain).
		if mime := pickTextBodyContentType(content); mime != "" {
			m.BodyParam = &ir.Param{Name: "body", Type: ir.Prim("io.Reader", "io")}
			m.HTTPCall.BodyKind = ir.BodyRawStream
			m.HTTPCall.BodyExpr = "body"
			m.HTTPCall.BodyContentType = mime
		}
	}
}

// resolveSharedParamEnum returns the cached Go type for a shared
// components/parameters entry whose schema is a string enum. When
// multiple operations reference the same parameter via $ref (the
// common pattern for Revolut-Api-Version, X-FAPI-* headers, etc.),
// the generator used to synthesize a separate enum Decl for each
// usage — leading to `OrdersRevolutAPIVersion`,
// `CustomersRevolutAPIVersion`, and so on with identical values,
// all unrelated Go types. Caching by $ref path gives every call
// site the same type.
//
// Non-enum shared parameters and inline parameters fall through
// to the regular resolveType path.
func (b *Builder) resolveSharedParamEnum(paramRef *openapi3.ParameterRef) *ir.Type {
	if paramRef == nil || paramRef.Ref == "" || paramRef.Value == nil {
		return nil
	}
	if cached, ok := b.sharedParamEnum[paramRef.Ref]; ok {
		return cached
	}
	p := paramRef.Value
	if p.Schema == nil || p.Schema.Value == nil {
		return nil
	}
	s := p.Schema.Value
	if !schemaTypeIs(s, "string") || len(s.Enum) == 0 {
		return nil
	}
	const prefix = "#/components/parameters/"
	if !strings.HasPrefix(paramRef.Ref, prefix) {
		return nil
	}
	goName := names.TypeName(strings.TrimPrefix(paramRef.Ref, prefix))
	if _, exists := b.declByName[goName]; !exists {
		values := make([]ir.EnumValue, 0, len(s.Enum))
		seen := map[string]bool{}
		for _, v := range s.Enum {
			sv, ok := v.(string)
			if !ok || seen[sv] {
				continue
			}
			seen[sv] = true
			values = append(values, ir.EnumValue{
				GoName: goName + names.TypeName(sv),
				Value:  sv,
			})
		}
		b.registerDecl(goName, &ir.Decl{
			Name:       goName,
			Kind:       ir.DeclEnum,
			Doc:        s.Description,
			EnumBase:   ir.Prim("string"),
			EnumValues: values,
		})
	}
	t := ir.Named(goName)
	b.sharedParamEnum[paramRef.Ref] = t
	return t
}

// pickTextBodyContentType returns the most-specific text-family
// content type the spec's request body declares, or "" when the
// body isn't a recognised text shape. Order mirrors the other
// content-type dispatchers — specific shapes first so text/plain
// doesn't swallow text/csv when both are present.
func pickTextBodyContentType(content openapi3.Content) string {
	for _, mime := range []string{"text/csv", "text/xml", "text/plain", "text/html"} {
		if _, ok := content[mime]; ok {
			return mime
		}
	}
	return ""
}

// stripOuterPointer peels every leading *T wrapper from a type. Used
// to normalise request-body receivers to value shape while leaving
// inner pointers (e.g. slice element or map value) untouched.
func stripOuterPointer(t *ir.Type) *ir.Type {
	for t != nil && t.IsPointer() {
		t = t.Elem
	}
	return t
}

// flagEncoderOnTarget locates the Decl a Named type points at and
// applies the mutator. No-op for types that don't resolve to a
// locally-declared struct.
func (b *Builder) flagEncoderOnTarget(t *ir.Type, mut func(*ir.Decl)) {
	for t != nil && t.IsPointer() {
		t = t.Elem
	}
	if t == nil || !t.IsNamed() {
		return
	}
	if decl, ok := b.declByName[t.Name]; ok && decl.Kind == ir.DeclStruct {
		mut(decl)
	}
}

// applyResponse scans the operation's 2xx responses and picks the
// first one that has a modelable content. 204 (and any 2xx without
// content) produces an error-only method. Non-JSON responses become
// []byte via RespRawBytes.
func (b *Builder) applyResponse(m *ir.Method, op *openapi3.Operation) {
	if op.Responses == nil {
		return
	}
	for _, code := range []string{"200", "201", "202", "204"} {
		rr := op.Responses.Value(code)
		if rr == nil || rr.Value == nil {
			continue
		}
		if rr.Value.Content == nil || len(rr.Value.Content) == 0 {
			// 2xx with no body: error-only method.
			m.HTTPCall.RespKind = ir.RespNone
			return
		}
		if mt, ok := rr.Value.Content["application/json"]; ok && mt != nil {
			// Use operationId as the parent hint so two operations
			// on the same resource don't collide on the same inline
			// response type. Falls back to the receiver name when
			// the spec omits operationId.
			parent := op.OperationID
			if parent == "" {
				parent = m.Receiver
			}
			t := b.resolveType(mt.Schema, Context{Parent: parent, Field: "response"})
			if t != nil {
				// Inline-object promotion returns *<Name>, correct for a
				// struct field but wrong as a top-level return — it
				// would produce **<Name> once emit adds its own pointer.
				// Strip the outer pointer when the underlying type is
				// a local Named.
				if t.IsPointer() && t.Elem != nil && t.Elem.IsNamed() {
					t = t.Elem
				}
				m.Returns = t
				m.HTTPCall.RespType = t
				if t.IsSlice() {
					m.HTTPCall.RespKind = ir.RespJSONList
				} else {
					m.HTTPCall.RespKind = ir.RespJSONValue
				}
				return
			}
			// Inline JSON object without a $ref: synthesize a named
			// response struct so it has a callable type.
			if mt.Schema != nil && mt.Schema.Value != nil {
				name := b.synthResponseName(op.OperationID)
				if synth := b.structFromSchema(name, mt.Schema.Value); synth != nil {
					b.registerDecl(name, synth)
					m.Returns = ir.Named(name)
					m.HTTPCall.RespKind = ir.RespJSONValue
					m.HTTPCall.RespType = m.Returns
					return
				}
			}
		}
		if mime, mt := pickRawContent(rr.Value.Content); mime != "" {
			_ = mt
			// Stream non-JSON responses (statements, report PDFs,
			// CSV exports) so the caller can pipe to disk without
			// buffering the whole payload. The emitter uses the
			// transport's DoRawStream path and returns
			// io.ReadCloser; caller is responsible for Close().
			m.Returns = ir.Prim("io.ReadCloser", "io")
			m.HTTPCall.RespKind = ir.RespRawBytes
			m.HTTPCall.RespType = m.Returns
			m.HTTPCall.Accept = mime
			return
		}
	}
}

// applySecurityScopes surfaces per-operation security scopes for
// godoc. The spec often carries them as scoped OAuth2-like scheme
// references (e.g. `AccessToken: [READ]`).
func (b *Builder) applySecurityScopes(m *ir.Method, op *openapi3.Operation) {
	if op.Security == nil {
		return
	}
	seen := map[string]bool{}
	for _, req := range *op.Security {
		for _, scopes := range req {
			for _, scope := range scopes {
				if scope == "" || seen[scope] {
					continue
				}
				seen[scope] = true
				m.Scopes = append(m.Scopes, scope)
			}
		}
	}
	sort.Strings(m.Scopes)
}

// ensureResource returns or creates the Resource for a tag.
func (b *Builder) ensureResource(tag string) *ir.Resource {
	name := names.TypeName(tag)
	if r, ok := b.resourceByName[name]; ok {
		return r
	}
	r := &ir.Resource{Name: name}
	b.resourceByName[name] = r
	b.resourceOrder = append(b.resourceOrder, name)
	return r
}


// docLines returns the godoc lines for an operation: the summary
// as the lead, then the full description (every line) so wire-format
// caveats and usage notes the spec documents survive into generated
// godoc.
func docLines(op *openapi3.Operation) []string {
	var out []string
	if s := firstLine(op.Summary); s != "" {
		out = append(out, s)
	}
	desc := strings.TrimSpace(op.Description)
	if desc == "" {
		return out
	}
	if len(out) > 0 {
		out = append(out, "")
	}
	for _, line := range strings.Split(desc, "\n") {
		out = append(out, strings.TrimRight(line, " \t"))
	}
	return out
}

func deprecationReason(op *openapi3.Operation) string {
	if !op.Deprecated {
		return ""
	}
	if op.Summary != "" {
		return firstLine(op.Summary)
	}
	return "the API marks this operation deprecated"
}

// isTransportOwnedHeader reports whether a spec-declared header
// parameter is one the transport sets on behalf of the caller. Such
// headers must not be exposed as method parameters because the
// transport actively strips caller-supplied values to prevent
// credential leakage and content-type mix-ups.
func isTransportOwnedHeader(name string) bool {
	switch http.CanonicalHeaderKey(name) {
	case "Authorization", "Content-Type", "Accept", "User-Agent":
		return true
	}
	return false
}

// isExplodeFalse reports whether a query parameter uses the
// style=form, explode=false serialization — items are joined with
// commas under a single key instead of repeating the key per value.
// OpenAPI's defaults for a query array are style=form, explode=true,
// so this fires only when the spec pins explode: false explicitly.
func isExplodeFalse(p *openapi3.Parameter) bool {
	if p == nil || p.Schema == nil || p.Schema.Value == nil {
		return false
	}
	if p.Schema.Value.Type == nil || !p.Schema.Value.Type.Is("array") {
		return false
	}
	if p.Explode == nil {
		return false
	}
	return !*p.Explode
}

func tagLooksDeprecated(tags []string) bool {
	for _, t := range tags {
		if strings.Contains(strings.ToLower(t), "deprecated") {
			return true
		}
	}
	return false
}

func concatParameters(a, b openapi3.Parameters) openapi3.Parameters {
	if len(a) == 0 {
		return b
	}
	out := make(openapi3.Parameters, 0, len(a)+len(b))
	out = append(out, a...)
	out = append(out, b...)
	return out
}

func pickOperation(item *openapi3.PathItem, verb string) *openapi3.Operation {
	switch verb {
	case http.MethodGet:
		return item.Get
	case http.MethodPost:
		return item.Post
	case http.MethodPut:
		return item.Put
	case http.MethodPatch:
		return item.Patch
	case http.MethodDelete:
		return item.Delete
	}
	return nil
}

// pickServerOverride returns the operation's server URL only when
// it differs from the document-level default. Many specs (Revolut
// included) repeat the root server on every PathItem; that's
// noise, not an override, and we'd otherwise inject the full URL
// into every emitted call.
func (b *Builder) pickServerOverride(op *openapi3.Operation, item *openapi3.PathItem) string {
	candidate := ""
	if op.Servers != nil && len(*op.Servers) > 0 {
		candidate = (*op.Servers)[0].URL
	} else if len(item.Servers) > 0 {
		candidate = item.Servers[0].URL
	}
	if candidate == "" {
		return ""
	}
	for _, root := range b.doc.Servers {
		if root != nil && root.URL == candidate {
			return ""
		}
	}
	return candidate
}

// pickRawContent chooses the preferred non-JSON content entry for a
// response. Recognised types are listed in descending preference.
// `*/*` and bare `text/*` fall through to the last bucket.
func pickRawContent(content openapi3.Content) (string, *openapi3.MediaType) {
	for _, mime := range []string{"text/csv", "application/pdf", "text/plain", "application/octet-stream", "*/*"} {
		if mt, ok := content[mime]; ok {
			return mime, mt
		}
	}
	for mime, mt := range content {
		if strings.HasPrefix(mime, "text/") || strings.HasPrefix(mime, "image/") || strings.HasPrefix(mime, "application/") {
			return mime, mt
		}
	}
	return "", nil
}

func sortedPaths(doc *openapi3.T) []string {
	m := doc.Paths.Map()
	out := make([]string, 0, len(m))
	for p := range m {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// synthResponseName generates a name for an inline JSON response
// object. Mirrors the old emitter's `<OperationID>Response`
// fallback.
func (b *Builder) synthResponseName(opID string) string {
	if opID == "" {
		return "Response"
	}
	return names.TypeName(opID) + "Response"
}

// docURL is the generator's companion to the per-operation godoc link.
func (b *Builder) docURL(opID string) string {
	if opID == "" || b.cfg.DocsBase == "" {
		return ""
	}
	return b.cfg.DocsBase + names.CamelToKebab(opID)
}

// pickDocURL prefers the operation's explicit externalDocs URL when
// the spec declares one; falls back to the synthesized docs-base +
// operation-id link. An empty string when neither is available.
func pickDocURL(fallback string, ed *openapi3.ExternalDocs) string {
	if ed != nil && ed.URL != "" {
		return ed.URL
	}
	return fallback
}
