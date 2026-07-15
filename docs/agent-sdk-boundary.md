# Agent SDK Boundary

Status: accepted normative architecture.

This document is the normative ownership and dependency contract for
`agent-sdk`. It deliberately does not track implementation tasks or claim
readiness. Implementation history belongs in Git, tests, release tags, and CI
evidence rather than a parallel documentation board.

## Accepted Decisions

1. `agent-sdk` is an ordinary reusable package tree inside the root
   `github.com/caelis-labs/caelis` Go module. It keeps the import prefix
   `github.com/caelis-labs/caelis/agent-sdk/...` but has no separate `go.mod`,
   dependency graph, version, release, or test lifecycle.
2. Independence means enforced dependency direction, explicit public
   contracts, package-level testability, durable compatibility, and reuse by
   multiple Caelis hosts. It does not require a separate module or repository.
3. ACP is Caelis's native interoperability and control language for built-in
   and external Agents, not only a presentation protocol.
4. The SDK owns reusable ACP-compatible semantics. The product ACP wire,
   compatibility policy, transport, and surface projection remain outside the
   SDK.
5. Handoff is a Control-owned controller-epoch transition. An Agent may report
   completion, missing capability, or a suggested next actor, but it cannot
   authorize or commit handoff.
6. Handoff decisions belong to explicit user control or dynamic orchestration
   policy in the Control-layer Agent Manage Loop.
7. Caelis will not build a deterministic workflow engine. A graph/DAG DSL,
   workflow nodes and edges, and SDK-owned sequential/parallel workflow state
   machines are explicit non-goals.
8. Caelis does not adopt Agent-as-tool, Handoff, Workflow node, and Remote Agent
   bridge as four required top-level Core abstractions. Existing task and
   delegation primitives remain available; remote is an ACP transport choice,
   not a separate Agent category.
9. Concrete provider/model directories are Caelis Control data. The SDK may
   provide reusable provider protocol adapters and model contracts, but it does
   not own recommended model lists, provider-specific capability tables,
   models.dev snapshots, or onboarding configuration.

## Ownership

| Layer | Owns | Must not own |
| --- | --- | --- |
| Agent SDK stable kernel | Agent/run values, model and tool contracts, canonical session events, policy and approval primitives, durable run/replay mechanics, task/delegation primitives, normalized ACP-compatible controller and participant contracts | Caelis profiles, UI state, agent-selection policy, Manage Loop decisions, product wire transport |
| Agent SDK bundled capabilities | Reusable provider protocol adapters, stores, sandbox backends, builtin tools, MCP, skills, and helpers useful to more than one host | Product imports, concrete model directories and snapshots, or product-specific assembly and presentation policy |
| Caelis Control | Agent registry and assembly, endpoint factories, provider/model directory and concrete capability metadata, credentials/process lifecycle, permission and review routing, Guardian/Reviewer/system Agents, dynamic orchestration, active-controller selection, handoff authorization and commit | Presentation rendering; autonomous model-driven ownership transfer |
| ACP product implementation | JSON-RPC/wire schema, transport, compatibility, ingress normalization, envelope projection, documented `_meta` | Agent-selection policy or a second copy of canonical model truth |
| Surfaces | Rendering ACP-shaped envelopes and collecting user input | Runtime, policy, persistence, tool, sandbox, or handoff decisions |

Package placement is still transitional. Ownership is determined by semantics,
not solely by the current directory name.

`control/modelconfig` resolves endpoint templates, authentication, catalog
metadata, persisted profiles, and runtime overrides before constructing an SDK
model from a complete provider configuration.
Provider list APIs may contribute discovered IDs and metadata when available,
but incomplete discovery responses do not make the SDK the owner of product
documentation or fallback capability policy.

## Dependency Rule

SDK packages must not depend on:

- `control/*`;
- `app/*`;
- `surfaces/*`;
- the product `protocol/acp/*` implementation;
- product-host `ports/*` packages;
- repository `internal/*` packages outside the `agent-sdk` package tree.

The product wire and host depend inward on reusable SDK contracts, never the
reverse. This rule is enforced by `make arch-lint` and
`make sdk-boundary-check`, including SDK test imports. The root module remains
the single build and release graph.

