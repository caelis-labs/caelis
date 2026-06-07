# Architecture Boundaries

This document defines the target package structure for the Caelis rewrite. It
is not an implementation plan. It is the contract that all implementation
phases must respect.

See [adk-go-patterns.md](adk-go-patterns.md) for the Go conventions that
inform these decisions.

## Design Principles

1. **Interfaces live in domain packages.** No separate `ports/` directory.
   Each domain owns its interface, types, and default implementations.
2. **Specific implementations in sub-packages.** When an implementation brings
   its own dependencies or is large, it gets a sub-package.
3. **Config + New pattern.** Every constructor uses a `Config` struct.
4. **Internal for private glue.** `internal/` holds implementation details
   that must not leak across domain boundaries.
5. **Flat domain packages.** Sub-packages exist only for distinct
   specializations, not organizational subdivisions.
6. **Four-layer architecture.** Import direction flows strictly inward:
   Entry -> Presentation/Control -> Control -> Infrastructure. Equivalently,
   Infrastructure is never allowed to import Control or Presentation.

## Four-Layer Architecture

```text
┌──────────────────────────────────────────────────────────────┐
│  Layer 1: Entry                                              │
│  cmd/caelis                                                  │
│  进程入口、flag 解析、stdin/stdout 路由、模式选择               │
├──────────────────────────────────────────────────────────────┤
│  Layer 2: Presentation                                       │
│  tui/   headless/   acp/server/                              │
│  三种表现模式，共享同一套 gateway.Service                      │
├──────────────────────────────────────────────────────────────┤
│  Layer 3: Control                                            │
│  gateway/   gateway/kernel/   app/   app/commands/           │
│  控制平面：Turn 生命周期、审批路由、Session→Event 投影、        │
│  命令语义处理、Agent Runtime 组装与配置                        │
├──────────────────────────────────────────────────────────────┤
│  Layer 4: Infrastructure                                     │
│  ┌─ ACP Protocol ──────────────────────────────────────────┐ │
│  │  acp/  acp/client/  acp/projector/  acp/terminal/       │ │
│  │  ACP Schema、JSON-RPC、客户端传输、投影                    │ │
│  └─────────────────────────────────────────────────────────┘ │
│  ┌─ Agent Core ────────────────────────────────────────────┐ │
│  │  session/  model/  tool/  agent/  runner/                │ │
│  │  sandbox/  policy/  skill/  artifact/                    │ │
│  │  Agent 核心域：会话、模型、工具、Agent、Runner、            │ │
│  │  沙箱、策略、技能、制品                                    │ │
│  └─────────────────────────────────────────────────────────┘ │
└──────────────────────────────────────────────────────────────┘
```

**三个表现模式共享同一套 runtime**: TUI、headless CLI、ACP server 都通过
`gateway.Service` 访问 Agent Runtime，互不感知对方的存在。未来新增 Web
或 App 表现层时，只需实现一个消费 `gateway.Service` 的新 Presentation
package。

**Control layer 的职责**: 不直接参与模型调用或工具执行，而是：
- 管理 Turn 生命周期（begin、submit、cancel、one-active-turn 约束）
- 路由审批决策（auto-review / manual）
- 将 Session Event 投影为 Gateway Event
- 通过 `app/` 组装 Agent Runtime（注入 session、model、tool、sandbox、
  policy、skill 依赖）
- 通过 `app/commands/` 承载 slash/doctor/model/sandbox 等命令语义

Presentation layer owns command input shape, rendering, and transport. Control
layer owns command semantics. For example, `tui/commands/` parses slash input
and renders completion, while `app/commands/` decides what `/model`, `/doctor`,
or sandbox commands mean against `gateway.Service` and app configuration.

## Package Tree

