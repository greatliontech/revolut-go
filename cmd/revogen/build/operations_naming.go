package build

import (
	"net/http"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"

	"github.com/greatliontech/revolut-go/cmd/revogen/names"
)

// This file holds method-name derivation: picking a Go method
// identifier for each operation from operationId + tag + verb +
// path segments. Isolated here because the rule set has grown
// substantial and the rest of operations.go is unrelated.

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
	return fallbackPathName(verb, path, tag, responseIsList(op))
}

// responseIsList reports whether the operation's 2xx response is
// an array/list shape. Used by the fallback name picker to choose
// Get vs List on a GET whose path doesn't end in a param: crypto-
// ramp's GET /config, /buy, /quote return single objects and
// would otherwise be misnamed List*.
func responseIsList(op *openapi3.Operation) bool {
	if op == nil || op.Responses == nil {
		return false
	}
	for _, code := range []string{"200", "201", "202"} {
		resp := op.Responses.Value(code)
		if resp == nil || resp.Value == nil {
			continue
		}
		mt := resp.Value.Content["application/json"]
		if mt == nil || mt.Schema == nil {
			continue
		}
		if s := mt.Schema.Value; s != nil && schemaTypeIs(s, "array") {
			return true
		}
	}
	return false
}

// stripOperationIDPrefixes takes the spec's operationId and drops
// redundant leading tokens so `validateAccountName` on tag
// `Counterparties` becomes `ValidateAccountName`, while
// `listAccounts` on tag `Accounts` becomes `List`.
//
// Only HTTP-canonical verbs are stripped — `get`/`list`/... on
// GET, `create`/`add`/... on POST, etc. Domain verbs like `cancel`
// or `freeze` survive: an operationId of `cancelCardInvitation` on
// POST stays as `Cancel`, not `Delete`.
func stripOperationIDPrefixes(id, verb, tag string) string {
	tokens := names.SplitWords(id)
	tagTokens := names.SplitWords(tag)
	verbToWord := map[string]string{
		http.MethodGet:    "Get",
		http.MethodPost:   "Create",
		http.MethodPut:    "Update",
		http.MethodPatch:  "Update",
		http.MethodDelete: "Delete",
	}
	httpSynonyms := map[string]map[string]bool{
		http.MethodGet:    {"get": true, "list": true, "fetch": true, "retrieve": true, "read": true},
		http.MethodPost:   {"create": true, "add": true, "submit": true, "post": true},
		http.MethodPut:    {"update": true, "replace": true, "put": true, "set": true},
		http.MethodPatch:  {"update": true, "modify": true, "patch": true},
		http.MethodDelete: {"delete": true, "remove": true, "destroy": true},
	}
	synonyms := httpSynonyms[verb]

	// Find the first token that's NOT a synonym for this HTTP method.
	// At most one leading verb gets stripped — `getList...` would only
	// drop `get`.
	nounStart := 0
	if len(tokens) > 0 && synonyms[strings.ToLower(tokens[0])] {
		nounStart = 1
	}
	nouns := tokens[nounStart:]

	// Track whether the stripped tag-stem token was plural — i.e.
	// `getAccounts` has its `Accounts` stem dropped, leaving an
	// empty noun. The plurality of the dropped token is what
	// distinguishes "List" (collection) from "Get" (single).
	nouns, droppedPlural := dropTagStemTracked(nouns, tagTokens)

	// If we end up with only the verb (no noun left), keep the
	// leading spec verb's Go word — but disambiguate
	// `getAccounts` (List) from `getAccount` (Get) using the
	// plurality of the dropped tag stem.
	if len(nouns) == 0 {
		if nounStart == 0 {
			return joinPascal(tokens)
		}
		leading := strings.ToLower(tokens[0])
		switch leading {
		case "get", "retrieve", "fetch":
			if droppedPlural {
				return "List"
			}
			return "Get"
		case "list":
			return "List"
		case "create", "add", "submit", "post":
			return "Create"
		case "update", "modify", "patch", "put":
			return "Update"
		case "delete", "remove", "cancel":
			return "Delete"
		}
		return verbToWord[verbSafe(verb)]
	}

	// Rebuild: if the operation led with a verb that matches the
	// HTTP method, prepend the canonical Go word for that verb so
	// `getAccountDetails` becomes `GetDetails`. Domain verbs that
	// happen to lead an operationId (e.g. POST `cancelInvitation`)
	// were not stripped above and pass through as the noun.
	if nounStart > 0 {
		leadingVerb := strings.ToLower(tokens[0])
		switch {
		case verb == http.MethodGet && (leadingVerb == "get" || leadingVerb == "list" || leadingVerb == "fetch" || leadingVerb == "retrieve" || leadingVerb == "read"):
			return "Get" + joinPascal(nouns)
		case verb == http.MethodPost && (leadingVerb == "create" || leadingVerb == "add" || leadingVerb == "submit" || leadingVerb == "post"):
			return "Create" + joinPascal(nouns)
		case (verb == http.MethodPut || verb == http.MethodPatch) && (leadingVerb == "update" || leadingVerb == "replace" || leadingVerb == "modify" || leadingVerb == "patch" || leadingVerb == "put" || leadingVerb == "set"):
			return "Update" + joinPascal(nouns)
		case verb == http.MethodDelete && (leadingVerb == "delete" || leadingVerb == "remove" || leadingVerb == "destroy"):
			return "Delete" + joinPascal(nouns)
		}
	}
	return joinPascal(nouns)
}

// dropTagStemTracked is dropTagStem that also reports whether the
// LAST consumed token was the plural form of its tag counterpart.
// The flag drives `getAccounts` → List / `getAccount` → Get
// disambiguation when the stem fully consumes the noun.
func dropTagStemTracked(nouns, tagTokens []string) ([]string, bool) {
	if len(nouns) == 0 || len(tagTokens) == 0 {
		return nouns, false
	}
	lowerEq := func(a, b string) bool { return strings.EqualFold(a, b) }
	singular := func(s string) string { return names.Singularise(strings.ToLower(s)) }
	i := 0
	lastDroppedPlural := false
	for i < len(nouns) && i < len(tagTokens) {
		n := nouns[i]
		t := tagTokens[i]
		switch {
		case lowerEq(n, t):
			lastDroppedPlural = !names.LooksSingular(strings.ToLower(n))
		case singular(n) == singular(t):
			lastDroppedPlural = !names.LooksSingular(strings.ToLower(n))
		default:
			return nouns[i:], lastDroppedPlural
		}
		i++
	}
	return nouns[i:], lastDroppedPlural
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
// HTTP verb's word produces a readable default. respIsList lets
// the picker distinguish Get-single from List-many on a GET whose
// path doesn't end in a param — a spec like crypto-ramp's
// GET /config (returns a single Config object) becomes GetConfig,
// not ListConfig.
func fallbackPathName(verb, path, tag string, respIsList bool) string {
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
		if !respIsList {
			if suffix == "" {
				return "Get"
			}
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
