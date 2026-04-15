package main

import (
	"strings"
	"unicode"
)

// initialisms the generator capitalises wholesale when converting
// snake_case to Go identifiers ("request_id" → "RequestID", not
// "RequestId"). Extend as needed.
var initialisms = map[string]bool{
	"ID":   true,
	"URL":  true,
	"URI":  true,
	"API":  true,
	"HTTP": true,
	"UUID": true,
	"IBAN": true,
	"BIC":  true,
	"SWIFT": true,
	"JWT":  true,
	"JSON": true,
}

// goTypeName turns a spec schema or tag name into an exported Go type
// name. Handles snake_case, kebab-case, and spaces.
func goTypeName(s string) string {
	if s == "" {
		return ""
	}
	parts := splitWords(s)
	var b strings.Builder
	for _, p := range parts {
		b.WriteString(titleWithInitialism(p))
	}
	return b.String()
}

// goFieldName turns a JSON field name into an exported Go field name.
func goFieldName(s string) string {
	return goTypeName(s)
}

// goParamName turns a spec path parameter (snake_case) into an
// unexported Go parameter name.
func goParamName(s string) string {
	parts := splitWords(s)
	if len(parts) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(strings.ToLower(parts[0]))
	for _, p := range parts[1:] {
		b.WriteString(titleWithInitialism(p))
	}
	return b.String()
}

// splitWords breaks a string into word tokens on non-letter/digit
// boundaries and also on camelCase humps.
func splitWords(s string) []string {
	// First split on non-alphanumeric.
	parts := strings.FieldsFunc(s, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	// Then split camelCase humps within each token.
	out := []string{}
	for _, p := range parts {
		out = append(out, splitCamel(p)...)
	}
	return out
}

func splitCamel(s string) []string {
	if s == "" {
		return nil
	}
	out := []string{}
	start := 0
	rs := []rune(s)
	for i := 1; i < len(rs); i++ {
		if unicode.IsUpper(rs[i]) && unicode.IsLower(rs[i-1]) {
			out = append(out, string(rs[start:i]))
			start = i
		}
	}
	out = append(out, string(rs[start:]))
	return out
}

func titleWithInitialism(p string) string {
	if p == "" {
		return ""
	}
	up := strings.ToUpper(p)
	if initialisms[up] {
		return up
	}
	return strings.ToUpper(p[:1]) + strings.ToLower(p[1:])
}

// camelToKebab turns "getAccount" into "get-account".
func camelToKebab(s string) string {
	var b strings.Builder
	for i, r := range s {
		if i > 0 && unicode.IsUpper(r) {
			b.WriteByte('-')
		}
		b.WriteRune(unicode.ToLower(r))
	}
	return b.String()
}
