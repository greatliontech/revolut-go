package emit

import (
	"strings"

	"github.com/greatliontech/revolut-go/cmd/revogen/ir"
	"github.com/greatliontech/revolut-go/cmd/revogen/names"
)

// writeResourceFile emits gen_<resource>.go: the resource struct,
// any per-method Params types, the methods themselves, and the
// pagination iterators.
func writeResourceFile(spec *ir.Spec, r *ir.Resource, imports []string) string {
	w := newFileWriter(spec.Package, imports)
	w.header()

	w.printf("// %s groups the %s endpoints.\n", r.Name, r.Name)
	w.printf("type %s struct {\n", r.Name)
	w.write("\tt *transport.Transport\n")
	w.write("}\n\n")

	for _, m := range r.Methods {
		// Render an `<Op>Params` type next to its method when we have
		// one. The Decl is in spec.Decls (and gets emitted in
		// gen_types.go as well) — not duplicated here. Methods
		// reference it by Named type via OptsParam.
		writeMethod(w, spec, m)
		if m.EmitSignedVariant {
			writeSignedMethod(w, spec, m)
		}
		if m.Pagination != nil {
			writePaginationMethod(w, spec, m)
		}
	}
	return w.buf.String()
}

// writeSignedMethod emits the `<Name>Signed` companion that routes
// through DoRaw to preserve the untouched response bytes, decodes
// JSON into the typed payload, and returns a Signed[T] bundling
// typed, raw, and metadata. Callers run detached-JWS verification
// against the raw bytes.
func writeSignedMethod(w *fileWriter, spec *ir.Spec, m *ir.Method) {
	if m.Returns == nil || m.HTTPCall.RespKind != ir.RespJSONValue {
		// Signed only makes sense for typed JSON payloads. Raw-bytes
		// responses already hand the caller bytes; union-returning
		// methods are dispatcher output, not a single verifiable
		// shape. Skip silently — the allowlist classifier has
		// already flagged which methods declare x-jws-signature.
		return
	}
	w.printf("// %sSigned is %s with the raw response body and ResponseMetadata\n", m.Name, m.Name)
	w.write("// preserved alongside the typed payload. Use it when you need to verify\n")
	w.write("// the detached x-jws-signature header against the untouched bytes.\n")
	w.printf("func (s *%s) %sSigned(%s) (*Signed[%s], error) {\n",
		m.Receiver, m.Name, signedMethodParamList(m), m.Returns.GoExpr())
	writePathParamValidatorsSigned(w, spec, m)
	writeFieldValidatorsSigned(w, m)
	writeSignedHTTPCall(w, m)
	w.write("}\n\n")
}

// signedMethodParamList mirrors methodParamList but returns the
// parameter list without the trailing newline handling of the
// main emitter.
func signedMethodParamList(m *ir.Method) string {
	return methodParamList(m)
}

func writePathParamValidatorsSigned(w *fileWriter, spec *ir.Spec, m *ir.Method) {
	for _, p := range m.PathParams {
		w.printf("\tif %s == \"\" {\n", p.Name)
		wire := p.WireName
		if wire == "" {
			wire = p.Name
		}
		w.printf("\t\treturn nil, errors.New(%q)\n", spec.ErrPrefix+": "+wire+" is required")
		w.write("\t}\n")
		if p.Format == "uuid" {
			w.printf("\tif !validate.IsUUID(%s) {\n", p.Name)
			w.printf("\t\treturn nil, errors.New(%q)\n", spec.ErrPrefix+": "+wire+" must be a valid UUID")
			w.write("\t}\n")
		}
	}
	for _, hp := range m.HeaderParams {
		if !hp.Required {
			continue
		}
		if hp.Type == nil {
			continue
		}
		if !(hp.Type.Kind == ir.KindPrim && hp.Type.Name == "string") && hp.Type.Kind != ir.KindNamed {
			continue
		}
		wire := headerWireName(hp)
		w.printf("\tif %s == \"\" {\n", hp.Name)
		w.printf("\t\treturn nil, errors.New(%q)\n", spec.ErrPrefix+": "+wire+" is required")
		w.write("\t}\n")
	}
}

