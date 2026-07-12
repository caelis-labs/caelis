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

## Terminal Projection

RUN_COMMAND, Bash-compatible command tools, and SPAWN share the same terminal
projection contract:

- `_meta.terminal_info`: local terminal identity for a tool call.
- `_meta.terminal_output`: exact output bytes in `data`.
- `_meta.terminal_exit`: local terminal termination state when known.
- `content[type="terminal"]`: an empty render anchor with the same terminal id.

The empty terminal anchor mounts a panel; it is not an output transport and must
not contain terminal text. ACP stdio, TUI, headless, and future GUI surfaces
must render bytes from `_meta.terminal_output` and avoid surface-private
terminal fallbacks.

Standard ACP client-created terminals remain reserved for execution that is
actually delegated to a client-created ACP terminal id.

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