```text
caelis/
│
│ ═══════ Layer 1: Entry ═══════
│
├── cmd/caelis/                 # 进程入口、flag 解析、模式选择
│
│ ═══════ Layer 2: Presentation ═══════
│
├── tui/                        # Bubble Tea 终端界面
│   ├── transcript/             # 会话记录渲染
│   ├── commands/               # 斜杠命令解析与派发
│   ├── input/                  # 输入与补全
│   ├── theme/                  # 主题与配色
│   ├── controladapter/         # Gateway Event → TUI 控制适配
│   └── tuikit/                 # 共享 TUI 原语
│
├── headless/                   # 一次性 CLI 输出（消费 gateway.Service）
│
├── acp/server/                 # ACP stdio 服务器（消费 gateway.Service）
│
│ ═══════ Layer 3: Control ═══════
│
├── gateway/                    # Surface-facing 服务契约
│   └── kernel/                 # Turn 注册表、审批路由、投影、参与者
│
├── app/                        # 组装根：组合所有域、注入依赖
│   └── commands/               # slash/doctor/model/sandbox 命令语义
│
│ ═══════ Layer 4: Infrastructure ═══════
│
│ ── ACP Protocol ──
│
├── acp/                        # ACP Schema、JSON-RPC 类型
│   ├── client/                 # ACP 客户端传输
│   ├── projector/              # Gateway Event → ACP Event 投影
│   └── terminal/               # ACP 终端处理
│
│ ── Agent Core ──
│
├── session/                    # Session, Event, State, Service
│   ├── inmemory.go             # InMemoryService()（测试用）
│   └── file/                   # File-backed Service
│
├── model/                      # LLM, Message, Part, Registry
│   ├── catalog/                # 模型目录与能力覆盖
│   └── providers/              # OpenAI, Anthropic, Gemini, DeepSeek, ...
│
├── tool/                       # Tool, Toolset, Call, Result, Registry
│   └── builtin/                # 内置工具
│       ├── filesystem/         # READ, WRITE, PATCH, LIST, GLOB, SEARCH
│       ├── shell/              # RUN_COMMAND
│       ├── task/               # TASK
│       ├── plan/               # PLAN
│       └── spawn/              # SPAWN
│
├── agent/                      # Agent 接口、Context、Callbacks
│   ├── llmagent/               # LLM-backed agent（模型/工具循环）
│   └── workflow/               # 延迟：LoopAgent, ParallelAgent, SequentialAgent
│
├── runner/                     # 一次 invocation 对一个 session 的完整执行
│
├── sandbox/                    # 命令/文件系统执行
│   ├── host/                   # Host（无沙箱）
│   ├── darwin/                 # macOS seatbelt
│   ├── linux/                  # Linux bubblewrap / Landlock
│   └── windows/                # Windows restricted-token
│
├── policy/                     # 工具授权
│   └── presets/                # workspace-write, read-only, ...
│
├── artifact/                   # 可选持久制品存储
│   └── fs/                     # 文件系统后端
│
├── skill/                      # 技能包加载与注册
│   └── embedded/               # 内置技能
│
├── internal/                   # 私有胶水（消费者不可导入）
│   ├── context/
│   ├── telemetry/
│   └── util/
│
└── npm/                        # npm 包装与平台二进制
```

## Dependency Rules

### Layer Rule

```text
Layer 1 ← Layer 2 ← Layer 3 ← Layer 4
```

Read the arrows as "is depended on by". In Go import terms, Entry may import
Presentation and Control, Presentation may import Control, and Control may
import Infrastructure. Infrastructure never imports outward. No cross-layer
sideways imports may bypass the layer boundary.

### Layer 1 → Layer 2/3

`cmd/caelis` imports `app/` (Layer 3) for composition, and directly imports
the selected Presentation package (`tui/`, `headless/`, or `acp/server/`) for
mode execution. `cmd/caelis` must not import Layer 4 domain packages directly.

### Layer 2 → Layer 3

All presentation surfaces import `gateway.Service` (Layer 3) for runtime
access:

| Package | May import | Must not import |
| --- | --- | --- |
| `tui/` | `gateway/`, `acp/` types, `model/` types if needed | `session/` stores, `runner/`, `sandbox/`, `policy/`, concrete providers |
| `headless/` | `gateway/`, `acp/` types | Layer 4 runtime internals |
| `acp/server/` | `acp/`, `gateway/`, stdlib | `runner/`, `gateway/kernel/`, `tui/` |

### Layer 3 Internal

