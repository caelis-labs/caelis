# Current State

This document records the features that must survive the rewrite and the
architecture problems that motivate it. Written from the `rewrite` baseline
at commit `bb0c960fcc6f82e281d468f9aca0d10c8d186a52`.

## Product Shape

Caelis is a terminal-first agent runtime. The stable product boundary is a
workspace-scoped session plus a gateway semantic event stream that can be
projected to ACP-native updates. The local binary can act as an interactive
TUI, a headless one-shot CLI, or an ACP stdio agent.

The primary execution path in the current code:

```
cmd/caelis -> internal/cli -> app/gatewayapp -> ports/gateway.Service
  -> internal/kernel -> ports/agent.Runtime -> impl/agent/local
  -> impl/agent/local/chat -> ports/model.LLM + ports/tool.Tool + ports/session.Service
```

In the rewrite, this becomes a 4-layer architecture:

```
Layer 1: cmd/caelis                         # 入口和模式选择
  ├── app.NewRuntime()                       # L3: 组装 gateway/runtime
  └── Layer 2: tui/ | headless/ | acp/server/ # 表现（共享 gateway）
        └── Layer 3: gateway.Service           # 控制平面
              └── gateway/kernel/              # Turn 注册表、审批路由
                    └── Layer 4: runner.Run()  # Agent Runtime
                          └── agent/llmagent.Run()
                                └── model.LLM + tool.Tool + session.Service
```

## Delivered Features

These features must be preserved through the rewrite. Each is listed with its
current package location and the target domain package.

### CLI And Modes

| Feature | Current Location | Target Domain |
| --- | --- | --- |
| Interactive TUI (TTY without prompt) | `surfaces/tui/app` | `tui/` |
| Headless one-shot (`-p` or piped stdin) | `surfaces/headless` | `headless/` over `gateway/` |
| ACP stdio serving (`caelis acp`) | `surfaces/acpserver` | `acp/server/` |
| Diagnostics (`caelis doctor`) | `app/gatewayapp` | `app/` |
| Sandbox lifecycle (`caelis sandbox setup\|fix\|reset\|clean`) | `app/gatewayapp` | `sandbox/` + `app/` |
| Flat flags for identity, model, approval, sandbox, etc. | `internal/cli` | `cmd/caelis` |

### Session And Persistence

| Feature | Current Location | Target Domain |
| --- | --- | --- |
| File-backed sessions (`~/.caelis/sessions`) | `impl/session/file` | `session/file/` |
| In-memory test storage | `impl/session/memory` | `session.InMemoryService()` |
| Session identity (app, user, workspace, session id) | `ports/session` | `session/` |
| Session list, load, resume, fork, binding | `ports/session` | `session/` |
| Canonical event types (user, assistant, tool, plan, ...) | `ports/session` | `session/` |
| Semantic v2 event payloads | `ports/session` | `session/` |
| Per-session state (model, reasoning, approval, usage) | `ports/session` | `session/` |
| Compaction state and usage accounting | `ports/session` + `impl/agent/local` | `session/` + `runner/` |

### Agent Runtime

| Feature | Current Location | Target Domain |
| --- | --- | --- |
| Local model-backed chat (streaming + non-streaming) | `impl/agent/local/chat` | `agent/llmagent/` |
| Provider-neutral message parts | `ports/model` | `model/` |
| Model-visible context from durable events | `ports/session` | `session/` |
| Invalid tool-call repair loop | `impl/agent/local/chat` | `agent/llmagent/` |
| Concurrent multi-tool execution | `impl/agent/local/chat` | `agent/llmagent/` |
| Overflow recovery through compaction | `impl/agent/local` | `runner/` |
| Async task and terminal stream support | `impl/agent/local` | `runner/` |
| Controller handoff to ACP controllers | `impl/agent/local` | `gateway/kernel/` + `runner/` |
| Sidecar and delegated participant flows | `impl/agent/local` | `gateway/kernel/` + `runner/` + `acp/` |

### Built-In Tools

| Feature | Current Location | Target Domain |
| --- | --- | --- |
| READ, LIST, GLOB, SEARCH | `impl/tool/builtin/filesystem` | `tool/builtin/filesystem/` |
| WRITE, PATCH (with revision guards) | `impl/tool/builtin/filesystem` | `tool/builtin/filesystem/` |
| RUN_COMMAND (sandbox-routed) | `impl/tool/builtin/shell` | `tool/builtin/shell/` |
| TASK wait\|write\|cancel | `impl/tool/builtin/task` | `tool/builtin/task/` |
| PLAN | `impl/tool/builtin/plan` | `tool/builtin/plan/` |
| SPAWN (delegation) | `impl/tool/builtin/spawn` | `tool/builtin/spawn/` |

### Models And Providers

| Feature | Current Location | Target Domain |
| --- | --- | --- |
| Provider-neutral contracts | `ports/model` | `model/` |
| OpenAI, Anthropic, Gemini, DeepSeek, MiniMax, Volcengine, ... | `impl/model/providers` | `model/providers/` |
| Model catalog and capability overlay | `impl/model/catalog` | `model/catalog/` |
| Model connection wizard | `app/gatewayapp` | `app/` |
| Per-session model switching | `app/gatewayapp` | `app/` |

