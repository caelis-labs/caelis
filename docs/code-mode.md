# Code Mode

The fixed `js_exec` entry runs JavaScript orchestration in a fresh Goja worker.
Actual tools, policy,
approval, Guardian, invocation identity, Journal and Task lifecycle remain owned
by Runtime.

## Model tools and discovery

The model uses one fixed `js_exec` entry with exactly one of `code` and `file`,
plus the foreground wait option. Runtime supplies general usage preferences and
an optional bounded hint snapshot through model instructions; the `js_exec`
description owns the JavaScript API contract. Hints contain partial name and
description summaries. Search finds tools not shown in the hints, and discovery
does not rewrite existing prompt slices or tool definitions.

Supply an async JavaScript function body, use `await`, and explicitly `return`
the desired result. Each execution starts with fresh state. `file` paths resolve
relative to the working directory. Use tools for filesystem and network I/O, and
RunCommand remains available both as a native function call and inside JavaScript.
Both routes use the same command producer, policy, approval, Journal and Task controls.

Call a known tool directly using the syntax shown by its name and schema. This
works for tools learned from Skills too; no search is needed when the schema is
known. Calls may use function or method syntax, for example:

```javascript
const file = await Read(args);
return {content: file.content};
await dtw.apps(args);
await context7['query-docs'](args);
```

Use `tools.search(selector)` when a schema is unknown or to find a tool. It
accepts these selectors:

- `{name}` looks up one exact tool name. A miss returns `not_found` and does not
  fall back to capability search.
- `{server,tool,plugin?}` looks up an exact method. An ambiguous match fails with
  `too_many_matches`; add `plugin` or use an exact name.
- `{query}` performs capability search only.
- `{server,plugin?,after?}` lists a server's tools. When `next` is present, pass
  it as `after` with the same `server` and `plugin` to continue.
- A legacy string tries an exact name first, then searches capabilities.

Search results have the shape
`{tools:[{name,description,input_schema,output_schema?,output_format,call_path,server?,tool?,plugin?}],count,truncated,omitted_count?,next?}`.
Each tool includes its complete schema. `server`, `tool`, and `plugin` preserve
original names when present. `call_path` is a JavaScript template such as
`tools.call("returned_name", args)`; supply your argument object for `args` and
use the path as returned. This handles special or conflicting names, including
names found during the current execution. `truncated`, `omitted_count`, and `next`
describe remaining results and pagination. Search returns this discovery object
directly; it is not a tool reply and cannot be passed to `tools.unpack`.

Calls resolve through one shared projection, regardless of direct syntax or
`tools.call`. Native tools that publish a structured JSON result expose their
business object: `const file = await Read(args)` allows `file.content`. Read's
content retains its existing line-number prefixes; projection does not parse it.
A tool's complete `output_schema`, when declared, is included in search results.

Successful MCP calls with an `outputSchema` expose their validated
`structuredContent` object. Missing or invalid structured content is a failed
result. Failed MCP calls expose diagnostic `{content:[...],structuredContent?}`
through `error.result`; its optional structured content is unverified and is not
promoted to a business value. MCP tools without that schema expose a fixed
`{content:[...],structuredContent?}` object. Content entries use the standard
`type` discriminator: text stays text, images/file references retain opaque
original identities, and resource/link objects retain their standard fields.
Projection never parses JSON-looking text or unboxes by the number of parts.
`output_format` identifies structured or standard content results; native tools
without a declared schema retain their native result contract.

`return await Read(args)` or `return await dtw.apps(args)` delivers the complete
original result, including its original text, JSON and attachments, when its
enumerable content and object references are unchanged. Nesting such an unchanged
original result retains its associated original text/media. Modified or copied
objects deliver their actual returned fields and original media references;
deleting `content` does not restore its removed text or attachments. Returning only
a business field delivers that selection; unreturned calls do not attach media.
Copying handle fields does not copy media authority. Only original media objects
from this invocation may select attachments, subject to the existing budgets.
An unchanged directly returned original result preserves original JSON tokens.
JavaScript computations and nested JSON encoding follow JavaScript Number
precision. When no business field has that name, non-enumerable `raw_json`
retains the original reply JSON string for exact tokens; returning that string
preserves them. A business `raw_json` field takes priority and makes this
compatibility read unavailable on that object. Convert BigInt to a string
before returning it. Omit undefined object fields or use null; encoding errors
identify the field path. An omitted return (or root undefined) is JSON null.

