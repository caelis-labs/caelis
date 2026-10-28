# Experimental Runtime features

Control exposes the independent `async_compact` and `idle_compact` opt-ins through
`experimental-features-v1`. Core defaults both to false. `control/experimental` owns their identifiers,
definitions and defaults; Surfaces render that catalog and the SDK receives
explicit Runtime/Manager options.

| ID | Effect |
| --- | --- |
| `async_compact` | Generate one background context checkpoint candidate within the active Turn. |
| `idle_compact` | Generate and commit a checkpoint after a completed Turn has remained idle for 60 seconds. |

MCP catalog refresh and deferred tool retirement are always enabled. They are not
experimental settings. Refresh does not require compaction; retirement uses a
durable canonical compact with synchronous, asynchronous or idle compaction. A generated
summary is never a retirement signal. See [MCP lifecycle](mcp-tool-lifecycle.md) and the
[SDK durability boundary](agent-sdk-boundary.md).

## Scope and activation

Values are Host defaults, persisted in the existing Store's `config.json` under
`experimental`, using the Host configuration revision and operation ledger.
Omitted values default to false; sparse writes change only the supplied keys.
Unknown keys, including unknown keys set to false, are rejected. The retired
`mcp_catalog_refresh` and `deferred_tool_retirement` input keys are accepted and
ignored for compatibility; reads and the editor expose only the two compaction settings.

Both workspace and explicit-profile Sessions sample these defaults at their next
Runtime activation. An existing activation keeps its current values until it is
released. Saving does not cancel running work or replace a live Runtime. No App
restart is required: a new Session's first work uses the saved defaults, and an
idle released Session samples them when activated again. A connected observer
can retain an existing activation, so a new Session is the immediate opt-in path.
These settings do not change external ACP agents or enable Guardian experiments.

Discovery returns `desired`, `scope: "host"`, and
`applies_at: "next_runtime_activation"`. With `session_id`, it also returns
`effective` only when that authorized Session has a live Runtime. An absent
`effective` means no active Runtime; reads never activate one. It does not mean
that the desired values have already taken effect.

## Control API

First call `POST /api/control/v1/initialize` and check for
`experimental-features-v1`. Executable version strings do not select behavior.
An older Host without this capability must be reported as unsupported.

```http
GET /api/control/v1/configuration/experimental?session_id=SESSION_ID
Authorization: Bearer HOST_TOKEN
```

The response includes feature definitions, complete desired values and the
current Host configuration `revision` as a decimal string. Read this revision
immediately before a write:

```http
POST /api/control/v1/configuration/experimental
Authorization: Bearer HOST_TOKEN
Content-Type: application/json
Idempotency-Key: enable-experiments-001

{
  "operation_id": "enable-experiments-001",
  "expected_revision": "42",
  "values": {
    "async_compact": true
  }
}
```

`42` is illustrative; use the actual discovery revision. The response is the
ordinary `CommandResult`. A conflict or rejection is not a saved configuration.
Read back desired values after a committed result. For an unknown result, retain
the original operation ID and exact payload and use the existing operation
readback; do not mint another operation to replay the same intent.

Go clients use `AppServerClients.Experimental.ExperimentalFeatures(ctx, sessionID)`
and `SetExperimentalFeatures(ctx, appserver.ExperimentalRequest{...})` after the
same capability negotiation. The optional client is present in both embedded and
HTTP bindings. OpenAPI and generated Go/TypeScript clients describe the routes.

A future Bot can explicitly initialize these Host defaults through this API
before starting new Session work, while preserving already-configured user
choices according to its product policy. The values are shared by every client
of that Host, not private Bot preferences or per-Session overrides. Core has no
implicit Bot detection and no default-on environment-variable path.

## Retry an MCP catalog read

After the same capability negotiation, an authorized Session owner can call
`AppServerClients.Experimental.RefreshMCPTools` or POST
`/api/control/v1/sessions/{session_id}/mcp/refresh`:

```json
{
  "operation_id": "retry-documents-1",
  "session_id": "current-session",
  "plugin_id": "application",
  "server_name": "documents"
}
```

Send the matching `Idempotency-Key`. Optional `expected_revision` and
`expected_controller_epoch` use the ordinary Session preconditions. Plugin IDs
are the configured directory namespaces; explicit-profile servers use
`application`. This retries `tools/list` on the existing activation-owned
Manager. It never activates a dormant Runtime, reconnects a failed startup, or
changes grants.
Session authorization and the shared operation ledger apply. A repeated original
request returns its receipt without another remote listing; unknown results keep
the original ID and payload for readback. Failed listing leaves dispatch blocked
and the directory warning visible until a later successful refresh.

## Terminal interaction

