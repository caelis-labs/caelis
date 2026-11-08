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
allowlist. A small name/description catalog is a partial, bounded Session hint snapshot;
other extensions are found by fuzzy search. Discovery never rewrites earlier prompt
slices or changes the serialized tools definitions.

Supply an async function body, use `await`, and explicitly `return` the desired
output. Each execution starts with fresh state. There are no Node.js, module,
direct filesystem, network or process APIs. `file` paths resolve relative to the
working directory; shell execution stays outside `js_exec`.

The async function body has two interfaces:

- `await tools.search(query)` first matches an exact callable native or current
  extension name, otherwise uses the existing fuzzy search. It returns
  `{tools:[{name,description,input_schema}],count,truncated}`. Schemas are complete;
  candidates omitted by the bounded result budget are marked by `truncated`.
  Extension bindings are recorded by the original canonical discovery owner.
- `tools.call(name, args)` returns a Promise resolving to `{parts:[...]}`. Text parts have `text`,
  JSON parts have `json`, and media parts carry an opaque invocation-owned handle.

`return await tools.call(name, args)` delivers the text, JSON and actual media to
model context. `return await tools.search(query)` delivers the search result.
Scripts may process or combine returned values; only media present in the final
return value is selected, never other calls' unreturned or historical media.
The bridge tracks real reply/part object identities privately. Selecting those
references preserves media; copying or reconstructing their fields creates
ordinary JSON. Business properties named kind, parts or handle have no control
meaning. Returned references are checked against the current invocation and
retained media budgets. A failed or unknown child remains failed even if JavaScript catches its
exception. No `tools.describe` or `output` interface is installed.

First MCP discovery establishes bindings reusable within that Session. Search
and calls may be sequentially awaited in the same script. Known schemas may be
reused across calls; a fresh VM clears only JS variables. Host failures reject
with an Error whose `reply` property contains the host reply. Local validation or
script errors may lack `reply`. Catching does not erase failed records, and an
unknown outcome must never be replayed blindly.
Arguments follow the schema, including required exact strings. Native filesystem
functions remain directly callable and may also be orchestrated from JS. Search
and call retain current source, generation, binding and permission checks. On a
stale result, search again; old arguments are not automatically executed.

Sources may implement the existing `tool.NamedSource`/`Registry.Lookup` contract.
Lookup grants no authority; both admission boundaries and the final Journal claim
remain in the original wrapper chain. Sources without Lookup retain the linear
fallback; fuzzy search retains its existing selector budgets. No lock crosses
approval or Tool I/O. Snapshot hints remain fixed within this API version; a
software API change creates its own presentation snapshot, not a new grant cache.

JSON Numbers outside JavaScript's safe integer range may round. The original
reply remains explicitly accessible through non-enumerable `reply.raw_json`,
which retains original JSON tokens. BigInt must be returned as a string.
Concurrent calls still require every target to be explicitly ParallelSafe.

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
failed or unknown execution to success. TUI projects those original child
identities in a default-collapsed Code Mode group. While running, its summary
shows the current child action. Opening the group shows actual tool names and
brief arguments; each tool's script, arguments and output have their own detail
disclosure. Approval decisions remain visible outside the collapsed group.
Grouping reuses the existing Turn and call identities and does not change the
execution path.

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

Exact serialized model tools and stable leading prompt bytes are captured for
first discovery, repeated orchestration and refresh/rediscovery. Token and model
request comparisons use the same task, actual usage and retained source/output
sizes; source-line counts or fixture-only usage are not savings evidence. Both
routes must have the same saved-script obligation, shell prohibition and chosen
summary/image outputs. Evaluate file edit/reread capability separately from
inline batching savings; first and resumed Turns are separate accounting groups.
A historical mixed-task usage difference does not isolate engine cost.
Real acceptance uses streaming models with independent generation budgets of at
least 600 seconds, no shorter enclosing client/header deadline and no added
request retry. Authorized existing gpt-6-luna is preferred; an explicit quota
failure permits deepseek-flash. Credentials are read only through the selected
existing configuration path, never exported into shared evidence. Context7 read
queries, a task-owned DTW observation/screenshot fixture and temporary fs work
are kept separate from deterministic fixtures and broader platform claims.
