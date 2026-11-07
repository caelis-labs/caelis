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
A small selected name/description catalog is a bounded Session snapshot; other
extensions are found by fuzzy search. Discovery never rewrites earlier prompt
slices or changes the serialized tools definitions.

The async function body has these interfaces:

- `await tools.search(query)` returns bounded candidate names and descriptions.
- `await tools.describe(name)` returns the current complete input schema and
  records the discovery binding through the existing canonical owner.
- `await tools.call(name, arguments)` invokes the already discovered Tool once.
- `output.text(value)` and `await output.image(handle)` explicitly select output.

Unknown schemas must be returned to the model before a later script supplies
arguments. JavaScript is orchestration, not a second model that understands a
previously unknown schema. Native fs identities need no redundant MCP discovery.
Returned JSON outside JavaScript's safe integer range may round in Number;
original JSON and receipts retain their tokens. BigInt must be stringified.

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
the catalog was refreshed and tools.describe/search must rediscover it. Old
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

## Acceptance

Owning checks cover native fs plus code-level shell denial (including aliases),
DTW domain exec, search/describe/call, schema replacement before effect claim,
Guardian parameters, async yield/handle/cancel/completion and unknown no-replay.
Model-context round trips and rendered TUI parent/child evidence cover durable
and visible behavior.

Exact serialized model tools and stable leading prompt bytes are captured for
first discovery, repeated orchestration and refresh/rediscovery. Token and model
request comparisons use the same task, actual usage and retained source/output
sizes; source-line counts or fixture-only usage are not savings evidence.
Real acceptance uses streaming models with independent generation budgets of at
least 600 seconds, no shorter enclosing client/header deadline and no added
request retry. Authorized existing gpt-6-luna is preferred; an explicit quota
failure permits deepseek-flash. Credentials are read only through the selected
existing configuration path, never exported into shared evidence. Context7 read
queries, a task-owned DTW observation/screenshot fixture and temporary fs work
are kept separate from deterministic fixtures and broader platform claims.
