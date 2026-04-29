# Caelis Architecture

## Current Entry Flow

The current local application path is:

`cmd/cli -> app/gatewayapp -> gateway -> adapters -> tui/headless`

- `cmd/cli` parses one flat flag set and decides whether to enter headless or
  interactive mode.
- `app/gatewayapp` assembles the local stack: prompt inputs, model lookup,
  sandbox/runtime selection, app config, and durable session storage.
- `gateway/` exposes the stable product-facing contracts for sessions, turns,
  replay, continuity, bindings, and control-plane state.
- `gateway/adapter/headless` runs one-shot turns over the gateway contract.
- `gateway/adapter/tui/runtime` turns gateway events and actions into the driver
  consumed by the Bubble Tea application in `tui/tuiapp`.

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

Adapters should depend on the root `gateway` contract, not on
`gateway/core` internals.

### 3. `app/gatewayapp/`

This package is the local composition root for the current product path.

It is responsible for:

- building the local runtime and gateway resolver
- storing app config under `~/.caelis/config.json`
- storing sessions under `~/.caelis/sessions`
- assembling prompts from built-in text, `AGENTS.md`, and local skill metadata
- persisting model and sandbox preferences for future turns

### 4. Adapters

Current local adapters are intentionally small:

- `gateway/adapter/headless`: one-shot execution for `-p` or piped stdin
- `gateway/adapter/tui/runtime`: bridge between gateway events and the TUI
  driver

These adapters translate between surface-specific interaction models and the
shared gateway contracts.

### 5. Presentation

The top-level `tui/` tree remains presentation code:

- `tui/tuiapp`: Bubble Tea application state machine and slash-command UX
- `tui/tuikit`: UI primitives
- `tui/modelcatalog`: model metadata used by `/connect`
- `tui/tuidiff`: diff rendering helpers

The TUI owns interaction and rendering, but not runtime orchestration.

## Adjacent ACP Packages

`acp/` and `acpbridge/` are still part of the repository, but they are adjacent
integration packages rather than the primary local CLI path. They provide ACP
schema, transport, projection, and runtime bridge helpers around the current
SDK and gateway stack.
