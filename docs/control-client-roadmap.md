# Control and Client Protocol Roadmap

Status: active planning and acceptance baseline.

Last reviewed: 2026-07-12.

This document orders the next Caelis architecture work after the Agent SDK
boundary cleanup. It is the shared reference for implementation scope,
milestone exit criteria, and cross-surface acceptance. Normative layer ownership
remains in [Architecture](architecture.md), [Agent SDK Boundary](agent-sdk-boundary.md),
and [ACP Projection Architecture](acp-projection-architecture.md).

## Outcome

Caelis will build one product client protocol for TUI, headless, app-server, and
GUI clients:

- the command side is an explicit Caelis Control API;
- the event side is one ordered, versioned, resumable `eventstream.Envelope`
  feed;
- standard ACP payloads remain standard and are rendered with ACP semantics;
- Caelis lifecycle, scope, relationship, goal, and orchestration facts are
  explicit versioned extensions;
- `agent-sdk/session.Event` and other typed durable records remain the source of
  truth rather than client envelopes or transcript caches.

ACP is the native Agent interoperability language and the common semantic
vocabulary projected to clients. It is not the entire Caelis product API.
Plugin management, credentials, sandbox setup, Goal control, and Manage Loop
operations remain Caelis commands rather than invented ACP Agent methods.
The Caelis Client Protocol is an ordered product envelope and command plane
around those ACP payloads, not a competing Agent semantic language.

The target direction is:

```text
TUI / headless ---------------------+
                                    +-> Caelis Client Protocol -> Control
GUI -> app-server transport adapter +          |
                                               +-> built-in Runtime / SDK
ACP client -> ACP server ----------------------+-> external ACP Agents
```

## Current Baseline

The repository has the right main boundaries, but the client protocol is not
yet complete.

| Area | Current state | Extension risk |
| --- | --- | --- |
| Turn output | Main turns use `eventstream.Envelope` | Terminal and subagent output are discovered through a second `StreamSubscriber` path |
| Command output | `RunCommand` publishes task-stream text | The empty terminal anchor plus Caelis metadata is a deliberate Zed-compatible projection, but it is not strict ACP terminal ownership |
| Spawn output | A task-stream frame can carry both text and a normalized child `session.Event` | The Spawn projector currently flattens the child event into parent-tool terminal text |
| Child rendering | TUI shows a compact Spawn panel | Child thought, tool, diff, plan, and nested content cannot be reconstructed by a rich client from flattened text |
| Replay | Parent canonical Spawn result is durable | The live child trace is transient and has no durable GUI reload authority |
| Client commands | In-process Control service supports current TUI workflows | Requests are ambient-session and live-handle oriented rather than network-safe and reconnectable |
| Dynamic orchestration | Handoff, leases, Reviewer, Guardian, and loop watchdog boundaries exist | Goal state and the cross-Turn Agent Manage Loop are not implemented |

The important implementation seam already exists:

- `agent-sdk/task/stream.Frame` can carry `Text`, `Event`, cursors, and lifecycle;
- the external ACP child runner normalizes each child update into a
  `session.Event` before publishing it;
- `protocol/acp/projector` already knows how to project normalized session
  events into ACP-shaped envelopes;
- the parent receives the final delegated result through a canonical Spawn/Task
  result, independently of the transient child trace.

The missing step is to preserve the semantic event through projection and move
compact formatting to the consuming Surface.

## Protocol Layers

| Layer | Contract | Owner |
| --- | --- | --- |
| Durable model and execution truth | Messages, tools, plans, task results, leases, approvals, Goal and orchestration records | SDK session/task contracts plus Control-owned stores |
| Normalized Agent semantics | Session, prompt, update, permission, cancellation, controller, participant | `agent-sdk/*` |
| ACP wire | Official ACP JSON-RPC schema, compatibility, capabilities, external ingress/egress | `protocol/acp/*` |
| Caelis client protocol | Commands, bootstrap state, Envelope codec, replay and resume | product `ports/*`, protocol package, and Control adapters |
| Transport | In-process, HTTP JSON, SSE, or WebSocket mapping | Surface/app-server adapters |
| View state | Transcript grouping, compact panels, icons, expansion, syntax highlighting | `surfaces/*` |

