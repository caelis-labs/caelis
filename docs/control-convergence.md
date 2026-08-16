# Control Convergence

This document defines the desired Control end state. It is not an implementation
map, migration log, or record of completed milestones.

## Goal

Every product client should use one transport-neutral Control boundary for
commands, Session lifecycle, approvals, replay, Task observation, status, and
future orchestration.

`control/appserver` is the aggregate boundary. It exposes the principal-bound
capability set consumed by presentation surfaces; focused `control/*` domains
remain the semantic owners composed behind it. Task observation remains a
separate stream and authority and should join the aggregate client capability
set without joining the Session feed. `app/*` owns Host composition and
transport adapters and must not import a concrete Surface.

```text
Surfaces -> Control contracts -> Agent Runtime / SDK
```

Adding a Surface must not require access to Runtime handles, Session stores,
model configuration internals, or product composition objects. Adding a Control
capability must not create another Surface-private API or grow one aggregate
facade without a coherent semantic owner.

## Target Ownership

Control should own:

- product configuration, credentials, model profiles, Agent bindings, and
  placement;
- Agent assembly, endpoint lifecycle, permissions, review policy, and system
  Agents;
- Session authorization, lifecycle, feed/replay, approvals, and status;
- Task directory and observation authorization;
- idempotent product operations and effect recovery;
- controller selection, handoff authorization, and the future Agent Manage
  Loop.

The SDK should own reusable Runtime, model, tool, Session, sandbox, task, and
normalized collaboration contracts. Protocol packages should own
product-neutral wire schema, compatibility, and projection. A transport codec
that directly serializes one Control domain stays with that Control owner.
Surfaces should own presentation and input only.

## Client Boundary

The AppServer contract is:

- transport-neutral and authenticated by an injected trusted principal;
- request-scoped, with explicit Session and target identity;
- revision-aware and idempotent for mutations;
- typed for conflicts, unsupported capabilities, and unknown outcomes;
- usable in-process and through a thin HTTP/SSE Host adapter; ACP remains its
  separate protocol surface;
- composed from focused Control domains rather than one ever-growing Service.

The HTTP Host adapter, CLI, TUI, ACP, and future GUI mappings should translate
DTOs and errors without owning business policy, replay, approval queues, or
persistence. The Host adapter is infrastructure around Control, not a
presentation surface parallel to TUI or ACP.

## Data and Stream Model

The convergence target has one authority for each concern:

- canonical Session facts and guarded state are durable truth;
- one Control-owned Session feed provides ordered live and replay delivery;
- one Control-owned Task service provides authorized transient observation;
- one opaque Envelope Cursor is the public resume token;
- typed Envelope fields carry scope, relation, position, approval, and identity;
- bounded observation loss is explicit and never blocks producers;
- transient Task or child output never becomes parent model context without a
  canonical result.

Main-Turn ingress and Task observation remain separate so a slow, disconnected,
or failed Task observer cannot delay or reclassify the parent Turn.

## Lifecycle and Effects

One canonical Turn owns the Session execution fence for its complete
asynchronous lifetime. Overlapping Control mutations require an explicit
purpose and matching revision or fence. Controller handoff is exclusive and
commits only after the previous owner is quiescent.

External effects use durable intent, stable identity, idempotency where
available, and explicit recovery. Control must preserve `unknown_outcome` when
it cannot prove whether an effect occurred; it must not convert uncertainty
into a blind retry or a fabricated success.

## Package Convergence

Move a capability only when its semantic ownership is stable and the move
removes real coupling or a duplicate path.

- reusable, product-neutral contracts converge under `agent-sdk/*`;
- stable product domains converge under focused `control/*` packages;
- composition and host-specific adapters remain private;
- presentation code remains under `surfaces/*`;
- product-neutral wire types remain under `protocol/*`; domain-bound codecs
  remain with their semantic owner.

Do not recreate `ports/*`, grow private prompt or Surface facades into product
APIs, or move packages merely to make the tree look complete. When a replacement
lands, remove the superseded wrapper, mirror, fallback, tests, and documentation
unless a maintained compatibility contract names its owner and removal
condition.

## Convergence Signals

The architecture is converging when:

- a new Surface needs only Control and protocol contracts;
- a new product operation has one owner and one idempotency path;
- Session replay and Task observation have no competing implementation;
- central composition files lose feature-specific branching;
- private facades shrink as stable domains emerge;
- architecture lint can enforce every deterministic dependency rule;
- durable round trips, live/replay parity, and failure tests prove semantic
  ownership instead of relying on comments.

## Non-Goals

- a deterministic workflow graph or SDK-owned scheduler;
- autonomous Agent handoff authority;
- Surface-owned replay, permission, or persistence;
- a durable UI transcript cache;
- a second product API hidden behind compatibility;
- package or repository movement without a concrete ownership benefit.
