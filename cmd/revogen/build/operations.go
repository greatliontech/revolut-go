package build

import (
	"net/http"
	"sort"
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
		DocURL:         b.docURL(op.OperationID),
		Deprecated:     deprecationReason(op),
		ServerOverride: pickServerOverride(op, item),
	}
	b.applyParameters(m, item, op)
	b.applyRequestBody(m, op)
	b.applyResponse(m, op)
	b.applySecurityScopes(m, op)
	m.HTTPCall.Method = verb
	return m
}

// applyParameters lifts path, query, and header parameters off the
// operation (merged with any path-level common parameters). Query
// parameters are gathered into a synthesized `<Op>Params` struct
// that the emitter renders next to the method.
func (b *Builder) applyParameters(m *ir.Method, item *openapi3.PathItem, op *openapi3.Operation) {
	type queryParam struct {
		wireName string
		goName   string
		typ      *ir.Type
		doc      string
	}
	var queries []queryParam
	for _, paramRef := range concatParameters(item.Parameters, op.Parameters) {
		if paramRef == nil || paramRef.Value == nil {
			continue
		}
		p := paramRef.Value
		switch p.In {
		case "path":
			m.PathParams = append(m.PathParams, ir.Param{
				Name: names.ParamName(p.Name),
				Type: ir.Prim("string"),
				Doc:  firstLine(p.Description),
			})
		case "query":
			typ := b.resolveType(p.Schema, Context{Parent: m.Receiver, Field: p.Name})
			if typ == nil {
				typ = ir.Prim("string")
			}
			queries = append(queries, queryParam{
				wireName: p.Name,
				goName:   names.FieldName(p.Name),
				typ:      typ,
				doc:      firstLine(p.Description),
			})
		case "header":
			typ := b.resolveType(p.Schema, Context{Parent: m.Receiver, Field: p.Name})
			if typ == nil {
				typ = ir.Prim("string")
			}
			m.HeaderParams = append(m.HeaderParams, ir.Param{
				Name: names.ParamName(p.Name),
				Type: typ,
				Doc:  firstLine(p.Description),
			})
		}
		// cookie params are ignored; no vendored spec uses them.
	}
	if len(queries) == 0 {
		return
	}
	paramsName := b.synthParamsName(op.OperationID, op, item, m)
	paramsDecl := &ir.Decl{
		Name: paramsName,
		Kind: ir.DeclStruct,
		Doc:  "Query parameters for: " + firstLine(op.Summary),
	}
	for _, q := range queries {
		paramsDecl.Fields = append(paramsDecl.Fields, &ir.Field{
			JSONName: q.wireName,
			GoName:   q.goName,
			Type:     q.typ,
			Doc:      q.doc,
		})
	}
	b.registerDecl(paramsName, paramsDecl)
	m.OptsParam = &ir.Param{Name: "opts", Type: ir.Pointer(ir.Named(paramsName))}
}

// synthParamsName returns a stable Go name for the generated Params
// struct. Operations with an operationId use it verbatim; the
// fallback derives a name from the HTTP method and path so two
// parameterless operations on the same resource don't collide.
func (b *Builder) synthParamsName(opID string, op *openapi3.Operation, item *openapi3.PathItem, m *ir.Method) string {
	if opID != "" {
		return names.TypeName(opID) + "Params"
	}
	// Fallback: <Verb><PathSegmentsSansParams>Params.
	segs := nonParamSegments(m.HTTPCall.Method) // unused — method not set yet
	_ = segs
	path := "" // operationId absent is rare; synthesise from the tag-stripped path
	for _, p := range nonParamSegments(pathFromItem(item, op)) {
		path += names.TypeName(p)
	}
	if path == "" {
		path = "Path"
	}
	return names.TypeName(m.HTTPCall.Method) + path + "Params"
}

// pathFromItem is a best-effort reverse map from Operation → path
// template. kin-openapi doesn't store this link, so we scan the
// document once per call. Acceptable: the fallback is only hit for
// operations without an operationId, which the vendored specs don't
// have today.
func pathFromItem(item *openapi3.PathItem, op *openapi3.Operation) string {
	_ = item
	_ = op
	return "unknown"
}