ACP protocol version, Caelis Envelope version, and HTTP API version are separate
compatibility dimensions. A change to one must not silently imply a change to
the others.

## Unified Event Feed

Every Surface must ultimately receive one ordered client feed. Runtime runner
events, task streams, external ACP updates, terminal output, participant state,
approval requests, Goal state, and Manage Loop decisions are merged by a
Control-owned broker before they reach a Surface.

`control.StreamSubscriber` is a transitional in-process source adapter. It may
continue to bridge the existing task-stream service while the broker is built,
but it is not the app-server or GUI contract. A Surface must not discover a
second stream from an already-rendered tool update.

The versioned Envelope contract must make these fields explicit:

- session, run, turn, scope, actor, and participant identity;
- stable parent relationship for delegated output, including parent Spawn call
  and task/delegation identity;
- ordered live sequence and one server-resolvable resume token;
- whether the event is durable, replayable mirror state, or transient live
  trace;
- exactly one standard ACP payload or one documented Caelis extension payload;
- one terminal lifecycle for each turn or scoped child execution.

Scope and parent relationships belong to the Caelis Envelope, not to custom
root fields added to standard ACP payloads. Display-only hints may remain under
`_meta.caelis`, but clients must not need undocumented metadata to recover
identity, ordering, lifecycle, or durability.

### RunCommand and Spawn share delivery, not rendering semantics

`RunCommand` and `Spawn` should use the same task-stream service, subscription
lifecycle, Control broker, ordering, backpressure, and cancellation path. They
must not be reduced to the same payload type.

| Source | Semantic payload | Client rendering |
| --- | --- | --- |
| `RunCommand` or local shell task | Opaque terminal output bytes plus terminal lifecycle | Terminal panel; preserve exact byte order through the documented Caelis terminal extension |
| Spawned Agent | Standard ACP message, thought, tool call/update, content, diff, plan, permission, and lifecycle semantics | The same ACP components used for a main Agent, nested under the Spawn/task scope |
| Parent Spawn tool | Delegation lifecycle, status, stable child link, and final canonical result | Parent tool card or compact summary; not a duplicate child transcript |

Official ACP `ToolCallContent` supports regular content, diffs, and terminal
anchors. A standard terminal anchor refers to a terminal actually created
through the ACP client terminal API. In that flow the Agent asks the Client to
execute a command with `terminal/create`, then reads the Client-owned process
through `terminal/output`, `terminal/wait_for_exit`, and related methods. It is
not a generic push channel for a command already running inside the Agent or
Caelis Runtime.

The current empty `content[type="terminal"]` anchor plus
`_meta.terminal_output` is a deliberate compatibility projection. It is known
to mount and update correctly in the tested Zed version, even though the
terminal ID does not have the standard client-created ownership. Prior
maintainer experiments routing this use case through
`terminal/create`/`terminal/output` did not provide equivalent behavior. The
compatibility path must not be removed merely to obtain schema purity; it is
subject to the decision gate below. Spawn output must still not be mislabeled as
terminal bytes merely because the current TUI mounts it in a terminal-looking
panel.

### T0: Terminal compatibility decision gate

The terminal choice is a measured interoperability decision, not an automatic
conformance rewrite. The candidate profiles are:

| Profile | Execution/output ownership | Strength | Risk or limitation |
| --- | --- | --- | --- |
| Zed compatibility anchor | Command remains in Caelis; empty terminal content mounts the panel; `_meta.terminal_output` carries bytes | Observed working in the tested Zed version and preserves the current task stream | Deliberate nonstandard terminal reference; other ACP clients may ignore or misinterpret it |
| Strict ACP content updates | Command remains in Caelis; standard tool content carries text snapshots or deltas | Standard tool-call syntax without false terminal ownership | May lose terminal panel behavior, exact byte streaming, ANSI handling, or efficient incremental updates |
| Standard ACP terminal | Client executes through `terminal/create`; Client owns output, wait, kill, and release | Fully matches ACP terminal lifecycle | Changes execution placement, sandbox, permission, environment, and recovery ownership; prior UX was not equivalent |
| Caelis client terminal stream | Command remains in Caelis; versioned Envelope extension carries bytes, cursor, and exit | Correct fit for TUI, app-server, and GUI with resume/replay semantics | Product protocol only; not a generic ACP-client solution |

