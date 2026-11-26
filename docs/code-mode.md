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
the desired result. `console.log`, `console.warn`, and `console.error` discard
their arguments after normal JavaScript evaluation and produce no output.
Top-level state remains available in the active environment. `file` paths resolve
relative to the working directory. Use tools for filesystem and network I/O, and
RunCommand remains available both as a native function call and inside JavaScript.
Both routes use the same command producer, policy, approval, tool facts and Task controls.

Call a known tool directly using the syntax shown by its name and schema. This
works for tools learned from Skills too; no search is needed when the schema is
known. Calls may use function or method syntax, for example:

```javascript
{
  const response = await Read(args);
  return response.data.content;
}
```

Method calls follow the same contract:

```javascript
await dtw.apps(args);
await context7['query-docs'](args);
```

`await tools.search()` returns a directory page `{tools,next}` of eligible
registered and ready deferred tools. Summaries include the name, a short
description, applicable MCP service and method metadata, and `call_path`, but
not schemas. A `query` matches all its terms, case-insensitively, and ranks
exact name, service and method matches ahead of weaker matches. Without a
query, entries are in name order. Pass a non-null `next` as `after` with the
same query. An empty page does not establish that a connecting service has
no tools.

`await tools.search({name})` returns one plain object with the complete input
schema, optional `output_schema`, original service metadata and `call_path`.
Exact lookup records the current tool binding. The entry's `output_format: "body"`
describes calls to that tool, not the search result. When declared,
`output_schema` describes the tool call's `.data`. Unknown names fail without
falling back to a keyword query. `tools.search({names})` performs up to 16 exact
lookups and returns `{tools:[{name,entry} or {name,error_code}]}` in input order,
within the call and reply budgets. `call_path` qualifies installed shortcuts
with `globalThis` so a local variable with the tool or service name cannot
shadow them. Otherwise it uses `tools.call(name, args)`.

Successful tool calls return a Body, through direct functions and `tools.call`
alike:

- `result.data` comes from the result's single structured content block and is
  validated against the tool's declared output schema when one exists. A missing
  block yields `undefined`. A block containing JSON `null` yields `null`. For
  Read file text, use `result.data.content`.
- `result.text` is a string property that joins only text blocks in content order
  with newlines. It is empty when there are no text blocks.
- `result.media` contains the original images and resource handles in content
  order. Copying their handle fields grants no media authority.

Return the Body itself to deliver all original blocks and preserve raw JSON
tokens. To return a subset, select data or text and any original media references
you want to deliver. JavaScript numbers may round. Encode BigInt as a string and
omit undefined fields or use null. An omitted return is null.

Inspect `error.message`, `error.code` and `error.result` when present;
`error.reply` aliases `error.result`. Script and local validation errors may
omit these properties. Only a failed tool's diagnostic Body retains the legacy
readers. `error.result.text()` joins text and diagnostic JSON in content order
with newlines, and `error.result.json()` parses that entire string as a single
JSON document. Empty text or multiple independent documents produce a parsing
error. Unverified structured output is not exposed by those readers. Returning
`error.result` directly still delivers its original reply; to select a diagnostic
attachment, return the original reference from `error.result.media`.

## Continuous environment

An active host Session has one disposable JavaScript environment. Execute scripts
in sequence. Later scripts can read, assign or redeclare top-level names,
regardless of their original declaration kind. Within one script, normal
JavaScript rules still apply: a local `const` cannot be assigned, duplicate
lexical names are errors, and block and function locals keep their scope.

A successful redeclaration publishes a new binding without changing the
one captured by an older closure. Assigning to an earlier mutable name
updates that binding; assigning to an earlier `const` replaces only the
Session-visible value. If a redeclaration's initializer fails, the previous
binding stays available. This does not roll back other successful bindings or
side effects. Top-level `await` waits for tools, and `return` selects this
script's output without clearing published names.

```javascript
// First js_exec
let response = await Read({path: "input.json"});
let file = response.data;
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
error is a fallback for overlapping submissions, not a scheduling mechanism. Task wait/cancel remains outside the VM. Within one
script, await dependent operations. Independent operations can execute
concurrently, even when they call the same tool.
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
execution. Explicit search can refresh a tool already declared to the model by
reading the current directory of the same request's MCP source. It does not
rewrite existing prompt slices or model tool arrays.

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

Sources may implement `toolset.NamedSource` for exact name lookup.
Lookup grants no authority; both admission boundaries and the final dispatch check
remain in the original wrapper chain. Sources without Lookup retain the linear
fallback; fuzzy search retains its existing selector budgets. No source or
discovery lock crosses approval or Tool I/O. Snapshot hints remain fixed within this API version; a
software API change creates its own presentation snapshot, not a new grant cache.

Independent tool calls can run concurrently, including calls to the same
registered tool. Each call has its own budget, cancellation, and receipt.

## Authority and invalidation

The parent process owns Host, Session, credentials and Tool wrappers. The child
receives fixed source bytes/hash and bounded JSON replies; no Go object,
credential, environment, module, filesystem, network or shell API is exposed.
Goja has one VM goroutine; Host completions resolve promises on that owner.
Tool arguments have no Code Mode byte budget; JSON type, cycle and depth checks
and each tool's schema still apply. The process protocol uses a uint32 payload
length per frame. Source, reply, output, retention, call and CPU budgets remain
independent of argument size.

Calls pass through Tool Policy, Guardian/approval, generation admission, and
lifecycle checks. The Manager refreshes the directory independently of prompt
assembly. If lookup detects a stale binding, `tools.search({name})` can reload
the schema for a later invocation. Offline and revoked access have distinct
errors.

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
Task observation. Returned business data, including a running inner command Task,
does not own the outer script's lifecycle. Commands and scripts have distinct
Task handles. An admitted `RunCommand` with `in_progress` advances independently
of the script. Use `Task.wait` with the command's original handle if its result
is needed. Cancellation also uses the command handle.
Closing a script's Host or resetting the VM leaves an already submitted command
under its original Run and Task authority.

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
authority. Stopping or cancelling the Run closes further Host admission.
Final safe points cancel script producers before joining them, including an
approval waiter. Cross-Turn background execution is not promised.

`yield_time_ms` bounds only the foreground wait, not execution. After yielding,
use Task wait/cancel on the original handle; do not resubmit the original
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
