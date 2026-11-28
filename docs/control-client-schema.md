# Control client compatibility

TUI and Bot clients share `api/control/v1/openapi.json`, the Control
HTTP/SSE client and the existing `/api/control/v1` routes. `/initialize` reports
protocol, Envelope and API versions plus capabilities. Executable versions do
not select protocol behavior. Clients check their required capabilities before
using them; an unknown optional capability does not prevent the handshake.

## Additive object fields

Schemas marked `x-caelis-ignore-unknown-fields` admit optional object extensions.
The public decoder ignores unrecognized properties while retaining validation
and defaults for known fields. This covers the shared command request families,
Session configuration patches and receipts, initialization, Session state and
status, presentation, completion/catalog responses, Task directories and feed
delivery containers, and UI preferences, including their marked nested objects. Other typed formats
retain their declared validation; the marker is not a global relaxation of all
Control objects.

Known revisions remain decimal strings without floating-point conversion.
Absent, false, null and empty collections keep their existing meanings. A patch
cannot use an extension to change a known creation-bound field, including
workspace or permissions. MCP credential/environment fields without a supported
private-secret contract remain rejected. Opaque tool schemas and application
content retain their data; ignoring protocol extensions does not strip keys
inside those payloads or change the selected tool-result format.

The Go generator retains declared fields for objects with this marker. Their
readers may discard unknown properties; these are different from vendor objects
whose additional keys are data and must survive round trips. TypeScript types
permit the marked objects' additional properties without changing declared types.

For a paired Guardian update, the Agent binding request sends
`guardian_bindings` with both handles and an empty `binding` object. Control
validates the pair and commits one Host configuration revision. A Host that
only understands single-handle binding rejects the empty handle before writing.
Binding-status clients can request `configuration_revision=true` to receive the
revision of the same Host snapshot as the bindings and targets, as a decimal
string. This opt-in preserves the response shape for existing strict clients.
The Team picker uses that revision for saves and requires a Host that returns it.

## Shell execution

Hosts with `shell-execution-v1` expose `POST /api/control/v1/shell/execute`. Use
`ExecuteShell` in the [Go HTTP client](../control/appserver/httpclient) and
`ShellRequest` and `ShellResult` in the generated
[TypeScript contracts](../clients/typescript/control-v1.gen.ts).

A managed loopback `caelis serve` Host advertises the capability with its
ordinary local Control credential from the Store. The endpoint requires a
direct loopback connection. ACP ingress uses a distinct credential, even when
it shares the same principal ID. Remote listeners, explicitly configured
Control tokens or token files, forwarded requests and internal Session Runtime
identities have no Shell grant. The ordinary credential carries the grant
when presented directly over loopback. A tunnel that presents a copy of that
credential as a loopback client is within the same authority boundary.
Ordinary clients can continue negotiating the existing capabilities.

Send an existing absolute `workspace` directory and a `command` of up to
32 KiB. The workspace must be separate from the Host Store. An omitted
`location` selects `sandbox`. Explicit `host` starts in that directory and
executes with the Host OS user's authority. The working directory does not
limit Host filesystem or network access.

Sandbox execution uses Seatbelt on macOS, Bubblewrap on Linux, and the
restricted-token workspace-write sandbox on Windows. macOS and Linux restrict
writes to the workspace and backend bootstrap paths, limit reads to the
workspace and bootstrap paths, and disable networking. Windows limits writes
with the native workspace ACL policy and its Store-managed temporary
environment, leaves network access enabled, and has no read ceiling.
Sandbox setup failure returns `503` without a Host fallback. Shell
and its sandbox initialize per request, independently of Host readiness.

A completed process returns its actual `location`, `backend`, `exit_code`,
`stdout`, and `stderr`. An ordinary nonzero process exit is a completed result.
The Host admits four concurrent calls. Excess calls return `429`. Execution
has a 20-second deadline and 64 KiB of raw output per stream. Deadline and
overflow return `504` and `413`. Client disconnect and Host shutdown cancel
active calls. Darwin and Linux use a one-shot worker and clean up its process
group. Both Windows routes launch commands in a kill-on-close Job Object and
retain the existing restricted-token ACL lifecycle for sandbox calls. Deliberate
same-user attacks on the Host, machine-wide exhaustion and Unix descendants
that deliberately leave their process group fall outside these guarantees.

Shell uses no model or Agent Session and has no durable receipt. If a worker
exits without a result, Control reports `unknown_outcome`. After a lost
response, reconcile external effects before issuing a new request.

## Session interrupt

`session-interrupt-v1` permits `POST /sessions/{session_id}/interrupt` to interrupt
the current Turn and the Session's managed delegation subtree, including while
the main Turn is idle.
`SessionInterruptRequest` requires the observed `expected_revision`; a changed
Session rejects the interrupt. An optional `expected_controller_epoch` also
fences a controller change when the observed Session has an epoch. The
operation ledger retains its receipt. Acceptance acknowledges cancellation
requests; producer terminal state is observed separately. The Session remains
open for later user input. `POST /sessions/{session_id}/cancel` retains its exact
Turn target requirement.

## Sessions workbench

The optional `session-workbench-v1` capability exposes the authorized
cross-workspace directory at `GET /session-workbench`. Its opaque cursor pages
active and unread Sessions before remaining history. When that priority set
changes, a continuation returns `409 Conflict` and the client starts a new
traversal without a cursor. A request may refresh specific mounted rows by ID.
Listing samples activity without creating Session observations.

`PUT /sessions/{session_id}/read-position` acknowledges a visible result for
the authenticated principal. The decimal `through_seq` identifies a durable
Session position. A terminal Turn Envelope can carry the result position in
`lifecycle.result_through_seq` when the narrative arrived as streaming chunks.
Repeated or older acknowledgments preserve the highest accepted position.
Acknowledgments leave Session history and ordering intact.

