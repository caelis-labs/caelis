# Agent SDK Boundary and Evolution Plan

Status: accepted direction as of 2026-07-10.

This document records the target boundary for `agent-sdk`, the architecture
review findings that block treating it as a stable dependency layer, and the
accepted ACP-native orchestration model. It is a direction and readiness plan,
not a claim that the current implementation already satisfies every contract.

## Accepted Decisions

1. `agent-sdk` is an ordinary reusable package tree inside the root
   `github.com/caelis-labs/caelis` Go module. It keeps the import prefix
   `github.com/caelis-labs/caelis/agent-sdk/...` but has no separate `go.mod`,
   dependency graph, version, release, or test lifecycle.
2. Independence means enforced dependency direction, explicit public
   contracts, package-level testability, durable compatibility, and reuse by
   multiple Caelis hosts. It does not require a separate Go module or Git
   repository.
3. ACP is Caelis's native interoperability and control language for built-in
   and external agents, not only a presentation protocol.
4. The SDK may own reusable ACP-compatible agent, controller, participant,
   event, permission, cancellation, and transfer contracts. The concrete ACP
   wire transport, Caelis product assembly, and surface projection remain
   outside the SDK.
5. Handoff is a control-plane ownership transition. An agent may report
   completion, missing capability, or a suggested next actor, but it cannot
   authorize or commit a handoff.
6. Handoff decisions belong to explicit user control or to the Agent Manage
   Loop and other dynamic orchestration policy in the Control layer.
7. Caelis will not build a deterministic workflow engine. A graph/DAG DSL,
   workflow nodes and edges, and SDK-owned sequential/parallel workflow state
   machines are explicit non-goals.
8. Caelis does not adopt Agent-as-tool, Handoff, Workflow node, and Remote agent
   bridge as four required top-level Core abstractions. Existing task and
   delegation primitives remain available; remote is an ACP transport choice,
   not a separate agent category.

## Current Baseline

The package layer is enforced without a nested module:

- the root `go.mod` is the single dependency graph and release version owner;
- root build, lint, vet, and test commands cover `agent-sdk/...` exactly once;
- `make sdk-boundary-check` rejects nested module metadata, checks production
  and test dependency closure, compiles the supported-package allowlist from an
  external consumer of the root module, and rejects unreviewed supported API
  declaration changes against `agent-sdk/api.txt`;
- architecture lint prevents dependencies on product-host and presentation
  packages.

At the 2026-07-10 review snapshot, however, the SDK contained roughly 58,000
lines of production Go, 57 externally importable packages, about 69 exported
interfaces, 297 exported structs, and 471 exported functions. All existing
tests and boundary gates passed, but they mostly prove that the current package
graph compiles. They do not yet prove safe persistence, replay correctness,
public API quality, or long-term compatibility.

The boundary should therefore be hardened in place. Module or repository
topology is not a substitute for contract quality.

## Target Ownership

| Layer | Owns | Must not own |
| --- | --- | --- |
| Agent SDK stable kernel | Agent/run values, model and tool contracts, canonical session events, durable run semantics, policy and approval primitives, task/delegation primitives, normalized ACP-compatible coordination contracts, replay and turn mechanics | Caelis product profiles, UI state, agent selection policy, Manage Loop decisions, raw product wire transports |
| Agent SDK bundled capabilities | Provider implementations, reusable stores, sandbox backends, builtin tools, MCP, skill and display helpers that remain useful to more than one host | Imports from `app/*`, `surfaces/*`, product-host `ports/*`, root `internal/*`, or the product `protocol/acp/*` implementation |
| Caelis Control | Agent registry and assembly, endpoint factories, credentials and process lifecycle, active-controller selection, policy and approval routing, Guardian/Reviewer/system agents, Agent Manage Loop, dynamic orchestration, handoff decisions and commits | Presentation rendering; autonomous model-driven ownership transfer |
| ACP product implementation | ACP JSON-RPC/wire schema, stdio or network transport, compatibility handling, adapters from external endpoints, projection into `eventstream.Envelope` | Agent selection policy or model-visible canonical state |
| Surfaces | Rendering ACP-shaped envelopes, collecting user input, documented `_meta` display extensions | Runtime, policy, persistence, tool, sandbox, or handoff decisions |

Package placement remains transitional. Ownership is determined by semantics,
not by the current directory name.

The current implementation enforces the handoff boundary directly:

- `agent-sdk/runtime` executes neutral controller and participant turns but has
  no `HandoffController` method and contains no Caelis context-selection text;
- hosts inject `controller.ContextRouter`, so missing routing policy fails
  assembly instead of silently sending an arbitrary transcript;
