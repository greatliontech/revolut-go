# revolut-go roadmap

Follow-up work identified by the post-refactor audits. Grouped by
horizon. Items within a group are roughly ordered by value.

## Done (2026-04-15 batch)

Items 1–4 and 12 from the batches below were shipped as a single
run. Summary:

- **1.** Request-body receivers normalised to value shape
  (`be09528`). Inline-object bodies no longer leak a pointer into
  the public signature. Breaking change for callers that wrote
  `&X{...}` on the 6 affected Business methods.
- **2.** Expenses `ListAll` iterator emitted (`fbb0b48`). Roadmap
  premise was partly stale: Accounting already had iterators,
  PaymentDrafts is unpaginated by spec (no opts, no cursor) — only
  Expenses needed the fix.
- **3.** Required query-param validation (`371f296`). Includes the
  `opts == nil` early-return guard when any field is required.
- **4.** `task test:gen-all` (`9a16ba2`) — regenerates every spec
  into `.revogen-scratch/` and runs `go vet`. Caught two header-
  type bugs that got fixed as adjacent work (`577c955`).
- **12.** Merchant and Open Banking wired as public clients
  (`d3d60b5`). Follow-up fix (`5fe471f`) makes per-operation
  server-override URLs environment-aware via a generated
  `SandboxHostAliases` map applied by the transport.

The "sandbox token bootstrap per API" bullet on item 12 was
closed by acknowledging the asymmetry: Merchant uses a static
secret (no bootstrap needed), Open Banking requires full TPP
onboarding (out of scope for a CLI). `cmd/auth-bootstrap` stays
Business-only.

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

**Outstanding: 5–11.** Natural next: 8 (now possible since
Merchant is generated), then 9–10 (both lean on the `test:gen-all`
scaffolding), then 5–7, then 11.
