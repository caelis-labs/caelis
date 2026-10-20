# Shared native Worker Sessions

A Worker is an ordinary native Session in the existing Control Host. Bot and TUI
use the same authenticated Host principal and Session permissions. The local user
can attach concurrently through the TUI. Neither observer owns execution or
displaces another observer; there is no control handoff or separate Worker grant.

Negotiate `turn-steering-receipts-v1` and `session-feed-v2` through
`GET /api/control/v1/initialize`. See [Session profiles](session-profiles.md) for
explicit client assembly, common operation receipts and authentication.

## Create, attach and work

All HTTP paths below are relative to `/api/control/v1`. Writes require matching
`operation_id` and `Idempotency-Key`. Persist the exact request before dispatch.

| Operation | Endpoint and request |
| --- | --- |
| Create Worker | `POST /sessions` with `{operation_id, cwd, title?}`; `cwd` is an absolute directory on the Host |
| Start a Turn | `POST /sessions/{session_id}/prompt` with `{operation_id, input, content_parts?}` |
| Append to a running Turn | `POST /sessions/{session_id}/steer` with `{operation_id, target, input, content_parts?}` |
| Inspect state | `GET /sessions/{session_id}/state` |
| Observe and recover | `GET /sessions/{session_id}/reconnect?after={cursor}`; SSE remains open across Turns |
| Discover asynchronous Tasks | `GET /sessions/{session_id}/tasks` and `/tasks/watch`; watch pushes directory changes |
| Observe Task output | `GET /sessions/{session_id}/tasks/{task_id}/events` or `/subscribe`; uses the existing independent Task stream |
| Interrupt | `POST /sessions/{session_id}/cancel` with `{operation_id, target}` |
| Decide approval | `POST /sessions/{session_id}/approvals/{request_id}/resolve` with `{operation_id, target, approval_request_id, outcome, option_id, approved}` |
| Reconcile create/prompt/steer | `GET /operations/{operation_id}` |

Creation may optionally select `model`, `reasoning_effort` and `fast_mode` using
the Host's configured model catalog. Without a selection the ordinary Host
default applies. Effort and fast mode require a model. These options affect only
the new Session. No credentials, callback tools, Bot instructions or memory are
copied. A model configuration failure after creation retains the Session address
with an unknown operation outcome; it must not trigger another creation.

If a response is lost, query the shared operation ID. Recovery uses the original
retained command receipt or exact canonical completion evidence, without
redispatch. An existing Session alone cannot prove the complete create-and-model
selection command. Missing/expired evidence stays unknown and does not authorize
another creation.

Use the returned Session ID unchanged:

```sh
caelis attach --control-url http://127.0.0.1:7777 \
  --session WORKER_SESSION_ID --control-token-file /absolute/path/to/host.token
```

`attach` inspects and resumes exactly that Session using its Host workspace. It
does not create a Session or read stdin as a prompt. Missing IDs fail. It rejects
`-p`, `--embedded` and permission overrides. Quit or close the terminal to detach;
the Worker continues. Use the TUI's explicit interrupt action or the cancel
endpoint to stop work. Host shutdown or replacement can interrupt execution.

The [compilable Bot example](../control/appserver/httpclient/worker_example_test.go)
uses supported public packages. The OpenAPI source and generated TypeScript/Go
wire clients include the same requests and receipts.

## Steering receipts

`target` contains the exact observed `handle_id`, `run_id` and `turn_id` from the
prompt receipt or live state. Steering admission rechecks that identity. It never
silently starts a Turn, stops and restarts work, or queues input for a later Turn.

For the native conversation runner, a successful command has
`outcome: committed` and `input_status: accepted`: the current Turn owns that
input, but an already-issued model request or in-flight tool batch may finish
first. At the next safe point, the canonical user-input envelope carries
`input_operation_id` and `input_status: applied`. It is persisted before the next
model request and survives canonical replay. Previously completed tool calls and
results remain in context and are not dispatched again. Inputs admitted while a
final response is in flight cause another model step in the same Turn.

