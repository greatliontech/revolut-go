package build

import (
	"sort"

	"github.com/greatliontech/revolut-go/cmd/revogen/ir"
)

// buildErrorType identifies the spec's consistent error-body schema,
// if any, and records it on the Spec so the emitter can generate a
// typed `<pkg>.Error` wrapper. Detection heuristic: pick the most
// frequently referenced schema across 4xx/5xx JSON responses. Ties
// are broken by ASCII order so output is deterministic.
//
// Specs without a consistent error shape leave ErrorType empty; the
// transport still surfaces core.APIError on non-2xx.
func (b *Builder) buildErrorType() {
	if b.doc.Info != nil {
		b.apiVer = b.doc.Info.Version
	}
	if b.doc.Paths == nil {
		return
	}
	counts := map[string]int{}
	for _, item := range b.doc.Paths.Map() {
		if item == nil {
			continue
		}
		for _, op := range item.Operations() {
			if op == nil || op.Responses == nil {
				continue
			}
			for code, rr := range op.Responses.Map() {
				if !isErrorCode(code) {
					continue
				}
				if rr == nil || rr.Value == nil || rr.Value.Content == nil {
					continue
				}
				mt := rr.Value.Content["application/json"]
				if mt == nil || mt.Schema == nil {
					continue
				}
				if name := schemaRefName(mt.Schema); name != "" {
					counts[b.resolvedName(name)]++
				}
			}
		}
	}
	b.errorType = pickErrorSchema(counts, b.declByName)
}

// pickErrorSchema returns the Go name of the struct that most
// frequently appears as a JSON error body, or "" when no single
// struct dominates. Only struct Decls qualify; aliases and enums
// can't carry Code/Message fields meaningfully.
func pickErrorSchema(counts map[string]int, declByName map[string]*ir.Decl) string {
	type entry struct {
		name  string
		count int
	}
	var entries []entry
	for name, n := range counts {
		decl, ok := declByName[name]
		if !ok || decl.Kind != ir.DeclStruct {
			continue
		}
		entries = append(entries, entry{name: name, count: n})
	}
	if len(entries) == 0 {
		return ""
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].count != entries[j].count {
			return entries[i].count > entries[j].count
		}
		return entries[i].name < entries[j].name
	})
	return entries[0].name
}

// isErrorCode reports whether an HTTP status code represents a
// client or server error.
func isErrorCode(code string) bool {
	if len(code) == 0 {
		return false
	}
	switch code[0] {
	case '4', '5':
		return true
	}
	return false
}
