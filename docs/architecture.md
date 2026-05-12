# Caelis Architecture

## Active Entry Flow

The local binary path is:

`cmd/caelis -> internal/cli -> app/gatewayapp -> kernel.Service`

The concrete local implementation lives in `internal/kernel` behind the public
`kernel` contract. Reusable contracts are in `ports/*`; concrete local
implementations are in `impl/*`; user and protocol adapters are in `surfaces/*`
and `protocol/acp`.

Surface routes adapt that shared app stack at the edge:

`surfaces/headless -> kernel.Service`

`surfaces/tui/gatewaydriver -> surfaces/tui/driver -> surfaces/tui/app`

`surfaces/acpserver -> protocol/acp/server`

- `cmd/caelis` is the only production binary entrypoint and only handles process
  startup.
- `internal/cli` parses one flat flag set and routes doctor, headless, ACP stdio,
  and interactive TUI modes.
- `app/gatewayapp` assembles the local stack: config store, prompt wiring, model
  registry, sandbox policy, tools, approval, session storage, and local runtime.
- `kernel/` names the public product contract for sessions, turns, replay,
  active runs, and control-plane state.
- `internal/kernel` owns the concrete local session/turn/control-plane
  implementation.

## Layering

### 1. `kernel/`

`kernel/` is the public product contract. External surfaces and future
extensions should depend on these values and service interfaces instead of
reaching into implementation packages.

It covers:

- session start/load/resume/fork/list/bind/replay
- turn begin/submit/interrupt/active-state operations
- control-plane handoff, participant attach/detach, and participant prompt
- stable public request, response, and event-envelope types

Today the public package re-exports many concrete types from
`internal/kernel`. That is legal Go, but it is migration debt: the public API
is stable at the import path, while the type definitions still live in a
private implementation package. New public kernel contract types should be
defined in `kernel/` first, with `internal/kernel` depending on that contract
shape or adapting to it behind the boundary.

### 2. `internal/kernel`

`internal/kernel` is the concrete local implementation of the public kernel
contract. It owns turn/session orchestration, active handles, canonical event
projection, approval routing, replay continuity, and control-plane coordination.

No surface package should import this package directly; surfaces use the public
`kernel` contract through `app/gatewayapp`.

### 3. `ports/`

`ports/*` contains public extension contracts. Port packages must not import
`app/*`, `impl/*`, `surfaces/*`, or `internal/*`.

Current ports include agent, approval, assembly, compact, config, controller,
delegation, model, policy, prompt, sandbox, session, skill, stream, subagent,
task, and tool. The dependency direction is the important boundary.

### 4. `impl/`

`impl/*` contains concrete implementations behind ports. It must not import
`app/*`, `surfaces/*`, or `internal/kernel`.

Current implementation packages cover:

- local and ACP-backed agents
- manual, deny, and model-backed approval strategies
- file-backed config storage
- session and task stores
- model providers
- static prompt assembly
- sandbox backends
- policy presets
- builtin tools
- in-memory stream service

### 5. `protocol/acp`

`protocol/acp` is the ACP protocol home. It exposes schema, JSON-RPC, client,
server, stdio transport, terminal, and projector packages.

Protocol schema packages should stay protocol-focused. Runtime adapters and app
composition belong in `impl/*` or `surfaces/*`.

### 6. `app/gatewayapp/`

`app/gatewayapp` is the local composition root. It is the production package
allowed to wire concrete implementations and the current kernel implementation.

It owns:

- `internal/configstore`: persisted app config and atomic writes
- `internal/modelregistry`: model/profile/default resolution
- `internal/promptassembly`: built-in prompt, AGENTS.md, and skill prompt assembly
- `internal/sandboxpolicy`: sandbox backend/root resolution
- `internal/agentregistry`: configured ACP agents and built-in agent metadata

Concrete tool, sandbox, runtime, and approval implementations are assembled
directly here. Extra `internal/*` packages should only exist when they own
real app policy, normalization, or persistence; a package that only wraps
constructors adds a false layer and should be removed.

Surface packages use the narrow `Stack` services and `Stack.Kernel()` instead
of reading these internals.

### 7. Surfaces

Surface packages translate interaction models into app/kernel calls. They should
not construct concrete model, sandbox, tool, or kernel implementation packages.

Current surface paths:

- `surfaces/headless`: one-shot execution for `-p` or piped stdin
- `surfaces/acpserver`: exposes the local stack as an ACP stdio agent
- `surfaces/tui/gatewaydriver`: bridge between gateway events/app services and
  the TUI driver contract
- `surfaces/tui/app`: Bubble Tea application state machine and slash-command UX
- `surfaces/tui/driver`: presentation-facing TUI driver contract
- `surfaces/tui/tuikit`, `surfaces/tui/acpprojector`, `surfaces/tui/tuidiff`:
  presentation helpers shared by the TUI app

### 8. `eval/`

`eval/` is the single home for build-tagged e2e and live evaluation tests. It
contains cross-stack checks that intentionally exercise public package seams
across kernel, app composition, ACP, CLI, local configured model, and the TUI
gateway driver.

Package-local integration tests that do not cross product boundaries should stay
next to their implementation and avoid the e2e naming/tag. Package-private live
diagnostics should not force new public APIs; either promote the behavior to a
black-box eval or cover it with ordinary package tests.

## Guardrails

Architecture tests enforce the main dependency rules:

- `ports/*` cannot import app, impl, internal, or surfaces packages.
- `impl/*` cannot import app, surfaces, or `internal/kernel`.
- `surfaces/*` cannot import `impl/*` or `internal/kernel`.
- `internal/kernel` cannot import app, impl, or surfaces.
- owned `protocol/acp` packages must stay independent of app and implementation
  packages, except for the shared display policy used by event projection.
- only app composition, CLI/bootstrap glue, and implementation packages may
  import `impl/*` in production code.

ACP event golden tests keep representative session/update and permission shapes
stable while the event source is tightened around ACP-native semantics.
