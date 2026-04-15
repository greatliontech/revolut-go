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

	// HostAliases lists per-operation / per-path server pairs the
	// spec declares. Each entry pairs a production host with its
	// sandbox counterpart so the transport can rewrite absolute URLs
	// emitted by [ServerOverride] methods when the client targets
	// sandbox. Doc-level servers don't appear here — those drive
	// base-URL selection in the revolut constructors directly.
	HostAliases []HostAlias
}

// HostAlias pairs a production host with its sandbox equivalent.
// Only the host portion of each URL is significant; paths are
// preserved from the incoming request.
type HostAlias struct {
	Production string
	Sandbox    string
}

// Resource is one tag-grouped set of methods — emitted as
// `type <Name> struct { t *transport.Transport }` plus its methods,
// into gen_<lower>.go.
type Resource struct {
	Name    string
	Doc     string
	Methods []*Method
}
