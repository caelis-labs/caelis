# Session And Runtime Contract

This document is the non-negotiable runtime contract for the rewrite. It
defines the durable session semantics, model-context replay rules, and
runtime Agent Loop ownership that must be preserved.

## Core Rule

One durable Caelis session is the source of truth for runtime context.

ACP is the client projection protocol. ACP is not the durable replacement
for model-visible messages. TUI transcript state is not the durable replay
source.

Reloaded model input must match the semantic message sequence produced at
runtime, except when the system prompt, available tools, selected model, or
loaded skills intentionally changed.

## Durable Session Facts

`session.Event` is the canonical event envelope. The rewrite should prefer
semantic payloads:

- `UserMessage`: model-visible user content.
- `AssistantMessage`: model-visible assistant content, reasoning, and
  tool-use parts.
- `SystemContext`: durable system context when it intentionally participates
  in model-visible history.
- `ToolCallPayload`: durable tool call anchor, ids, names, args, status,
  metadata, content, and provider replay details.
- `ToolResultPayload`: durable tool execution output, model-visible result
  content, display content, error state, truncation, and metadata.
- `PlanPayload`: durable plan state.
- `Notice` and `Lifecycle`: runtime state that may be canonical or transient
  depending on visibility.
- `Scope`: turn, controller, participant, ACP, and source ownership.
- `Actor`: user, controller, participant, tool, or system ownership.

Legacy fields are accepted for migration and transient projection:

- `Message`
- `Tool`
- `Protocol`
- `Text`
- `Meta`

They must not become the only durable source for new model-critical data.

## Visibility Semantics

| Visibility | Meaning | Model context | Replay transcript |
| --- | --- | --- | --- |
| `canonical` or empty | Durable session history | yes when event type is invocation-visible | yes |
| `ui_only` | Transient live rendering or progress | no | no unless explicitly requested |
| `overlay` | Invocation-only overlay state | yes if selected by context builder | no |
| `mirror` | Durable transcript-only mirror | no | yes |

Rules:

- `VisibilityUIOnly` chunks may be sent live but cannot be required for
  reload.
- Durable final events must contain complete model-visible state.
- Mirror events are useful for display but must not enter model context.
- Overlay events are useful for invocation but should not become transcript
  truth.

## Model Message Sequence

The context builder must produce a provider-neutral `[]model.Message` from
durable semantic events.

Required ordering:

1. prepend system/instruction context selected for the current turn;
2. scan durable semantic events in session order;
3. emit each model-visible user, assistant, tool-call, and tool-result message
   at its original semantic position;
4. keep assistant tool-use messages before their matching tool-result messages;
5. preserve multi-tool result ordering according to the provider-neutral model
   sequence;
6. insert compaction/system context at the point where it replaces or
   summarizes previous history.

Multi-tool assistant turns must preserve one assistant semantic message even
if tool execution creates multiple per-tool anchors. Per-tool anchors are
allowed for execution and projection boundaries, but replay must not create
duplicate assistant messages.

Tool results must carry enough durable state to rebuild the model-visible
tool result without re-reading UI display text.

## Runtime Agent Loop Ownership

### `agent/llmagent/`

Owns the provider-neutral Agent Loop:

1. build one model request from `agent.InvocationContext`;
2. call `model.LLM.Generate`;
3. collect streaming or final response events;
4. canonicalize assistant tool calls;
5. emit assistant/tool-call events;
6. execute tool calls through wrapped `tool.Tool` instances;
7. emit tool-result events;
8. continue until the assistant produces final text or the context ends.

### `runner/`

Owns local runtime orchestration around that loop:

- load session state and events;
- prepare invocation context;
- append user input;
- resolve agent spec and policy profile;
- wrap tools for policy, approval, runtime task handling, and subagents;
- persist canonical events;
- handle compaction overflow recovery;
- update usage and run state;
- expose stream/task state.

### `gateway/kernel/`

Owns gateway orchestration outside the loop:

- start and cancel active turns;
- enforce one active turn per session;
- resolve turn requests through the configured resolver;
- route approvals to manual or automatic decision paths;
- bridge participant prompts;
- project persisted session events to gateway events;
- replay events after cursors.

The kernel must not invoke model providers or implement tool behavior.

## ACP Projection Contract

The gateway emits stable event payloads that can be projected to standard
ACP via `acp/projector/`:

- user messages -> `user_message_chunk`
- assistant reasoning -> `agent_thought_chunk`
- assistant text -> `agent_message_chunk`
- tool calls -> `tool_call`
- tool results and progress -> `tool_call_update`
- plans -> `plan`
- approvals -> `request_permission`
- lifecycle and participant state -> ACP-compatible update plus Caelis
  `_meta`

Caelis display hints belong under ACP `_meta.caelis`. `_meta` may carry
terminal output ids, stream cursors, cwd, display names, task ids,
route/backend facts, and visual hints. It must not be the only copy of
model-critical data unless the field is explicitly defined as replay
metadata and covered by tests.

## External ACP Contract

External ACP agents meet built-in agents at the gateway boundary:

- ACP input is normalized into canonical session events before storage.
- ACP live updates may be forwarded to clients, but storage remains
  semantic.
- External ACP participants can be sidecars, delegated subagents, or main
  controllers.
- Delegated subagent private tool work must not leak into the main model
  context unless it is summarized into shared dialogue.
- Shared user and final assistant dialogue can be visible across
  participants.

## Approval Contract

Tool approval has three separate concepts:

- **approval mode**: how an approval is decided (`auto-review` or `manual`);
- **policy profile**: which policy rules are active (`workspace-write`);
- **sandbox constraints**: how a tool or command must execute.

These must not be collapsed into one `mode` string in new internal contracts.

Approval requests must preserve:

- tool id, name, input, title, and kind;
- session/run/turn identity;
- participant or subagent origin;
- risk/reason text when available;
- chosen option and final outcome.

## Compaction Contract

Compaction is part of runtime context management, not UI display.

Compaction events must preserve enough semantic state for later invocation.
Usage accounting must remain in durable session state or semantic metadata
and must not enter model-visible history unless intentionally represented
as system context.

Overflow recovery may retry the turn after compaction, but it must not
append duplicate user input or duplicate assistant tool calls.

## Required Invariants

Every rewrite touching session, runtime, gateway, ACP, or TUI projection
must preserve these invariants:

- store round-trip rebuilds the same model-visible message sequence;
- ACP replay derives from canonical events, not transient live chunks;
- TUI reload consumes the same ACP-native event stream as live rendering;
- `ui_only` progress is optional for replay;
- tool-call ids match tool-result ids after reload;
- multi-tool assistant turns do not duplicate assistant messages;
- provider replay metadata such as reasoning signatures survives
  persistence;
- approval and lifecycle events do not become accidental model-visible
  content;
- participant/private subagent events obey visibility rules;
- legacy fields are migration inputs, not new durable ownership.
