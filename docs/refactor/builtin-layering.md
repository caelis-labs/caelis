# Built-In Implementation Layering

This document defines how built-in implementations should fit into the rewrite.
The goal is to keep built-ins close to their domain interfaces without
recreating the current `ports/` plus `impl/` reading split.

## Rule

Built-ins are first-class domain implementations, not a separate architecture
layer.

The package that defines the public interface owns the nearby built-in
implementations:

```text
tool/
  tool.go                 # Tool, Toolset, Call, Result, Schema
  registry.go             # Registry interface + small memory registry
  builtin/
    builtin.go            # Build default toolset
    filesystem/
    shell/
    task/
    plan/
    spawn/

session/
  service.go              # Service interface + InMemoryService()
  file/                   # Durable file implementation

sandbox/
  sandbox.go              # Backend, Runtime, filesystem and command contracts
  host/
  darwin/                 # seatbelt
  linux/                  # bubblewrap + Landlock selection
  windows/                # restricted-token workspace-write

policy/
  policy.go               # Engine, Decision, Profile
  presets/

agent/
  agent.go                # Agent interface and context contracts
  llmagent/               # Built-in LLM-backed agent
  workflow/

skill/
  skill.go                # Bundle, loader, registry
  embedded/

acp/
  schema.go               # ACP protocol types
  client/
  projector/
  terminal/

acp/server/
  server.go               # Presentation adapter over gateway.Service
```

## Built-In Tool Flow

```text
model tool call
  -> agent/llmagent validates call shape
  -> runner resolves tool by name
  -> runner applies policy wrapper
  -> runner applies approval wrapper when required
  -> runner attaches progress observer and task context
  -> tool/builtin/* executes through supplied tool/sandbox/task dependencies
  -> runner persists session.ToolCallPayload + session.ToolResultPayload
  -> gateway/kernel projects to gateway.Event
  -> acp/projector and tui render from gateway events
```

Built-in tools must return provider-neutral `tool.Result` values. They must not
return ACP wire payloads or TUI display models directly.

## Tool Package Boundaries

### `tool/`

Owns:

- `Tool`, `Toolset`, `Registry`;
- `Call`, `Result`, `Schema`, `Location`, `Content`;
- result truncation helpers;
- error codes;
- memory registry for tests and composition.

Must not own:

- filesystem traversal;
- process execution;
- approval decisions;
- sandbox enforcement;
- gateway or ACP projection.

### `tool/builtin/`

Owns:

- `Config` for composing built-in dependencies;
- `DefaultToolset(cfg Config)` or equivalent constructor;
- reserved built-in names;
- grouping of built-in subpackages.

It may depend on `tool/`, `model/`, `sandbox/`, and small runtime-facing
interfaces needed by `TASK`, `PLAN`, and `SPAWN`. It must not depend on
`session/`, `runner/`, `gateway/`, `tui/`, `acp/`, or `app/`.

### `tool/builtin/filesystem/`

Owns `READ`, `WRITE`, `PATCH`, `LIST`, `GLOB`, and `SEARCH`.

Dependencies:

- `tool/` for call/result contracts;
- `sandbox.FileSystem` or a narrower filesystem interface;
- standard library parsing and diff helpers.

Rules:

- revision guards stay in filesystem built-ins;
- mutation diff metadata is domain metadata, not ACP content;
- ignore/exclude rules stay local to filesystem behavior;
- approval and writable-root decisions stay in policy/runner wrappers.

### `tool/builtin/shell/`

Owns `RUN_COMMAND`.

Dependencies:

- `tool/`;
- `sandbox.Backend` or command runtime interface.

Rules:

- shell tool prepares command requests and returns structured command results;
- sandbox route/backend selection is supplied by runner/policy wrappers;
- async yielding is coordinated with runner task services, not by the shell tool
  inventing a separate control plane.

### `tool/builtin/task/`

Owns the model-visible `TASK wait|write|cancel` declaration and argument
validation.

Execution belongs to a runner-owned task service. The narrow task controller
interface should live in `tool/` so runner can implement it without importing a
built-in package. `tool/builtin/task` must not import `runner/`.

### `tool/builtin/plan/`

Owns the `PLAN` declaration and validation of plan entries.

The model-visible result says the plan changed. Durable plan state is persisted
by the runner/session path as `session.PlanPayload`, not by TUI transcript
state.

