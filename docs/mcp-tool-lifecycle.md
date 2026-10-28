# MCP tool lifecycle

MCP catalog refresh and deferred tool retirement are always enabled. `mcp.NewManager`
processes tool-list notifications and refreshes the server capability directory.
Runtime uses version-bound ToolSearch exposure and the temporary `tool_retire`
management tool with the ordinary `tool_search` tool and MCP definitions carrying
complete source fingerprints. Asynchronous and idle compaction remain independent opt-ins;
see [Experimental features](experimental-features.md).

Refresh can run without any compact. Retirement applies at a durable canonical
compact, with synchronous, asynchronous or idle compaction. Neither disconnects a
server, uninstalls a plugin, or deletes history.
The reproducible local protocol and long-session experiments are described in
[Testing](testing.md#experimental-mcp-tool-lifecycle).

## Ownership and identity

MCP Manager owns the full accepted server capability directory. ToolSearch only
selects from that directory. Runtime owns each Session's exposed subset, policy,
approval, journaling, compaction and replay. The chat loop pins the callable and
schema for a model response and its complete tool batch. Every next request
refreshes the visible catalog in both static and profile-resolved Sessions,
independently of retirement. Sources pass through
the existing Runtime wrappers; there is no separate execution path.

A projected name remains the exact routing name. Discovery also
returns a `binding` with `name`, an opaque `identity` hash of plugin/server/remote
name, and `version`, the existing MCP source fingerprint. The version includes
configuration, effective stdio launch environment digest, declared server version,
protocol and full remote tool definition. Secrets are not returned. After a successful refresh, an already exposed tool
with the same projected name uses its current definition on the next model
request, without another ToolSearch. A changed source still requires fresh source
approval. The pinned response and tool batch keep their original schema; obsolete
handles fail final dispatch validation. Newly added, unexposed names still require
ToolSearch.
Connection identity is additionally pinned by the callable; a reconnected
Manager cannot revive a handle from a closed connection. A tool that disappears
and later returns also receives a new callable binding, even with identical
identity/schema bytes; old pending calls remain invalid.

Canonical ToolSearch results retain these bindings without repeating schemas.
Name-only legacy discoveries do not authorize version-bound exposure: old
messages/results remain readable, and tools can be rediscovered.

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
that same activation-owned Manager. A stable failed or disconnected directory
can be captured for an unrelated checkpoint; a changed revision or incomplete
initialization still rejects adoption, and unavailable tools stay blocked.

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

## Marking and safe adoption

After the first dynamic tool is exposed, Runtime also exposes `tool_retire`. Its
stable schema accepts `action: "mark" | "cancel"` and exact bindings in `tools`.
It validates the whole request before recording marks. Marking preserves the
current tool schemas and grants. Rediscovery returns the binding even for an
already exposed tool, and cancels its mark; use in the same complete tool batch
also cancels its mark conservatively. The server directory stays searchable.

Runtime stores a versioned exposure projection in canonical result metadata.
Temporarily unready catalogs do not erase exact durable bindings or pending
marks; only ready, current bindings become callable.
At compaction persistence it derives the current projection from canonical events,
removes eligible marked bindings, and puts the resulting set in the same
checkpoint append. Only a confirmed append is adopted. Exposure reconstruction
requires the canonical compact event's ID, sequence and valid coverage; a
generated or ready candidate has no durable sequence and cannot retire tools.
Rereading the same event is an idempotent projection. There is no second
state update after the checkpoint. Failure or cancellation before commit leaves
the previous projection. An uncertain append reads back the original event ID
and exact payload: a confirmed commit may be adopted, while an unresolved result
returns an error without live adoption. A subsequent canonical load recovers any
committed checkpoint. Reconciliation never repeats a tool effect. All original
ToolCall/ToolResult events remain intact.

Unpaired pending tool calls prevent retirement across the checkpoint. A manual
compact while this Runtime has an active model/tool batch preserves the marks;
an idle compact or Runtime's normal safe compaction boundary may apply them.
This mechanism requires one live Runtime owner per Session, under the existing Session
fence. When all dynamic tools are retired, the management tool disappears too.
ToolSearch remains available and can expose the tools and manager again. A
retired tool remains absent on later model requests, even if catalog refresh
keeps its name available. It requires a new ToolSearch discovery to become
exposed again.

The default policy is explicit per-tool marking. A service-level action could
expand to the exact currently exposed bindings when marked, but must not capture
future tools or disable that service. No such action is implemented. Frequency,
recency or LRU are useful measurements; they are not reliable evidence that a tool
is no longer needed, particularly for rarely used recovery tools. Automatic
usage-based retirement is not implemented.

## Cache and async compatibility

Marking changes result/history data but not the tools prefix. Adding discovered
tools changes the tools list; removal at a successful compact changes it again
alongside the new context baseline. The existing local prefix estimator hashes
instructions, tools and output shape, not provider KV state. Codex OAuth uses
Session affinity for its prompt-cache key; ordinary OpenAI Responses does not set
that key. Native Anthropic requests install up to four 5-minute cache-control
breakpoints after encoding; Anthropic-compatible routes do not inherit that
policy. Cache usage preserves reported zero values and missing fields. Stable
affinity or installed breakpoints alone do not prove reuse.

OpenAI requires compatible rendered prefixes and request settings for reuse;
Anthropic describes the hierarchy as tools, system, then messages. Therefore
removing schemas can invalidate reuse of later blocks even if their text is
unchanged. These are protocol-informed expectations, not measured savings.
See [OpenAI prompt caching](https://developers.openai.com/api/docs/guides/prompt-caching)
and [Anthropic prompt caching](https://platform.claude.com/docs/en/build-with-claude/prompt-caching).

The checkpoint integration contracts are:

- Capture `Manager.CatalogRevision()` (also forwarded by the Runtime deferred
  wrapper) and `MCPGrantStore.RevocationRevision()` along with activation, model,
  policy and request-prefix identity. Revalidate before adoption. Catalog revision
  covers notifications and hidden tools, not just model-visible schemas; it is
  activation-local, not a provider cache key or cross-reconnect identity.
- `Manager.ValidateCatalog(ctx, revision)` and
  `MCPGrantStore.ValidateRevocation(ctx, revision)` return release guards for the
  short checkpoint commit. Both fail closed on contention or stale identity;
  the catalog guard also rejects an uncertain or uninitialized directory.
  Compose them with the embedding's configuration/policy guard in one consistent
  lock order, and release in reverse order on every path. Map catalog staleness to
  the model-snapshot stale error. Do not call guarded owners again while held.
  Guard acquisition is separate from provider `Admit` and does not record a
  request. Never hold these guards across generation, approval or tool execution.
  Local notification, connection-loss publication, refresh, revocation and
  dispatch admission linearize outside the commit critical section; this does
  not defer revocation to a future compact or claim knowledge of remote changes
  before local observation.
- Mark/cancel alone need not invalidate a candidate's model prefix. Adoption must
  read the current canonical exposure and complete tail, so later use/rediscovery
  can cancel an earlier mark. Never restore the background snapshot's pending set.
- Run `checkpointExposure` inside the validated persistence path using the
  freshly revalidated current source. Use the original fence and CAS; retry only
  with the checkpoint caller's source validator. Recompute exposure after any accepted
  CAS tail change before retrying an append known not to have committed. Unknown
  writes must retain the original event ID and full payload for reconciliation.
  Recompute request usage after canonical adoption.
- Any catalog, source/schema, grant, activation or policy change conservatively
  invalidates the candidate. An ordinary exposure change invalidates its request
  prefix. Failed compact/adoption keeps both marks and the old exposed set.

Host assembly supplies asynchronous and idle compaction options from activation-pinned
Control defaults; refresh and retirement do not depend on either option.
Runtime composes the directory and grant guards with the existing request
validator for synchronous, asynchronous and idle checkpoints. Explicit-profile
requests additionally hold their desired configuration revision through commit.
The same writer consumes current canonical exposure and reconciles uncertain
writes; no compaction callback or separate retirement transaction is installed.
