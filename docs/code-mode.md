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
allowlist. A small name/description catalog is a bounded Session snapshot; other
extensions are found by fuzzy search. Discovery never rewrites earlier prompt
slices or changes the serialized tools definitions.

The async function body has these interfaces:

- `await tools.search(query)` is for unknown extension names and returns
  `{tools:[{name,description}],count,truncated}`. Candidates do not admit calls.
- `await tools.describe(name)` accepts a name string (for example
  `searchResult.tools[0].name`), returns the current complete input schema and
  records the discovery binding through the existing canonical owner.
- `await tools.call(name, arguments)` invokes the already discovered Tool once.
- `output.text(value)` and `await output.image(handle)` explicitly select output.

Call native filesystem functions already in `tools` directly; do not search the
extension directory for them. For a known extension name,
use exact describe once only when the complete schema is absent from context.
Otherwise call directly. Valid discovery in the same Session is reusable across
VMs and Turns; a fresh VM clears local JS variables. Runtime still checks current
identity, source/schema, generation and permissions on every call. Missing schema
can be fetched by name without fuzzy search.
Choose arguments by the actual schema. Strings that the task requires to be
passed exactly must be transmitted unchanged, rather than paraphrased.

Sources may implement `tool.NamedSource` using the existing `Registry.Lookup`
signature. Runtime wraps only the located canonical candidate and restores a
Host from its Session's discovered bindings. Lookup grants no eligibility:
source/schema, generation, policy and both execution admission boundaries remain
live. MCP Manager derives its name index from its current directory under the
same owner lock; dirty, revoked, replaced or disconnected sources cannot return
a dispatchable candidate or promote another source's same-name tool. No lock is
held across approval or tool I/O. Sources without Lookup use the existing linear
enumeration fallback. Unknown fuzzy search still covers its candidate directory
under the normal selector budgets.

Unknown schemas must be returned to the model before a later script supplies
arguments. JavaScript is orchestration, not a second model that understands a
previously unknown schema. Native fs identities need no redundant MCP discovery.
Returned JSON outside JavaScript's safe integer range may round in Number;
original JSON and receipts retain their tokens. `reply.raw_json` is explicitly
accessible and lossless, but non-enumerable: `output.text(reply)`, JSON.stringify
and object spread omit that duplicate serialization. Selected `parts` keep their
normal shape; explicitly output `reply.raw_json` when original tokens are needed.
BigInt must be stringified.

Tool replies carry normalized `parts`: select `kind === "json"` and read
`.json`, rather than expecting the MCP envelope's `structuredContent` property.
Inspect the returned payload shape before choosing business fields. Perform
deterministic filtering, grouping, counting and calculation in the VM, then emit
only the required summary and sources. For a JSON payload with `batch_id` and
`records`:

```js
const r = await tools.call(name, args);
const d = r.parts.find(p => p.kind === "json").json;
output.text({count: d.records.length, source: d.batch_id});
```

Filesystem orchestration is optional. For numeric file contents, await reads
sequentially when computing in JS:

```js
const a = await tools.call("Read", {path: "a.txt"});
const b = await tools.call("Read", {path: "b.txt"});
const x = a.parts.find(p => p.kind === "json").json;
const y = b.parts.find(p => p.kind === "json").json;
output.text({total: Number(x.content) + Number(y.content), sources: ["a.txt", "b.txt"]});
```

After describing an extension, select image handles in the same invocation:

```js
const r = await tools.call(name, args);
for (const p of r.parts) if (p.handle) await output.image(p.handle);
```

These examples do not grant parallel safety. Concurrent calls still require
every target to be explicitly ParallelSafe.

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
the catalog was refreshed and tools.describe(name) must reload it. Old
arguments are not dispatched or automatically retried with a new schema.
Offline and revoked access retain their own errors.

The model receives only explicitly selected summaries and original media.
Actual child names, exact arguments, results, failures, unknown outcomes and
receipts remain in the existing audit/event chain. Filtering must not convert
failed or unknown execution to success. TUI projects those original child
identities under a collapsible js_exec parent; discovery is compact presentation,
not another consumer-owned execution path.

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
DTW domain exec, candidate search/exact describe, Session reuse, schema replacement before effect claim,
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
