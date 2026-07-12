# ACP Projection Architecture

ACP has two related roles in Caelis:

1. It is the native interoperability and control language shared by built-in
   and external Agents.
2. It is the common protocol projected to presentation surfaces.

This document focuses on the second role. The reusable SDK boundary and
ACP-native orchestration decisions are documented in
[docs/agent-sdk-boundary.md](agent-sdk-boundary.md).

Caelis presentation surfaces consume standard ACP-shaped payloads plus
documented optional `_meta` extensions. TUI, ACP stdio/server, headless, and
future GUI surfaces should not own runtime, control, tool, sandbox, stream, or
persistence semantics.

```text
Built-in Agent Runtime -------------------------------+
                                                       +-> normalized SDK ACP semantics
External ACP Agent -> transport/lifecycle adapter ----+   -> Control / Agent Manage Loop
                                                            -> eventstream.Envelope
                                                            -> surfaces
```

The control layer may bridge local runtime events, system-managed agent events,
or external ACP-agent updates. Surfaces should not need to know which source
produced an event once it has been normalized into `eventstream.Envelope`.

Native ACP means semantic equivalence, not mandatory JSON-RPC serialization for
in-process Agents. Canonical message and tool payloads remain the model-context
truth; an ACP update or surface envelope is not a replacement for them.

## Semantic and Wire Ownership

`agent-sdk/session.ProtocolUpdate`, `ProtocolApproval`, and their nested DTOs
are the single normalized semantic owner shared by built-in and external
Agents. They contain no JSON-RPC transport requirement and the SDK does not
import the product ACP implementation.

`protocol/acp/schema` owns only public ACP wire shapes, including JSON field
names and patch-style pointer fields. `protocol/acp/semantic` is the adapter
between those wire DTOs and the SDK owner. External ingress decodes through
that adapter before producing session events; projection encodes through it
before adding product display policy or documented `_meta` extensions. This
keeps compatibility, terminal rendering, and transport details outside the
SDK without maintaining a second semantic schema.

## Orchestration Ownership

Built-in and external Agents differ in transport, process lifecycle, trust, and
policy. They do not use different top-level controller or participant semantics.
Control selects endpoints and authorizes handoff; projection only represents
the resulting normalized facts. The full ownership, dynamic orchestration, and
no-workflow rules are defined once in
[Agent SDK Boundary](agent-sdk-boundary.md).

Message/tool/plan updates plus permission, cancellation, participant, and
handoff use the centralized semantic path.
External controller permission ingress and prompt responses route through
`protocol/acp/semantic`; built-in participant and Control-authorized handoff
facts use SDK-owned constructors. Architecture lint rejects new direct
participant/handoff protocol construction outside the SDK semantic owner.

## Task Stream Projection

`RunCommand`, Bash-compatible command tools, and `Spawn` share the task-stream
service, subscription lifecycle, ordering, and backpressure path. They do not
share rendering semantics.

Local command execution projects opaque terminal bytes through the documented
Caelis extension:

- `_meta.terminal_info`: local terminal identity for a tool call;
- `_meta.terminal_output`: exact output bytes in `data`;
- `_meta.terminal_exit`: local terminal termination state when known.

The current empty `content[type="terminal"]` anchor is not an output transport;
the Caelis metadata carries the bytes. This is a deliberate compatibility
projection that has been observed to mount and update correctly in the tested
Zed version. It does not claim standard terminal ownership because the official
ACP terminal flow uses `terminal/create` to execute the command in the Client
environment and the Client then owns output, wait, kill, and release.

Forcing an existing Caelis Runtime command through that standard flow changes
execution placement, sandbox, permission, environment, and recovery ownership.
The compatibility anchor therefore remains supported until the T0 tradeoff
experiment selects profiles for Zed interoperability, strict ACP, and the
Caelis client protocol. Strict ACP mode may only emit a standard terminal
anchor for a real client-created terminal. The detailed experiment and removal
criteria live in the roadmap.

The current repository does not support that client-hosted execution path end
to end. Outbound terminal RPC callbacks exist, but `RunCommand` remains bound to
the SDK sandbox/task lifecycle and the ACP Prompt path does not select the
remote Client as its execution backend. Caelis ACP Client connections therefore
advertise `clientCapabilities.terminal=false` unless a complete terminal
handler is explicitly installed. The reverse local-output adapter used by the
Zed compatibility projection does not change that capability assessment.

A spawned Agent instead projects its normalized child events as standard ACP
message, thought, tool, content, diff, plan, permission, and lifecycle
semantics. Caelis Envelope scope and relationship fields associate those
payloads with the parent Spawn call and durable task identity. A Surface may
derive a compact text panel from those events, but the formatted text is not the
protocol or replay authority. Future GUI clients render the same scoped ACP
payloads with the same components used for a main Agent.

The parent receives the delegated final result through the canonical Spawn/Task
result. Live child events remain transient until a linked semantic child replay
authority is implemented; they must not be promoted into parent model context
or reconstructed from terminal text. `control.StreamSubscriber` is a
transitional in-process source adapter and must move behind a Control-owned
client event broker rather than becoming an app-server or GUI contract.

The ordered migration and acceptance criteria are defined in
[Control and Client Protocol Roadmap](control-client-roadmap.md).

## Session Identity

`session.SessionID` is globally unique within one filestore root. Workspace key
is creation/listing/display metadata and may participate in policy decisions,
but it is not part of session identity.

ACP and gateway surfaces must pass the session id they received and must not
keep in-memory `sessionId -> workspace/cwd` caches to repair later requests.

ACP projection does not create a second persistence authority. Main-controller
and participant prompt streams receive the owning Turn's SDK `MutationGuard`,
and every canonical event materialized by `internal/acpbridge` preserves it on
the Session append. Participant attach/detach is separately classified as
Control-owned lifecycle metadata; controller handoff remains an exclusive,
fenced Control transition. Transport source labels and `_meta` never grant
lease authority.

Before v1.0, unsupported old session/index layouts may fail explicitly. Caelis
prefers the clean identity model over compatibility fallbacks.