| Package | May import | Must not import |
| --- | --- | --- |
| `gateway/` | `session/`, `model/`, stdlib | `runner/`, `tool/`, `sandbox/`, `policy/`, `acp/`, `tui/`, `app/` |
| `gateway/kernel/` | `gateway/`, `runner/`, `session/`, `agent/`, `model/`, `policy/` | `acp/`, `tui/`, `app/`, concrete model providers, concrete sandbox backends |
| `app/` | Layer 4 domain packages, `gateway/`, `gateway/kernel/` | `tui/`, `headless/`, `acp/server/`, package-private internals of domains |
| `app/commands/` | `app/`, `gateway/`, provider-neutral Layer 4 types | `tui/`, `headless/`, `acp/server/`, runtime internals |

### Layer 4: ACP Protocol

| Package | May import | Must not import |
| --- | --- | --- |
| `acp/` | stdlib, ACP protocol deps | `gateway/`, `runner/`, `tui/`, `app/` |
| `acp/client/` | `acp/`, stdlib | `gateway/`, `tui/`, `app/` |
| `acp/projector/` | `acp/`, `gateway/` | `runner/`, `tui/`, `app/` |
| `acp/terminal/` | `acp/`, stdlib | `gateway/`, `tui/`, `app/` |

### Layer 4: Agent Core

| Package | May import | Must not import |
| --- | --- | --- |
| `model/` | stdlib, provider-neutral helper deps | `session/`, `tool/`, `agent/`, `runner/`, `gateway/`, `acp/`, `tui/`, `app/` |
| `session/` | `model/`, stdlib | `tool/`, `agent/`, `runner/`, `gateway/`, `acp/`, `tui/`, `app/` |
| `sandbox/` | stdlib, platform libs in backends | `session/`, `model/`, `tool/`, `agent/`, `runner/`, `gateway/`, `acp/`, `tui/`, `app/` |
| `tool/` | `model/`, stdlib | `agent/`, `runner/`, `gateway/`, `acp/`, `tui/`, `app/` |
| `tool/builtin/*` | `tool/`, `model/`, `sandbox/`, narrow task/delegation interfaces | `session/`, `runner/`, `gateway/`, `acp/`, `tui/`, `app/` |
| `policy/` | `tool/`, `sandbox/`, stdlib | `agent/`, `runner/`, `gateway/`, `acp/`, `tui/`, `app/` |
| `artifact/` | stdlib | `session/`, `model/`, `tool/`, `agent/`, `runner/`, `gateway/`, `acp/`, `tui/`, `app/` |
| `agent/` | `model/`, `session/`, `tool/`, stdlib | `runner/`, `gateway/`, `acp/`, `tui/`, `app/` |
| `agent/llmagent/` | `agent/`, `model/`, `session/`, `tool/` | `runner/`, `gateway/`, `acp/`, `tui/`, `app/`, concrete sandbox backends |
| `runner/` | `agent/`, `model/`, `session/`, `tool/`, `sandbox/`, `policy/`, `skill/`, optional `artifact/` | `tool/builtin/*`, `gateway/`, `acp/`, `tui/`, `app/` |

### Cross-Package Within Layer 4

Layer 4 内部的跨包导入必须是单向的，不能形成环：

```text
model/ ← session/      (session imports model types)
model/ ← tool/          (tool imports model types)
model+session+tool ← agent/     (agent imports all three)
agent+session+model+tool+sandbox+policy+skill ← runner/
```

`session/` 和 `model/` 是叶子包，不导入其他 Layer 4 域包。

### Internal Packages

`internal/` 只被拥有它的公开包和更低层域导入。优先使用
`agent/internal/*`、`gateway/internal/*`、`tool/builtin/internal/*` 等域内
internal。消费者不得导入其他域的私有胶水。

## Domain Responsibility Map

### Layer 4: Agent Core

#### `session/`

Owns: session identity, state key-value store, event envelope, event types
(user, assistant, tool call, tool result, plan, compaction, lifecycle,
notice), event visibility rules, session store/service contract, model
context reconstruction.

Must not own: ACP wire formatting, UI display policy, provider-specific
migration code.

#### `model/`

Owns: provider-neutral LLM interface, message types (Message, Part, Content),
stream events, tool specs, output specs, retry behavior, provider registry,
model refs, aliases, capabilities, catalog.