The working hypothesis, pending the experiment, is:

- Caelis TUI, app-server, and GUI use the Caelis client terminal stream;
- Zed interoperability retains the compatibility anchor through an explicit
  compatibility profile;
- a strict ACP profile emits no synthetic standard terminal reference and uses
  standard tool content or a documented reduced live-output experience;
- `terminal/create` is used only when command execution is genuinely delegated
  to the ACP Client.

Current support verdict: the repository has standard terminal JSON-RPC client
plumbing, but it does not support client-hosted terminal execution end to end.
`RunCommand` is assembled with an SDK `sandbox.Runtime` and executes inside the
Caelis task, journal, approval, cancellation, and stream lifecycle. The ACP
Prompt path does not inject `TerminalClientCallbacks` as an execution backend.
The existing `LocalTerminalAdapter` serves the opposite compatibility need: it
lets a client query output already owned by a Caelis task stream.

Until T0 selects and implements a different route:

- Caelis acting as an ACP Client must advertise
  `clientCapabilities.terminal=false` unless an explicit, complete
  client-terminal execution handler is wired. Current external
  controller/subagent connections already follow this rule because they pass no
  terminal handler;
- Caelis acting as an ACP Agent does not invent an
  `agentCapabilities.terminal` field. Terminal support is a Client capability;
  a remote Client advertising it only makes the callbacks available and does
  not require Caelis to use them;
- the reverse `terminal/output` compatibility adapter and empty Zed anchor do
  not count as standard client-hosted terminal support;
- standard terminal calls remain optional and disabled by default. Local
  RunCommand and Caelis client streaming continue independently.

The experiment must use one deterministic RunCommand fixture with interleaved
stdout/stderr, UTF-8 boundary splits, ANSI output, long-output truncation,
non-zero exit, cancellation, and completion. Run the same fixture through:

1. empty anchor plus Caelis metadata;
2. Caelis metadata without an anchor;
3. standard `tool_call_update` content deltas or snapshots;
4. a real `terminal/create`/`terminal/output`/`wait_for_exit`/`release` lifecycle.

Capture the exact ACP messages and rendered behavior in current Zed, Caelis TUI,
headless output, and a strict schema/reference-client harness. Score each
variant on:

- whether a live panel mounts and remains attached to the tool call;
- byte ordering, duplication, UTF-8, ANSI, truncation, exit, and cancellation;
- sandbox, CWD, environment, approval, and process ownership;
- cursor/reconnect/replay behavior and terminal resource cleanup;
- strict ACP schema validity and behavior when the extension is unknown.

The decision record must name the selected profile per client class, capability
or configuration negotiation, fallback behavior, compatibility owner, and
removal/revisit condition. A Zed screenshot or manual observation alone is
useful evidence but not the complete acceptance artifact; retain wire captures
and deterministic regression fixtures. Persist the result as
`docs/terminal-compatibility-decision.md`; keep machine-readable fixtures under
a focused `protocol/acp/fixture/terminal` corpus or an equivalent nearby test
location.

### Spawn semantic projection

The target live path is:

```text
child ACP session/update
  -> normalize to SDK session.Event
  -> task stream Frame.Event
  -> project to standard ACP payload
  -> Envelope(scope=subagent, scope_id=task_id, parent=Spawn call)
  -> Control client broker
  -> TUI compact view or GUI rich ACP view
```

The following invariants are required:

1. The child runner publishes normalized semantic events. Formatting tool calls
   into lines such as `Read file completed` is presentation logic and must not
   be the authoritative child stream.
2. Spawn projection preserves `agent_message_chunk`, `agent_thought_chunk`,
   `tool_call`, `tool_call_update`, `plan`, content blocks, diffs, locations,
   status, and negotiated terminal anchors.
3. Child tool-call IDs are scoped by child session/task identity. They are not
   rewritten to the parent Spawn call ID.
4. The Envelope carries the stable parent Spawn call and task relationship.
   The parent tool may receive lifecycle and final-result updates, but not a
   second flattened copy of every child event.
