package lower

import "github.com/greatliontech/revolut-go/cmd/revogen/ir"

// RunAll applies every lowering pass in the order the pipeline
// requires. The order is load-bearing:
//
//  1. Unions propagates marker-method membership through nested
//     interface variants. Must run before the passes that rewrite
//     or consult type references.
//  2. ReadOnly clones shared request/response Decls and retargets
//     BodyParam.Type. Needs the original names still in place.
//  3. Validators walks request-body and opts structs to emit
//     required-field checks. Runs after ReadOnly so validators
//     are attached to the clones (not the response-shaped
//     originals) and before ResolveNames so the validators'
//     JSON-path messages use spec-side names, unaffected by Go
//     collision resolution.
//  4. ResolveNames is the final IR pass — it rewrites every
//     reference to collision-suffix Go names. Anything that
//     populates type references must have finished before this
//     point.
//
// Callers should prefer RunAll over invoking the individual
// passes directly; the individual functions stay exported for
// targeted unit tests only.
func RunAll(spec *ir.Spec) {
	Unions(spec)
	ReadOnly(spec)
	Validators(spec)
	ResolveNames(spec)
}