Must not own: provider-specific HTTP/SSE/auth (those live in
`model/providers/`), session state, tool execution.

#### `tool/`

Owns: tool declaration, tool call, tool result, toolset, registry, observer,
errors, truncation, pure conversion helpers.

Must not own: tool execution logic (lives in `tool/builtin/`), approval
routing, sandbox constraints.

#### `agent/`

Owns: Agent interface, agent context hierarchy, callback types, agent
config, agent tree navigation.

Must not own: model invocation loop, tool execution, session persistence,
approval routing, ACP protocol.

#### `agent/llmagent/`

Owns: LLM-backed agent - model request construction, tool-call loop,
tool result sequencing, streaming response handling.

Must not own: session mechanics, approval UI, sandbox selection.

#### `runner/`

Owns: one invocation execution against a session: session loading, context
preparation, agent dispatch, tool resolution, policy/approval/tool wrappers,
compaction recovery, task/subagent execution, event persistence, and run state.

Must not own: surface subscriptions, active-turn conflict policy, ACP request
permission wire shape, TUI rendering, or CLI mode selection.

#### `sandbox/`

Owns: backend-neutral command execution, filesystem access, constraints,
descriptors, setup/status, async sessions, platform-specific enforcement.

Must not own: policy decisions, approval routing, UI diagnostics.

#### `policy/`

Owns: tool authorization input/output, mode options, policy profiles,
decisions (allow/deny/approval-request), metadata keys.

Must not own: approval UI, sandbox enforcement, tool execution.

#### `artifact/`

Owns: optional durable media/file artifact storage if the rewrite needs a
separate artifact domain for multimodal attachments. If session-local file
references are enough, this package should be deferred.

#### `skill/`

Owns: skill bundles, skill loading, skill registry, embedded skills.

### Layer 4: ACP Protocol

#### `acp/`

Owns: ACP schema, JSON-RPC types, client, projector, terminal subpackages.

Must not own: local runtime internals, TUI rendering.

### Layer 3: Control

#### `gateway/`

Owns: surface-facing service contract: session lifecycle, turn lifecycle,
active turn submission/cancellation, control-plane operations, gateway events,
approvals, usage, and event metadata. The root gateway package is protocol
neutral; ACP projection imports gateway, not the other way around.

#### `gateway/kernel/`

Owns: active turn registry, gateway request validation, approval routing,
binding state, session replay, session-to-gateway projection,
participant/control-plane orchestration.

Must not own: model invocation, tool implementation, TUI rendering.

#### `app/`

Owns: composition root, default wiring, app-level services (model connection,
ACP agent registration, sandbox status, doctor, agent profiles), and runtime
configuration for constructing `gateway.Service`.

Must not own: runtime loop internals, TUI transcript logic, session
persistence mechanics, or Presentation package construction.

#### `app/commands/`

Owns: command semantics shared by Presentation surfaces: doctor, model
selection, sandbox lifecycle, agent profiles, and slash command effects.

Must not own: TUI parsing, TUI completion rendering, ACP wire formatting, or
headless stdout formatting.

### Layer 2: Presentation

#### `tui/`

Owns: Bubble Tea state, input, transcript rendering, command dispatch,
completion, theme, layout, visual policy.

Must not own: session persistence, runtime policy, model/provider config
logic. All runtime access goes through `gateway.Service`.

`tui/commands/` owns slash input syntax, keybindings, completion display, and
dispatch to `app/commands/`. It does not own command semantics.

#### `headless/`

Owns: one-shot CLI output mode. Consumes `gateway.Service` for a single turn.
Returns structured or plain-text output to stdout.

Must not own: runtime internals, session persistence.

#### `acp/server/`

Owns: ACP stdio server transport. Consumes `gateway.Service` and projects
events via `acp/projector/`.

Must not own: runtime internals, TUI rendering, gateway logic.

### Layer 1: Entry

#### `cmd/caelis`

Owns: process entry, flag parsing, stdin/stdout routing, mode selection.

Must not own: domain logic, runtime internals, UI rendering.

## Interface Sketches