5. Child permission requests retain child scope and stable request identity so
   a reconnecting client can respond through the common approval command API.
6. TUI compact text is derived from the standard child events in
   `surfaces/transcript`; GUI clients can render the same events as nested rich
   ACP components.
7. A compatibility text mirror, if temporarily retained, is explicitly marked
   and suppressed whenever the semantic event was delivered. It has a removal
   milestone and is never replay truth.

The minimum renderer behavior is:

| ACP update | Required scoped behavior |
| --- | --- |
| `agent_message_chunk` | Group by message identity when available and render as child assistant content |
| `agent_thought_chunk` | Render as distinct, optionally collapsible thought content; never merge into the final answer |
| `tool_call` / `tool_call_update` | Maintain a child tool card by child `toolCallId`, status, kind, content, diff, location, and raw details policy |
| `plan` | Replace the current plan for that child scope, matching ACP replace-all semantics |
| `session/request_permission` | Render and resolve a child-scoped approval without losing parent Turn identity |
| child lifecycle | Close only the scoped child execution; do not close the parent Turn unless its own lifecycle terminates |

### Child replay authority

The parent Session must continue to persist only the canonical delegated result
that may enter parent model context. A GUI reload nevertheless needs a semantic
source for the nested child view.

The target is a linked Caelis-owned child transcript or participant stream,
keyed by the parent Session and durable task/delegation identity. Normalized
child ACP events may be stored there as semantic mirror records. They are not
parent canonical messages, and they are never reconstructed from TUI-formatted
text or `_meta.terminal_output`.

Until that store exists, child envelopes must declare themselves transient and
the GUI may only promise live rich rendering plus the durable parent final
result. GUI history acceptance requires the linked child replay authority.

## Client Command and App-Server Contract

The event feed is only half of a usable GUI protocol. The command side must use
explicit request-scoped values rather than connection-local Go handles.

Every state-changing command must carry:

- Session ID and, where applicable, Goal, run, turn, task, approval, or
  participant identity;
- authenticated actor principal and authorization context;
- operation or idempotency ID;
- expected revision when concurrent updates are possible;
- a typed result that distinguishes accepted, committed, conflicted, rejected,
  and unknown outcomes.

The initial command set includes:

- create, list, load, close, and inspect Session;
- submit prompt, steer, cancel, and resume/attach where the runtime supports it;
- resolve approval by stable pause/request identity;
- attach, detach, prompt, or cancel participant/subagent work;
- request and authorize controller handoff;
- create, update, pause, resume, cancel, and inspect Goal/Manage Loop state;
- product resource operations such as model, plugin, sandbox, and credential
  configuration as Caelis commands rather than ACP Agent methods.

Bootstrap returns a typed `SessionState` containing run state, controller epoch,
participants, pending approvals, active Goals, Manage Loop state, capabilities,
and a replay boundary. A GUI must be able to recover from bootstrap plus replay
without invoking TUI-specific status methods.

The first app-server adapter should use ordinary HTTP JSON commands and one SSE
event feed. WebSocket can be added later without changing command DTOs or
Envelope semantics. Official ACP remote HTTP work remains independent: raw ACP
transports serve ACP clients and external Agents, while the Caelis app-server
serves product clients.

## Goal and Agent Manage Loop

Goal and Loop must be designed as Control concepts before GUI implementation so
the client protocol does not later need a second state model.

### Vocabulary

| Concept | Meaning | Authority |
| --- | --- | --- |
| ACP plan | An Agent-presented, replace-all execution plan for one scoped session | Agent proposes and updates; client renders |
| Goal | Durable user/product objective, constraints, and acceptance criteria | User or authorized Control command |
| Turn | One fenced controller or participant interaction | Control dispatches; Runtime executes |
| Agent Manage Loop | Cross-Turn dynamic Control process that selects the next authorized action | Control only |
| Generation-loop watchdog | Narrow safety detector for repeated model output within one live Turn | Existing Control watchdog |

An ACP plan is not a Goal record, and the generation-loop watchdog is not the
Agent Manage Loop.

### Goal state

A minimal durable Goal contains:

