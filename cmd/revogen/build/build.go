package build

import (
	"github.com/getkin/kin-openapi/openapi3"

	"github.com/greatliontech/revolut-go/cmd/revogen/ir"
	"github.com/greatliontech/revolut-go/cmd/revogen/names"
)

// Config forwards per-package knobs from the CLI into the build
// stage. Every field is optional; the empty Config produces a
// sensible default Spec but callers should at least set Package.
type Config struct {
	Package           string
	ResourceAllow     []string // empty = allow all tags
	IncludeDeprecated bool
	ErrPrefix         string
	DocsBase          string
}

// FromOpenAPI lowers an openapi3.T into an ir.Spec. The returned
// Spec has no Go-name collisions between resource and Decl names
// (those are reserved up front); other collisions (method vs.
// method, enum const vs. enum const) are resolved by the
// lower/names pass that runs after this.
func FromOpenAPI(doc *openapi3.T, cfg Config) (*ir.Spec, error) {
	b := &Builder{
		doc:        doc,
		cfg:        cfg,
		declByName: map[string]*ir.Decl{},
		resourceByName: map[string]*ir.Resource{},
		specNameToGo: map[string]string{},
		reserved:   map[string]bool{},
	}
	b.reserveResourceNames()
	b.buildDecls()
	b.buildOperations()
	b.buildCallbacks()
	b.buildErrorType()
	return b.finalize(), nil
}

// Builder holds the mutable state of a single build pass. It is not
// intended for reuse across documents.
type Builder struct {
	doc *openapi3.T
	cfg Config

	// declOrder preserves deterministic emission order (schema name,
	// ASCII). declByName is the lookup index.
	declOrder  []string
	declByName map[string]*ir.Decl

	resourceOrder  []string
	resourceByName map[string]*ir.Resource

	// specNameToGo memoises the spec-name → Go-name mapping so that
	// collision suffixes and initialism normalisation happen once.
	specNameToGo map[string]string

	// reserved captures Go identifiers already claimed by resource
	// structs so schema resolution can detour to a suffixed form
	// rather than emit two declarations with the same name.
	reserved map[string]bool

	callbacks []*ir.Callback
	errorType string
	apiVer    string
}

// resolvedName returns the Go identifier a spec schema name resolves
// to, applying initialism normalisation and resource-collision
// avoidance.
func (b *Builder) resolvedName(specName string) string {
	if cached, ok := b.specNameToGo[specName]; ok {
		return cached
	}
	goName := names.TypeName(specName)
	if b.reserved[goName] {
		goName += "Response"
	}
	b.specNameToGo[specName] = goName
	return goName
}

// reserveResourceNames walks the document's operations once, ahead
// of any schema resolution, to claim the Go identifiers that will
// become resource structs. Schemas whose natural Go name collides
// get a `Response` suffix at resolution time.
func (b *Builder) reserveResourceNames() {
	// Early return for specs with no Paths.
	if b.doc.Paths == nil {
		return
	}
	allow := map[string]bool{}
	for _, t := range b.cfg.ResourceAllow {
		allow[t] = true
	}
	for _, item := range b.doc.Paths.Map() {
		if item == nil {
			continue
		}
		for _, op := range item.Operations() {
			if op == nil {
				continue
			}
			tag := "Untagged"
			if len(op.Tags) > 0 {
				tag = op.Tags[0]
			}
			if len(allow) > 0 && !allow[tag] {
				continue
			}
			b.reserved[names.TypeName(tag)] = true
		}
	}
}

// finalize assembles the Spec from the Builder's accumulated state
// with a deterministic sort order.
func (b *Builder) finalize() *ir.Spec {
	spec := &ir.Spec{
		Package:    b.cfg.Package,
		APIVersion: b.apiVer,
		ErrorType:  b.errorType,
		ErrPrefix:  b.cfg.ErrPrefix,
		DocsBase:   b.cfg.DocsBase,
		Callbacks:  b.callbacks,
	}
	spec.Decls = make([]*ir.Decl, 0, len(b.declOrder))
	for _, name := range b.declOrder {
		spec.Decls = append(spec.Decls, b.declByName[name])
	}
	spec.Resources = make([]*ir.Resource, 0, len(b.resourceOrder))
	for _, name := range b.resourceOrder {
		spec.Resources = append(spec.Resources, b.resourceByName[name])
	}
	return spec
}

// buildDecls, buildOperations, buildCallbacks, buildErrorType are
// defined in sibling files. The placeholders here keep the flow of
// FromOpenAPI readable; the real logic lives next to the data it
// touches.

// buildOperations is defined in operations.go.
func (b *Builder) buildOperations() {
	// Stub: Phase 3c.
}

// buildCallbacks is defined in callbacks.go.
func (b *Builder) buildCallbacks() {
	// Stub: Phase 3d.
}

// buildErrorType is defined in errors.go.
func (b *Builder) buildErrorType() {
	// Stub: Phase 3d.
	if b.doc.Info != nil {
		b.apiVer = b.doc.Info.Version
	}
}