func writeFieldValidatorsSigned(w *fileWriter, m *ir.Method) {
	for _, v := range m.Validators {
		w.printf("\tif %s {\n", v.Cond)
		w.printf("\t\treturn nil, errors.New(%q)\n", v.Message)
		w.write("\t}\n")
	}
}

// writeSignedHTTPCall issues the raw request and assembles Signed.
// Headers always come back from DoRaw; JSON unmarshal happens
// after the raw slice is captured so both halves are available.
func writeSignedHTTPCall(w *fileWriter, m *ir.Method) {
	pathExpr := renderPathExpr(m)
	if m.OptsParam != nil {
		w.printf("\tpath := %s\n", pathExpr)
		w.write("\tif q := opts.encode().Encode(); q != \"\" {\n")
		w.write("\t\tpath += \"?\" + q\n")
		w.write("\t}\n")
		pathExpr = "path"
	}
	w.write("\tr := transport.RawRequest{\n")
	if m.BodyParam != nil && m.HTTPCall.BodyKind == ir.BodyJSON {
		w.printf("\t\tJSONBody: %s,\n", m.BodyParam.Name)
	}
	w.write("\t}\n")
	if len(m.HeaderParams) > 0 {
		w.write("\tr.Headers = http.Header{}\n")
		for _, hp := range m.HeaderParams {
			writeHeaderSet(w, hp)
		}
	}
	httpVerb := "http.Method" + httpVerbWord(m.HTTPCall.Method)
	w.printf("\tbody, hdr, err := s.t.DoRaw(ctx, %s, %s, r)\n", httpVerb, pathExpr)
	w.write("\tif err != nil { return nil, err }\n")
	w.printf("\tvar out %s\n", m.HTTPCall.RespType.GoExpr())
	// Empty 2xx (204 No Content, async endpoints returning 200 without
	// a body) leave out at its zero value; Signed still returns the
	// raw bytes + metadata so callers can inspect what the server sent.
	w.write("\tif len(body) > 0 {\n")
	w.write("\t\tif err := json.Unmarshal(body, &out); err != nil { return nil, err }\n")
	w.write("\t}\n")
	w.printf("\treturn &Signed[%s]{Typed: &out, Raw: body, Metadata: extractResponseMetadata(hdr)}, nil\n",
		m.Returns.GoExpr())
}

func writeMethod(w *fileWriter, spec *ir.Spec, m *ir.Method) {
	// First doc line gets prefixed with the method name (Go
	// convention "// Foo does X."). Subsequent lines pass through.
	if len(m.Doc) > 0 {
		w.printf("// %s %s\n", m.Name, lower1(m.Doc[0]))
		for _, line := range m.Doc[1:] {
			if line == "" {
				w.write("//\n")
				continue
			}
			w.printf("// %s\n", line)
		}
	} else {
		w.printf("// %s performs the %s operation.\n", m.Name, m.Name)
	}
	if m.DocURL != "" {
		w.write("//\n")
		w.printf("// Docs: %s\n", m.DocURL)
	}
	if len(m.Scopes) > 0 {
		w.printf("// Required scopes: %s\n", strings.Join(m.Scopes, ", "))
	}
	if m.Deprecated != "" {
		w.printf("//\n// Deprecated: %s\n", m.Deprecated)
	}
	w.printf("func (s *%s) %s(%s) %s {\n", m.Receiver, m.Name, methodParamList(m), methodReturnList(m))
	writePathParamValidators(w, spec, m)
	writeFieldValidators(w, m)
	writeHTTPCall(w, spec, m)
	w.write("}\n\n")
}

func methodParamList(m *ir.Method) string {
	parts := []string{"ctx context.Context"}
	for _, p := range m.PathParams {
		parts = append(parts, p.Name+" "+p.Type.GoExpr())
	}
	for _, p := range m.HeaderParams {
		parts = append(parts, p.Name+" "+p.Type.GoExpr())
	}
	if m.BodyParam != nil {
		parts = append(parts, m.BodyParam.Name+" "+m.BodyParam.Type.GoExpr())
	}
	if m.OptsParam != nil {
		parts = append(parts, m.OptsParam.Name+" "+m.OptsParam.Type.GoExpr())
	}
	return strings.Join(parts, ", ")
}