- stable Goal ID, Session/workspace binding, objective, and acceptance criteria;
- user constraints and delegated authority boundary;
- status such as pending, active, waiting, satisfied, blocked, or cancelled;
- current revision, creation/update actors, and timestamps;
- optional current controller, run/turn, and child task correlations;
- the last verified progress summary and blocking reason.

Goal records are Control-owned product state. They are not hidden in a system
prompt, inferred from the latest user message on every restart, or represented
only by ACP plan entries. Goal mutations use CAS and idempotency and emit a
documented `caelis/goal` client event. Goal state does not automatically become
model context; Control supplies the relevant objective and constraints to each
authorized Turn.

### Manage Loop state and decisions

The Manage Loop remains event-driven:

```text
observe -> evaluate -> decide -> authorize -> dispatch -> verify -> checkpoint
   ^                                                                  |
   +--------------------------- continue -----------------------------+
```

It observes durable Session events, Goal revision, controller/participant
state, capabilities, approvals, review results, usage, progress, and user
input. It may decide to:

- continue with the current controller;
- dispatch bounded participant or Spawn work;
- request user input or permission;
- ask Guardian/Reviewer for a policy or quality judgment;
- authorize and commit a controller handoff;
- mark the Goal satisfied, blocked, waiting, or cancelled.

Every decision that can change ownership or cause an external effect has a
stable decision ID, observed state revision, action, rationale/audit summary,
authorization result, idempotency identity, and terminal outcome. Effect intent
is durable before dispatch; recovery never blindly repeats an unknown external
effect.

Only Control may commit a Goal transition, dispatch, or handoff. Agent output
may suggest completion, missing capability, or a next actor, but remains
advisory. User authority and permission policy are not broadened by putting the
decision inside a loop.

For multi-process app-server deployment, exactly one Manage Loop owner may act
for one Goal/Session epoch. The owner requires a renewable fenced lease or
equivalent CAS claim. The existing Session execution lease still serializes
each canonical Turn; the orchestration lease prevents two Control workers from
making competing next-action decisions between Turns.

Manage Loop updates emit a documented `caelis/orchestration` client event with
Goal, loop epoch, decision, action, state, and correlation IDs. These events are
product extensions; Agent message, tool, plan, and permission payloads remain
standard ACP.

### Explicit non-goal

Caelis will not add a workflow graph, node/edge DSL, DAG scheduler, or
SDK-owned Sequential/Parallel/Loop Agent classes. Task dependencies that arise
during execution are observations and decisions in the Manage Loop, not a
precompiled graph definition.

## Target Ownership

Package names remain subject to bounded implementation review, but ownership is
fixed:

| Responsibility | Target owner |
| --- | --- |
| Runtime task stream and normalized child events | `agent-sdk/task/*` and `agent-sdk/runtime` |
| Official ACP schema, codec, conformance, and projection | `protocol/acp/*` |
| Product client commands | a narrow product contract under `ports/*` |
| Envelope codec, version, resume, and extension schema | root `protocol/acp/eventstream` or nearby root `protocol/acp/*` product protocol packages |
| Live stream fan-in and replay broker | Control/application internal package |
| Goal store, Manage Loop lease, decisions, policy, dispatch, and handoff | Control layer, near `internal/controlplane` but outside central coordinator files |
| Compact Spawn panel and rich transcript view models | `surfaces/transcript` and individual Surfaces |
| HTTP/SSE or WebSocket | thin app-server Surface adapter |

Goal and Manage Loop product contracts must not be added to `agent-sdk` merely
to make them public. Reusable SDK types may carry neutral Goal correlation or
progress observations only when more than one host needs those semantics.

## Milestones

Milestones are ordered by dependency. Each milestone is complete only when its
exit criteria pass; code location or nominal feature presence is insufficient.

### M0: ACP conformance and client protocol baseline

Deliverables:

- pin the reviewed official ACP v1 schema and add conformance fixtures;
- remove or correctly negotiate nonstandard ACP root fields and methods;
- correct standard usage/cost, config option, `_meta`, and unknown-variant
  handling;
- complete T0 and classify the empty terminal anchor as an explicit
  compatibility profile, a selected standard replacement, or both;
