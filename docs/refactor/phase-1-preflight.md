# Phase 1 Preflight Checklist

This checklist is the gate between architecture design and any Go package
skeleton or implementation work. It exists to prevent the rewrite from
recreating the same ambiguity in a cleaner directory tree.

## Scope

Phase 1 is complete only when the architecture can answer these questions
without reading current implementation code:

- which package owns each product capability;
- which package defines each public interface;
- which package provides each built-in implementation;
- which dependencies are allowed and forbidden;
- which durable session events rebuild model context;
- which validation gate proves the next phase is safe to start.

Phase 1 does not implement behavior.

## Package Ownership Gate

Every target package must have one owner and one reason to exist.

| Package | Required decision |
| --- | --- |
| `session/` | Durable session identity, state, events, replay, and `InMemoryService()` |
| `session/file/` | File-backed durable store |
| `model/` | Provider-neutral model contract and message types |
| `model/providers/` | Concrete provider adapters |
| `tool/` | Tool declaration, call, result, schema, registry, runtime context |
| `tool/builtin/*` | Built-in tool declarations and execution bodies |
| `agent/` | Agent interface, invocation context, callbacks, delegation contracts |
| `agent/llmagent/` | Model/tool loop only |
| `agent/workflow/` | Deferred until a concrete workflow requirement exists |
| `runner/` | One invocation against one session |
| `sandbox/` | Backend-neutral command and filesystem enforcement contracts |
| `sandbox/*` | Platform-specific sandbox backends |
| `policy/` | Tool authorization contracts |
| `policy/presets/` | Built-in policy profiles |
| `artifact/` | Deferred unless separate durable artifact storage is needed |
| `skill/` | Skill bundle loading and registry |
| `skill/embedded/` | Built-in packaged skills |
| `gateway/` | Surface-facing service contract and event envelope |
| `gateway/kernel/` | Turn registry, approval routing, projection, participants |
| `acp/` | ACP protocol types |
| `acp/client/` | ACP client transport |
| `acp/server/` | ACP server over `gateway.Service` |
| `acp/projector/` | Gateway event to ACP event projection |
| `tui/*` | Terminal UI rendering, syntax commands, input, theme, primitives |
| `tui/controladapter/` | Gateway Event to TUI control adaptation |
| `app/` | Control/runtime composition |
| `app/commands/` | Shared command semantics |
| `cmd/caelis/` | Process entry, mode selection, Presentation startup |

If a package cannot be described this way, do not create it.

## Interface Gate

For each public interface, document:

- the owning domain package;
- the minimal methods required by direct consumers;
- the value types crossing the boundary;
- whether the interface is implemented by a built-in package, app wiring, or
  tests;
- which package is forbidden from importing it.

Do not introduce a public interface just to mirror an implementation. Interfaces
exist at stable seams: session store, model provider, tool execution, sandbox
backend, policy engine, agent, runner, gateway, and ACP projection.

## Built-In Tool Gate

Before adding any built-in tool implementation, confirm:

- the interface it implements lives in the owning domain package;
- the implementation sits beside that domain, not under a separate `impl/`
  tree;
- it does not import `runner/`, `gateway/`, `tui/`, `acp/`, or `app/`;
- it returns provider-neutral domain results;
- session persistence is handled by runner/session, not by the built-in;
- display projection is handled by gateway/ACP/TUI, not by the built-in.

Specific checks:

- `TASK` uses a narrow task controller contract in `tool/`;
- `SPAWN` uses a delegation contract in `agent/` or `tool/`;
- `PLAN` returns a domain result that runner maps to durable plan state;
- filesystem and shell built-ins receive sandbox-backed capabilities instead of
  choosing sandbox routes themselves.

## Runtime Gate

Before implementing `agent/llmagent/` or `runner/`, confirm:

- `agent/llmagent/` owns the model/tool loop, not session persistence;
- runner owns session loading, context preparation, tool wrapping, event
  persistence, compaction recovery, tasks, and subagents;
- gateway/kernel owns active turn conflict policy and approval routing;
- no surface package calls runner directly;
- headless, TUI, and ACP serving all enter through gateway.
- `app/` constructs `gateway.Service` but does not import Presentation packages;
- `cmd/caelis` passes the shared `gateway.Service` to exactly one selected
  Presentation package.

## Session Replay Gate

The session contract must define durable semantic payloads for:

- user messages;
- assistant text and reasoning;
- assistant tool-use anchors;
- tool results, errors, truncation, and replay metadata;
- compaction/system context;
- plans;
- approvals and lifecycle state;
- participant scope and visibility.

The model context builder must scan durable events in semantic session order.
It must not rebuild model input from ACP chunks, TUI transcript text, or
`ui_only` progress.

## Dependency Gate

Before creating a package skeleton, update architecture lint to enforce:

- root domains do not import runner, gateway, ACP, TUI, or app;
- `tool/builtin/*` does not import session or runner;
- runner does not import `tool/builtin/*`, gateway, ACP, TUI, or app;
- gateway root does not import runner, ACP, TUI, app, or concrete providers;
- ACP root does not import gateway; only `acp/server` and `acp/projector` may
  import `gateway/`;
- TUI imports gateway or ACP projection contracts, not session stores or
  runner internals;
- `app/` does not import `tui/`, `headless/`, or `acp/server/`;
- `cmd/caelis` does not import Layer 4 domain packages directly.

## Documentation Gate

Before creating or editing rewrite docs, confirm:

- the change updates an existing owner document when possible;
- every new `docs/refactor/*.md` file is listed in `README.md`;
- the file has one responsibility and does not duplicate another doc's table;
- implementation notes are kept in package docs or tests, not accumulated here.

## Non-Goals

Phase 1 must not:

- rewrite current runtime behavior;
- add compatibility fallbacks;
- create production constructors that only panic;
- implement file stores, providers, tools, sandbox backends, runner, gateway,
  ACP, TUI, or app behavior;
- preserve package paths only to reduce migration effort;
- copy provider-specific adk-go API shapes into Caelis contracts.

## Exit

Phase 1 can move forward when:

- this checklist has no unanswered item;
- [architecture-boundaries.md](architecture-boundaries.md),
  [builtin-layering.md](builtin-layering.md), and
  [session-runtime-contract.md](session-runtime-contract.md) agree;
- [validation-plan.md](validation-plan.md) contains the command gate for the
  next phase;
- old package paths appear only in current-state mapping or cleanup notes.
