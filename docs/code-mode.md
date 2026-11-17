# Code Mode

The fixed `js_exec` entry runs JavaScript orchestration in a continuous Goja environment for the active host Session.
Actual tools, policy,
approval, Guardian, invocation identity, tool facts and Task lifecycle remain owned
by Runtime.

## Model tools and discovery

The model uses one fixed `js_exec` entry with exactly one of `code`, `file` and `reset:true`,
plus the foreground wait option. Runtime supplies general usage preferences and
an optional bounded hint snapshot through model instructions; the `js_exec`
description owns the JavaScript API contract. Hints contain partial name and
description summaries. Search finds tools not shown in the hints, and discovery
does not rewrite existing prompt slices or tool definitions.

Supply an async JavaScript function body, use `await`, and explicitly `return`
the desired result. Top-level state remains available in the active environment. `file` paths resolve
relative to the working directory. Use tools for filesystem and network I/O, and
RunCommand remains available both as a native function call and inside JavaScript.
Both routes use the same command producer, policy, approval, tool facts and Task controls.

Call a known tool directly using the syntax shown by its name and schema. This
works for tools learned from Skills too; no search is needed when the schema is
known. Calls may use function or method syntax, for example:

```javascript
const file = await Read(args);
return {content: file.json().content};
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

Read a search Body with `(await tools.search(selector)).json()`. Its JSON has the shape
`{tools:[{name,description,input_schema,output_schema?,output_format,call_path,server?,tool?,plugin?}],count,truncated,omitted_count?,next?}`.
Each tool includes its complete schema. `server`, `tool`, and `plugin` preserve
original names when present. `call_path` is a JavaScript template such as
`tools.call("returned_name", args)`; supply your argument object for `args` and
use the path as returned. This handles special or conflicting names, including
names found during the current execution. `truncated`, `omitted_count`, and `next`
describe remaining results and pagination. Search uses the same repeatable Body API as every other tool entry.

Every tool call returns the same Body object, through direct functions and
`tools.call` alike:

```javascript
const response = await Read({path: "input.json"});
const file = response.json();
return {content: file.content};
```

`result.text()` joins the tool reply's textual and JSON content in original order
with a newline between entries. JSON entries retain their original tokens. For
Read file text, use `result.json().content`; `text()` contains the reply envelope.
`result.json()` uses the successful, schema-validated structured content when
available; otherwise it parses the complete `text()` as one JSON document.
Empty text and multiple independent JSON documents produce a parsing error.
Both reads are synchronous and repeatable. `result.media` contains every original
media/file reference in content order. Copying its handle fields grants no media
authority. Returning a Body directly delivers its complete original reply;
returning other values delivers their actual fields and selected original media
references. Deleting fields from parsed JSON cannot restore removed attachments.

Declared output schemas remain validated at the tool boundary. Inspect
`error.message`, `error.code` and `error.result` when present; `error.reply` aliases
`result`. A failed Host call exposes a diagnostic Body whose structured content
is unverified; its `json()` only parses text. Script and local validation errors
may omit these properties. Use `text()` and `media` for failure diagnostics.
JavaScript Number computations follow Number precision; `text()` and a complete
Body return retain original JSON tokens. Encode BigInt as a string, omit undefined
fields or use null. An omitted return is null.

## Continuous environment

An active host Session has one disposable JavaScript environment. Execute scripts
in sequence. Top-level `let`, `const`, `var`, function and class declarations stay
available to later scripts; ordinary assignment updates that same binding.
`let` and `const` cannot be redeclared in the same environment, and `const` cannot
be assigned. `var` and function declarations may be repeated. Block and function
locals retain their JavaScript scope. Top-level `await` waits for tools and
`return` selects this script's output without clearing its bindings. Bindings are
reserved before execution; failed or unreached lexical initialization stays
uninitialized until the environment is reset.

```javascript
// First js_exec
let response = await Read({path: "input.json"});
let file = response.json();
return file.content;