func methodReturnList(m *ir.Method) string {
	meta := emitsResponseMetadata(m)
	if m.Returns == nil {
		if meta {
			return "(ResponseMetadata, error)"
		}
		return "error"
	}
	expr := m.Returns.GoExpr()
	if !m.Returns.IsSlice() && !isInterfaceReturn(m) && !isRawBytesReturn(m) {
		expr = "*" + expr
	}
	if meta {
		return "(" + expr + ", ResponseMetadata, error)"
	}
	return "(" + expr + ", error)"
}

// emitsResponseMetadata reports whether m needs to surface
// ResponseMetadata alongside its return. Separate predicate so
// every other place that has to reason about the return shape
// (zero-value for error returns, method body, etc.) agrees.
func emitsResponseMetadata(m *ir.Method) bool {
	return len(m.ResponseMetadata) > 0
}

func isInterfaceReturn(m *ir.Method) bool {
	return m.HTTPCall.RespKind == ir.RespUnionTagged || m.HTTPCall.RespKind == ir.RespUnionProbe
}

func isRawBytesReturn(m *ir.Method) bool {
	return m.HTTPCall.RespKind == ir.RespRawBytes
}

func writePathParamValidators(w *fileWriter, spec *ir.Spec, m *ir.Method) {
	zero := zeroForReturn(m)
	for _, p := range m.PathParams {
		w.printf("\tif %s == \"\" {\n", p.Name)
		wire := p.WireName
		if wire == "" {
			wire = p.Name
		}
		w.printf("\t\treturn %serrors.New(%q)\n", zero, spec.ErrPrefix+": "+wire+" is required")
		w.write("\t}\n")
		if p.Format == "uuid" {
			w.printf("\tif !validate.IsUUID(%s) {\n", p.Name)
			w.printf("\t\treturn %serrors.New(%q)\n", zero, spec.ErrPrefix+": "+wire+" must be a valid UUID")
			w.write("\t}\n")
		}
	}
	// Required string-typed header params get the same empty-string
	// pre-flight check. Non-string headers (int / bool) have no
	// meaningful zero sentinel, so those stay unchecked — Required
	// on a numeric header is a spec oddity the server can surface.
	for _, hp := range m.HeaderParams {
		if !hp.Required {
			continue
		}
		if hp.Type == nil {
			continue
		}
		if !(hp.Type.Kind == ir.KindPrim && hp.Type.Name == "string") && hp.Type.Kind != ir.KindNamed {
			continue
		}
		wire := headerWireName(hp)
		w.printf("\tif %s == \"\" {\n", hp.Name)
		w.printf("\t\treturn %serrors.New(%q)\n", zero, spec.ErrPrefix+": "+wire+" is required")
		w.write("\t}\n")
	}
}

func writeFieldValidators(w *fileWriter, m *ir.Method) {
	zero := zeroForReturn(m)
	for _, v := range m.Validators {
		w.printf("\tif %s {\n", v.Cond)
		w.printf("\t\treturn %serrors.New(%q)\n", zero, v.Message)
		w.write("\t}\n")
	}
}

// zeroForReturn returns the zero value (with trailing comma) for
// the method's success return type, used in `return X, err` lines
// when the method has a value return alongside the error.
func zeroForReturn(m *ir.Method) string {
	var b strings.Builder
	if m.Returns != nil {
		b.WriteString("nil, ")
	}
	if emitsResponseMetadata(m) {
		b.WriteString("ResponseMetadata{}, ")
	}
	return b.String()
}

