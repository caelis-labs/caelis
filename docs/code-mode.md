# Code Mode foundation (experimental)

This SDK PoC has no product registration or selected language engine. Its baseline
is `83743d3396faf4fd36fb8768ed16d0a2cd97384d`. The deterministic executor used by
tests is a protocol fixture, not a JavaScript implementation.

## Source findings and ownership

- Runtime already injects concrete native tools before journal, policy, invocation
  identity and lifecycle wrappers. A script must call those tools, including the
  wrapped MCPTool; it must never call MCP Manager directly.
- Canonical successful `tool_search` results own MCP discovery. Raw search calls,
  old name-only history and same-response searches grant no script authority.
  MCP source fingerprints and Manager's final admission already fence replacement.
- `journaledTool.Call` writes pre-execution states, but normally returns terminal
  metadata for the canonical chat result to persist. Nested calls need the existing
  `persistToolExecutionReceipt` owner because their aggregate is a different result.
  The metadata carrier uses JSON numbers so full input/result tokens survive it.
- Session journal recovery already marks interrupted effects unknown and never
  replays Call. Code Mode adds no recovery executor, journal or Session authority.

## Common contract

`tool/codemode.Executor.Execute(ctx, Source, Limits, Host)` receives immutable
source text, path and SHA-256, explicit transfer limits and a restricted Host.
Host calls accept a name and JSON object and return bounded JSON
parts and opaque media handles. Go retains tools, policy, credentials, Session,
full results and receipts. Host handles live only for that invocation. Explicitly
emitted handles become original media parts in the model result, never base64 text.
Executor is the trusted Go adapter: it translates Host to bounded JSON RPC and
must never expose the Host Go object to the untrusted language engine.
Structured JSON, IsError and trusted TurnComplete retain their distinct semantics.
Reply.outcome distinguishes succeeded, failed and unknown_outcome. Non-read-only
MCP calls with a request failure or missing response conservatively report a typed
unknown outcome; neither Runtime recovery nor the bridge retries that effect.

The declaration accepts exactly one of `code` and `file`. Files are opened through
the supplied sandbox Runtime filesystem under existing workspace constraints,
read once with a size limit, and hashed. Each execution reads again; adapters must
execute these bytes, never resolve the path through a module cache. Only a real
Node adapter may advertise `node_repl`; other JavaScript adapters use `js_repl`.
The common layer provides no Node APIs, persistent heap or workflow scheduler.

At invocation start the bridge pins current native declarations and currently
discovered MCP bindings. Before dispatch, including after approval, it rechecks
the source/schema and model snapshot generation validator. Dynamic snapshots
without Validate cannot dispatch script host calls; static bindings belong to
their immutable Runtime/Turn assembly. MCP keeps its
own final catalog and grant revocation checks. Native tools stay native, and
concrete Runtime injection happens before wrapping.

Every nested call has a fresh Runtime invocation identity and typed parent
relation. Display mirrors carry actual tool names and arguments without adding
inner tool messages to the model transcript. Each terminal receipt persists
independently before its bounded projection reaches the executor.

Limits apply before source reads, argument copies, projection/transport and
output acceptance. Adapters receive those limits and must enforce them before
worker RPC decoding and result encoding/allocation. Host retains full tool facts
in existing journal receipts; these
limits cannot prevent allocation inside an arbitrary tool or external MCP SDK.
Concurrent requests require explicit ParallelSafe on every active target. A
trusted TurnComplete closes admission even when an executor ignores its reply.
Rejected requests count toward the host request budget. Receipt persistence or
transport failure closes the bridge even if the executor swallows the error.
The JSON output limit and cumulative opaque-media retention limit are separate.

Cancellation closes admission, cancels inflight requests and drains for a bounded
grace period. Unsettled effects retain original IDs and journal state for existing
unknown-outcome recovery. Adapters must stop their producer on cancellation;
engine CPU/fuel accounting must pause during host/approval waits. There is no new
Turn timeout, automatic retry or replay of script/tool effects.

## Backend integration and common evaluation

Implement Executor behind this contract, register one concrete declaration in an
isolated SDK composition, and keep all tool RPCs on Host. Enforce process/memory/
CPU limits and cancellable producer shutdown in the adapter. Reuse this foundation
commit unchanged for each later comparison worktree.

Evaluate the same real tasks and inputs: discover then serially read and aggregate;
native plus MCP calls; schema/source/generation replacement and revoked approval;
denied approval; one completed effect followed by failure/cancellation; interrupted
effect recovery without replay; structured JSON plus image handle emission; source
file edit and rerun; bounded large input/result; concurrent safe and unsafe calls;
CPU-only cancellation versus approval/tool waiting. Record original operation IDs,
terminal receipts, model tokens, host transfers, engine CPU/wall/memory, startup,
cleanup and actual effect counts. Fixture success does not establish language
compatibility, sandbox security, live-model quality or production readiness.
