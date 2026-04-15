package ir

// Spec is the complete result of the build + lower pipeline, ready
// for the emit stage to walk.
type Spec struct {
	Package    string
	APIVersion string // from OpenAPI info.version; emitted as an APIVersion const

	// ErrorType, when non-empty, names a Decl whose shape matches the
	// typed error schema the spec returns on 4xx/5xx responses. The
	// transport will attempt to decode into this type before falling
	// back to core.APIError.
	ErrorType string

	// Per-package knobs forwarded from the CLI config so the build /
	// lower / emit stages can stay pure functions of Spec.
	ErrPrefix string // error-message prefix, e.g. "business"
	DocsBase  string // base URL for per-operation godoc links

	Decls     []*Decl
	Resources []*Resource
	Callbacks []*Callback
}

// Resource is one tag-grouped set of methods — emitted as
// `type <Name> struct { t *transport.Transport }` plus its methods,
// into gen_<lower>.go.
type Resource struct {
	Name    string
	Doc     string
	Methods []*Method
}