These sketches define package shape, not final implementation. They use Caelis
provider-neutral types only. Do not import adk-go concrete APIs such as
`genai.Content` or `genai.Schema`.

### `session/`

```go
type Ref struct {
    AppName      string
    UserID       string
    WorkspaceKey string
    SessionID    string
}

type Session struct {
    Ref         Ref
    Workspace   Workspace
    Title       string
    State       State
    Controller  ControllerBinding
    Participants []ParticipantBinding
    CreatedAt   time.Time
    UpdatedAt   time.Time
}

type Service interface {
    Create(context.Context, CreateRequest) (Session, error)
    Get(context.Context, Ref) (Session, error)
    List(context.Context, ListRequest) (ListResponse, error)
    Fork(context.Context, ForkRequest) (Session, error)
    Delete(context.Context, Ref) error
    AppendEvent(context.Context, Ref, Event) (Event, error)
    Events(context.Context, EventsRequest) ([]Event, error)
    UpdateState(context.Context, Ref, func(State) (State, error)) error
}
```

### `model/`

```go
type LLM interface {
    Name() string
    Generate(context.Context, Request) iter.Seq2[ResponseEvent, error]
}

type Registry interface {
    Resolve(context.Context, Ref) (LLM, ModelInfo, error)
    List(context.Context) ([]ModelInfo, error)
}
```

### `tool/`

```go
type Tool interface {
    Definition() Definition
    Run(Context, Call) (Result, error)
}

type Toolset interface {
    Name() string
    Tools(context.Context) ([]Tool, error)
}

type Registry interface {
    Lookup(context.Context, string) (Tool, bool, error)
    List(context.Context) ([]Tool, error)
}

type Definition struct {
    Name        string
    Description string
    Schema      Schema
    Metadata    map[string]any
}
```

### `agent/`

```go
type Agent interface {
    Name() string
    Description() string
    Run(InvocationContext) iter.Seq2[session.Event, error]
    SubAgents() []Agent
    FindAgent(name string) Agent
}

type InvocationContext interface {
    context.Context
    Agent() Agent
    Session() session.Session
    InvocationID() string
    Branch() string
    UserMessage() model.Message
    RunConfig() *RunConfig
    EndInvocation()
    Ended() bool
}

type ToolContext interface {
    tool.Context
    AgentName() string
    InvocationID() string
}
```

### `runner/`

```go
type Config struct {
    AppName        string
    Agent          agent.Agent
    Sessions       session.Service
    ModelRegistry  model.Registry
    ToolRegistry   tool.Registry
    Sandbox        sandbox.Factory
    Policy         policy.Engine
    Skills         skill.Registry
}

type Runner struct {
    // concrete fields are private
}

func New(Config) (*Runner, error)

func (r *Runner) Run(context.Context, RunRequest) iter.Seq2[session.Event, error]
```

If the optional `artifact/` domain is adopted, extend `runner.Config` with an
`artifact.Service` dependency at that phase. Do not import `artifact/` into
runner before the domain is needed by a concrete feature.

### `sandbox/`

```go
type Backend interface {
    Name() string
    Describe(context.Context) (Descriptor, error)
    Run(context.Context, CommandRequest) (CommandResult, error)
    FileSystem(context.Context, Constraints) (FileSystem, error)
    Status(context.Context) (Status, error)
    Close() error
}

type Factory interface {
    Create(context.Context, Config) (Backend, error)
    Available(context.Context) ([]Descriptor, error)
}
```

### `policy/`

```go
type Engine interface {
    Evaluate(context.Context, Request) (Decision, error)
}

type Profile interface {
    Name() string
    Evaluate(context.Context, Request) (Decision, error)
}
```

### `gateway/`

```go
type Service interface {
    CreateSession(context.Context, CreateSessionRequest) (session.Session, error)
    ListSessions(context.Context, ListSessionsRequest) (ListSessionsResponse, error)
    DeleteSession(context.Context, DeleteSessionRequest) error
    BeginTurn(context.Context, TurnRequest) (Turn, error)
    Submit(context.Context, SubmitRequest) error
    Cancel(context.Context, CancelRequest) error
    Replay(context.Context, ReplayRequest) (ReplayResponse, error)
    Subscribe(context.Context, SubscribeRequest) iter.Seq2[EventEnvelope, error]
}

type EventEnvelope struct {
    Kind      string
    SessionID string
    RunID     string
    TurnID    string
    Payload   any
    Metadata  map[string]any
}
```

