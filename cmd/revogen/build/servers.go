package build

import (
	"net/url"
	"sort"

	"github.com/getkin/kin-openapi/openapi3"

	"github.com/greatliontech/revolut-go/cmd/revogen/ir"
)

// collectHostAliases walks every path-item and operation `servers:`
// block the spec declares and records (production, sandbox) host
// pairs. A pair is recognised when the block lists exactly two
// entries and their hosts differ only by a leading `sandbox-`
// segment on the leftmost label — Revolut's convention. Doc-level
// servers are intentionally excluded; the client constructor picks
// between them directly based on the caller's Environment option.
//
// Deduplication is by production host. Ordering is stable by
// production host.
func (b *Builder) collectHostAliases() []ir.HostAlias {
	if b.doc.Paths == nil {
		return nil
	}
	seen := map[string]string{} // prod host → sandbox host
	record := func(servers openapi3.Servers) {
		if len(servers) != 2 {
			return
		}
		prod, sandbox := servers[0].URL, servers[1].URL
		ph, sh := hostOf(prod), hostOf(sandbox)
		if ph == "" || sh == "" || ph == sh {
			return
		}
		// Keep the production→sandbox direction only when hostnames
		// follow Revolut's `sandbox-<prod>` pattern. Anything else is
		// probably a misordering or a non-paired server list; skip it
		// rather than guess.
		if "sandbox-"+ph != sh {
			return
		}
		if existing, ok := seen[ph]; ok && existing != sh {
			// Conflicting mapping — leave the first recording in place.
			return
		}
		seen[ph] = sh
	}
	for _, item := range b.doc.Paths.Map() {
		if item == nil {
			continue
		}
		if len(item.Servers) > 0 {
			record(item.Servers)
		}
		for _, op := range item.Operations() {
			if op == nil || op.Servers == nil {
				continue
			}
			record(*op.Servers)
		}
	}
	if len(seen) == 0 {
		return nil
	}
	prods := make([]string, 0, len(seen))
	for p := range seen {
		prods = append(prods, p)
	}
	sort.Strings(prods)
	out := make([]ir.HostAlias, 0, len(prods))
	for _, p := range prods {
		out = append(out, ir.HostAlias{Production: p, Sandbox: seen[p]})
	}
	return out
}

// hostOf returns the host portion of a server URL, or "" when the
// URL is unparseable. Strips the scheme so the alias table stays
// scheme-agnostic — the resolve step preserves the original scheme
// when it rewrites a request URL.
func hostOf(serverURL string) string {
	u, err := url.Parse(serverURL)
	if err != nil {
		return ""
	}
	return u.Host
}
