package ir

// DeclKind discriminates Decl.
type DeclKind int

const (
	// DeclStruct is a Go struct type. Fields carry every JSON property
	// the spec declares on the schema, plus any synthesized catch-all
	// map for additionalProperties.
	DeclStruct DeclKind = iota
	// DeclEnum is a Go named type with const values. Base is either
	// "string" or "int64".
	DeclEnum
	// DeclAlias is `type X = Target` — used for schemas that are
	// nominal wrappers around primitives (core.Currency, a simple
	// string-typed pattern, etc.).
	DeclAlias
	// DeclInterface is a sealed Go interface used to model OpenAPI
	// unions (discriminator with propertyName, or named-ref oneOf/
	// anyOf). Variants carry a marker method referenced here.
	DeclInterface
)

// Decl is a top-level Go declaration emitted into the package.
type Decl struct {
	Name string
	Doc  string
	Kind DeclKind

	// DeclStruct ----------------------------------------------------
	Fields              []*Field
	AnyOfRequiredGroups [][]string   // JSON-name groups, conditional-required validation
	ImplementsUnions    []string     // union interface names this struct is a variant of
	UnionDispatch       *UnionLink   // non-nil when the struct is a wire-tagged variant
	FormEncoder         bool         // emit encodeForm() helper
	MultipartEncoder    bool         // emit encodeMultipart() helper
	QueryParamsEncoder  bool         // emit encode() url.Values for an *<Op>Params struct
	ExtraMap            *Type        // map[string]T catch-all when properties + additionalProperties coexist

	// DeclEnum ------------------------------------------------------
	EnumBase   *Type // Prim("string") or Prim("int64")
	EnumValues []EnumValue

	// DeclAlias -----------------------------------------------------
	AliasTarget *Type

	// DeclInterface -------------------------------------------------
	MarkerMethod  string         // unexported marker method every variant implements
	Variants      []Variant      // the union's members (ordered by discriminator mapping or $ref order)
	Discriminator *Discriminator // non-nil when the union is wire-tagged
}

// UnionLink ties a variant struct back to the union it belongs to.
// The emitter consults it to inject the discriminator value when
// marshaling a variant.
type UnionLink struct {
	UnionName    string // Go name of the union interface
	PropertyName string // wire field carrying the tag, e.g. "type"
	Value        string // wire value for this variant, e.g. "apple_pay"
}

// Discriminator describes the wire-tag field of a real union.
type Discriminator struct {
	PropertyName string // wire field carrying the tag
}

// Variant is one member of a union interface.
type Variant struct {
	GoName string
	Tag    string // wire value from the discriminator mapping, or the spec schema name for untagged unions
	// RequiredProbe carries the JSON field names that uniquely
	// identify this variant when the union is untagged. Empty for
	// wire-tagged unions.
	RequiredProbe []string
}

// Field is one JSON field on a struct.
type Field struct {
	JSONName   string
	GoName     string
	Type       *Type
	Required   bool
	ReadOnly   bool
	WriteOnly  bool
	Deprecated string // non-empty when the spec marks the field deprecated
	Doc        string
	// DefaultDoc carries the spec's `default:` value when it's not a
	// machine-readable literal (e.g. the prose "the date-time at which
	// the request is made"). Surfaced in godoc only.
	DefaultDoc string
	// DefaultLiteral, when non-empty, is a Go expression that
	// evaluates to the field's default. Populated for literal int /
	// string / named-string schema defaults; the emitter uses it to
	// synthesize an ApplyDefaults method on the containing Params
	// struct. Bool defaults are intentionally skipped: the caller
	// may have set false on purpose, and zero-value detection would
	// overwrite it.
	DefaultLiteral string

	// ExplodeFalse, when true, marks an array query parameter that
	// OpenAPI declares as style=form + explode=false, meaning items
	// are serialised as a single comma-joined value (key=a,b,c)
	// instead of repeated entries (key=a&key=b&key=c). Zero value
	// keeps the default explode=true behaviour.
	ExplodeFalse bool
}

// EnumValue is one entry in an enum.
type EnumValue struct {
	GoName string
	Value  any // string or int64
	Doc    string
}