func writeHTTPCall(w *fileWriter, spec *ir.Spec, m *ir.Method) {
	pathExpr := renderPathExpr(m)
	if m.OptsParam != nil {
		w.printf("\tpath := %s\n", pathExpr)
		w.write("\tif q := opts.encode().Encode(); q != \"\" {\n")
		w.write("\t\tpath += \"?\" + q\n")
		w.write("\t}\n")
		pathExpr = "path"
	}

	bodyArg := "nil"
	if m.BodyParam != nil {
		bodyArg = m.BodyParam.Name
	}

	// Methods that need to attach request headers (open-banking's
	// x-fapi-*, etc.) always go through DoRaw because it's the only
	// transport entry point that forwards a header map. JSON
	// request/response bodies still work — DoRaw marshals the
	// JSONBody and the emitted code json.Unmarshals the returned
	// byte slice.
	forceRaw := len(m.HeaderParams) > 0
	switch m.HTTPCall.BodyKind {
	case ir.BodyJSON, ir.BodyNone:
		if forceRaw {
			writeRawHTTPCall(w, m, pathExpr)
		} else {
			writeJSONHTTPCall(w, m, pathExpr, bodyArg)
		}
	case ir.BodyForm, ir.BodyMultipart, ir.BodyRawStream:
		writeRawHTTPCall(w, m, pathExpr)
	}
	_ = spec
}

func writeJSONHTTPCall(w *fileWriter, m *ir.Method, pathExpr, bodyArg string) {
	httpVerb := "http.Method" + httpVerbWord(m.HTTPCall.Method)
	if emitsResponseMetadata(m) {
		writeJSONHTTPCallWithHeaders(w, m, pathExpr, bodyArg, httpVerb)
		return
	}
	switch m.HTTPCall.RespKind {
	case ir.RespNone:
		w.printf("\treturn s.t.Do(ctx, %s, %s, %s, nil)\n", httpVerb, pathExpr, bodyArg)
	case ir.RespJSONValue:
		w.printf("\tvar out %s\n", m.HTTPCall.RespType.GoExpr())
		w.printf("\tif err := s.t.Do(ctx, %s, %s, %s, &out); err != nil {\n", httpVerb, pathExpr, bodyArg)
		w.write("\t\treturn nil, err\n")
		w.write("\t}\n")
		w.write("\treturn &out, nil\n")
	case ir.RespJSONList:
		w.printf("\tvar out %s\n", m.HTTPCall.RespType.GoExpr())
		w.printf("\tif err := s.t.Do(ctx, %s, %s, %s, &out); err != nil {\n", httpVerb, pathExpr, bodyArg)
		w.write("\t\treturn nil, err\n")
		w.write("\t}\n")
		w.write("\treturn out, nil\n")
	case ir.RespUnionTagged, ir.RespUnionProbe:
		w.write("\tvar raw json.RawMessage\n")
		w.printf("\tif err := s.t.Do(ctx, %s, %s, %s, &raw); err != nil {\n", httpVerb, pathExpr, bodyArg)
		w.write("\t\treturn nil, err\n")
		w.write("\t}\n")
		w.printf("\treturn decode%s(raw)\n", m.HTTPCall.RespType.GoExpr())
	case ir.RespRawBytes:
		// Reachable only if BodyKind was JSON but resp is raw bytes;
		// we still go through DoRaw because Do can't surface bytes.
		writeRawHTTPCall(w, m, pathExpr)
	}
}

// writeJSONHTTPCallWithHeaders is the metadata-bearing twin of
// writeJSONHTTPCall — routes through DoWithHeaders so the 2xx
// response's http.Header is available to populate ResponseMetadata
// before the method returns.
func writeJSONHTTPCallWithHeaders(w *fileWriter, m *ir.Method, pathExpr, bodyArg, httpVerb string) {
	switch m.HTTPCall.RespKind {
	case ir.RespNone:
		w.printf("\thdr, err := s.t.DoWithHeaders(ctx, %s, %s, %s, nil)\n", httpVerb, pathExpr, bodyArg)
		w.write("\tif err != nil {\n\t\treturn ResponseMetadata{}, err\n\t}\n")
		w.write("\treturn extractResponseMetadata(hdr), nil\n")
	case ir.RespJSONValue:
		w.printf("\tvar out %s\n", m.HTTPCall.RespType.GoExpr())
		w.printf("\thdr, err := s.t.DoWithHeaders(ctx, %s, %s, %s, &out)\n", httpVerb, pathExpr, bodyArg)
		w.write("\tif err != nil {\n\t\treturn nil, ResponseMetadata{}, err\n\t}\n")
		w.write("\treturn &out, extractResponseMetadata(hdr), nil\n")
	case ir.RespJSONList:
		w.printf("\tvar out %s\n", m.HTTPCall.RespType.GoExpr())
		w.printf("\thdr, err := s.t.DoWithHeaders(ctx, %s, %s, %s, &out)\n", httpVerb, pathExpr, bodyArg)
		w.write("\tif err != nil {\n\t\treturn nil, ResponseMetadata{}, err\n\t}\n")
		w.write("\treturn out, extractResponseMetadata(hdr), nil\n")
	case ir.RespUnionTagged, ir.RespUnionProbe:
		w.write("\tvar raw json.RawMessage\n")
		w.printf("\thdr, err := s.t.DoWithHeaders(ctx, %s, %s, %s, &raw)\n", httpVerb, pathExpr, bodyArg)
		w.write("\tif err != nil {\n\t\treturn nil, ResponseMetadata{}, err\n\t}\n")
		w.printf("\tout, err := decode%s(raw)\n", m.HTTPCall.RespType.GoExpr())
		w.write("\tif err != nil {\n\t\treturn nil, ResponseMetadata{}, err\n\t}\n")
		w.write("\treturn out, extractResponseMetadata(hdr), nil\n")
	case ir.RespRawBytes:
		writeRawHTTPCall(w, m, pathExpr)
	}
}