- specify the Caelis Envelope JSON schema, versioning, capabilities, extension
  registry, and transport-neutral resume contract;
- separate ACP protocol version from Envelope and app-server API versions.

Exit criteria:

- every strict-mode standard ACP fixture validates against the pinned official
  schema;
- standard wire values round-trip without losing `_meta` or unknown supported
  variants;
- custom Caelis data does not add fields to an official ACP type root;
- strict ACP mode only emits standard terminal anchors for terminals created
  through the ACP client contract;
- default Caelis ACP Client initialization advertises terminal unsupported, and
  a regression test proves it becomes true only with a complete terminal
  handler;
- the selected Zed compatibility profile has deterministic wire and rendered
  regression evidence plus an owner and revisit condition;
- Envelope encode/decode and version rejection have whole-object tests;
- architecture lint prevents a second ACP semantic owner.

### M1: One live feed and semantic Spawn output

Deliverables:

- introduce the Control-owned event broker and move task-stream fan-in behind
  it;
- project `Frame.Event` for Spawn as child-scoped standard ACP payloads;
- retain RunCommand exact terminal-byte behavior;
- make parent Spawn updates lifecycle/final-result summaries rather than child
  transcript copies;
- derive the existing TUI compact panel from semantic child events;
- remove child trace formatting from the ACP runner once the TUI migration is
  complete.

Exit criteria:

- TUI and headless consume one feed and no Surface calls `StreamSubscriber`;
- a fixture child emitting message, thought, tool call/update, diff, plan, and
  final answer reaches the client as the same semantic ACP sequence;
- child tool IDs and parent Spawn ID remain distinct and correlated;
- no child narrative or tool activity is rendered twice;
- RunCommand byte/order/exit tests remain unchanged;
- child permission requests remain correctly scoped and resolvable.

### M2: Replay, reconnect, and thin app-server

Deliverables:

- one server-resolvable resume token spanning active live buffers and durable
  projection fallback;
- a linked child semantic replay authority and stable parent/task link;
- explicit Session identity independent of workspace key and CWD;
- request-scoped prompt, steer, cancel, approval, participant, and handoff
  commands;
- typed bootstrap `SessionState`;
- HTTP JSON plus SSE app-server adapter and generated client schema/types.

Exit criteria:

- disconnect/resume produces no missing or duplicate semantic event;
- a process restart can rebuild the main transcript, nested child view, tool
  status, participants, and controller epoch from bootstrap plus replay;
- a pending main or child approval can be answered after reconnect;
- two workspaces can list and replay globally unique Sessions without CWD/key
  confusion;
- in-process and app-server clients produce equivalent whole Envelopes for the
  same scenario;
- authorization and idempotent retry tests cover every state-changing command.

### M3: Durable Goal control plane

Deliverables:

- Goal schema, store, CAS/idempotency rules, lifecycle, commands, bootstrap
  state, and `caelis/goal` events;
- explicit distinction between Goal state and ACP plan projection;
- Goal correlation on turns, decisions, participants, and delegated tasks;
- user-authority and cancellation rules.

Exit criteria:

- Goal create/update/wait/satisfy/block/cancel transitions have conformance and
  conflict tests;
- replay and restart reproduce exactly the same Goal state and event sequence;
- an ACP plan update cannot mutate Goal authority or terminal status;
- GUI/headless fixtures can render Goal state without reading prompts or
  private metadata;
- concurrent Control workers cannot commit conflicting Goal revisions.

### M4: Agent Manage Loop vertical slice

Deliverables:

- one-Goal/one-Session loop owner lease and durable loop epoch;
- observe/evaluate/decide/authorize/dispatch/verify checkpoints;
- durable decision intent/outcome records and `caelis/orchestration` events;
- integration with existing controller selection, participants, approval,
  Guardian/Reviewer, handoff coordinator, and execution lease;
- stop, block, cancel, recovery, and unknown-outcome policy.

Exit criteria:

- one Goal can span multiple dynamically selected Turns without a static graph;
- restart resumes from the last durable decision without repeating a committed
  external effect;
- two loop owners cannot dispatch the same next action;
- Agent-suggested handoff cannot bypass Control authorization or the Session
  lease;