- root-private `internal/controlplane` owns Caelis shared-ledger selection,
  endpoint activation/deactivation, process reattach and rollback, durable
  binding refresh, and atomic handoff binding/event persistence;
- `internal/kernel` receives Control explicitly and never infers orchestration
  authority by type-asserting the execution Runtime.

## ACP-Native Collaboration Model

Built-in and external agents should expose the same effective language:

- session identity and lifecycle;
- declared capabilities and configuration;
- prompt and content input;
- message, thought, tool, plan, permission, and lifecycle updates;
- cancellation and completion;
- controller and participant identity.

The transport may be an in-process call, stdio ACP, or a future network
connection. Native ACP means semantic equivalence; it does not require an
in-process built-in agent to serialize every call through JSON-RPC.

```text
Built-in Agent Runtime -------------------------------+
                                                       |
External ACP Agent -> ACP transport/lifecycle adapter -+-> normalized SDK ACP contracts
                                                            -> Control / Agent Manage Loop
                                                            -> eventstream.Envelope
                                                            -> surfaces
```

The normalized contract needs one stable semantic owner. Keeping copied,
independently evolving ACP-shaped DTOs in both `agent-sdk/session` and
`protocol/acp/schema` would create two schemas. The target is:

- reusable semantic contracts flow from the SDK toward product adapters;
- `protocol/acp` depends on and encodes those contracts where the public ACP
  wire shape permits;
- Caelis-specific wire compatibility and `_meta` extensions stay in
  `protocol/acp`;
- the SDK never imports the product `protocol/acp` package.

ACP-native collaboration does not mean:

- raw ACP payloads are the only persisted or model-visible truth;
- external agents are trusted by default;
- every UI or transport type belongs in the SDK;
- built-in agents must run as child processes;
- agents may autonomously transfer control.

## Controller, Participant, Delegation, and Handoff

Caelis uses two reusable collaboration roles:

- A **controller** owns the next main-session turn for one controller epoch.
- A **participant** is attached to the session as a bounded collaborator or
  sidecar without automatically replacing the controller.

Task, SPAWN, and delegation primitives may use these roles, but Caelis does not
need a generalized `Agent.asTool` abstraction. A bounded delegated result enters
the parent model context through a canonical task or tool result, not through
transient child stream output.

Handoff is a transition between controller epochs. The SDK may define neutral
request, result, transfer-record, and endpoint contracts, but the Control layer
owns the operation:

1. observe session, run, capability, and policy state;
2. decide whether ownership should change;
3. obtain any required approval;
4. quiesce or cancel the current controller as required;
5. activate the selected endpoint and synchronize canonical context;
6. atomically persist the new binding, epoch, and transfer event;
7. resume dispatch through the selected controller.

There must be no LLM-facing handoff tool. A model output may be an advisory
signal to the Control layer, but it has no authority to mutate the active
controller binding.

## Dynamic Orchestration, Not Deterministic Workflow

The future Agent Manage Loop is an event-driven control loop:

```text
observe -> evaluate -> select/dispatch/handoff -> verify -> continue or stop
```

The path is selected at runtime from session state, events, policy, capability,
review results, and user intent. Decisions that affect ownership or durable
execution must be auditable and persisted.

Caelis intentionally does not provide:

- a workflow graph or node/edge DSL;
- a static graph executor;
- SDK-owned Sequential, Parallel, or Loop workflow agent classes;
- a separate RemoteAgent domain abstraction parallel to built-in agents.

Application code may still implement explicit procedures where needed. They
are ordinary Control logic using SDK primitives, not a new deterministic
workflow subsystem.

## Durable Facts and Projection

`session.Event` remains the durable source of truth, but different payloads
have different responsibilities:

- `Event.Message`, `Event.Tool`, and other canonical semantic payloads rebuild
  model context.
- ACP-compatible controller, participant, permission, and transfer payloads
  record coordination facts and support replayable projection.
- Protocol mirrors are not a second copy of model context.
- `_meta` remains display/debug data unless a field is explicitly documented
  as replay metadata.

External ACP input must be normalized before storage. Transient participant or
subagent stream chunks must not enter durable parent model context unless they
are carried by a canonical message, task, or tool result.

## Release-Blocking Risk Register

The following correctness and security work remains P0 for a stable dependency
layer:

