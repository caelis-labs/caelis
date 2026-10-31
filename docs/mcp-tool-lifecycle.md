# MCP tools and fixed model entry points

Every MCP-enabled native Session presents the fixed `tool_search` and `mcp_call`
function declarations. Directory changes, search results and reconnection never
append MCP schemas to the model's `tools` array. Ordinary model capability filters
still apply to non-MCP tools. This contract covers the serialized tools field;
messages, instructions, model configuration and other request prefixes can change.

## Discovery and invocation

`tool_search` uses the existing Manager directory and selector. Results contain
exact names, descriptions, complete normalized `input_schema` objects, and
identity/version bindings. A per-result budget omits whole entries, with omission
counts, rather than returning truncated callable schemas. There is no per-run
64-tool limit or accumulated schema-token exposure budget.

Successful new-format canonical search receipts are the sole durable discovery
source. Runtime projects their bindings across compaction and file-Store resume;
it does not maintain another durable registry. Old ToolSearch results, exposure
metadata and checkpoint discovered-name fields remain readable history only.
Legacy Sessions resume with fixed tools and must search again before an MCP call.
There is no legacy execution path or `tool_retire` tool.

`mcp_call(tool_name, arguments)` requires an exact previously discovered name and
object arguments. Runtime binds the assembled real Tool once per complete model
response, compares its identity/version and validates its accepted input schema.
A search in the same response cannot authorize an undiscovered sibling call.
Changed or unavailable bindings fail closed and require a new search. No name
lookup fallback grants eligibility.

The resolved Tool enters the existing lifecycle, invocation, policy/approval and
execution-journal wrappers. It is never dispatched directly through Manager.
Policy, approvals, display events and the one Journal record use the real name
and arguments. The model-facing call/result retain the original `mcp_call` name
and call ID; a typed original-call field preserves that pairing through Store
round trips and unknown-result recovery. Recovery never replays an unknown MCP
effect or invents a recoverer. Final Manager admission still checks the pinned
catalog handle immediately before dispatch.

## Directory refresh and revocation

A tool-list notification immediately makes that server's catalog uncertain and
blocks new dispatch through all its pinned handles. One worker per server merges
notifications in a bounded wake-up channel and fetches a complete paginated list.
A notification generation change during pagination discards the read and retries
at most three times within the existing startup timeout. Repeated cursors,
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
it does not promote a colliding server or remote name. `RevokeServer` blocks all
new calls immediately but leaves server/plugin shutdown to the embedding owner.
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
