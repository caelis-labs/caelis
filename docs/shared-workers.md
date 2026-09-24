# Shared native Worker Sessions

A Worker is an ordinary native Session in the existing Control Host. An enrolled
application can create one and receives a durable grant to that Session. The
local user can attach concurrently through the existing TUI. Neither observer
owns execution or displaces another observer; there is no control handoff.

The Host must advertise `shared-native-workers-v1`, `turn-steering-receipts-v1`
and `session-feed-v2` through `GET /api/control/v1/initialize`. Authentication and
enrollment follow [Application Runtime](application-runtime.md). Use a scoped
application credential for Bot operations and the user's Host credential for TUI
attach. Do not give the Bot a Host credential to bypass missing permissions.

## Create, attach and work

All HTTP paths below are relative to `/api/control/v1`. Writes require matching
`operation_id` and `Idempotency-Key`. Persist the exact request before dispatch.

| Operation | Endpoint and request |
| --- | --- |
| Create Worker | `POST /application/workers` with `{operation_id, cwd, title?}`; `cwd` is an absolute directory on the Host |
| List owned Workers | `GET /application/workers`; returns scoped Session grants, not execution status |
| Start a Turn | `POST /sessions/{session_id}/prompt` with `{operation_id, input, content_parts?}` |
| Append to a running Turn | `POST /sessions/{session_id}/steer` with `{operation_id, target, input, content_parts?}` |
| Inspect state | `GET /sessions/{session_id}/state` |
| Observe and recover | `GET /sessions/{session_id}/reconnect?after={cursor}`; SSE remains open across Turns |
| Discover asynchronous Tasks | `GET /sessions/{session_id}/tasks` and `/tasks/watch`; watch pushes directory changes |
| Observe Task output | `GET /sessions/{session_id}/tasks/{task_id}/events` or `/subscribe`; uses the existing independent Task stream |
| Interrupt | `POST /sessions/{session_id}/cancel` with `{operation_id, target}` |
| Decide approval | `POST /sessions/{session_id}/approvals/{request_id}/resolve` with `{operation_id, target, approval_request_id, outcome, option_id, approved}` |
| Reconcile application create/prompt/steer | `GET /application/operations/{operation_id}` |

Creation may optionally select `model`, `reasoning_effort` and `fast_mode` using
the Host's configured model catalog. Without a selection the ordinary Host
default applies. Effort and fast mode require a model. These options affect only
the new Session. No credentials, callback tools, Bot instructions or memory are
copied. A model configuration failure after creation retains the Session address
with an unknown operation outcome; it must not trigger another creation.

If the Host loses the application receipt after the complete create-and-configure
command commits, operation queries and identical retries recover its retained
command receipt and Session ID. Recovery requires the exact application scope,
operation, action and request. A Worker grant or an existing Session alone is
insufficient; missing or expired command evidence keeps the outcome unknown and
never redispatches creation.

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
Application main Sessions support the same `/steer` endpoint for actual collected
user input; summaries and background triggers must retain their ordinary
application source/grant path and must not be relabeled as user steering.

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
trusted-workspace instructions, MCP, plugins, environment, Workspace Memory and
the native permission/approval policy. Bot-private Application Session state,
callback catalogs and private Memory are not inherited. A workspace path grants
no extra tool privileges; normal workspace trust and approvals still apply.

The application can inspect, prompt, steer, interrupt and resolve approvals only
for its own Worker grants. It cannot adopt an arbitrary Session ID, mutate Host
configuration or acquire another application's Worker. Lease expiry or revocation
removes its mutation authority without cancelling the user's native Session.
The user retains access. Application main Sessions keep their separate isolation.

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
