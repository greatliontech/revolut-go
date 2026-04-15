# revolut-go roadmap

Follow-up work identified by the post-refactor audits. Grouped by
horizon. Items within a group are roughly ordered by value.

## Done

All ROADMAP items either shipped or explicitly closed.

**Near-term (items 1–4):**

- **1.** Request-body receivers normalised to value shape
  (`be09528`). Inline-object bodies no longer leak a pointer into
  the public signature.
- **2.** Expenses `ListAll` iterator emitted (`fbb0b48`). Roadmap
  premise was partly stale: Accounting already had iterators,
  PaymentDrafts is unpaginated by spec — only Expenses needed
  the fix.
- **3.** Required query-param validation (`371f296`). Emits
  `opts == nil` early-return guard when any field is required.
- **4.** `task test:gen-all` (`9a16ba2`) — regenerates every spec
  into `.revogen-scratch/` and runs `go vet`. Caught two header-
  type bugs fixed as adjacent work (`577c955`).

**Medium-term (items 5–8):**

- **5.** Spec-driven response-header exposure (`80f6c06`,
  `f60065b`). Allowlist classifier → per-package
  `ResponseMetadata` struct; OB methods return
  `(T, ResponseMetadata, error)`; 32 endpoints with a JSON-typed
  2xx response and `x-jws-signature` also emit `<Name>Signed`
  returning `Signed[T]{Typed, Raw, Metadata}` so callers can
  run detached-JWS verification. A 33rd endpoint
  (`GetConsentsConsentIDFile`) also declares the header but
  returns `[]byte` already, so no wrapper is needed. No in-SDK
  JWS helper — SDK stays plumbing. Plus `APIError.RetryAfter`
  (`89f8b01`) parsed unconditionally on 4xx/5xx.
- **6.** Machine-readable defaults (`13db46c`). `ApplyDefaults()`
  method on Params structs, opt-in, skips zero-ambiguous bool
  defaults and prose defaults.
- **7.** `format:` validation for path-param UUIDs (`956bb4b`).
  ir.Param carries Format; applyParameters reads it off the spec;
  the emitter attaches a local RFC-4122 `isUUID` pre-flight check
  after the empty-string guard. Local isUUID helper emitted once
  per package. Scope kept narrow: path-param UUIDs only — email /
  uri / body-level checks stay out because server error messages
  for those are already actionable.
- **8.** Callback decoder regression test (`9a8b79b`). Adjacent
  discovery: merchant's callback declared an editorial
  discriminator (`propertyName: event` with prose mapping keys
  never appearing on the wire); classifier now detects mismatch
  and flattens to a struct instead of emitting a dispatch that
  rejects every real payload.

**Longer-term (items 9–11):**

- **9.** Pipeline order enforcement (`bf2c9cd`). `lower.RunAll`
  owns the sequence.
- **10.** Golden-signature snapshot (`0b7bcc5`). Every generated
  package's public API pinned to `cmd/revogen/testdata/golden/*.txt`.
- **11.** Decouple build/ from openapi3 — **SKIPPED**. The "adopt
  3.1" motivator turned out to be already-resolved: kin-openapi
  accepts 3.1 specs transparently (revolut-x.yaml ships as 3.1.0
  and generates cleanly). Nothing in the git log or codebase
  flags the coupling as painful. Revisit only if we actually
  need to migrate parsers.

**Separate track (item 12) — Merchant, Open Banking, Crypto Ramp,
Revolut X all wired:**

- Merchant + Open Banking (`d3d60b5`); Crypto Ramp + Revolut X
  (`483bf86`). Host-alias rewriting (`5fe471f`) makes
  per-operation server-override URLs environment-aware via a
  generated `SandboxHostAliases` map applied by the transport.
- Sandbox token bootstrap per API closed by design: Merchant uses
  a static secret (no bootstrap needed), Open Banking requires
  full TPP onboarding (out of scope for a CLI).
  `cmd/auth-bootstrap` stays Business-only.

## Near-term — small, high leverage

### 1. Normalise request-body receiver types
The generator emits some request bodies as `req T` (value) and
others as `req *T` (pointer), depending on whether the spec
declares the body inline or via `$ref`. Pick one shape — value —
and apply it consistently. Rationale: no nil-deref risk, no
pointer-vs-value ambiguity for callers, no zero-value surprise.

**Surface**: `cmd/revogen/build/operations.go` applyRequestBody;
`cmd/revogen/emit/methods.go` method signature rendering.

### 2. Fill the three `ListAll` gaps
Accounting, Expenses, and PaymentDrafts have paginated `List`
endpoints but no iterator because their cursor/page shape doesn't
match any of the three detectors in `build/pagination.go`. Each
uses `limit + page_token` but something about the response struct
field or param name slips past. One targeted case per spec shape.

**Surface**: `cmd/revogen/build/pagination.go`.

### 3. Required query-param validation
When a query param is declared `required: true` the generator
currently renders it as an optional field on the Params struct and
encodes with `omitempty`. Emit a validator so a caller who forgets
the param sees a local error instead of a server 400.

