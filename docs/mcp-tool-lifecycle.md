# MCP tools and fixed model entry points

Native product Sessions use [Code Mode](code-mode.md): the fixed `js_exec`
declaration exposes Session tool routes by original name plus `tools.search` and
`tools.call` inside an async script. SDK embedders without Code Mode use the fixed
`tool_search` and `mcp_call` function declarations. Directory changes, search
results and reconnection never append MCP schemas to the model's `tools` array.
Ordinary model capability filters
still apply to non-MCP tools. This contract covers the serialized tools field;
messages, instructions, model configuration and other request prefixes can change.

## Discovery and invocation

Both `tools.search(query)` in Code Mode and `tool_search` without Code Mode
use the existing Manager directory and selector. Results contain
exact names, descriptions, complete normalized `input_schema` objects, and
identity/version bindings. A per-result budget omits whole entries, with omission
counts, rather than returning truncated callable schemas. There is no per-run
64-tool limit or accumulated schema-token exposure budget.

MCP startup is asynchronous. Search reads the currently ready directory once;
it does not wait for connecting servers. An empty legacy search result serializes as `{}`; Code Mode returns an empty
`tools` list. Neither grants a discovery binding. It means no ready match at that instant, including
while all servers are connecting, rather than proof that the plugin has no tools.
A fresh search after readiness can return complete schemas. A later Code Mode
execution also snapshots the then-current Session names and can call a known
original name directly; non-Code Mode `mcp_call` still requires a fresh discovery
binding. An earlier empty search receipt is never filled in later. Complete ready
servers remain searchable independently of slower or failed servers.

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
activation. Search after readiness when a complete schema is needed or before
non-Code Mode `mcp_call`. Code Mode snapshots current Session names for each
execution; a direct call needs no search when its schema is known, and canonical `tools.call`
can resolve a newly ready name during the current execution. A provider fixture
can wait between model responses within that original Turn. It must not inject a
warmup Prompt, use stale results as evidence of current readiness, or treat
`connecting` as tool absence.

Canonical search receipts remain the durable discovery records. Started child
Journal records for Code Mode tool executions also preserve the binding used by a
direct MCP call.
Runtime rebuilds both from canonical events across fresh VMs, compaction and
file-Store resume; it does not maintain another durable registry. Old ToolSearch
results, exposure metadata and checkpoint discovered-name fields remain readable
history only. Historical name-only discovery requires a fresh search for
non-Code Mode `mcp_call`; a Code Mode direct call without a prior binding pins the
current real Tool identity/version. Valid bindings remain reusable while their
source and schema are unchanged. There is no legacy execution path or
`tool_retire` tool.

`mcp_call(tool_name, arguments)` is for SDK embedders without Code Mode. It
requires an exact previously discovered name and object arguments. Runtime binds
the assembled real Tool once per complete model response, compares its
identity/version and validates its accepted input schema. Without Code Mode, a
search in the same model response cannot authorize an undiscovered sibling call.
In Code Mode, an original-name call with a known schema or `tools.call(name, args)` can run
without search. A first direct MCP call with no prior binding pins the current
identity/version. If a later call is stale after a source/schema change,
`tools.search(name)` retrieves the complete schema and explicitly refreshes the
binding before a subsequent call. Search grants no policy or approval authority;
availability, revocation and approval retain their own checks. No name lookup
fallback grants eligibility to non-Code Mode `mcp_call`.

The resolved Tool enters the existing lifecycle, invocation, policy/approval and
execution-journal wrappers. It is never dispatched directly through Manager.
Policy, approvals, display events and the one Journal record use the real name
and arguments. Without Code Mode, the model-facing call/result retain the original `mcp_call`
name and call ID; a typed original-call field preserves that pairing through
Store round trips and unknown-result recovery. Recovery never replays an unknown MCP
effect or invents a recoverer. Final Manager admission still checks the pinned
catalog handle immediately before dispatch.

## Directory refresh and revocation

A tool-list notification immediately makes that server's catalog uncertain and
blocks new dispatch through all its pinned handles. One worker per server merges
notifications in a bounded wake-up channel and fetches a complete paginated list.
A notification generation change during pagination discards the read and retries
at most three times within the independent listing timeout. Repeated cursors,
duplicate names, listing failures and cancellation retain the last whole directory.
A failed refresh exposes a status warning and keeps dispatch blocked until another
notification or explicit `Manager.RefreshTools` succeeds. The authorized
[Control retry command](experimental-features.md#retry-an-mcp-catalog-read) uses
that same activation-owned Manager. Unrelated compaction does not inspect or mutate the MCP directory.

The MCP client supports legacy standalone SSE and the SDK's
2026-07-28 subscription path. It suppresses the SDK's separate tool-page TTL cache:
otherwise a late page could repopulate that cache after notification invalidation.
The Manager remains the single directory owner. Cursor bytes stay opaque. The
page-count limit is 4096; there is no new per-server tool-count truncation.

An accepted projected-name owner stays pinned for that Manager activation. Removing
it does not promote a colliding server or remote name. Candidates rejected from
the Session tool set because of a projected-name collision are not available to
Code Mode; qualified paths cannot restore them. `RevokeServer` blocks all new
calls immediately but leaves server/plugin shutdown to the embedding owner.
A source/schema change also rejects already-selected handles at final dispatch,
including those that were waiting for approval. Calls admitted before revocation
retain their results and are never automatically retried. This cannot undo an
external effect already in flight or detect a remote removal before the client
receives evidence of it.

MCP grants separately capture per-scope revocation epochs before review and check
them at dispatch. Revocation while waiting for source or per-call approval rejects
the old call; a late approval cannot recreate its persistent grant. These checks
apply independently of compaction. Epochs are process-local: the existing
shared Host grant store owns persistence, and no pending callback survives a
Runtime restart.