func writeRawHTTPCall(w *fileWriter, m *ir.Method, pathExpr string) {
	w.write("\tr := transport.RawRequest{\n")
	switch m.HTTPCall.BodyKind {
	case ir.BodyJSON:
		if m.BodyParam != nil {
			w.printf("\t\tJSONBody: %s,\n", m.BodyParam.Name)
		}
	case ir.BodyForm:
		w.printf("\t\tFormBody: %s,\n", m.HTTPCall.BodyExpr)
	case ir.BodyRawStream:
		if m.BodyParam != nil {
			w.printf("\t\tRawBody: %s,\n", m.BodyParam.Name)
			ct := m.HTTPCall.BodyContentType
			if ct == "" {
				ct = "application/octet-stream"
			}
			w.printf("\t\tRawContentType: %q,\n", ct)
		}
	case ir.BodyMultipart:
		// RawBody/RawContentType filled in below after invoking the
		// per-type encoder.
	}
	if m.HTTPCall.Accept != "" {
		w.printf("\t\tAccept: %q,\n", m.HTTPCall.Accept)
	}
	w.write("\t}\n")

	// Attach caller-supplied header params (open-banking's
	// x-fapi-*, etc.) to the outgoing request.
	if len(m.HeaderParams) > 0 {
		w.write("\tr.Headers = http.Header{}\n")
		for _, hp := range m.HeaderParams {
			writeHeaderSet(w, hp)
		}
	}

	if m.HTTPCall.BodyKind == ir.BodyMultipart {
		w.write("\tmpBody, mpCT, err := req.encodeMultipart()\n")
		w.write("\tif err != nil {\n")
		w.printf("\t\treturn %serr\n", zeroForReturn(m))
		w.write("\t}\n")
		w.write("\tr.RawBody = mpBody\n")
		w.write("\tr.RawContentType = mpCT\n")
	}

	httpVerb := "http.Method" + httpVerbWord(m.HTTPCall.Method)

	// Streaming path: when the response is declared non-JSON
	// (PDFs, CSV, octet-stream), return io.ReadCloser instead of
	// buffering the body. Caller owns the Close().
	if m.HTTPCall.RespKind == ir.RespRawBytes {
		hdrBind := "_"
		if emitsResponseMetadata(m) {
			hdrBind = "hdr"
		}
		w.printf("\tstream, %s, err := s.t.DoRawStream(ctx, %s, %s, r)\n", hdrBind, httpVerb, pathExpr)
		w.write("\tif err != nil {\n")
		w.printf("\t\treturn %serr\n", zeroForReturn(m))
		w.write("\t}\n")
		if emitsResponseMetadata(m) {
			w.write("\treturn stream, extractResponseMetadata(hdr), nil\n")
		} else {
			w.write("\treturn stream, nil\n")
		}
		return
	}

	hdrBind := "_"
	if emitsResponseMetadata(m) {
		hdrBind = "hdr"
	}
	// Name the response body local so it can't shadow a request
	// body param whose name is also "body" (text/csv, octet-stream).
	respLocal := "body"
	if m.BodyParam != nil && m.BodyParam.Name == "body" {
		respLocal = "respBody"
	}
	w.printf("\t%s, %s, err := s.t.DoRaw(ctx, %s, %s, r)\n", respLocal, hdrBind, httpVerb, pathExpr)
	w.write("\tif err != nil {\n")
	w.printf("\t\treturn %serr\n", zeroForReturn(m))
	w.write("\t}\n")

	metaSuffix := ""
	if emitsResponseMetadata(m) {
		metaSuffix = "extractResponseMetadata(hdr), "
	}
	switch m.HTTPCall.RespKind {
	case ir.RespNone:
		w.printf("\t_ = %s\n", respLocal)
		if emitsResponseMetadata(m) {
			w.write("\treturn extractResponseMetadata(hdr), nil\n")
		} else {
			w.write("\treturn nil\n")
		}
	case ir.RespJSONList:
		w.printf("\tvar out %s\n", m.HTTPCall.RespType.GoExpr())
		// Empty 2xx (204 / async ack) leaves out at its zero value
		// instead of erroring on json.Unmarshal("", ...).
		w.printf("\tif len(%s) > 0 {\n", respLocal)
		w.printf("\t\tif err := json.Unmarshal(%s, &out); err != nil {\n", respLocal)
		w.printf("\t\t\treturn %serr\n", zeroForReturn(m))
		w.write("\t\t}\n")
		w.write("\t}\n")
		w.printf("\treturn out, %snil\n", metaSuffix)
	default:
		w.printf("\tvar out %s\n", m.HTTPCall.RespType.GoExpr())
		w.printf("\tif len(%s) > 0 {\n", respLocal)
		w.printf("\t\tif err := json.Unmarshal(%s, &out); err != nil {\n", respLocal)
		w.printf("\t\t\treturn %serr\n", zeroForReturn(m))
		w.write("\t\t}\n")
		w.write("\t}\n")
		w.printf("\treturn &out, %snil\n", metaSuffix)
	}
}

