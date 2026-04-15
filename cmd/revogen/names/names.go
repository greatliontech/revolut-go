// Package names provides the string-to-identifier conversions the
// generator uses when turning spec names (snake_case, kebab-case,
// camelCase, prose) into Go-friendly identifiers. Every function is
// pure; no package state beyond the initialism set.
package names

import (
	"strings"
	"unicode"
)

// initialisms the generator capitalises wholesale when converting
// snake_case to Go identifiers ("request_id" → "RequestID", not
// "RequestId"). Extend in initialisms.go.
var initialisms = defaultInitialisms()

// Register adds an acronym to the initialism set (all-caps form).
// Call from init() in a dedicated file to keep the default set
// declarative.
func Register(acronym string) {
	initialisms[strings.ToUpper(acronym)] = true
}

// TypeName turns a spec schema or tag name into an exported Go type
// name. Handles snake_case, kebab-case, spaces, camelCase.
func TypeName(s string) string {
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

// FieldName turns a JSON field name into an exported Go field name.
func FieldName(s string) string { return TypeName(s) }

// ParamName turns a spec path parameter (snake_case or camelCase)
// into an unexported Go parameter name. The first word is
// lowercased, subsequent words use initialism handling.
func ParamName(s string) string {
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

// CanonicalParamKey normalises a parameter name for case- and
// separator-insensitive lookup: lowercased, underscores and hyphens
// stripped. `card_invitation_id`, `cardInvitationId`, and
// `card-invitation-id` all collapse to `cardinvitationid`.
func CanonicalParamKey(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if r == '_' || r == '-' {
			continue
		}
		if r >= 'A' && r <= 'Z' {
			r += 'a' - 'A'
		}
		b.WriteRune(r)
	}
	return b.String()
}

// CamelToKebab turns "getAccount" into "get-account". Used for
// operationId → URL slug conversion for doc links.
func CamelToKebab(s string) string {
	var b strings.Builder
	for i, r := range s {
		if i > 0 && unicode.IsUpper(r) {
			b.WriteByte('-')
		}
		b.WriteRune(unicode.ToLower(r))
	}
	return b.String()
}

// Singularise is a conservative English plural-to-singular converter.
// It handles enough to derive `Account` from `accounts` in path
// segments without a full pluraliser.
func Singularise(s string) string {
	switch {
	case strings.HasSuffix(s, "ies") && len(s) > 3:
		return s[:len(s)-3] + "y"
	case strings.HasSuffix(s, "ses"), strings.HasSuffix(s, "xes"), strings.HasSuffix(s, "zes"):
		return s[:len(s)-2]
	case strings.HasSuffix(s, "s") && !strings.HasSuffix(s, "ss"):
		return s[:len(s)-1]
	}
	return s
}

// LooksSingular reports whether s is already its own singular form.
func LooksSingular(s string) bool { return Singularise(s) == s }

// SplitWords breaks a string into word tokens on non-letter/digit
// boundaries and on camelCase humps.
func SplitWords(s string) []string { return splitWords(s) }

func splitWords(s string) []string {
	parts := strings.FieldsFunc(s, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	out := make([]string, 0, len(parts))
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
