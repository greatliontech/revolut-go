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
		if m.Pagination != nil {
			writePaginationMethod(w, spec, m)
		}
	}
	return w.buf.String()
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
	if m.Returns == nil {
		return "error"
	}
	expr := m.Returns.GoExpr()
	if !m.Returns.IsSlice() && !isInterfaceReturn(m) && !isRawBytesReturn(m) {
		expr = "*" + expr
	}
	return "(" + expr + ", error)"
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
	if m.Returns == nil {
		return ""
	}
	return "nil, "
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

	switch m.HTTPCall.BodyKind {
	case ir.BodyJSON, ir.BodyNone:
		writeJSONHTTPCall(w, m, pathExpr, bodyArg)
	case ir.BodyForm, ir.BodyMultipart, ir.BodyRawStream:
		writeRawHTTPCall(w, m, pathExpr)
	}
	_ = spec
}

func writeJSONHTTPCall(w *fileWriter, m *ir.Method, pathExpr, bodyArg string) {
	httpVerb := "http.Method" + httpVerbWord(m.HTTPCall.Method)
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
			w.write("\t\tRawContentType: \"application/octet-stream\",\n")
		}
	case ir.BodyMultipart:
		// RawBody/RawContentType filled in below after invoking the
		// per-type encoder.
	}
	if m.HTTPCall.Accept != "" {
		w.printf("\t\tAccept: %q,\n", m.HTTPCall.Accept)
	}
	w.write("\t}\n")

	if m.HTTPCall.BodyKind == ir.BodyMultipart {
		w.write("\tmpBody, mpCT, err := req.encodeMultipart()\n")
		w.write("\tif err != nil {\n")
		w.printf("\t\treturn %serr\n", zeroForReturn(m))
		w.write("\t}\n")
		w.write("\tr.RawBody = mpBody\n")
		w.write("\tr.RawContentType = mpCT\n")
	}

	httpVerb := "http.Method" + httpVerbWord(m.HTTPCall.Method)
	w.printf("\tbody, _, err := s.t.DoRaw(ctx, %s, %s, r)\n", httpVerb, pathExpr)
	w.write("\tif err != nil {\n")
	w.printf("\t\treturn %serr\n", zeroForReturn(m))
	w.write("\t}\n")

	switch m.HTTPCall.RespKind {
	case ir.RespNone:
		w.write("\t_ = body\n\treturn nil\n")
	case ir.RespRawBytes:
		w.write("\treturn body, nil\n")
	case ir.RespJSONList:
		w.printf("\tvar out %s\n", m.HTTPCall.RespType.GoExpr())
		w.write("\tif err := json.Unmarshal(body, &out); err != nil {\n")
		w.printf("\t\treturn %serr\n", zeroForReturn(m))
		w.write("\t}\n\treturn out, nil\n")
	default:
		w.printf("\tvar out %s\n", m.HTTPCall.RespType.GoExpr())
		w.write("\tif err := json.Unmarshal(body, &out); err != nil {\n")
		w.printf("\t\treturn %serr\n", zeroForReturn(m))
		w.write("\t}\n\treturn &out, nil\n")
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
		tokenExpr := "resp." + p.NextTokenField
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
		w.printf("\t\t\tp.%s = %s\n", p.PageTokenParam, assign)
	case ir.PaginationTimeWindow:
		w.write("\t\t\tif len(resp) == 0 { return }\n")
		w.write("\t\t\tfor _, item := range resp {\n")
		w.write("\t\t\t\tif !yield(item, nil) { return }\n\t\t\t}\n")
		w.printf("\t\t\tp.%s = resp[len(resp)-1].%s\n", p.AdvanceParam, p.AdvanceFromItem)
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
