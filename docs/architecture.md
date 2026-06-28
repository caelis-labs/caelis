# Caelis Architecture

Caelis keeps one durable session model and projects that model to ACP-native
client streams. The durable store owns model semantics; client protocols and UI
view models are projections.

## Layers

- `cmd/caelis` is the binary entrypoint.
- `internal/cli` selects doctor, ACP stdio, headless, or TUI mode.
- `app/gatewayapp` wires concrete implementations into the local runtime.
- `internal/kernel` owns turns, replay, approvals, active handles, participants,
  lifecycle, and gateway orchestration.
- `ports/*` define public extension contracts.
- `impl/*` contains concrete providers, tools, stores, sandbox backends, policy,
  prompt, stream, and agent implementations.
- `protocol/acp` owns ACP schema, JSON-RPC, eventstream envelopes, terminal
  metadata, and projection helpers.
- `surfaces/*` adapt the kernel gateway to user interfaces. Surfaces do not own
  model, tool, sandbox, or persistence semantics.

## Durable Session Contract

`ports/session.Event` is the source of truth for persisted runtime context.
Durable model-visible facts must be stored as canonical payloads:

- `Event.Message` for user and assistant model messages, including text,
  reasoning, tool-use parts, and provider replay metadata.
- `Event.Tool` for durable tool call/result state, including ids, names, args,
  status, output, content, truncation, and replay boundaries.
- `PlanPayload` for durable plan state.
- `EventProtocol{Method, Update, Permission}` for ACP/control-plane projection
  payloads such as permission, participant, and handoff state.

`Event.Protocol.Update` is not the local Agent SDK replay source. Protocol-only
canonical assistant, tool, or plan facts are invalid unless the corresponding
durable semantic payload is present.

## Visibility

- `canonical`: persisted, replayed, and model-visible when the event carries
  model semantics.
- `mirror`: persisted/replayed as a client-facing mirror, not a second copy of
  parent model context.
- `ui_only`: transient live trace; not durable parent model context.
- `overlay`: transient UI state; not invocation-visible.
- `notice`: client notification state; not model-visible history.

Subagent structured stream events are `ui_only` trace. The parent model receives
subagent output through the durable `SPAWN`/`TASK` tool result, not through child
assistant stream chunks.

## Client Event Protocol

`protocol/acp/eventstream.Envelope` is the v1 client stream container for TUI,
headless, ACP bridges, and future GUI/app-server surfaces. It carries:

- standard ACP `session/update` payloads;
- standard ACP `request_permission` payloads;
- Caelis extension events for lifecycle, participant state, approval review,
  notices, and errors.

Usage is emitted only as standard ACP `session/update` `usage_update`. Token
breakdown is stored under `_meta.caelis.usage`; `size` is only set when a real
context window or budget is known.

SSE uses `cursor` as the event id and serializes the full envelope as event
data. WebSocket transports serialize the envelope directly. ACP stdio maps
standard payloads to JSON-RPC ACP messages.

Cursor fields:

- `cursor`: opaque per-envelope resume id.
- `event_id`: durable source session event id when the envelope comes from
  session replay/projection.
- `projection_id`: stable per-source projection id for clients that need
  cross-mode deduplication or replay resume.

Replay cursors use durable projection identity. Live cursors are stream-local,
but session-derived live envelopes expose `projection_id` when available.

## ACP Ingress

External ACP updates are normalized at ingress before they enter durable
history. Main-controller final user/assistant semantics become canonical
session events. Controller assistant/tool/plan stream updates default to
client trace unless they carry durable semantic payloads. Subagent structured
streams remain `ui_only` trace.

## Projection Boundaries

Canonical priority is:

1. `Event.Tool` and `Event.Message` semantic payloads;
2. `Event.Protocol` ACP/control-plane projection payloads;
3. `_meta.caelis` display/debug hints.

`surfaces/transcript.Event` is a UI view model. It is not a wire protocol,
store schema, or app-server API.

`ports/gateway` exposes service contracts and internal payload helpers for
kernel/app coordination. Client-facing surfaces consume `eventstream.Envelope`
and standard ACP payloads directly.

## Compatibility Stance

Before `v1.0.0`, Caelis prefers a clear canonical schema over preserving old
wire or store fallbacks. Unsupported legacy session formats and protocol-only
canonical history should fail explicitly instead of being replayed with guessed
semantics.