A stale target returns a conflict. The same operation and identical request
return the original receipt; changing either payload or target under that ID
conflicts. A duplicate accepted receipt is not a second application event.
External ACP controllers can reject steering as unsupported; their remote
admission does not promise a native canonical applied receipt.

An accepted input that has no applied event before cancellation, failure or Host
loss must not be reported as applied. An unknown receipt or disconnected stream
does not authorize another operation ID. Reconcile the saved ID and restore the
feed. A new user instruction for a later Turn is a new operation, not a retry.
Explicit-profile Sessions support the same `/steer` endpoint for actual collected
user input; summaries and background triggers must retain their ordinary
source/grant path and must not be relabeled as user steering.

## Observation, state and permissions

Each subscription has its own cursor and backpressure. The reconnect bootstrap
couples current state with the history/feed boundary. Apply `append_page`
deliveries, or stage `replace_begin`/`replace_page` until `replace_end` before
replacing visible history. Commit `next_cursor` only after applying the delivery.
Never replay a prompt to recover observation. Missing or expired spool cursors
select canonical replacement, including applied steering inputs and tool results.
See [ACP Projection Contract](acp-projection-architecture.md) for delivery rules.

Use `run.active`, `run.status` and `approval.active` together. Running work can
wait for approval; `completed`, `failed`, `interrupted`/`cancelled` and `unknown`
are distinct. An unknown prior-Host outcome is not success. `caelis/notice` is a
nonblocking notice and does not fail a Turn. A terminal lifecycle belongs to its
exact Turn; the Session subscription stays open to observe later user or Bot
Turns without status polling.

The Worker samples normal Host configuration at activation: native tools,
trusted-workspace instructions, MCP, plugins, environment and the native
permission/approval policy. Bot-private profile state and callback catalogs are
not inherited. Any application-provided private memory capability remains owned
by that application; Core does not provide built-in Memory inheritance. A workspace path grants
no extra tool privileges; normal workspace trust and approvals still apply.

The same authenticated owner can use native Session compaction, model and
approval/controller/presentation selection, handoff, closure, status, completion,
participants and exact child input through the ordinary `/sessions/{id}` APIs.
Each action retains its native revision/controller fences and shared receipt.
Host-global configuration changes use the corresponding explicit Host action;
Session model selection does not implicitly change the Host default.

Trusted embedded adapters bind the same principal through `BindAppServerClients`.
Task, child-input and focused services preserve canonical native Session ownership.
A different principal cannot acquire a Session by knowing its ID. Closing the
Session seals mutations while retaining history and original receipts. UI detach
leaves execution running.

Explicit-profile Sessions use the [Session configuration](session-profiles.md)
owner for dynamic assembly and reject ignored native model selectors. Their
children use the native participant/Task owners and preserve the root source,
profile and native Task identity. There are no Application/Connection grants.

Require `session-terminal-wait-v1` for producer-exit observation and
`session-terminal-control-v1` for native kill/release. Terminal IDs must resolve
through the Session Task directory. Reads/waits do not consume the model's Task
output cursor or invent an exit for unknown work. Cancelling a waiter ends only
that observation.

Both clients resolve the current approval FIFO head with its request ID and exact
target. Only the first valid decision wins; stale or competing decisions cannot
change the settled result. Observers dismiss the matching approval on its
settlement event, regardless of which client answered.

Command failures use structured outcome/code fields. Command and later Turn
failures have Host diagnostics with Session, Turn and operation correlation.
`logs/runtime.jsonl` is owner-readable,
rolls at 2 MiB and retains one bounded backup. Command diagnostics record fixed
classifications and identities, not credentials, prompt bodies or full errors.
The stream spool has its own bounded rolling retention and is never execution
authority. Scheduling, task bubbles and proactive behavior remain Bot-host work.