Only import paths in `agent-sdk/supported-packages.txt` receive the declared
pre-v1 source-compatibility review. Other non-`internal` SDK paths are bundled
implementations or experimental helpers until explicitly promoted. The API
snapshot is a review gate, not by itself proof of SemVer compatibility or
behavioral correctness.

## ACP-native Collaboration

Built-in and external Agents expose the same effective language:

- session identity and lifecycle;
- declared capabilities and configuration;
- prompt/content input;
- message, thought, tool, plan, permission, and lifecycle updates;
- cancellation and completion;
- controller and participant identity.

The transport may be an in-process call, stdio ACP, or a future network
connection. Native ACP means semantic equivalence; an in-process Agent does not
need to serialize every call through JSON-RPC.

```text
Built-in Agent Runtime -------------------------------+
                                                       |
External ACP Agent -> transport/lifecycle adapter -----+-> normalized SDK semantics
                                                            -> Caelis Control
                                                            -> product ACP projection
                                                            -> surfaces
```

The normalized semantic contract has one stable owner:

- reusable DTOs and invariants flow from `agent-sdk/session` and other SDK
  contracts toward product adapters;
- `protocol/acp/schema` owns public wire shapes;
- `protocol/acp/semantic` is the wire-to-SDK codec and normalization boundary;
- Caelis-specific compatibility and `_meta` extensions stay in `protocol/acp`;
- external input is normalized before it enters durable state.

ACP-native collaboration does not mean that raw ACP payloads are the only
persisted/model-visible truth, that external Agents are trusted by default, or
that every transport and presentation type belongs in the SDK.

## Controller, Participant, Delegation, and Handoff

A **controller** owns the next main-session turn for one controller epoch. A
**participant** is a bounded collaborator or sidecar and does not automatically
replace the controller.

Task, Spawn, and delegation primitives may use those roles. Caelis does not
need a generalized `Agent.asTool` abstraction. A delegated result enters parent
model context through a canonical task/tool/message fact, never through
transient child stream output.

The SDK may define neutral endpoint, controller, participant, cancellation,
transfer, and recovery contracts. Control owns the handoff operation:

1. observe session, capability, policy, and run state;
2. decide whether ownership should change;
3. obtain any required user or policy approval;
4. quiesce/cancel the current controller as required;
5. activate the selected endpoint and synchronize canonical context;
6. atomically persist the binding, epoch, and handoff fact;
7. resume dispatch through the selected controller.

There is no LLM-facing handoff tool. A model recommendation is advisory input to
Control, not authority to mutate the controller binding.

The current Caelis implementation places product assembly in
`internal/controlassembly` and shared-ledger routing, endpoint lifecycle,
recovery, and handoff coordination in `internal/controlplane`. Runtime consumes
injected neutral routes and mechanisms. Control now supplies normalized
participant roles and control principals to SDK task/subagent code; product
source strings remain audit provenance and are not interpreted by the SDK.

## Shared Session execution and collaboration

A workspace may host multiple Sessions and those Sessions may execute in
parallel. Within one Session, Caelis separates the durable collaboration ledger
from the current canonical Turn:

- one local controller, ACP controller, or Side ACP/Reviewer prompt owns the
  execution lease for its complete asynchronous Turn;
- `/claude`, `/codex`, `@agent`, and ACP-backed `/review` are valid participant
  Turn owners. Their normalized ACP event forwarder carries the same
  `MutationGuard` as the user event, so transport projection cannot lose the
  fence before persistence;
- participant attach/detach is Control-owned collaboration metadata, not a
  second model Turn. It may overlap an active Turn only under the explicit
  `participant` purpose and still requires revision, delegation, attachment
  generation, atomic lifecycle-event, and exact committed-result checks;
- approval resolution, watchdog audit checkpoints, and validated system-result
  commits are the other explicitly classified Control writes that may coexist
  with a live Turn. Unknown Control purposes fail closed while a lease is live;
- controller handoff and coordinator binding changes are exclusive. Control
  first acquires and heartbeats the Session execution lease, carries that fence
  on the atomic binding/event commit, and releases it before the new controller
  receives a Turn. A live old Turn therefore prevents endpoint activation and
  ownership transfer;
