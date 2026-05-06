# Caelis Architecture

## Current Entry Flow

The current local binary path is:

`cmd/caelis -> internal/cli -> app/gatewayapp -> gateway`

Surfaces adapt that shared gateway path at the edge:

`headless -> gateway`

`tui/gatewaydriver -> tui/driver -> tui/tuiapp`

`acpbridge/gatewayagent -> acpbridge/agentruntime -> acp`

- `cmd/caelis` is the only production binary entrypoint and only handles process
  startup.
- `internal/cli` parses one flat flag set and routes doctor, headless, ACP stdio,
  and interactive TUI modes.
- `app/gatewayapp` assembles the local stack: prompt inputs, model lookup,
  sandbox/runtime selection, app config, and durable session storage.
- `gateway/` exposes the stable product-facing contracts for sessions, turns,
  replay, continuity, bindings, and control-plane state.
- `headless` runs one-shot turns over the gateway contract.
- `tui/gatewaydriver` implements the TUI driver contract consumed by the Bubble
  Tea application in `tui/tuiapp`.
- `acpbridge/gatewayagent` exposes the local stack as a standard ACP agent.

## Layering

### 1. `sdk/`

The SDK is the reusable foundation. Root packages stay contract-first and pure;
concrete implementations live in subpackages.

Examples:

- `sdk/runtime` with `sdk/runtime/local`
- `sdk/session` with `sdk/session/file`
- `sdk/tool` with `sdk/tool/builtin`
- `sdk/sandbox` with `sdk/sandbox/host`, `sdk/sandbox/bwrap`,
  `sdk/sandbox/landlock`, and `sdk/sandbox/seatbelt`

This layer owns runtime, session, model/provider, tool, sandbox, delegation,
plugin, and terminal primitives.

### 2. `gateway/`

The gateway is the product seam built only on the SDK.

- `gateway/` root re-exports stable request/response types and service
  interfaces.
- `gateway/core` owns session lifecycle, turn orchestration, replay,
  continuity, bindings, and control-plane behavior.
- `gateway/host` owns foreground/daemon host lifecycle and remote-session
  helpers.

Concrete surface adapters do not live under `gateway/`. They should depend on
the root `gateway` contract, not on `gateway/core` internals.

### 3. `app/gatewayapp/`

This package is the local composition root for the current product path.

It is responsible for:

- building the local runtime and gateway resolver
- storing app config under `~/.caelis/config.json`
- storing sessions under `~/.caelis/sessions`
- assembling prompts from built-in text, `AGENTS.md`, and local skill metadata
- persisting model and sandbox preferences for future turns
- exposing narrow app services that surface adapters can bind to at the edge

`app/gatewayapp` should not import TUI, headless, or ACP bridge adapters. It
builds the local app stack; `internal/cli` and the surface packages decide how
to expose that stack.

There is intentionally no `app/tuiadapter` or `app/tuisurface` package. TUI
driver construction belongs to `tui/gatewaydriver`.

### 4. Surface Adapters

Current local adapters are intentionally small:

- `headless`: one-shot execution for `-p` or piped stdin
- `tui/gatewaydriver`: bridge between gateway events/app services and the TUI
  driver contract
- `acpbridge/gatewayagent`: bridge between the local stack and the standard ACP
  agent runtime

These adapters translate between surface-specific interaction models and the
shared gateway contracts. They should depend on the root `gateway` contract and
the relevant surface contract, but not on `gateway/core` internals.

### 5. Presentation

The top-level `tui/` tree remains presentation code:

- `tui/tuiapp`: Bubble Tea application state machine and slash-command UX
- `tui/driver`: presentation-facing driver contract consumed by the TUI and
  implemented by adapters
- `tui/tuikit`: UI primitives
- `tui/modelcatalog`: model metadata used by `/connect`
- `tui/tuidiff`: diff rendering helpers

The TUI owns interaction, rendering, and its driver contract, but not runtime
orchestration, config persistence, model lookup ownership, sandbox selection, or
gateway stack assembly.

## Adjacent ACP Packages

`acp/` and `acpbridge/` are still part of the repository, but they are adjacent
integration packages rather than the primary local CLI path. They provide ACP
schema, transport, projection, and runtime bridge helpers around the current
SDK and gateway stack.