The SDK owns a narrow compatibility path for existing scripts: `.parts` and
`tools.unpack(reply)` keep access to original part objects, while `.raw_json`
keeps its original wire-reply meaning. Compatibility properties on business
objects are non-enumerable and never shadow an actual business field. Embedders
that return legacy `codemode.Reply` values without a result contract keep the
legacy shape. New scripts use business values; compatibility is retained until
those callers can migrate without losing original attachment identity.

Inspect `error.message`, `error.code` and `error.result` when present.
`error.reply` is a compatibility alias for that same projected result. Exact misses report
`not_found`; ambiguous matches report `too_many_matches`. A `stale_revision`
error means search again and revise the arguments. Observe unknown outcomes
without replaying the input.

## Runtime-owned execution contracts

Canonical search receipts and started child Journal records for Code Mode tool
executions preserve MCP identity/version bindings across fresh Goja workers,
compaction and file-Store resume. A first direct MCP call without a prior binding
pins the current real Tool identity/version. Its arguments and execution still
pass normal Runtime schema and policy admission. Later calls enforce that binding.
If the bound MCP source or schema changes, the direct call fails stale before
executing old arguments. Use `tools.search({name})` to get the complete schema and
explicitly refresh the binding, then make a later call under the refreshed binding.
Each VM snapshots current Session names; a later VM sees newly ready tools, and a
search result's `call_path` can resolve a name that became ready during the current
execution. Search does not rewrite existing prompt slices or model tool arrays.
Outside Code Mode, `mcp_call` still requires prior discovery.

Host failures reject with an Error whose `result` property contains the projected
failure diagnostics; `reply` aliases it. Failed MCP structured content has not
been validated as a successful business result.
Host failures also expose `error.code`; tool-specific error codes and effect
details stay in the reply's original content. Local validation or script errors may
lack `reply`.
Catching does not erase failed records. An uncaught script exception produces
`javascript_error` plus the Runtime-retained failed replies, including original
call IDs and JSON tokens. Caught failures retain compact disposition records and
explicitly selected output. JavaScript exception properties do not determine
execution outcomes or attachment identity. Model delivery and Task completion
use the same public diagnostics; internal Host causes stay private. Permission
messages are generic unless the Runtime owner explicitly marks a fixed diagnostic
as public. Uncaught failures retain media handles as JSON evidence without
attaching images. To deliver a diagnostic image, catch the error and return its
original media reference. An unknown outcome must never be replayed blindly.
Arguments follow each tool's schema, including required exact strings. Direct
calls, generated call paths, and tool search retain the Runtime-owned source,
schema, policy, approval, Guardian, cancellation, Journal, media and budget
checks. A stale schema requires a fresh search before a later call; old arguments
are never automatically executed.

Sources may implement the existing `tool.NamedSource`/`Registry.Lookup` contract.
Lookup grants no authority; both admission boundaries and the final Journal claim
remain in the original wrapper chain. Sources without Lookup retain the linear
fallback; fuzzy search retains its existing selector budgets. No source or
discovery lock crosses approval or Tool I/O. Snapshot hints remain fixed within this API version; a
software API change creates its own presentation snapshot, not a new grant cache.

