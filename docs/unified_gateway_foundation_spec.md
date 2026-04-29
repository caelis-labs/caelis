# Unified Gateway Foundation Spec

## Status

This document is the active contract reference for the SDK-backed gateway layer.

The current local entry flow is:

`cmd/cli -> app/gatewayapp -> gateway -> adapters -> tui/headless`

For the current package map, see [architecture.md](architecture.md). This file
focuses on the gateway contract, canonical event semantics, and the remaining
surface work that should stay outside the local TUI/headless path.

## Product Goal

Caelis uses the gateway as the single product-facing control plane over the SDK.
The same boundary must support:

- local TUI turns
- headless one-shot turns
- ACP-backed participants, subagents, and main-controller handoffs
- future GUI, daemon, and remote-channel surfaces

The goal is not to copy another product feature-for-feature. The goal is one
runtime orchestration contract that multiple surfaces can consume without
rebuilding session, replay, approval, interrupt, or agent-control logic.

## Gateway Responsibilities

The gateway owns:

- session lifecycle and session lookup
- turn lifecycle and active-run arbitration
- replay and continuity over stable cursors
- approval requests and decisions
- control-plane state for local and ACP controllers
- canonical event projection from SDK/runtime/session activity
- binding state for local and future remote surfaces

The gateway does not own:

- prompt assembly
- provider credential lookup
- concrete sandbox/runtime construction
- TUI rendering, grouping, or layout
- ACP wire transport details

Those remain in `app/gatewayapp`, SDK implementation packages, adapters, or
presentation code.

## Layering Rules

1. `gateway/` root exposes the adapter-facing contract.
2. `gateway/core` owns implementation details for local session, turn, replay,
   approval, and control-plane behavior.
3. Adapters depend on the root `gateway` contract, not on `gateway/core`
   internals.
4. `app/gatewayapp` is the local composition root that wires SDK runtime,
   session store, model lookup, prompt assembly, sandbox policy, and gateway
   resolver.
5. UI code must consume gateway events and driver APIs instead of reaching into
   SDK runtime internals.

## Canonical Event Contract

Gateway events are the channel-neutral product contract. They carry stable
metadata plus typed payloads for the event family.

Current event families include:

- user and assistant messages
- reasoning and notice narratives
- plan updates
- tool call and tool result lifecycle
- approval requests
- participant lifecycle
- controller handoff
- compaction lifecycle
- generic lifecycle and system notices

Events must carry enough scope and origin metadata for adapters to distinguish:

- main controller activity
- ACP participant activity
- delegated subagent activity
- local runtime activity

Adapters may preserve raw protocol data for diagnostics, but UI and headless
surfaces should prefer canonical payloads.

## ACP And Multi-Agent Model

The gateway treats ACP as an integration source, not a parallel product path.

- A local kernel controller and an ACP controller both occupy the same active
  controller slot.
- ACP participants and spawned ACP subagents project into the same canonical
  participant and tool/narrative event families.
- Handoff is the explicit mechanism for changing the active controller.
- Agents must not silently replace the session controller without going through
  the gateway control-plane contract.

## Current Local Acceptance

The local path is acceptable when:

- headless and TUI entry both run through `app/gatewayapp` and `gateway`
- adapters consume root `gateway` contracts
- replay and continuity use gateway-owned cursors and session references
- canonical payloads cover TUI-visible narrative, tool, plan, approval,
  participant, handoff, compaction, and lifecycle events
- control-plane state exposes local and ACP controller ownership through the
  gateway contract
- tests cover the local gateway, adapters, and TUI projection paths

## Deferred Surface Work

The following remain outside the local release baseline unless a future task
explicitly scopes them:

- daemon host lifecycle as a product surface
- remote transport discovery and pairing
- channel-scoped auth and tenancy policy
- Telegram, Discord, webhook, or other remote adapters
- multi-remote session hosting beyond the current host-oriented primitives

These should enter through gateway/host or a future gateway adapter layer, not
by bypassing the gateway contract.