| Risk | Required invariant |
| --- | --- |
| Policy decisions currently have fail-open paths | Only explicit allow executes a tool. Unknown profiles, missing decision functions, empty actions, and invalid decisions fail closed. |
| State, metadata, and tool payload clone paths are shallow | Stored values and read-only snapshots cannot share mutable nested references with callers. Failed updates leave stored state unchanged. |
| Concurrent runs and compaction have no session revision/CAS contract | Events have monotonic sequence, sessions have revision, writes use expected revision, and compaction records its covered sequence. |
| File persistence cannot always satisfy its advertised atomic event/state contract | A store either atomically commits event batches and state deltas with idempotency, or explicitly declares that it is not a production transactional adapter. |
| Tool side effects have an unknown-outcome crash window | Durable tool execution records distinguish prepared, approved, started, succeeded, failed, cancellation requested, cancelled, and unknown outcome. |

External side effects cannot be made generally exactly-once by the Agent Core.
The contract is at-least-once execution with stable execution keys,
idempotency where available, declared effect class, and explicit unknown
outcome recovery.

## SDK Contract Quality

The SDK remains one package tree in the root module, but it needs an intentional
public surface:

- Maintain an explicit allowlist of supported public packages.
- Permit `agent-sdk/internal` for implementation helpers when it materially
  reduces the compatibility surface. Go's `internal` import rule keeps those
  helpers unavailable to product packages.
- Split broad interfaces by consumer capability instead of requiring every
  store or executor to implement unrelated control functions.
- Keep concrete providers, stores, sandbox backends, tools, and MCP support in
  the same package tree when useful, but make their dependency direction point
  inward. Do not create modules merely to simulate cleanliness.
- Add external black-box tests, runnable examples, API-diff checks, schema
  migration fixtures, race tests, and crash/fault-injection tests.
- Run the root lint, vet, and test once, plus architecture lint, SDK dependency
  closure, and a root-module external consumer check in CI.
- Document compatibility, minimum Go version, supported platforms, error
  contracts, event ordering, cancellation, and persistence semantics.

The product `runtime/assembly` state keys, profile/mode selection, UI-oriented
controller status, process discovery, and handoff target selection are examples
of Control concerns to peel off. Reusable endpoint, controller, participant,
turn, cancellation, and transfer values are not peel-off targets merely because
they are ACP-compatible.

## Migration Slices

1. **Safety and persistence**: fix fail-open authorization, recursive value
   isolation, session revision/CAS, atomic append, and durable tool execution.
2. **Formalize the ACP contract owner**: remove duplicated semantic DTO
   ownership while preserving product wire/projection adapters and the SDK
   import boundary.
3. **Correct orchestration ownership**: move agent selection, activation
   sequencing, context-routing policy, handoff commit coordination, product
   assembly, and UI/status policy into Control. Keep reusable mechanisms and
   neutral records in the SDK.
4. **Govern the public API**: introduce a supported-package allowlist, internal
   helpers, narrow capability interfaces, examples, compatibility checks, and
   SDK boundary checks in root CI.
5. **Build the dynamic Manage Loop**: consume durable events and capability
   state to coordinate built-in and external ACP endpoints without introducing
   a deterministic workflow engine.
6. **Validate multiple hosts**: run the same SDK contract suite with a local
   host and a cloud-oriented host. Local and cloud differ in store, lease,
   sandbox, transport, and executor adapters, not in Core semantics.

Module extraction, physical repository extraction, and adapter-module
proliferation are not migration slices. Revisit them only through a new explicit
architecture decision if operational constraints later require it.

## Readiness Gates

The SDK is ready to be treated as a stable dependency layer when:

- the P0 risk register is closed with race, fault, and replay tests;
- built-in and external agents use the same normalized coordination contracts;
- only Control can select or transfer the active controller;
- model context can be rebuilt exactly from canonical durable facts;
- ACP protocol mirrors and `_meta` cannot silently become model truth;
- SDK public packages and compatibility policy are explicit;
- SDK code has no dependency on Caelis product-host, wire implementation, or
  presentation packages;
- package-boundary and external behavioral consumers pass without importing
  product packages or relying on product internals;
- no deterministic workflow engine or autonomous handoff path has entered the
  Core.

## Comparative Inputs

External SDKs are design inputs, not Caelis's taxonomy:

- OpenAI's distinction between manager-owned agent calls and ownership-changing
  handoffs reinforces the need to make ownership explicit, but Caelis does not
  need to expose both as first-class Core abstractions. See
  [Orchestration and handoffs](https://developers.openai.com/api/docs/guides/agents/orchestration).
- Anthropic's Agent SDK demonstrates that a reusable agent dependency can ship
  an agent loop and bundled tools without requiring each adapter to become a
  separate repository. See
  [Agent SDK overview](https://code.claude.com/docs/en/agent-sdk/overview).
- Google ADK's separation of Session, State, Memory, and Event processing is a
  useful persistence reference. Its workflow-node model is not a Caelis target.
  See [Sessions](https://adk.dev/sessions/) and
  [Event loop](https://adk.dev/runtime/event-loop/).