## Peer versions

The optional `idle-command-admission-v1` capability enables
`GET /sessions/{session_id}/state/idle`. The Host accepts an idle-only command
at this check when both the foreground Run and the Session execution claim are
idle. ACP performs admission before publishing the command echo or dispatching
its handler. With an older Host the bridge advertises `idleOnly: false` to ACP
peers. Standard v2 clients can still submit ordinary commands.

The unified Session protocol is the current baseline. Pre-unification Application
routes and credentials are removed; old clients need an update. This breaking
cutover does not discard their durable Stores: the isolated Store importer owns
that migration, separately from current client negotiation.

Compatible versions keep consumed semantics and optional fields additive. New
response fields or effectful options require an explicit capability when an older
compatible peer cannot safely consume them. There is one maintained schema and
no frozen legacy protocol implementation.

Field tolerance alone does not establish full cross-version compatibility. Live
and replayed display replacement, exact approvals, operation receipts and unknown
effects must retain their semantics. Wire compatibility does not authorize an
older executable to write a newer Store format.

`approval.active` is the caller-resolvable FIFO head. Automatic review requests
remain in the same native queue and contribute to `run.waiting_approval`, but
must not appear in `approval.active`: the queue rejects client decisions for
them. Review events report progress and outcomes; their arrival order does not
grant caller authority. State reads and reconnect bootstrap use the same queue
projection, including when no foreground Turn owns a background child's request.
Native manual requests retain their original target and options. No new field or
capability is needed to enforce this existing contract.

## Reproducible release matrix

The [release interoperability runner](../scripts/control_compatibility/README.md)
builds the current production client and Host, with optional compatible peer refs.
The peer mode checks both directions
against isolated Stores, including original mutation receipts, configuration,
resources, native tool results and SSE recovery after Host replacement. The evidence
is source-built binary acceptance; its synthetic provider is not a live-account
or native Bot UI test.

## Managed Host replacement

`host-idle-shutdown-v1` adds `expected_instance_id` and `if_idle` to the existing
`POST /host/shutdown`. The Host atomically rejects new mutation/Runtime admission
only when existing work is idle. A conflict leaves the old Host usable. The
lifecycle manager owns executable selection, exclusive Store ownership, readiness
and rollback; subscriptions never select or start a Host during reconnection.
Explicit `service stop` and `service restart` retain their interrupting semantics.

A newer CLI attaches to a compatible busy Host instead of failing startup. The
lifecycle manager stages the candidate and a short-lived CLI waiter retries the
same atomic upgrade with 1, 2, 4, then at most 5-second delays (with jitter)
until running Session turns finish. Busy retries do not hash or copy the CLI. It owns no protocol listener or
Store writer. A Store lifecycle lock serializes replacement; a per-build waiter
lock suppresses duplicates. The waiter is bound to the original Host instance:
explicit stop or another replacement retires it. Subscriptions and heartbeats
are not active work. Detached Tasks and child work after a Session becomes idle
do not defer replacement; shutdown cancels those resources. Transient mutation dispatch can defer an atomic attempt.

The one-time pre-v0.72.2 release transition is isolated in
`internal/cli/legacy_host_upgrade.go`. It reads the old Host's live Session activity;
when no Session turn runs, its public shutdown is allowed even without the atomic
interface or when v0.72.0/1 reject shutdown solely for background work. A task admitted in the legacy read/shutdown gap
can be interrupted. Busy legacy Hosts remain usable and are upgraded after work
finishes. This exception never applies to newer releases or unknown builds;
remove the whole boundary once the minimum installed Host is v0.72.2.

## Retired Core Memory

Control owns the compatibility reader for retired Core Memory. `configstore`
accepts any valid JSON in the legacy `memory` field, retains it opaquely on
unrelated saves, and does not validate credentials or open Memory storage.
`agentbinding` ignores `steward` and `memory-verifier` in loaded bindings and
saved sets. They are absent from catalogs, placement and `/team`, remain reserved
against reuse as custom roles, and cannot retain a model dependency.

Existing bind/reset request shapes still decode. Requests for either retired
handle return HTTP 400 with a terminal `rejected` command receipt and a retirement
error before configuration persistence or scheduling. The configuration revision
does not advance; the receipt never reports a saved or executed operation.
Ordinary role mutations continue to save the full Host document, retaining opaque
`memory` JSON and omitting retired bindings. Binding-set commands carry a set name,
not a client-owned role table; applying an old set ignores retired entries before
validating the remaining bindings. These ignored entries cannot make another
role update or an otherwise valid saved set fail.

Deprecated embedding selectors and binding references are accepted but ignored,
including callbacks.
`gatewayapp` owns those source-compatible input types until the next breaking
embedding API boundary. Remove the configuration and wire shims only when the
minimum supported upgrade source and client versions exclude Memory writers and
requests. Release notes must name the actual last writer and first no-write
release before raising that floor; this contract assigns no unreleased version.
`internal/cli` retains legacy Memory environment filtering and startup-error
translation solely for older managed Hosts; remove those cases when the minimum
managed Host version excludes embedded Memory.

Canonical Session events, tool results, opaque Memory pins and metadata remain
unchanged. Replay renders and reconstructs historical `Remember`/`Recall` results
without calling Memory. New native tool catalogs omit both tools; a model that
requests either receives the ordinary unavailable-tool error, with no saved-memory
claim. Existing Memory database, WAL, credentials and other files are neither
migrated nor deleted. The explicit read-only Store diagnostic can still inspect
retained file layout and reports Memory as retired, without a required schema.
Notebook files, summaries, ordinary Session stores and other Agents retain their
own owners and behavior.