### Policies, Approvals, And Sandbox

| Feature | Current Location | Target Domain |
| --- | --- | --- |
| Approval modes (auto-review, manual) | `ports/policy` | `policy/` |
| Policy profiles (workspace-write) | `impl/policy/presets` | `policy/presets/` |
| Policy decisions (allow/deny/approval) | `ports/policy` | `policy/` |
| Sandbox backends (host, macOS, Linux, Windows) | `impl/sandbox/*` | `sandbox/*/` |
| Windows workspace-write ACL | `impl/sandbox/windows` | `sandbox/windows/` |

### ACP And External Agents

| Feature | Current Location | Target Domain |
| --- | --- | --- |
| ACP server, client, JSON-RPC, stdio | `protocol/acp` | `acp/` |
| ACP event projection | `protocol/acp/projector` | `acp/projector/` |
| External ACP agent registration | `impl/agent/acp` | `app/` + `acp/` + `gateway/kernel/` |
| Subagent profile bindings | `app/gatewayapp` | `app/` |

### TUI

| Feature | Current Location | Target Domain |
| --- | --- | --- |
| Core slash commands | `surfaces/tui/app` | `tui/commands/` syntax + `app/commands/` semantics |
| Transcript rendering | `surfaces/tui/app` | `tui/transcript/` |
| Completion | `surfaces/tui/app` | `tui/input/` |
| Theme adaptation | `surfaces/tui/app` | `tui/theme/` |
| ACP child commands | `surfaces/tui/app` | `tui/commands/` syntax + `app/commands/` semantics |

### Packaging And Release

| Feature | Current Location | Target Domain |
| --- | --- | --- |
| Go binary (`./cmd/caelis`) | `cmd/caelis` | `cmd/caelis` |
| npm wrapper + platform packages | `npm/` | `npm/` |
| GoReleaser dry-run | `.goreleaser.yml` | `.goreleaser.yml` |
| Make targets (quality, test, build, lint) | `Makefile` | `Makefile` |

## Code Size Snapshot

At baseline (commit `bb0c960`):

- Production Go files: 444
- Test Go files: 202
- Production Go lines: 119,682
- Test Go lines: 73,882
- Direct Go dependencies: 14

### Largest Production Packages

| Package | Lines | Problem |
| --- | ---: | --- |
| `surfaces/tui/app` | 33,333 | Monolith - mixes ingest, reduction, rendering, dispatch, state |
| `impl/agent/local` | 8,463 | Orchestration cluster - too many concerns |
| `impl/model/providers` | 6,887 | Many providers, acceptable if split |
| `app/gatewayapp` | 5,742 | Service accumulation |
| `app/gatewayapp/controladapter` | 4,810 | Adapter bloat |
| `internal/kernel` | 4,756 | Mixed gateway concerns |
| `surfaces/tui/tuikit` | 3,360 | TUI primitives |
| `ports/session` | 3,132 | Event types + legacy migration + projection |
| `impl/sandbox/windows` | 2,812 | Platform-specific, acceptable |
| `impl/tool/builtin/filesystem` | 2,572 | Multiple tools, acceptable |

### Largest Production Files

| File | Lines |
| --- | ---: |
| `impl/sandbox/windows/runtime_windows.go` | 2,174 |
| `surfaces/tui/app/model_input.go` | 1,474 |
| `app/gatewayapp/controladapter/adapter.go` | 1,381 |
| `impl/agent/acp/controller/manager.go` | 1,357 |
| `surfaces/tui/app/tool_display.go` | 1,323 |
| `surfaces/tui/app/view_render.go` | 1,264 |

## What The Rewrite Must Fix

1. **No `ports/impl` split.** Interfaces and implementations co-locate in
   domain packages. Reading `session.Service` should lead directly to its
   implementations, not to a different directory tree.

2. **TUI decomposition.** `surfaces/tui/app` (33K lines) must be split into
   `tui/transcript/`, `tui/commands/`, `tui/input/`, `tui/theme/`, and
   shared primitives.

3. **Runtime loop clarity.** The current `impl/agent/local` owns too much.
   The rewrite separates `runner/` (session orchestration) from
   `agent/llmagent/` (model loop).

4. **Gateway simplification.** `internal/kernel` mixes turn registry,
   projection, and approvals. The rewrite moves this to `gateway/kernel/`.

5. **Event type reduction.** The current `ports/session` mixes durable events,
   legacy migration, and projection helpers. The rewrite should have a
   focused event type with clear construction helpers.

6. **Dependency direction.** The current code has circular dependencies
   mediated by `internal/`. The rewrite enforces a strict DAG.

7. **Provider-specific API leakage.** Architecture docs and interfaces must
   avoid importing adk-go concrete types such as `genai.Content`. Caelis keeps
   provider-neutral `model.Message`, `model.Part`, and tool schema types at the
   domain boundary.
