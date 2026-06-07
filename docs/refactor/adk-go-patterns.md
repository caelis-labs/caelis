# adk-go Architecture Patterns

This document extracts structural patterns from Google's
[adk-go](https://github.com/google/adk-go) that are relevant to the Caelis
rewrite. It is not a code reference. It documents Go architecture conventions
that produce clean, readable, maintainable packages.

## 1. Interfaces Live in Domain Packages

There is no separate `ports/` directory. Each domain owns its interface
directly:

| Domain | Interface | Package |
| --- | --- | --- |
| Agent | `Agent` | `agent/agent.go` |
| Model | `LLM` | `model/llm.go` |
| Tool | `Tool`, `Toolset` | `tool/tool.go` |
| Session | `Session`, `Service` | `session/session.go`, `session/service.go` |
| Artifact | `Service` | `artifact/service.go` |
| Memory | `Service` | `memory/service.go` |
| Plugin | `Plugin` (struct) | `plugin/plugin.go` |

A developer reading `session.Service` navigates to `session/service.go`.
There is no `ports/session` vs `impl/session/file` split to mentally bridge.

## 2. Default Implementations Co-locate

Simple default implementations live next to their interfaces:

- `session.InMemoryService()` in `session/service.go`
- `artifact.InMemoryService()` in `artifact/inmemory.go`
- `memory.InMemoryService()` in `memory/inmemory.go`

No separate directory is needed for an in-memory implementation. The
constructor function (`InMemoryService()`) is the public entry point, and
the concrete type (`inMemoryService`) is unexported.

## 3. Specific Implementations in Sub-packages

When an implementation needs its own dependencies or is large, it gets a
sub-package:

```
session/
  service.go          # Service interface + InMemoryService()
  inmemory.go         # in-memory implementation
  session.go          # Session interface, Event, State
  database/           # database-backed Service implementation
  vertexai/           # VertexAI-backed Service implementation

artifact/
  service.go          # Service interface + InMemoryService()
  inmemory.go         # in-memory implementation
  gcsartifact/        # GCS-backed Service implementation
```

## 4. Config Struct + Constructor Pattern

Every domain that creates objects uses a `Config` struct and a `New()`
constructor:

```go
// agent/agent.go
type Config struct {
    Name        string
    Description string
    SubAgents   []Agent
    Run         func(InvocationContext) iter.Seq2[*session.Event, error]
}

func New(cfg Config) (Agent, error) {
    // validation, then return concrete unexported type
}
```

```go
// runner/runner.go
type Config struct {
    AppName        string
    Agent          agent.Agent
    SessionService session.Service
}

func New(cfg Config) (*Runner, error)
```

This pattern:
- Makes construction explicit and testable.
- Allows validation at construction time, not at first use.
- Keeps the exported API surface small (Config + New).

## 5. Context Hierarchy

Context types form a clear hierarchy of escalating access:

```go
// Read-only access to session state
type ReadonlyContext interface {
    context.Context
    UserContent() ProviderContent
    InvocationID() string
    AgentName() string
    ReadonlyState() session.ReadonlyState
}

// Mutable access for callbacks
type CallbackContext interface {
    ReadonlyContext
    Artifacts() Artifacts
    State() session.State
}

// Full access for tools
type ToolContext interface {
    CallbackContext
    FunctionCallID() string
    Actions() *session.EventActions
    SearchMemory(ctx context.Context, query string) (*memory.SearchResponse, error)
    ToolConfirmation() *toolconfirmation.ToolConfirmation
    RequestConfirmation(hint string, payload any) error
}

// Full access for agent invocation
type InvocationContext interface {
    context.Context
    Agent() Agent
    Artifacts() Artifacts
    Memory() Memory
    Session() session.Session
    InvocationID() string
    Branch() string
    UserContent() ProviderContent
    RunConfig() *RunConfig
    EndInvocation()
    Ended() bool
}
```

`ProviderContent` is a stand-in name in this document. Caelis should use its own
provider-neutral `model.Message` or equivalent type.

Key principles:
- Each level adds only the methods its consumers need.
- `ReadonlyContext` is used by `Toolset.Tools()` - tools that only list
  declarations don't need mutable state.
- `ToolContext` is used by tool `Run()` - tools that execute need full access.

## 6. Streaming via iter.Seq2

Go 1.23 iterators replace channels for streaming:

```go
// model/llm.go
type LLM interface {
    Name() string
    Generate(ctx context.Context, req *LLMRequest, stream bool) iter.Seq2[*LLMResponse, error]
}

// agent/agent.go
type Agent interface {
    Run(InvocationContext) iter.Seq2[*session.Event, error]
}
```

`iter.Seq2` is simpler than channels:
- No goroutine lifecycle management.
- No channel close semantics.
- Consumer controls pace via `yield` return value.
- Composable with `iter.Pull2` when needed.

## 7. Internal for Private Glue Only

`internal/` packages contain only implementation details that must not leak:

```
internal/
  agent/          # agent tree, parent map, state
  context/        # InvocationContext implementation
  llminternal/    # LLM callback types, model internals
  toolinternal/   # tool utilities
  sessionutils/   # session helpers
  memory/         # memory adapter
  artifact/       # artifact adapter
  telemetry/      # tracing helpers
  utils/          # shared utilities
  version/        # build version
```

These are imported only by the domain packages that own the corresponding
interface. They are never imported by consumers.

## 8. Flat Domain Packages

Domain packages are flat. `agent/` contains:
- `agent.go` - Agent interface + Config + New
- `context.go` - context hierarchy
- `run_config.go` - RunConfig
- `callback_context.go` - callback types
- `loader.go` - agent loading

Sub-packages exist only for distinct agent types:
- `agent/llmagent/` - LLM-backed agent
- `agent/workflowagents/loopagent/` - loop agent
- `agent/workflowagents/parallelagent/` - parallel agent
- `agent/workflowagents/sequentialagent/` - sequential agent
- `agent/remoteagent/` - remote agent

This keeps the core package readable. Sub-packages are specializations,
not organizational subdivisions.

## 9. Minimal Domain Dependencies

adk-go has many direct dependencies because it includes cloud backends,
servers, telemetry, and examples. The useful pattern is not the dependency
count; it is that domain packages import only what they need:

- `model` imports the provider SDK it is built around.
- `agent` imports `model`, `session`, `memory`, `artifact`, `tool`.
- `tool` imports `agent`, `model` (for context and types).
- `runner` imports `agent`, `session`, `model`, `artifact`, `memory`, `plugin`.
- `session` imports `model` (for LLMResponse in Event).

No circular dependencies. Domain interfaces stay in packages that can be
imported broadly without pulling in concrete backends, servers, or UI code.

Caelis should adopt the shape, not the exact import graph. In particular,
Caelis keeps tool runtime context in `tool/` instead of making `tool/` import
`agent/`, and it keeps `session/` payloads provider-neutral rather than storing
provider SDK response types directly.

## 10. Linting

- golangci-lint with `goimports` and `gofumpt`.
- Google Go Style Guide.
- `goheader` for license headers.
- `staticcheck` with sensible exclusions.

## Summary for Caelis

The key structural decisions to adopt:

1. **No `ports/` and `impl/` split.** Each domain package owns its interface
   and default implementations. Specific implementations live in sub-packages.
2. **Config + New pattern** for all constructors.
3. **Context hierarchy** with escalating access levels.
4. **iter.Seq2** for streaming (Caelis already uses this).
5. **Internal for private glue.** Domain packages may have `internal/`
   sub-packages for implementation details.
6. **Flat domain packages** with sub-packages only for distinct specializations.
7. **Dependency direction:** core domain -> runtime -> gateway -> surfaces.
8. **Do not copy provider-specific APIs.** adk-go can expose Google-specific
   provider types because it is Google ADK. Caelis must keep `model.Message`,
   `model.Part`, `tool.Schema`, and ACP types provider-neutral, with provider
   adapters below `model/providers/`.