// Later js_exec in the same active Session
return {content: file.content, images: response.media};
```

`runtime.state()` returns this environment's generation and the current execution
identity on demand. `js_exec({reset:true})`, mutually exclusive with `code` and
`file`, replaces the environment and cancels its old script. It works independently
of the occupied VM. A new environment does not wait for the old environment's
cleanup. Old responses stay bound to their old execution and generation; no tool
effect is replayed. Original Task records and tool facts retain their disposition,
including uncertain effects. Reset and environment release clear bindings,
retained media and their cumulative retention budget.

The SDK Runtime owns the environment under its existing Session execution
activation. The product releases it through `sessionRuntimeRegistry` only after
Session uses, producer work, observers and running Tasks permit the ordinary
resource-release path. There is no additional VM idle timer or Task dependency
lease. Session reactivation/resume creates a new environment. No reset notification
is injected into model instructions; scripts may inspect `runtime.state()` when
needed.

One script occupies the environment until its execution terminates. Foreground
yield is not termination: observe the original Task to completion before sending
the next script, or use `reset:true` to interrupt the occupied environment. A busy
error is a fallback for overlapping submissions, not a scheduling mechanism. Task read/wait/cancel remains outside the VM. Within one
script, await dependent operations; independent operations may be expressed
concurrently. Runtime schedules their actual execution.
Current Run cancellation, approvals, Guardian, tool facts and Task ownership remain
authoritative; retained variables do not keep an old Run alive or grant new tool
authority. Execution cancellation interrupts and, if necessary, kills the original
worker. Cleanup is bounded at the process boundary; pending tool producers keep
their existing Runtime receipt and quiescence obligations.

Exact misses report `not_found`; ambiguous matches report `too_many_matches`. A `stale_revision`
error means search again and revise the arguments. Observe unknown outcomes
without replaying the input.

## Runtime-owned execution contracts

Canonical search receipts and typed nested ToolCall facts preserve MCP identity/version bindings across environment replacement,
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

Tool-specific error codes and effect details stay in the reply's original content.
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
schema, policy, approval, Guardian, cancellation, tool facts, media and budget
checks. A stale schema requires a fresh search before a later call; old arguments
are never automatically executed.

Sources may implement the existing `tool.NamedSource`/`Registry.Lookup` contract.
Lookup grants no authority; both admission boundaries and the final dispatch check
remain in the original wrapper chain. Sources without Lookup retain the linear
fallback; fuzzy search retains its existing selector budgets. No source or
discovery lock crosses approval or Tool I/O. Snapshot hints remain fixed within this API version; a
software API change creates its own presentation snapshot, not a new grant cache.

Runtime schedules tool execution through the
[Run-owned execution gate](agent-sdk-boundary.md#runtime-and-capabilities).
Discovery, call budgets, cancellation and receipts keep their existing owners.

## Authority and invalidation

The parent process owns Host, Session, credentials and Tool wrappers. The child
receives fixed source bytes/hash and bounded JSON replies; no Go object,
credential, environment, module, filesystem, network or shell API is exposed.
Goja has one VM goroutine; Host completions resolve promises on that owner.
Tool arguments have no Code Mode byte budget; JSON type, cycle and depth checks
and each tool's schema still apply. The process protocol uses a uint32 payload
length per frame. Source, reply, output, retention, call and CPU budgets remain
independent of argument size.

Calls pass through the existing real Tool policy, Guardian/approval, source and
generation admission, dispatch admission and lifecycle wrappers. Discovery
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

Each environment owns a Goja child process in the same executable. Embedders
install the early WorkerMain dispatch from the
[SDK quickstart](../agent-sdk/README.md#code-mode-executable-entry) and release
Session environments with their execution resources. File reads reuse source
policy and execute the frozen source bytes/hash. CPU accounting applies to each
execution; idle Host waits are separate. Process separation and JS capability
restrictions do not constitute a secure OS sandbox against engine exploits.

Scripts yield within the original Run. The Run owner remains
until the producer is quiescent; it does not extend generation or permission
authority. Stop, cancellation and TurnComplete close further Host admission.
Final safe points cancel script producers before joining them, including an
approval waiter. Cross-Turn background execution is not promised.

`yield_time_ms` bounds only the foreground wait, not execution. After yielding,
use Task read/wait/cancel on the original handle; do not resubmit the original
script. `reset:true` can interrupt it without waiting for completion.
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
