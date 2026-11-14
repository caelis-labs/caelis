# Code Mode

The fixed `js_exec` entry runs JavaScript orchestration in a fresh Goja worker.
Actual tools, policy,
approval, Guardian, invocation identity, Journal and Task lifecycle remain owned
by Runtime.

## Model tools and discovery

Core filesystem and shell tools keep their native function-call definitions.
Filesystem tools are also callable from JavaScript through their real wrapped
Tool identities. Shell, RunCommand, aliases and equivalent native process-launch
capabilities are rejected in code before JS dispatch. A name containing `exec`
is not sufficient to classify a tool: Desktop World exec is a domain operation,
not the native shell entry.

Extensions use one fixed `js_exec` definition, accepting exactly one of `code`
and `file`, plus the existing foreground wait convention. The model does not
also receive `tool_search`, `mcp_call`, or changing MCP schemas in its tools array.
Product assembly selects canonical extension names via
`AgentSpec.Metadata[codemode.MetadataSelectedTools]`; the SDK has no integration
allowlist. A small name/description catalog is a partial, bounded Session hint
snapshot. Code Mode can call any currently available Session tool by its known
original name; search is for complete schemas and explicit binding refreshes, not a
call prerequisite. Discovery never rewrites earlier prompt slices or changes the
serialized tools definitions.

Runtime supplies the [SDK usage guidance](../agent-sdk/tool/codemode/codemode.go)
and hint snapshot through `model.Request.Instructions`, separate from user and
tool messages. Responses uses the top-level `instructions` field; Chat
Completions uses a `system` message. Hint descriptions remain external capability
metadata. The `js_exec` description is the short entry summary; the guidance owns
detailed API usage, output selection and error handling.

Supply an async function body, use `await`, and explicitly `return` the desired
output. Each execution starts with fresh state. There are no Node.js, module,
direct filesystem, network or process APIs. `file` paths resolve relative to the
working directory; shell execution stays outside `js_exec`.

The async function body can call Session tools by their original names and
exposes these SDK operations:

- Call a currently available Session tool by its original name when its schema
  is known, including from a Skill. No search is required first. For example:
  ```javascript
  await Read(args);
  await dtw.apps(args);
  await context7['query-docs'](args);
  ```
  Each Goja execution snapshots the names currently available to the Session.
  Names that collide with JavaScript or bridge globals, or compete for a shortcut,
  have no global shortcut. Use these qualified paths for exact names:
  ```javascript
  await tools.native[originalName](args);
  await tools.mcp[rawServer][rawTool](args);
  await tools.plugins[plugin][rawServer][rawTool](args);
  ```
  An ambiguous explicit path rejects the call. These routes never add aliases,
  overwrite globals, or restore tools rejected by Manager name-collision handling.
  The current
  `js_exec` and native shell/process tools are not call targets, so scripts cannot
  recursively invoke Code Mode or launch native processes.
- `await tools.search(query)` is for complete schema lookup. It first matches an
  exact callable native or current extension name, otherwise uses the existing
  fuzzy search. It returns
  `{tools:[{name,description,input_schema}],count,truncated,omitted_count?,next?}`.
  `tools.search({server,plugin?,after?})` instead matches an exact MCP server and
  optional plugin identity without fuzzy selection. Current model and source
  eligibility are applied before the result limits and schema budget. Service
  pages use name order; when `next` is present, pass it as `after` with the same
  server/plugin to continue. A removed, revoked or foreign continuation anchor
  requires restarting discovery. Both routes return complete schemas;
  `truncated` and `omitted_count` describe remaining eligible candidates.
  An individually oversized schema produces a budget error. Extension bindings
  are recorded by the original canonical discovery owner. Use search when the
  schema is unknown or to explicitly refresh a stale binding.
- Tool calls return a Promise resolving to `{parts:[...]}`. Text parts have
  `text`, JSON parts have `json`, and media parts carry an opaque invocation-owned
  handle.
- `tools.unpack(reply)` returns `{json:[],text:[],media:[]}` in each part kind's
  original order. All JSON and text parts are retained, including repeated
  representations; media entries are original part references. The input must be
  an original tool reply, including one obtained from `error.reply`.
- For canonical-name compatibility, `tools.call(name, args)` uses the same call
  path and reply shape.

Returning the result of a direct tool call, `tools.call(name, args)`, or
`tools.search(query)` delivers its selected output to model context. Scripts may
process or combine returned values. Only media present in the final return value is selected;
unreturned or historical media from other calls is ignored.
The bridge tracks real reply/part object identities privately. Selecting those
references preserves media; copying or reconstructing their fields creates
ordinary JSON. Business properties named kind, parts or handle have no control
meaning. Returned references are checked against the current invocation and
retained media budgets. A failed or unknown child remains failed even if JavaScript catches its
exception. No `tools.describe` or `output` interface is installed.

Canonical search receipts and started child Journal records for Code Mode tool
executions preserve MCP identity/version bindings across fresh Goja workers,
compaction and file-Store resume. A first direct MCP call without a prior binding
pins the current real Tool identity/version. Its arguments and execution still
pass normal Runtime schema and policy admission. Later calls enforce that binding.
If the bound MCP source or schema changes, the direct call fails stale before
executing old arguments. Use `tools.search(name)` to get the complete schema and explicitly
refresh the binding, then make a later call under the refreshed binding. Each VM
snapshots current Session names; a later VM sees newly ready tools, and
`tools.call(canonicalName, args)` can also resolve a name that became ready during
the current execution. Search does not rewrite existing prompt slices or model
tool arrays. Outside Code Mode, `mcp_call` still requires prior discovery.

Host failures reject with an Error whose `reply` property contains the host reply.
Typed Host failures also expose `error.code`; tool-specific error codes and effect
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
Arguments follow the schema, including required exact strings. Native filesystem
functions remain directly callable and may also be orchestrated from JS. Direct,
qualified and canonical calls use the same Runtime source, generation, schema,
policy, approval, Guardian, cancellation, Journal, media and budget checks. A stale
binding requires an explicit schema search before a later call; old arguments are
never automatically executed.

Sources may implement the existing `tool.NamedSource`/`Registry.Lookup` contract.
Lookup grants no authority; both admission boundaries and the final Journal claim
remain in the original wrapper chain. Sources without Lookup retain the linear
fallback; fuzzy search retains its existing selector budgets. No source or
discovery lock crosses approval or Tool I/O. Snapshot hints remain fixed within this API version; a
software API change creates its own presentation snapshot, not a new grant cache.

JSON Numbers outside JavaScript's safe integer range may round. The original
reply remains explicitly accessible through non-enumerable `reply.raw_json`,
which retains original JSON tokens. BigInt must be returned as a string. Return only JSON values; omit
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
the catalog was refreshed and tools.search(name) must reload it. Old
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

The outer script source, return values and diagnostic bodies have no trajectory
entry, including on failure or with no child calls. Argument disclosure uses the
original call inputs and does not display result bodies. Canonical receipts,
IsError, media and approval routing retain their original owners. Replay uses
the same direct parent identities; child panes retain existing Task and scope
routing.

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