// renderPathExpr produces the Go expression building the URL.
// When ServerOverride is set, the expression is the absolute URL
// of the override + the path tail; the transport's resolve step
// keeps absolute URLs untouched.
func renderPathExpr(m *ir.Method) string {
	tmpl := strings.TrimPrefix(m.HTTPCall.PathExpr, "/")
	paramByName := map[string]string{}
	for _, p := range m.PathParams {
		paramByName[names.CanonicalParamKey(p.Name)] = p.Name
	}
	var parts []string
	rest := tmpl
	for {
		i := strings.Index(rest, "{")
		if i < 0 {
			if rest != "" {
				parts = append(parts, quote(rest))
			}
			break
		}
		j := strings.Index(rest, "}")
		if j < 0 {
			parts = append(parts, quote(rest))
			break
		}
		literal := rest[:i]
		name := rest[i+1 : j]
		if literal != "" {
			parts = append(parts, quote(literal))
		}
		if goName, ok := paramByName[names.CanonicalParamKey(name)]; ok {
			parts = append(parts, "url.PathEscape("+goName+")")
		} else {
			parts = append(parts, quote("{"+name+"}"))
		}
		rest = rest[j+1:]
	}
	if m.ServerOverride != "" {
		// Prepend the override's full URL, retaining its trailing "/"
		// or absence, then concatenate. Server overrides are absolute
		// URLs that the transport's resolve() returns untouched.
		base := m.ServerOverride
		if !strings.HasSuffix(base, "/") {
			base += "/"
		}
		parts = append([]string{quote(base)}, parts...)
	}
	if len(parts) == 0 {
		return `""`
	}
	return strings.Join(parts, " + ")
}

func quote(s string) string { return "\"" + s + "\"" }

// headerWireName returns the wire header name for a header param.
// Prefers the spec-side WireName if available (e.g. "x-fapi-*"
// survives verbatim); falls back to the Go identifier.
func headerWireName(p ir.Param) string {
	if p.WireName != "" {
		return p.WireName
	}
	return p.Name
}