`/experimental` opens the compact checkbox editor.
The initial checkboxes show the server's saved values. Arrow keys move, Space
stages a draft, and Enter saves the changed values with the loaded revision.
The current Runtime values are shown separately. A successful save closes the
editor only after readback confirms the requested values.

Esc discards an unsaved draft and closes. During a dispatched save, Esc closes
without canceling the write; the editor says so. Reopen to read current truth.
Errors retain the draft. An unresolved save retains its original request and
freezes further edits; Enter checks that same operation and payload. A known
CAS rejection or a committed write superseded by another client refreshes the
baseline while preserving selections for a deliberate new save. A completed
operation never freezes that draft; only an unresolved operation does. Unsupported Hosts and failed reads never display fabricated checkboxes.

## Checkpoint authority

Runtime composes the request configuration validator, MCP catalog guard and
MCP grant-revocation guard in that order, releasing in reverse order. The Session
write queue is acquired first. Owner guards cover the short checkpoint append
and any original-payload reconciliation; they never cover summary generation,
approval or remote execution. Explicit-profile configuration validation uses its
existing Store mutex and does not call `AdmitRequest` or alter `LastRequest`.
Catalog and grant guard acquisition fails closed on contention. Catalog guards
also protect connection-loss publication.

The sole checkpoint writer attaches the adopted exposure state to the canonical
compact event. A CAS retry that accepts a newer canonical tail recalculates that
state from the latest source. An unknown write is reconciled with the original
ID and complete payload before any further action. Tool admission still checks
current bindings and permissions independently of compaction.

Summary model fidelity and prompt tuning are outside these switches' structural
validation. Token counts and fixture latency do not establish provider cache hits
or monetary savings. The [testing guide](testing.md) names deterministic,
real-local-protocol and rendered-terminal evidence separately.

## Idle compaction

A successful native Turn may retain its last actual request and canonical final
answer. After the original fence is atomically released, the Host schedules one
60-second timer. Observer connections alone do not prevent this work; foreground
uses, running Tasks and pending approvals do. Foreground work cancels the job
without waiting for generation or acquiring an idle write lock. Each Host admits
one idle generation at a time and drops overlapping opportunities.

Eligibility requires at least two completed Turns and 16k estimated delta tokens,
a known context window at least half full but below the ordinary compaction
watermark, complete tool-call/result batches, and unchanged source/configuration.
Requests on Codex-named channels, including Magpie Codex routes, are treated as
`NeverExpires` and skipped. Other channels use a five-minute freshness assumption
with a 30-second margin, measured from dispatch; clock discontinuities also skip
work. This is not a cache-hit guarantee or a provider allowlist.

The Store's 180-second opportunity window is separate from the two-second grant
for one fixed checkpoint. Foreground acquisition or another mutation permanently
revokes that opportunity. Generation allows at most two actual attempts and has
a 30-second deadline. The same adoption checks as async compaction require a
strictly smaller projected context, without an additional percentage/token saving
threshold. A completed candidate displaced by foreground activity can be retained
in bounded memory for up to 120 seconds and revalidated under the new foreground
fence; its old grant is never reused.

A dedicated work reference retains the provider/configuration lease until the
producer drains and original-ID usage accounting settles. Shutdown cancels, drains,
settles accounting, then releases dependencies. If drain or accounting cannot finish,
shutdown reports an error and retains dependencies. Durable bounded reservations
allow an exclusively owned Host restart to journal sealed usage, or an explicit
unknown outcome when no terminal data survived; recovery never replays generation.

## Native Anthropic cache and usage

Native Anthropic requests receive at most four `ephemeral/5m` breakpoints after
wire conversion. Priority is the last user message, the user before the last
assistant, the penultimate user, then system and tools, with duplicate points
removed. This preserves an explicit lookup boundary for frozen async requests,
completed-request idle prefixes and batches of queued user messages. Compatible
endpoints do not inherit these markers. Canonical messages and tool schemas are
unchanged; compaction appends its instruction to the exact dispatched prefix.

Replacing history with a checkpoint creates a new prefix. The optimization makes
the generation request eligible to reuse the old input; it does not promise that
requests after adoption can read all the old history from cache. The final answer
appended for idle generation was output, so it is new input rather than a proven
cache read. TTL, provider scope, minimum length and server availability still
control real hits. Failed or cancelled attempts can incur usage even without an
adopted checkpoint, and each actual attempt remains in accounting.

Usage retains cache reads, cache writes, reported 5m/1h write breakdowns, and
presence separately. For Anthropic, normalized prompt tokens contain uncached
input plus writes; display Input additionally includes reads, exactly once.
Write and TTL columns are breakdowns, not additional Total tokens. The TUI shows
`0` for an observed zero and `-` for missing cache measurements. Sparse stream
updates preserve prior counters while explicit zero can replace them. No cache
price is synthesized into provider `CostMicros`; fixtures establish request and
accounting behavior, not measured monetary savings.
