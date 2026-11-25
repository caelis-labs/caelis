# MCP tools and fixed model entry points

Native product Sessions use [Code Mode](code-mode.md): the fixed `js_exec`
declaration exposes Session tool routes by original name plus `tools.search` and
`tools.call` inside an async script. Discovery is available only inside JavaScript. Directory changes, search
results and reconnection never append MCP schemas to the model's `tools` array.
Ordinary model capability filters
still apply to non-MCP tools. This contract covers the serialized tools field;
messages, instructions, model configuration and other request prefixes can change.

## Discovery and invocation

Use `await tools.search()` for the current ready directory, optionally filtered by
keywords and paginated by name. Pages contain names and descriptions, not schemas
or bindings. `await tools.search({name})` returns the exact entry and records the
current binding. Neither path calls a model. An empty page means no match at that
instant, not proof of absence while servers are connecting. See [Code Mode's
JavaScript API](code-mode.md#model-tools-and-discovery) for the search objects and
the Body returned by a tool call.

Control's Session MCP status reports this same Runtime's connection state and
catalog. `running` alone describes a connection, not a ready catalog: check that
`refreshing`, `stale` and `refresh_failed` have cleared, with no warning, and that
the expected tool directory is present. Startup failures appear as a short, Session-scoped TUI hint rather than chat
history; `/mcp` retains the failed server and a bounded diagnostic stage/reason.
Transport startup and protocol initialization have a 30-second budget; the
first complete `tools/list` has a separate 30 seconds, starting after connection.
Directory refresh uses the same independent 30-second listing budget. Parent
cancellation or an earlier caller deadline still bounds both stages; shutdown
drains the SDK-owned process/connection before returning.

For stdio, `startup/initialize` includes launcher work: a started npx process
does not prove package resolution or the MCP peer's handshake has started.
Only npm-prefixed fetch/error-code evidence in the last 4096 stderr bytes is
classified as a registry failure. The tail is cleared after initialization;
status/hints contain fixed categories, never raw stderr, URLs or credentials.
Without that evidence, a timed-out npm launcher reports incomplete startup and
suggests checking registry connectivity. HTTP 401/403 and typed network errors
are reported separately. A deadline alone does not prove a network or
authentication failure. Inspection and reconnection do not
activate a cold Runtime. Refresh
targets an already active Runtime; it is not a startup operation. Integration
clients must activate through actual work and observe readiness for that
activation. Search after readiness when a complete schema is needed. Code Mode
snapshots current Session names for each execution; a direct call needs no search
when its schema is known, and canonical `tools.call`
can resolve a newly ready name during the current execution. A provider fixture
can wait between model responses within that original Turn. It must not inject a
warmup Prompt, use stale results as evidence of current readiness, or treat
`connecting` as tool absence.

Canonical search receipts remain the durable discovery records. Typed nested ToolCall facts also preserve the binding used by a
direct MCP call.
Runtime rebuilds both from canonical events across fresh VMs, compaction and
file-Store resume; it does not maintain another durable registry. Old ToolSearch
results, exposure metadata and checkpoint discovered-name fields remain readable
history only. A Code Mode direct call without a prior binding pins the
current real Tool identity/version. Valid bindings remain reusable while their
source and schema are unchanged. There is no legacy execution path or
`tool_retire` tool.

In Code Mode, an original-name call with a known schema or `tools.call(name, args)` can run
without search. A first direct MCP call with no prior binding pins the current
identity/version. If a later call is stale after a source/schema change,
`tools.search({name})` retrieves the complete schema and explicitly refreshes the
binding before a subsequent call. Search grants no policy or approval authority; availability, revocation and approval retain their own checks.

The resolved Tool enters the existing lifecycle, invocation, policy/approval and
dispatch-admission wrappers. It is never dispatched directly through Manager.
Policy, approvals, display events and the canonical ToolCall/ToolResult use the real name
and arguments. Code Mode records each nested invocation under its `js_exec` parent.
Model history pairs each result with the tool name and call ID seen by the model.
MCP `structuredContent` accepts any JSON value on protocol `2026-07-28` and
later. Earlier negotiated protocols require an object. A successful reply with
a declared `outputSchema` must include a matching value. Explicit JSON `null`
is a value and is validated against the schema. Server-reported errors retain
their failure payloads and skip output schema validation. The adapter drops
text blocks that exactly repeat the serialized value and retains the other
content in server order.

Recovery never replays an unknown MCP
effect or invents a recoverer. At borrow, the Manager checks that the selected
callable still matches the exact published entry and that its client and catalog
are ready. A changed or removed entry makes old handles stale, even if the same
definition is later re-added. Unchanged entries keep their handles across
successful refreshes. The Manager releases its locks before MCP I/O.

## Directory refresh and revocation

A tool-list notification marks that server's catalog uncertain and blocks
fresh lookups, binding checks and new calls, including calls using retained
handles. One worker per server merges notifications in a bounded wake-up channel
and fetches a complete paginated list.
A notification generation change during pagination discards the read and retries
at most three times within the independent listing timeout. Repeated cursors,
duplicate names, listing failures and cancellation retain the last whole directory.
A failed refresh exposes a status warning and keeps admission blocked until a
later refresh succeeds, whether triggered by another notification or explicit
`Manager.RefreshTools`. The authorized
[Control retry command](experimental-features.md#retry-an-mcp-catalog-read) uses
the Session's selected connection. Sessions sharing that connection observe the
same refresh. Compaction has no role in directory admission.

The MCP client supports legacy standalone SSE and the SDK's
2026-07-28 subscription path. It suppresses the SDK's separate tool-page TTL cache:
otherwise a late page could repopulate that cache after notification invalidation.
The Manager remains the single directory owner. Cursor bytes stay opaque. The
page-count limit is 4096; there is no new per-server tool-count truncation.

A Host configuration change replaces the selected directory across affected Sessions
in one publication. During replacement startup, discovery exposes only currently
ready entries. Retained callables for a superseded connection fail with
`stale_revision`. An exact search after the replacement is ready refreshes the
binding. Calls admitted before publication finish on their original connection.

An accepted projected-name owner stays pinned for that Manager activation. Removing
it does not promote a colliding server or remote name. Candidates rejected from
the Session tool set because of a projected-name collision are not available to
Code Mode; qualified paths cannot restore them. `RevokeServer` blocks all new
calls immediately but leaves server/plugin shutdown to the embedding owner.
Runtime checks schema bindings when it resolves a call. Code Mode also checks
the selected target before dispatch. Calls already admitted can finish after
catalog changes or revocation and are never automatically retried.

Policy consumes typed Session approval/revocation facts and exact configuration
rules. Runtime checks cancellation and current authorization before consuming
its single-use dispatch permit. Persistent rule validation uses the existing
configuration transaction boundary. Both guards are released before MCP I/O.
Revocation prevents pending calls and late permission responses from restoring
access. See
[Policy admission](agent-sdk-boundary.md) for scope and historical data contracts.