### `tool/builtin/spawn/`

Owns the `SPAWN` declaration and agent-selection schema.

Actual delegation belongs to runner/subagent services because it creates child
sessions, streams, approvals, and visibility boundaries. The delegation
interface should live in `agent/` or `tool/`, then be implemented by runner.
`SPAWN` must not call ACP clients directly from the tool package and must not
import `runner/`.

## Built-In Agent Flow

`agent/llmagent/` is the built-in model-backed agent. It owns only the
model/tool loop:

- build model requests from `agent.InvocationContext`;
- stream model responses;
- canonicalize tool calls;
- execute resolved tools through the context-provided executor;
- emit semantic session events.

It must not know about:

- CLI flags;
- TUI state;
- ACP JSON-RPC;
- file-backed session paths;
- sandbox backend selection.

Workflow agents under `agent/workflow/` are deferred built-in agent
specializations. Add them only when a current Caelis workflow needs loop,
parallel, or sequential composition. They compose child agents but must not own
session persistence.

## Built-In ACP Implementations

ACP is a protocol domain, not an implementation dumping ground.

- `acp/schema` or root `acp` owns protocol types.
- `acp/client` owns client transport.
- `acp/projector` owns gateway-event to ACP-event projection.
- `acp/terminal` owns terminal payload helpers.
- `acp/server` is Presentation: it owns stdio serving over `gateway.Service`,
  not protocol semantics or runtime orchestration.
- ACP-backed agents should live where they are consumed:
  - `agent/remote/` if presented as an `agent.Agent`;
  - `runner/participants/` or `gateway/kernel` support if used as controller or
    participant orchestration.

External ACP input must normalize into `session.Event` before storage.

## Built-In Sandbox Implementations

Sandbox backends live under `sandbox/` because they implement `sandbox`
contracts:

- `sandbox/host`
- `sandbox/darwin` or `sandbox/seatbelt`
- `sandbox/linux`
- `sandbox/windows`

Platform package names should be lowercase and Go-friendly. Avoid `macOS/` in
import paths.

Backends enforce constraints. They do not decide whether a tool call is allowed.
Policy produces the decision; sandbox executes the already-approved
constraints.

## Built-In Policy Implementations

Policy presets live under `policy/presets/`.

The root `policy/` package owns:

- `Engine`;
- `Decision`;
- `Action`;
- `Profile`;
- `ModeOptions`;
- metadata keys for policy profile and policy-supplied roots.

`policy/presets/` owns:

- `workspace-write`;
- `read-only`;
- sensitive path rules;
- default command and filesystem tool policy.

Approval mode is not policy profile. New code must keep approval routing,
policy profile, and sandbox constraints as separate typed concepts.

## Built-In Skill Implementations

`skill/` owns bundle discovery and registry contracts. `skill/embedded/` owns
system skills packaged with Caelis.

Skill discovery may read the filesystem through app/runtime configuration, but
skill packages must not depend on TUI rendering or gateway active-turn state.

## Dependencies Matrix

| Built-in package | May import | Must not import |
| --- | --- | --- |
| `tool/builtin` | `tool`, `model`, `sandbox`, narrow task/delegation interfaces defined below `tool` or `agent` | `session`, `runner`, `gateway`, `tui`, `acp`, `app` |
| `agent/llmagent` | `agent`, `model`, `tool`, `session` | `gateway`, `tui`, `acp`, `app`, sandbox backends |
| `sandbox/*` | `sandbox`, standard library, platform libraries | `tool`, `agent`, `runner`, `gateway`, `tui` |
| `policy/presets` | `policy`, `tool`, `sandbox` | `runner`, `gateway`, `tui`, `app` |
| `session/file` | `session`, standard library | `runner`, `gateway`, `tui`, `app` |
| `model/providers` | `model`, provider SDKs/HTTP helpers | `session`, `tool`, `runner`, `gateway`, `tui` |
| `acp/projector` | `acp`, `gateway` | `session`, `runner`, `gateway/kernel`, `tui`, `app` |

## Phase 1 Deliverable

Phase 1 must decide and document the built-in package ownership above before
implementation begins. It should not implement tool behavior. The output is:

- final package tree;
- dependency matrix;
- interface and constructor shape;
- built-in registry/composition design;
- list of built-ins and their required dependencies;
- tests each built-in module must eventually satisfy.