JSON Numbers outside JavaScript's safe integer range may round. The
`raw_json` compatibility property described above retains original JSON tokens
when no business field shadows it. BigInt must be returned as a string. Return only JSON values; omit
optional undefined properties or use null. An omitted return is null. Encoding errors
identify the field path. Scripts choose sequential or concurrent calls according
to tool descriptions and operation semantics. `Promise.all` remains available;
Runtime enforces the tool owner's concurrency capability internally through the
[Run-owned execution gate](agent-sdk-boundary.md#runtime-and-capabilities).
Discovery does not expose a concurrency flag. Ordinary admission, call budgets,
cancellation and receipt ownership apply to every call.

## Authority and invalidation

The parent process owns Host, Session, credentials and Tool wrappers. The child
receives fixed source bytes/hash and bounded JSON messages; no Go object,
credential, environment, module, filesystem, network or shell API is exposed.
Goja has one VM goroutine; Host completions resolve promises on that owner.

Calls pass through the existing real Tool policy, Guardian/approval, source and
generation admission, effect claim, Journal and lifecycle wrappers. Discovery
is not an execution grant. Refresh remains low-frequency Manager-owned work;
searches do not push full catalog updates into prompts. Before an old binding
can execute, a changed source/schema produces a distinct stale-binding error:
the catalog was refreshed and tools.search({name}) must reload it. Old
arguments are not dispatched or automatically retried with a new schema.
Offline and revoked access retain their own errors.

The model receives only explicitly selected summaries and original media.
Actual child names, exact arguments, results, failures, unknown outcomes and
receipts remain in the existing audit/event chain. Filtering must not convert
failed or unknown execution to success. A caught, durably settled ordinary tool
failure permits subsequent calls and a final summary while retaining the failed
receipt and overall error flag. Cancellation, authority, persistence, transport
and unknown-effect failures still stop the script.

TUI gives each script invocation its own `Running script` / `Ran script`
disclosure with the existing Explored indentation. Actual child calls appear as
they start. The group header expands adjacent same-name summaries into individual
calls. Each call preview occupies one physical line, using the existing middle
fold for long arguments; clicking it expands the original arguments and clicking
again folds them. Failed calls are red; running calls use the existing activity
style. Model narration and unrelated direct calls remain outside. MCP arguments
are not interpreted to invent domain summaries.

The group dot uses the recorded script completion, independently of cumulative
`IsError` and Task state. A caught child failure remains red on that call while a
normally completed script has a normal dot. An uncaught exception has a red dot.
Older records without this observation do not infer script failure from child
errors. Zero-child scripts still have their own running or terminal entry.

The title includes a folded one-line source summary, or the script filename.
Clicking the summary uses the existing command argument disclosure to show the
frozen bytes that actually executed, including when the file has since changed.
Children show only real tool names, arguments and call state. A script with no
children shows its actual final return or short error through the existing
folded output panel; it never manufactures a completed/Result placeholder.
Source and output disclosure uses the existing panel token, without a new drawer.
Canonical IsError, media, receipts and approval routing keep their original owners.
Replay uses the same direct parents and existing Task/scope routing.

A yielded script observes its original Task even while its source or child list
is folded. Original Task terminal updates settle the script without requiring a
Task.read call. Returned business data, including a running inner command Task,
does not own the outer script's lifecycle. Commands and scripts have distinct
Task handles; cancel the producer that owns a pending command/approval. Canceling
an already returned outer script does not implicitly cancel an independent
inner command Task. Existing Run stop/generation boundaries remain authoritative.

## Execution and Tasks

Every execution starts a new Goja child process in the same Go executable.
Embedders must install the early WorkerMain dispatch documented in the
[SDK quickstart](../agent-sdk/README.md#code-mode-executable-entry).
There is no cross-call REPL, persistent Goja stack or restart replay. File reads
reuse the source-read policy path, then execute only those fixed bytes/hash.
CPU time and model network budgets are separate. Host/approval waiting is not
CPU time. Interrupt is followed by kill/reap if needed. No hard VM heap bound is
claimed; missing a hard heap limiter is not a default rejection condition.
64MiB is an experimental observation target, not a product admission threshold.
Process separation and JS capability restrictions do not constitute a secure OS
sandbox against an engine exploit.

Scripts yield within the original Run. The Run owner remains
until the producer is quiescent; it does not extend generation or permission
authority. Stop, cancellation and TurnComplete close further Host admission.
Final safe points cancel script producers before joining them, including an
approval waiter. Cross-Turn background execution is not promised.

`yield_time_ms` bounds only the foreground wait, not execution. After yielding,
use Task read/wait/cancel on the original handle; do not resubmit `js_exec`.
Foreground yield transfers observation to the existing Task owner with one
original handle. Query, wait, cancellation and completion reuse Task control and output cursors.
Selected output is published at script completion; child progress uses the
original tool event stream. Waiting less does not shorten execution. A yielded
child keeps its original handle; scripts can query it through Host without
resubmitting the operation. Cancellation reaches owned work and reaps the worker;
completed/unknown effects retain their original receipts. Restart exposes the
record and interrupted/unknown state, without recreating the script or stack.
Task support does not grant shell access from JS.

Cursor checkpoints write only when the delivered model frontier advances.
Checkpoints and terminal commits share the existing Task mutation claim and
CAS revision. A terminal persistence failure preserves the last committed
snapshot and original child receipts; Run producer completion and Task
observations surface that failure. Producer closure is reported as unknown
outcome rather than publishing an uncommitted completed state. Scripts and
child effects are never replayed to repair durability.

## Acceptance

Owning checks cover native fs plus code-level shell denial (including aliases),
DTW domain exec, capability/exact-name search, Session reuse, schema replacement before effect claim,
Guardian parameters, async yield/handle/cancel/completion and unknown no-replay.
Model-context round trips and rendered TUI parent/child evidence cover durable
and visible behavior.