- Goal satisfaction requires an explicit verified Control decision;
- waiting for user/approval is stable across disconnect and restart;
- watchdog interruption remains a separate safety event rather than a Manage
  Loop step.

### M5: GUI vertical slice

Deliverables:

- generated client for command and Envelope schemas;
- main and nested scoped ACP renderers;
- terminal, content, diff, location, thought, plan, approval, participant,
  Goal, and orchestration views;
- reconnect, backfill, cancellation, and error/unknown-outcome UX.

Exit criteria:

- GUI, TUI, and headless consume the same recorded Envelope fixtures;
- GUI renders Spawn child tools and plans from ACP semantics, never from a
  preformatted text trace;
- live and reload screenshots show equivalent semantic state;
- no GUI-only runtime, policy, task, or persistence rule exists;
- app-server remains a thin transport and authorization adapter.

## Acceptance Matrix

The following scenarios are release-level regression fixtures for this roadmap:

| Scenario | Required evidence |
| --- | --- |
| Main built-in Agent | Live/replay whole-Envelope equality for durable facts |
| Main external ACP Agent | Same normalized semantic sequence as equivalent built-in Agent |
| RunCommand | Exact output bytes, cursor order, terminal exit, reconnect |
| Spawn child narrative | Message IDs, thought separation, parent/task scope, no duplicate text |
| Spawn child tools | Child tool IDs, status patches, content/diff/location rendering |
| Spawn child permission | Stable scoped approval ID and reconnectable resolution |
| Spawn completion | One child lifecycle terminal plus one canonical parent result |
| Child reload | Linked semantic replay rebuilds the nested view without parent model pollution |
| Session reconnect | Bootstrap plus resume token has no gaps or duplicates |
| Goal lifecycle | CAS, idempotency, audit actor, replay, authorization |
| Manage Loop dispatch | Owner fencing, decision intent/outcome, no duplicate effect |
| Handoff | Explicit authorization, exclusive Session lease, atomic epoch/binding/event |
| Cross-surface | TUI, headless, app-server, and GUI agree on semantic envelopes |

Protocol or persistence changes must use whole-object/event comparisons. UI
reload tests do not replace model-context or Goal/decision round-trip tests.

## Non-Goals and Guardrails

- Do not expose `StreamSubscriber` as the public network protocol.
- Do not preserve TUI Spawn formatting inside Runtime or ACP ingress.
- Do not flatten child ACP tool/plan/thought events into terminal text for rich
  clients.
- Do not persist a TUI transcript cache as parent model truth.
- Do not use undocumented `_meta` as the only Goal, ordering, relationship, or
  recovery state.
- Do not extend official ACP types with custom root fields.
- Do not let an Agent commit Goal completion, handoff, or the next orchestration
  action autonomously.
- Do not create separate orchestration semantics for built-in and external ACP
  Agents.
- Do not begin a workflow graph/node engine under the name Goal or Loop.

## First Bounded Implementation Slice

Start with the bounded T0 terminal experiment and decision record; do not change
the working Zed projection before that evidence exists. Continue M0 with ACP
conformance and Envelope contract tests. The next bounded slice is the M1 Spawn
fixture: preserve one child `Frame.Event` through projection, render it in the
existing TUI compact panel, and prove that the parent Spawn result remains the
only canonical parent-model fact.

Full GUI feature work should not begin before M1. GUI history and production
app-server work require M2. Goal and Manage Loop implementation starts only
after command identity, event ordering, replay, and authorization are stable.

## External References

- [ACP Prompt Turn](https://agentclientprotocol.com/protocol/v1/prompt-turn)
- [ACP Tool Calls](https://agentclientprotocol.com/protocol/v1/tool-calls)
- [ACP Terminals](https://agentclientprotocol.com/protocol/v1/terminals)
- [ACP Agent Plan](https://agentclientprotocol.com/protocol/v1/agent-plan)
- [ACP Extensibility](https://agentclientprotocol.com/protocol/v1/extensibility)
- [ACP Transports](https://agentclientprotocol.com/protocol/v1/transports)
- [ACP v1 schema](https://github.com/agentclientprotocol/agent-client-protocol/tree/main/schema/v1)
