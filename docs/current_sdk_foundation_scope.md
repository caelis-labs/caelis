# Current SDK Foundation Scope

## Status

The SDK foundation is no longer a purely future-facing branch of the design.

The current local product path already runs through:

`cmd/cli -> app/gatewayapp -> gateway -> sdk`

This document now records the boundary rules that still matter while that stack
continues to evolve.

For the current package map, see [architecture.md](architecture.md). For the
gateway-layer design target, see
[unified_gateway_foundation_spec.md](unified_gateway_foundation_spec.md).

## What Must Stay True

1. `sdk/` remains independent of product surfaces such as `tui/`,
   `app/gatewayapp`, and `cmd/cli`.
2. Root `sdk` packages stay contract-first; concrete implementations live in
   subpackages such as `sdk/runtime/local` and `sdk/session/file`.
3. `gateway/` remains the product-facing orchestration boundary built on the
   SDK, rather than letting UI code or app-owned composition logic reach
   directly into SDK internals.
4. `app/gatewayapp` stays the local composition root for prompt assembly,
   config/session stores, model lookup, and sandbox/runtime wiring.
5. Adapters keep consuming the stable root `gateway` contract instead of
   importing `gateway/core` implementation details.

## Current Engineering Rules

### 1. Keep SDK boundaries clean

The SDK owns reusable contracts and implementations for:

- runtime and controller orchestration
- session models and persistence services
- tools, plugins, delegation, and terminal abstractions
- sandbox selection and execution backends

It should not gain direct knowledge of TUI presentation code, CLI flag parsing,
or app-owned configuration persistence.

### 2. Keep gateway as the product seam

The gateway layer translates SDK capabilities into product-facing session,
turn, replay, continuity, and control-plane contracts.

That means new product surfaces should normally plug into `gateway/` first,
then adapt into presentation or transport code.

### 3. Keep local assembly in app/gatewayapp

`app/gatewayapp` is the place where the local process decides:

- which session store to use
- which runtime implementation to instantiate
- how prompt fragments are assembled
- how model lookup and sandbox settings are persisted

That wiring should not leak upward into adapters or downward into the SDK.

## Related Documents

- [architecture.md](architecture.md): current codebase layering and entry flow
- [unified_gateway_foundation_spec.md](unified_gateway_foundation_spec.md):
  gateway contract intent and deferred work

Historical implementation plans and migration prompts have been removed. Keep
new durable rules in the current references above rather than adding dated plan
documents.