## Execution Flow

```text
cmd/caelis
  → app.NewRuntime()                   # Layer 3: 组装 Gateway + Runtime
    → gateway.New(kernel.New(...))     # Layer 3: Gateway + Kernel
    → runner.New(...)                  # Layer 4: Runner 注入所有服务
  → 模式选择（Layer 1 职责）
    → tui.New(gateway).Run()           # Layer 2: 交互式
    → headless.New(gateway).Run()      # Layer 2: 一次性
    → acpserver.New(gateway).Serve()   # Layer 2: ACP stdio
```

**关键**: 三种表现模式共享同一套 `gateway.Service`，互不感知。`app/`
负责组装 runtime；`cmd/caelis` 负责选择表现层并传入同一个 gateway。

## What Changes vs Current Code

| Current | Target | Layer | Reason |
| --- | --- | --- | --- |
| `ports/session/` | `session/` | L4 | Interface + types in one place |
| `ports/model/` | `model/` | L4 | Interface + types in one place |
| `ports/tool/` | `tool/` | L4 | Interface + types in one place |
| `ports/agent/` | `agent/` | L4 | Interface + types in one place |
| `ports/gateway/` | `gateway/` | L3 | Control plane service contract |
| `ports/sandbox/` | `sandbox/` | L4 | Interface + types in one place |
| `ports/policy/` | `policy/` | L4 | Interface + types in one place |
| `impl/session/file/` | `session/file/` | L4 | Impl near interface |
| `impl/session/memory/` | `session.InMemoryService()` | L4 | Simple test impl near interface |
| `impl/model/providers/` | `model/providers/` | L4 | Impl near interface |
| `impl/tool/builtin/` | `tool/builtin/` | L4 | Impl near interface |
| `impl/agent/local/` | `runner/` + `agent/llmagent/` | L4 | Split orchestration from loop |
| `impl/agent/local/chat/` | `agent/llmagent/` | L4 | Agent loop in agent domain |
| `impl/sandbox/*` | `sandbox/*` | L4 | Impl near interface |
| `impl/policy/*` | `policy/presets/` | L4 | Impl near interface |
| `impl/agent/acp/` | `acp/` + runner/gateway participant support | L4+L3 | ACP is protocol; orchestration in control |
| `internal/kernel/` | `gateway/kernel/` | L3 | Gateway owns kernel |
| `surfaces/tui/app/` | `tui/` + sub-packages | L2 | Split by concern |
| `surfaces/headless/` | `headless/` | L2 | Presentation surface |
| `surfaces/acpserver/` | `acp/server/` | L2 | Presentation surface |
| `app/gatewayapp/controladapter/` | `tui/controladapter/` | L2 | TUI-specific adapter belongs to Presentation |
| `app/gatewayapp/` | `app/` + `app/commands/` | L3 | Split runtime composition from command semantics |
| `protocol/acp/` | `acp/` + sub-packages | L4 | ACP is a domain, not a protocol overlay |

## Package Size Targets

After the rewrite, no domain package should exceed 3000 production lines.
The largest package should be `tui/` (split into sub-packages) or
`model/providers/` (many providers). The core domain packages (`session/`,
`model/`, `tool/`, `agent/`) should each be under 1500 lines.

| Package family | Target |
| --- | --- |
| `session/` (all sub-packages) | < 3000 lines |
| `model/` (all sub-packages) | < 5000 lines |
| `tool/` (all sub-packages) | < 4000 lines |
| `agent/` (all sub-packages) | < 3000 lines |
| `runner/` | < 1000 lines |
| `sandbox/` (all sub-packages) | < 3000 lines |
| `policy/` | < 1000 lines |
| `gateway/` (all sub-packages) | < 3000 lines |
| `acp/` (all sub-packages) | < 3000 lines |
| `tui/` (all sub-packages) | < 8000 lines |
| `app/` | < 2000 lines |