// writeHeaderSet emits the r.Headers.Set call for one header param.
// String-typed headers get an empty-string guard (matching the
// Revolut spec convention that omitted headers mean "default").
// Numeric and boolean headers are always set — there's no zero
// sentinel. Named string-backed types (spec enums) get the guard
// plus a string(...) conversion.
func writeHeaderSet(w *fileWriter, hp ir.Param) {
	wire := headerWireName(hp)
	t := hp.Type
	switch {
	case t.Kind == ir.KindPrim && t.Name == "string":
		w.printf("\tif %s != \"\" {\n", hp.Name)
		w.printf("\t\tr.Headers.Set(%q, %s)\n", wire, hp.Name)
		w.write("\t}\n")
	case t.Kind == ir.KindNamed:
		w.printf("\tif %s != \"\" {\n", hp.Name)
		w.printf("\t\tr.Headers.Set(%q, string(%s))\n", wire, hp.Name)
		w.write("\t}\n")
	case t.Kind == ir.KindPrim && (t.Name == "int" || t.Name == "int32"):
		w.printf("\tr.Headers.Set(%q, strconv.Itoa(int(%s)))\n", wire, hp.Name)
	case t.Kind == ir.KindPrim && t.Name == "int64":
		w.printf("\tr.Headers.Set(%q, strconv.FormatInt(%s, 10))\n", wire, hp.Name)
	case t.Kind == ir.KindPrim && t.Name == "bool":
		w.printf("\tr.Headers.Set(%q, strconv.FormatBool(%s))\n", wire, hp.Name)
	default:
		w.printf("\tr.Headers.Set(%q, fmt.Sprint(%s))\n", wire, hp.Name)
	}
}


func httpVerbWord(verb string) string {
	if verb == "" {
		return ""
	}
	return strings.ToUpper(verb[:1]) + strings.ToLower(verb[1:])
}