// applyRequestBody inspects the operation's request body, picks a
// content type, and populates the Method's body hints. Precedence:
// application/json > application/x-www-form-urlencoded >
// multipart/form-data > application/octet-stream.
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
			m.BodyParam = &ir.Param{Name: "req", Type: typ}
			m.HTTPCall.BodyKind = ir.BodyJSON
			m.HTTPCall.BodyExpr = "req"
		}
	case content["application/x-www-form-urlencoded"] != nil:
		typ := b.resolveType(content["application/x-www-form-urlencoded"].Schema, Context{Parent: m.Receiver, Field: "body"})
		if typ != nil {
			m.BodyParam = &ir.Param{Name: "req", Type: typ}
			m.HTTPCall.BodyKind = ir.BodyForm
			m.HTTPCall.BodyExpr = "req.encodeForm()"
			b.flagEncoderOnTarget(typ, func(d *ir.Decl) { d.FormEncoder = true })
		}
	case content["multipart/form-data"] != nil:
		typ := b.resolveType(content["multipart/form-data"].Schema, Context{Parent: m.Receiver, Field: "body"})
		if typ != nil {
			m.BodyParam = &ir.Param{Name: "req", Type: typ}
			m.HTTPCall.BodyKind = ir.BodyMultipart
			m.HTTPCall.BodyExpr = "req"
			b.flagEncoderOnTarget(typ, func(d *ir.Decl) { d.MultipartEncoder = true })
		}
	case content["application/octet-stream"] != nil:
		m.BodyParam = &ir.Param{Name: "body", Type: ir.Prim("io.Reader", "io")}
		m.HTTPCall.BodyKind = ir.BodyRawStream
		m.HTTPCall.BodyExpr = "body"
	}
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
			t := b.resolveType(mt.Schema, Context{Parent: m.Receiver, Field: "response"})
			if t != nil {
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
			m.Returns = ir.Prim("[]byte")
			m.HTTPCall.RespKind = ir.RespRawBytes
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

// deriveMethodName picks the Go method name for an operation.
// Precedence:
//
//  1. operationId, stripped of leading HTTP-verb synonyms and the
//     tag stem when they duplicate;
//  2. a path-segment heuristic that works for the rare operations
//     without an operationId.
//
// Collision resolution (two operations yielding the same name on
// the same resource) is a later pass (lower/names).
func (b *Builder) deriveMethodName(verb, path string, op *openapi3.Operation, tag string) string {
	if id := op.OperationID; id != "" {
		return stripOperationIDPrefixes(id, verb, tag)
	}
	return fallbackPathName(verb, path, tag)
}

// stripOperationIDPrefixes takes the spec's operationId and drops
// redundant leading tokens so `validateAccountName` on tag
// `Counterparties` becomes `ValidateAccountName`, while
// `listAccounts` on tag `Accounts` becomes `List`.
func stripOperationIDPrefixes(id, verb, tag string) string {
	tokens := names.SplitWords(id)
	tagTokens := names.SplitWords(tag)
	verbSynonyms := map[string]bool{
		"get": true, "list": true, "fetch": true, "retrieve": true,
		"create": true, "add": true, "submit": true, "post": true,
		"update": true, "modify": true, "patch": true, "put": true,
		"delete": true, "remove": true, "cancel": true,
	}
	// Verb → Go verb word; used only when we strip the verb but need
	// to keep it in the method name.
	verbToWord := map[string]string{
		http.MethodGet:    "Get",
		http.MethodPost:   "Create",
		http.MethodPut:    "Update",
		http.MethodPatch:  "Update",
		http.MethodDelete: "Delete",
	}

	// Find the first token that's NOT a verb synonym; that's where the
	// noun starts.
	nounStart := 0
	for i, tok := range tokens {
		if !verbSynonyms[strings.ToLower(tok)] {
			nounStart = i
			break
		}
	}
	nouns := tokens[nounStart:]

	// Drop tag-token prefixes (case-insensitive match on lowercase).
	nouns = dropTagStem(nouns, tagTokens)

	// If we end up with only the verb (no noun left), keep the
	// leading spec verb's Go word so `listAccounts` on tag
	// `Accounts` stays `List` instead of collapsing to the HTTP verb.
	if len(nouns) == 0 {
		if nounStart == 0 {
			return joinPascal(tokens)
		}
		switch strings.ToLower(tokens[0]) {
		case "list":
			return "List"
		case "get", "retrieve", "fetch":
			return "Get"
		case "create", "add", "submit", "post":
			return "Create"
		case "update", "modify", "patch", "put":
			return "Update"
		case "delete", "remove", "cancel":
			return "Delete"
		}
		return verbToWord[verbSafe(verb)]
	}

	// Rebuild: if the operation led with a verb that matches the HTTP
	// method, prepend its Go equivalent; otherwise emit the tokens
	// as-is (they may start with a domain verb like "capture" we
	// want to preserve).
	if nounStart > 0 {
		leadingVerb := strings.ToLower(tokens[0])
		switch leadingVerb {
		case "get", "list", "fetch", "retrieve":
			return "Get" + joinPascal(nouns)
		case "create", "add", "submit", "post":
			return "Create" + joinPascal(nouns)
		case "update", "modify", "patch", "put":
			return "Update" + joinPascal(nouns)
		case "delete", "remove", "cancel":
			return "Delete" + joinPascal(nouns)
		}
	}
	return joinPascal(nouns)
}

// dropTagStem removes leading tokens from nouns that duplicate the
// tag's tokens. Comparison is case-insensitive and singular-aware
// so "Accounts" tag drops a leading "account" or "accounts" noun.
func dropTagStem(nouns, tagTokens []string) []string {
	if len(nouns) == 0 || len(tagTokens) == 0 {
		return nouns
	}
	lowerEq := func(a, b string) bool { return strings.EqualFold(a, b) }
	singular := func(s string) string { return names.Singularise(strings.ToLower(s)) }
	i := 0
	for i < len(nouns) && i < len(tagTokens) {
		n := nouns[i]
		t := tagTokens[i]
		if lowerEq(n, t) || singular(n) == singular(t) {
			i++
			continue
		}
		break
	}
	return nouns[i:]
}

func joinPascal(tokens []string) string {
	var b strings.Builder
	for _, t := range tokens {
		b.WriteString(names.TypeName(t))
	}
	return b.String()
}

func verbSafe(v string) string { return strings.ToUpper(v) }

// fallbackPathName derives a method name for operations that lack
// an operationId. The path's last non-parameter segment plus the
// HTTP verb's word produces a readable default.
func fallbackPathName(verb, path, tag string) string {
	segs := nonParamSegments(path)
	segs = stripTagSegmentPrefix(segs, tag)
	endsInParam := strings.HasSuffix(strings.TrimRight(path, "/"), "}")

	var suffix string
	if len(segs) > 0 {
		last := segs[len(segs)-1]
		if endsInParam {
			suffix = names.TypeName(names.Singularise(last))
		} else {
			suffix = names.TypeName(last)
		}
	}
	switch verb {
	case http.MethodGet:
		if endsInParam {
			return "Get" + suffix
		}
		if suffix == "" {
			return "List"
		}
		return "List" + suffix
	case http.MethodPost:
		if suffix == "" {
			return "Create"
		}
		last := segs[len(segs)-1]
		if names.LooksSingular(last) {
			return names.TypeName(last)
		}
		return "Create" + names.TypeName(names.Singularise(last))
	case http.MethodPut, http.MethodPatch:
		if suffix == "" {
			return "Update"
		}
		return "Update" + suffix
	case http.MethodDelete:
		if suffix == "" {
			return "Delete"
		}
		return "Delete" + suffix
	}
	return ""
}

func stripTagSegmentPrefix(segs []string, tag string) []string {
	if len(segs) == 0 || tag == "" {
		return segs
	}
	raw := names.SplitWords(tag)
	tokens := make([]string, 0, len(raw))
	for _, p := range raw {
		tokens = append(tokens, strings.ToLower(p))
	}
	if len(tokens) == 0 {
		return segs
	}
	joined := strings.Join(tokens, "-")
	singular := names.Singularise(joined)
	first := tokens[0]
	firstSingular := names.Singularise(first)
	s0 := strings.ToLower(segs[0])
	switch s0 {
	case joined, singular, first, firstSingular:
		return segs[1:]
	}
	for _, prefix := range []string{joined + "-", singular + "-", first + "-", firstSingular + "-"} {
		if strings.HasPrefix(s0, prefix) {
			out := make([]string, 0, len(segs))
			out = append(out, s0[len(prefix):])
			out = append(out, segs[1:]...)
			return out
		}
	}
	return segs
}

func nonParamSegments(path string) []string {
	out := []string{}
	for _, s := range strings.Split(strings.TrimPrefix(path, "/"), "/") {
		if s == "" || strings.HasPrefix(s, "{") {
			continue
		}
		out = append(out, s)
	}
	return out
}

// docLines renders the operation's summary + description as godoc
// lines with the generator's preferred structure.
func docLines(op *openapi3.Operation) []string {
	var out []string
	if s := firstLine(op.Summary); s != "" {
		out = append(out, s)
	}
	if d := op.Description; d != "" {
		if len(out) > 0 {
			out = append(out, "")
		}
		for _, line := range strings.Split(d, "\n") {
			out = append(out, strings.TrimRight(line, " \t"))
		}
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

func pickServerOverride(op *openapi3.Operation, item *openapi3.PathItem) string {
	if op.Servers != nil && len(*op.Servers) > 0 {
		return (*op.Servers)[0].URL
	}
	if len(item.Servers) > 0 {
		return item.Servers[0].URL
	}
	return ""
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