- a nested system Agent operating on a different staging Session masks the
  parent fence and obtains independent placement. A parent Session fence is
  never valid authority for the child Session.

This contract intentionally does not permit two independent canonical Turns to
append dialogue or execute ownership-changing effects concurrently in one
Session. Multi-Agent workspace collaboration remains available through
parallel Sessions, parent-fenced delegated tasks, and participant lifecycle;
the orchestration layer decides when the next same-Session Turn runs. Raw child
stream output never becomes parent model truth without a canonical result.

## Dynamic Orchestration

The Agent Manage Loop is an event-driven Control loop:

```text
observe -> evaluate -> select/dispatch/handoff -> verify -> continue or stop
```

The path is selected at runtime from durable events, capability state, policy,
review results, progress, and user intent. Decisions that affect ownership or
durable execution are auditable and persisted.

Caelis intentionally does not provide a workflow graph/node/edge DSL, a static
graph executor, SDK-owned Sequential/Parallel/Loop Agent classes, or a separate
RemoteAgent domain abstraction. Explicit product procedures remain ordinary
Control logic using SDK primitives.

## Runtime Safety Contract

Fixed generic step/model/tool budgets are not part of the SDK `Run` contract.
They can abort valid open-ended Agent work. This does not mean execution may be
unbounded without policy: Control must be able to observe lifecycle, usage,
elapsed time, repeated action signatures, and progress. The watchdog has one
narrow active safety action: interrupt a live Turn only after high-confidence
model-output loop evidence. Other cancellation and confirmation policy belongs
to explicit Control orchestration, not watchdog capacity handling. The
production Control host implements this above the fenced Runtime decorator.

Runtime safety also requires:

- fail-closed capability negotiation against the actual model, tool, and
  executor instances selected by Control;
- a bounded, single-consumer event stream with defined close/cancel behavior;
- typed lifecycle interception and observer telemetry that cannot hang or alter
  execution accidentally;
- ordered input guardrails with isolated mutable input, explicit failure policy,
  bounded non-cooperative work, and typed rejection;
- cancellation-request state distinct from proven terminal cancellation;
- no unsafe continuation across an unknown side-effect boundary.

For local built-in Agents, tool definitions declare concrete sandbox execution
requirements. Control derives the union from the final augmented tool set plus
the merged per-turn output and streaming request, then validates the selected
model and sandbox descriptors before calling Runtime. ACP-controlled external
Agents are validated by their ACP endpoint contract instead; local execution
requirements are not incorrectly projected onto that remote invocation.
Runtime repeats model/output validation as a defensive public boundary. Output
contracts are strict: an unknown mode or schema mode without a schema is an
error, never an implicit text fallback.

`TraceSink` is observer-only and asynchronous. Delivery preserves the
start/terminal order within one lifecycle operation, but sinks must tolerate
concurrent operations. A hard outstanding-dispatch cap bounds non-cooperative
sinks; once saturated, trace records are dropped instead of blocking execution.

Runtime normalizes the caller context before guardrails run. A timed-out
guardrail that ignores cancellation retains an outstanding slot until its call
really exits. The per-Runtime cap therefore acts as a stuck-call circuit
breaker: saturation is a typed resource-exhaustion failure and never creates
another guardrail goroutine.

The production Control host also owns a generation-tail loop watchdog above
Runtime and the session lease wrapper. It is not a wall-clock task timeout. It
probes the live Runner event stream for high-confidence generation loops:
exact reasoning/assistant tail cycles, or identical tool name+args steps only
when the content segment since the previous tool call is also identical
(different thought with the same tool is progress). Stream deltas are
concatenated without inserted separators; empty tool args fail open.
High-confidence hits claim one interrupt and cancel the live Turn
(`WatchdogActionInterrupt`); the durable loop checkpoint is best-effort audit,
not a precondition and not model context. Review runs asynchronously in at most
eight Runtime-wide slots. Saturation drops that evidence window: there is no
queue and no capacity-triggered Cancel. Reviewer timeout/failure/panic and
checkpoint failure never delay normal stream completion, enter the Turn event
stream, or cancel the Turn. Normal completion, explicit Close, or public Cancel
invalidates every late watchdog decision. Public Cancel and a concurrently
validated loop Interrupt still share one underlying cancellation effect.