// writePaginationMethod emits `<MethodName>All` returning an
// iter.Seq2 over the iterator.
func writePaginationMethod(w *fileWriter, spec *ir.Spec, m *ir.Method) {
	p := m.Pagination
	name := m.Name + "All"
	w.printf("// %s iterates every page of %s, yielding one %s per\n", name, m.Name, p.ItemType.GoExpr())
	w.write("// step. Break out of the loop to stop early.\n")

	parts := []string{"ctx context.Context"}
	for _, pp := range m.PathParams {
		parts = append(parts, pp.Name+" "+pp.Type.GoExpr())
	}
	for _, hp := range m.HeaderParams {
		parts = append(parts, hp.Name+" "+hp.Type.GoExpr())
	}
	if m.OptsParam != nil {
		parts = append(parts, m.OptsParam.Name+" "+m.OptsParam.Type.GoExpr())
	}
	w.printf("func (s *%s) %s(%s) iter.Seq2[%s, error] {\n",
		m.Receiver, name, strings.Join(parts, ", "), p.ItemType.GoExpr())
	w.printf("\treturn func(yield func(%s, error) bool) {\n", p.ItemType.GoExpr())

	// Copy opts so we can mutate the cursor / advance fields in place.
	if m.OptsParam != nil {
		paramsType := strings.TrimPrefix(m.OptsParam.Type.GoExpr(), "*")
		w.printf("\t\tvar p %s\n", paramsType)
		w.printf("\t\tif %s != nil { p = *%s }\n", m.OptsParam.Name, m.OptsParam.Name)
	}
	for _, pp := range m.PathParams {
		w.printf("\t\tif %s == \"\" {\n", pp.Name)
		w.printf("\t\t\tvar zero %s\n", p.ItemType.GoExpr())
		w.printf("\t\t\tyield(zero, errors.New(%q))\n", spec.ErrPrefix+": "+pp.Name+" is required")
		w.write("\t\t\treturn\n\t\t}\n")
	}
	w.write("\t\tfor {\n")
	// Honour context cancellation before each page: the caller's ctx
	// may already be Done even though the previous page returned
	// cleanly (classic: the caller broke out of the loop in code that
	// returned before reaching the `break`). Without this check the
	// iterator would spend one more round-trip before failing.
	w.printf("\t\t\tif err := ctx.Err(); err != nil {\n")
	w.printf("\t\t\t\tvar zero %s\n", p.ItemType.GoExpr())
	w.write("\t\t\t\tyield(zero, err)\n\t\t\t\treturn\n\t\t\t}\n")
	call := "s." + m.Name + "(ctx"
	for _, pp := range m.PathParams {
		call += ", " + pp.Name
	}
	for _, hp := range m.HeaderParams {
		call += ", " + hp.Name
	}
	if m.OptsParam != nil {
		call += ", &p"
	}
	call += ")"
	w.printf("\t\t\tresp, err := %s\n", call)
	w.write("\t\t\tif err != nil {\n")
	w.printf("\t\t\t\tvar zero %s\n", p.ItemType.GoExpr())
	w.write("\t\t\t\tyield(zero, err)\n\t\t\t\treturn\n\t\t\t}\n")

	switch p.Shape {
	case ir.PaginationCursor:
		w.printf("\t\t\tfor _, item := range resp.%s {\n", p.ItemsField)
		w.write("\t\t\t\tif !yield(item, nil) { return }\n\t\t\t}\n")
		// NextTokenField may be nested: "Metadata.NextCursor".
		// When it is, guard the first segment for nil (common
		// shape: response.Metadata is *Metadata).
		tokenExpr := "resp." + p.NextTokenField
		if dot := strings.Index(p.NextTokenField, "."); dot > 0 {
			parent := "resp." + p.NextTokenField[:dot]
			w.printf("\t\t\tif %s == nil { return }\n", parent)
		}
		if p.NextTokenType != nil && p.NextTokenType.IsPointer() {
			w.printf("\t\t\tif %s == nil || *%s == \"\" { return }\n", tokenExpr, tokenExpr)
			tokenExpr = "*" + tokenExpr
		} else {
			w.printf("\t\t\tif %s == \"\" { return }\n", tokenExpr)
		}
		assign := tokenExpr
		if p.PageTokenType != nil {
			ptName := p.PageTokenType.GoExpr()
			ntName := ""
			if p.NextTokenType != nil {
				ntName = strings.TrimPrefix(p.NextTokenType.GoExpr(), "*")
			}
			if ptName != ntName {
				assign = ptName + "(" + tokenExpr + ")"
			}
		}
		// Stall guard: a buggy server that returns the same cursor
		// would otherwise spin the iterator forever. Compare against
		// the previous token value and stop on equality.
		w.printf("\t\t\tnextTok := %s\n", assign)
		w.printf("\t\t\tif nextTok == p.%s { return }\n", p.PageTokenParam)
		w.printf("\t\t\tp.%s = nextTok\n", p.PageTokenParam)
	case ir.PaginationTimeWindow:
		w.write("\t\t\tif len(resp) == 0 { return }\n")
		w.write("\t\t\tfor _, item := range resp {\n")
		w.write("\t\t\t\tif !yield(item, nil) { return }\n\t\t\t}\n")
		// Stall guard: if the advance field on the last item equals
		// the current cursor, the server isn't making progress.
		w.printf("\t\t\tnextAdv := resp[len(resp)-1].%s\n", p.AdvanceFromItem)
		w.printf("\t\t\tif nextAdv == p.%s { return }\n", p.AdvanceParam)
		w.printf("\t\t\tp.%s = nextAdv\n", p.AdvanceParam)
	case ir.PaginationLimit:
		w.write("\t\t\tif len(resp) == 0 { return }\n")
		w.write("\t\t\tfor _, item := range resp {\n")
		w.write("\t\t\t\tif !yield(item, nil) { return }\n\t\t\t}\n")
		// Limit shape with no explicit cursor — exhaust on a short page.
		if m.OptsParam != nil {
			pf := names.FieldName(p.PageSizeParam)
			w.printf("\t\t\tif p.%s == 0 || int64(len(resp)) < int64(p.%s) { return }\n", pf, pf)
		}
		if p.PageParam != "" {
			w.printf("\t\t\tp.%s++\n", p.PageParam)
		}
	}
	w.write("\t\t}\n\t}\n}\n\n")
}