**Surface**: `cmd/revogen/build/operations.go` query-param
handling; `cmd/revogen/lower/validators.go` picking up the new
required flag.

### 4. `task test:gen-all`
CI-runnable task that regenerates every vendored spec into a
scratch dir and runs `go vet`. Optionally diffs a small golden
signature file per spec to catch unintentional API breaks. Would
have caught the `**T` double-pointer regression before commit.

**Surface**: `Taskfile.yml`, new `cmd/revogen/testdata/golden/*`.

## Medium-term — feature coverage

### 5. Response header exposure
Open-banking declares `x-fapi-interaction-id` (correlation ID) and
`Retry-After` (rate-limit hint) on 2xx responses. The generator
currently discards all response headers. Decide on an API shape:
either `(T, http.Header, error)` return or a per-method response
envelope. Plumb through emit.

**Surface**: `cmd/revogen/build/operations.go` response detection;
`cmd/revogen/ir/method.go` if a new field is needed;
`cmd/revogen/emit/methods.go` signature rendering;
`internal/transport/transport.go` to forward headers.

### 6. Machine-readable defaults
Apply spec-declared `default:` values that are literal integers,
strings, or booleans via an explicit `ApplyDefaults()` method on
Params structs (not auto-applied — caller opt-in, since some
defaults are server-side). Prose defaults stay in godoc.

**Surface**: `cmd/revogen/emit/types.go` query-params encoder
nearby.

### 7. `format:` validation
Run local validation for well-known formats before the HTTP call:
`uuid`, `date-time`, `email`, `uri`. Pluggable — a user can
disable. Guards against obvious input errors.

**Surface**: `cmd/revogen/lower/validators.go`.

### 8. Callback decoder regression test
Merchant defines webhook callbacks; the generator emits
`Decode<Name>(body io.Reader)` helpers but no test proves they
round-trip a real payload. Add an httptest-style test that loads
a merchant example payload, decodes through the generated helper,
and asserts the result.

**Surface**: test that lives alongside a generated merchant
package once one exists (tied to item 12).

## Longer-term — architectural

### 9. Pipeline order enforcement
`cmd/revogen/main.go` chains `lower.Unions` → `lower.ReadOnly` →
`lower.Validators` → `lower.ResolveNames` in a specific order. The
order matters (ResolveNames rewrites references other passes
populate), but nothing enforces it. Move chaining into a single
`lower.RunAll(spec)` entry that owns the order, and deprecate
direct calls.

**Surface**: `cmd/revogen/lower/` entry point; `cmd/revogen/main.go`.

### 10. Golden-signature tests per resource
Snapshot current `business/gen_*.go` public signatures (types +
method sigs, not bodies) into a golden file. Regen diffs against
it; any unintentional API break fails CI. Complements item 4.

**Surface**: `cmd/revogen/testdata/` or `business/golden/`.

### 11. Decouple `build/` from `openapi3`
`build/Builder` holds a `*openapi3.T` directly and every helper
reads from it. Introduce a thin "parsed spec" interface so the
kin-openapi dependency lives only in `loader/`. Enables swapping
the parser or supporting OpenAPI 3.1 without touching every build
pass.

**Surface**: `cmd/revogen/build/` — significant refactor.

## Separate track — Gap 5 (Merchant + Open-Banking)

### 12. Wire Merchant and Open-Banking as public clients
The generator produces clean output for both specs. Remaining
work is plumbing:

- `revolut.NewMerchantClient(auth, opts...)` returning
  `*merchant.Client`.
- `revolut.NewOpenBankingClient(auth, opts...)` returning
  `*openbanking.Client`.
- Base-URL options per API (production vs sandbox hosts differ
  between business and merchant / open-banking).
- Taskfile `gen:merchant` / `gen:openbanking` entries.
- Sandbox token bootstrap per API.

**Surface**: `revolut.go`, new `merchant.go` / `openbanking.go`;
`Taskfile.yml`.

## Explicitly NOT on the roadmap

- Reverting operationId-based method naming to the old path
  heuristic. The old names were prettier in isolation but
  collided systematically; operationId reflects spec intent.
- Re-introducing pointer promotion (`*bool` / `*int64`) for
  required scalars on request bodies. Zero is a valid wire value
  when the field is required, and `revolut.Ptr(false)` was noise.
- Built-in retry/backoff inside `Transport`. Different users have
  different risk tolerances; a fixed policy inside the SDK gets
  them all wrong. Belongs in user code or an explicit middleware.
- Prose default values applied at runtime. `default: the current
  date` can't be machine-parsed; goes to godoc only.

---

**Status: ROADMAP closed.** Item 11 deliberately deferred; item 7
reopened and shipped after adversarial audit flagged the skip
rationale as weak. Adjacent audit-driven fixes (cycle-breaking on
self-referential schemas, enum-value dedup in openbanking,
defensive-copy of HostAliases, behaviour tests for OB metadata +
Signed, prose-default heuristic) also landed. If new work
surfaces from spec updates or user feedback, start a fresh
ROADMAP.
