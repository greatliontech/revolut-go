package build

import (
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
)

// TestCollectHostAliases_PairsMatchingSandboxHost: a per-path
// servers block that lists production followed by the matching
// sandbox host is recorded; a block that's missing the
// `sandbox-` prefix pattern is skipped so a misordered or
// non-paired list can't silently produce a wrong alias.
func TestCollectHostAliases_PairsMatchingSandboxHost(t *testing.T) {
	b := newTestBuilder()
	b.doc.Paths = &openapi3.Paths{}

	paired := pathItem()
	paired.Servers = openapi3.Servers{
		{URL: "https://apis.revolut.com"},
		{URL: "https://sandbox-apis.revolut.com"},
	}
	b.doc.Paths.Set("/a", paired)

	unpaired := pathItem()
	unpaired.Servers = openapi3.Servers{
		{URL: "https://unpaired.example.com"},
		{URL: "https://also-unpaired.example.com"},
	}
	b.doc.Paths.Set("/b", unpaired)

	aliases := b.collectHostAliases()
	if len(aliases) != 1 {
		t.Fatalf("want 1 alias, got %d: %+v", len(aliases), aliases)
	}
	if aliases[0].Production != "apis.revolut.com" || aliases[0].Sandbox != "sandbox-apis.revolut.com" {
		t.Errorf("unexpected alias: %+v", aliases[0])
	}
}

// TestCollectHostAliases_OperationLevelServers walks both
// path-item and operation-level server blocks and dedups by
// production host.
func TestCollectHostAliases_OperationLevelServers(t *testing.T) {
	b := newTestBuilder()
	b.doc.Paths = &openapi3.Paths{}

	item := pathItem()
	item.Servers = openapi3.Servers{
		{URL: "https://b2b.revolut.com/api/2.0"},
		{URL: "https://sandbox-b2b.revolut.com/api/2.0"},
	}
	opServers := openapi3.Servers{
		{URL: "https://apis.revolut.com"},
		{URL: "https://sandbox-apis.revolut.com"},
	}
	item.Get = &openapi3.Operation{
		OperationID: "getX",
		Tags:        []string{"X"},
		Servers:     &opServers,
		Responses:   openapi3.NewResponses(),
	}
	item.Get.Responses.Set("200", &openapi3.ResponseRef{Value: &openapi3.Response{}})
	b.doc.Paths.Set("/x", item)

	// Duplicate production host on a second path to confirm dedup.
	dupe := pathItem()
	dupe.Servers = openapi3.Servers{
		{URL: "https://b2b.revolut.com/api/2.0"},
		{URL: "https://sandbox-b2b.revolut.com/api/2.0"},
	}
	b.doc.Paths.Set("/y", dupe)

	aliases := b.collectHostAliases()
	if len(aliases) != 2 {
		t.Fatalf("want 2 aliases, got %d: %+v", len(aliases), aliases)
	}
	// Sorted by production host: apis... < b2b...
	if aliases[0].Production != "apis.revolut.com" || aliases[1].Production != "b2b.revolut.com" {
		t.Errorf("unexpected order: %+v", aliases)
	}
}
