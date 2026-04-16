// Package validate carries the format-validation helpers the
// generated SDK code calls before issuing an HTTP request. The
// helpers are shared across every generated package (business,
// merchant, open-banking, crypto-ramp, revolut-x) so a bug fix in a
// regex cache or UUID canonical form lives in exactly one place.
//
// Generated code imports this package via its public API. The
// non-hot-path helpers (IsUUID, HasJSONKey) are small enough to
// inline; the bounded-work helpers (MatchPattern, NumberAsFloat)
// allocate caches on first use.
package validate

import (
	"encoding/json"
	"regexp"
	"sync"
)

// IsUUID reports whether s is in the RFC 4122 canonical form:
// 8-4-4-4-12 hex digits separated by dashes, no braces, no URN
// wrapper. Used by generated path-param validators to reject
// malformed IDs before issuing the HTTP call.
//
// The check is forgiving-but-typical: UUIDv1/v4/v6 look identical
// structurally, so we don't version-discriminate.
func IsUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, r := range s {
		switch i {
		case 8, 13, 18, 23:
			if r != '-' {
				return false
			}
		default:
			switch {
			case r >= '0' && r <= '9':
			case r >= 'a' && r <= 'f':
			case r >= 'A' && r <= 'F':
			default:
				return false
			}
		}
	}
	return true
}

// patternCache memoises compiled regexes across the lifetime of the
// process. Spec patterns are bounded and known at build time, so
// growth is bounded.
var patternCache sync.Map

// MatchPattern reports whether s matches the regex pattern. The
// pattern is compiled once per unique value via regexp.MustCompile
// and cached; bad spec patterns crash the program on first use
// rather than silently let malformed input through. Pattern is
// sourced from the OpenAPI spec, not from user input.
func MatchPattern(pattern, s string) bool {
	v, ok := patternCache.Load(pattern)
	if !ok {
		re := regexp.MustCompile(pattern)
		v, _ = patternCache.LoadOrStore(pattern, re)
	}
	return v.(*regexp.Regexp).MatchString(s)
}

// NumberAsFloat coerces a json.Number into a float64 for
// spec-declared minimum/maximum bound checks. Non-numeric or empty
// strings return 0 so the guard only fires on true out-of-range
// values; malformed numbers fail server-side.
func NumberAsFloat(n json.Number) float64 {
	if n == "" {
		return 0
	}
	f, err := n.Float64()
	if err != nil {
		return 0
	}
	return f
}

// HasJSONKey reports whether the decoded probe map contains the
// given JSON key. Used by untagged-union decoders that probe a
// variant's required fields.
func HasJSONKey(probe map[string]json.RawMessage, key string) bool {
	_, ok := probe[key]
	return ok
}