Hosts may implement OpenTelemetry as an interceptor or sink adapter. The SDK
does not depend on a telemetry implementation.

## Durable Facts and Replay

`session.Event` is the durable source of truth, with payload ownership by
semantics:

- `Event.Message` carries canonical model messages;
- `Event.Tool` carries canonical tool calls and results;
- `PlanPayload` carries plan state;
- normalized protocol payloads carry coordination facts and replayable product
  projection;
- protocol mirrors and undocumented `_meta` are not a second model context.

Visibility rules are:

- `canonical`: durable and model-visible when it carries model semantics;
- `mirror`: durable client-facing projection, not model truth;
- `journal`: durable execution/recovery truth, promoted into model context only
  through a defined canonical semantic fact;
- `ui_only`, `overlay`, and `notice`: transient presentation state.

External side effects cannot be made generally exactly-once. The reusable
contract is a stable execution identity, declared effect class, idempotency
where available, durable state transitions, and explicit unknown-outcome
recovery. Unknown outcome must remain visible to the next decision-maker; a
journal record that disappears from model context is insufficient.

Persistence implementations must satisfy the capability they advertise:

- event and state mutations that define one fact are atomically committed;
- expected revision is a real CAS contract;
- identical retry deduplicates the complete transaction, including derived
  state, while a changed payload conflicts;
- compaction records its covered event sequence and replay retains every later
  fact regardless of physical file position;
- raw durable JSON migrates before typed decoding when unknown-field
  preservation is part of the compatibility contract;
- restart recovery produces a safe terminal, interrupted, resumable, or unknown
  state rather than silently replaying an effect.

Persistence/replay changes require whole-object round-trip tests comparing the
rebuilt `[]model.Message` with runtime-produced context. Projection/UI reload
tests do not substitute for this evidence.

The reference file store applies `MigrateEventJSON` to event-log lines and WAL
event payloads before unmarshalling `session.Event`. Event and nested journal
schemas migrate independently, so a current event cannot bypass an older run,
tool-execution, or pause-token record. Raw migration preserves unknown fields at
every object level; typed replay then deliberately projects only the current
semantic contract. Journal-only migration facts remain excluded from rebuilt
model history.

## Stable-dependency Invariants

The SDK is treated as a stable dependency layer only while:

- correctness, fault, race, and replay tests cover its persistence and
  side-effect boundaries;
- built-in and external Agents conform to the same normalized ACP semantics;
- only Control can select or transfer the active controller;
- model context is exactly rebuildable from canonical durable facts, including
  unknown outcomes;
- public imports and compatibility policy are explicit and tested by a real
  external consumer;
- local and cloud-oriented hosts exercise the same Core contract with different
  store, lease, sandbox, transport, and executor adapters;
- no deterministic workflow engine or autonomous handoff path has entered Core.

## Comparative Inputs

External SDKs inform constraints; they do not define Caelis's taxonomy:

- OpenAI's distinction between manager-owned calls and ownership-changing
  handoffs reinforces explicit ownership, but Caelis does not need both as
  first-class Core abstractions. See
  [Orchestration and handoffs](https://developers.openai.com/api/docs/guides/agents/orchestration).
- Anthropic's Agent SDK demonstrates that a reusable dependency may ship an
  Agent loop and bundled tools without making each adapter a separate
  repository. See
  [Agent SDK overview](https://code.claude.com/docs/en/agent-sdk/overview).
- Google ADK's Session, State, Memory, and Event separation is a useful
  persistence reference. Its workflow-node model is not a Caelis target. See
  [Sessions](https://adk.dev/sessions/) and
  [Event loop](https://adk.dev/runtime/event-loop/).

## Document Ownership

- [Caelis Architecture](architecture.md): layer map and repository package map.
- This document: normative SDK/Control/ACP ownership and readiness invariants.
- [Agent SDK Usage and Compatibility](agent-sdk-usage.md): consumer-facing
  behavior and known limitations.
- [ACP Projection Architecture](acp-projection-architecture.md): semantic-to-wire
  and surface projection.
- [Control Client Protocol v1 — M2 Design](control-client-m2-design.md): accepted
  product-client commands, Session feed, replay, and HTTP/SSE boundary.
- [Release](release.md): release mechanics and post-publish verification.
