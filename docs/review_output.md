---

## 1. Executive Summary

1. **你真正应该冻结为 TUI / GUI / app-server / headless 的长期公共协议，不是 `ports/gateway.Event`，而是 `protocol/acp/eventstream.Envelope` 的一个显式 v1 版本。**
   当前 `eventstream.Envelope` 已经处在正确位置：它承载 `session/update`、`request_permission`、scope、cursor、turn、usage、lifecycle、participant 等面向客户端的事件事实，且 `surfaces/transcript` 已经主要从它投影 UI view model。`gateway.Event` 则重复了 session、ACP、transcript 三层字段，长期会成为语义漂移源。

2. **`ports/session.Event` 的设计方向基本正确：它是 durable canonical model semantics，而不是 ACP transcript cache。**
   `ports/session/event.go`、`semantic.go`、`event_validation.go` 和 `impl/session/file|memory` 已经明确把 `Event.Message` 作为模型可见消息，把 `Event.Tool` 作为工具执行状态，并拒绝 protocol-only 的 durable core event。这是项目里最强的架构资产之一。

3. **最大 P0 风险在外部 ACP agent / subagent 入口：`impl/agent/acp/subagent/runner.go` 仍会构造 `VisibilityCanonical` 但只有 `Protocol/Text`、没有 `Message/Tool/PlanPayload` 的事件。**
   具体是 `acpUpdateEvent` 这条路径。它和你的契约 #3、#4、#9 冲突：外部 ACP 输入进入存储前应规范化成 canonical session events，而不是把 ACP projection 当 durable event。这个问题如果不修，未来 replay、GUI、app-server、multi-agent history 都会出现不可解释的差异。

4. **`gateway.Event` 当前是有用的 adapter DTO，但不适合继续作为 public port 的稳定协议。**
   `ports/gateway/types.go` 里 `NarrativePayload`、`ToolCallPayload`、`ToolResultPayload`、`PlanPayload`、`ParticipantPayload`、`LifecyclePayload` 基本都在重复 `session.Event` 和 ACP update/eventstream 的语义。`protocol/acp/projector/gateway.go` 又标明它是 compatibility bridge。这是典型过渡层，v1 前应该降级或收敛。

5. **ACP schema 接近 ACP v1 生态，但不是严格贴合。**
   官方 ACP 是基于 JSON-RPC 2.0 的协议，包含 request/response 方法和 one-way notifications；初始化阶段协商 `protocolVersion` 与 capabilities，当前稳定 ACP protocol version 是 `1`。([Agent Client Protocol][1]) ([Agent Client Protocol][2]) ([GitHub][3])
   你的 `protocol/acp/schema` 已覆盖 `session/update`、content chunks、tool call/update、permission 等核心概念，但本地 `ContentChunk` 缺少官方 schema 里的 `_meta` 和 `messageId`，`request_permission` 也缺 `_meta`，usage 目前更多走 `caelis/usage` 而不是标准 `usage_update`。

6. **`_meta` 的原则写得对，但代码里还有几个污染点：工具名、terminal info、terminal output 有时从 meta 反推 canonical/display 语义。**
   例如 `internal/kernel/event_projection.go` 的 `canonicalToolName` 会优先读 `_meta.caelis.runtime.tool.name`，再读 `Event.Tool.Name`；`protocol/acp/projector/projector.go` 的 `terminal_info` / `terminal_output` 还是 top-level meta key，而不是统一放进 `_meta.caelis`。这会削弱“`_meta` 不是模型关键数据唯一副本”的契约。

7. **持久化 replay 对内置 Agent SDK agent 已经有相当强的测试资产，但对外部 ACP participant、approval、compaction、provider reasoning signature、participant/handoff round-trip 的覆盖还不够。**
   `eval/regression_*`、`impl/session/file/store_test.go`、`ports/session/session_test.go` 都在保护核心事件持久化，但没有完全覆盖 ACP ingress canonicalization 和多参与者 durable replay。

8. **`session.EventProtocol` 的 alias 设计是短期兼容可以，长期危险。**
   `EventProtocol.Participant`、`Handoff`、`ToolCall`、`Plan`、`Approval` 多数是 `json:"-"` alias。`CloneEventProtocol` 会尝试归一化，但反序列化后这些 alias 不一定可恢复。`ProjectPermissionRequest` 还直接读 `event.Protocol.Approval`，而不是 durable 字段 `Permission`。这类结构 v1 前应清理。

9. **`surfaces/transcript.Event` 应该是 UI view model，不是公共 wire protocol。**
   它很适合 TUI/GUI 共享“会话转录视图”，但它是从 ACP-native stream 投影出来的 UI 模型。把它当 protocol freeze 会把渲染状态、anchor、panel、approval UI 等细节带进 app-server/headless 边界。

10. **整体架构方向是对的，但现在处在“正确核心 + 多个过渡桥 + ACP ingress 未完全收敛”的阶段。**
    v1 前最重要的事不是继续加新功能，而是把事件协议收敛到三层：`session.Event` durable canonical、`eventstream.EnvelopeV1` client protocol、`transcript.Event` UI view model。第四套 `gateway.Event` 要么降级，要么明确只做内部 adapter。

---

## 2. Architecture Map：当前真实层次和事件流

当前真实事件流大致是这样：

```text
model/provider stream + tool runtime
  |
  | impl/agent/local/chat/*
  |   - response.go: final assistant / multi-tool assistant events
  |   - tool_event.go: tool result/status/provider metadata
  |   - history.go: rebuild model context from session events
  v
ports/session.Event
  - Event.Message: model-visible user/assistant/system/tool-result semantics
  - Event.Tool: durable tool execution state
  - Event.Protocol: ACP/client projection contract, not local replay source
  - Event.Meta["_meta"].caelis: display/runtime/provider hints
  - VisibilityCanonical / UIOnly / Mirror / Overlay / Notice
  |
  | impl/session/memory.Store / impl/session/file.Store
  |   - AppendEvent canonicalizes, validates, skips transient
  |   - file event log rejects legacy semantic sidecars
  v
durable session store
  |
  | internal/kernel
  |   - event_projection.go: session.Event -> gateway.EventEnvelope
  |   - gateway_replay.go: load session events -> replay projection
  |   - handle.go: publishSessionEvent / publishWithACPProjection / ACPEvents
  v
ports/gateway.EventEnvelope       <-- current in-process gateway DTO
  - gateway.EventKind
  - NarrativePayload
  - ToolCallPayload / ToolResultPayload
  - PlanPayload
  - ApprovalPayload
  - ParticipantPayload
  - LifecyclePayload
  - Protocol *session.EventProtocol
  |
  | protocol/acp/projector
  |   - ProjectEvent: session.Event -> ACP notifications
  |   - ProjectGatewayEventEnvelope: gateway bridge -> eventstream.Envelope
  v
protocol/acp/eventstream.Envelope <-- best long-term public client stream
  - Kind: session/update, session/request_permission, caelis/*
  - Update schema.Update
  - Permission *RequestPermissionRequest
  - Cursor / SessionID / HandleID / RunID / TurnID
  - Scope / Actor / ParticipantID / Final
  - Usage / Notice / Lifecycle / Participant / ApprovalReview
  - Meta
  |
  +--> surfaces/transcript.Event
  |      - ProjectACPEventToEvents
  |      - ProjectReplayEvents
  |      - UI narrative/tool/plan/approval/lifecycle/usage view model
  |
  +--> surfaces/tui/*
  |      - control_service_eventstream.go
  |      - render_dispatch.go
  |      - transcript rendering, approval UI, tool panels
  |
  +--> app/gatewayapp/controladapter/*
  |      - adapter_turn.go uses ACPEvents() when available
  |      - fallback projects gateway events
  |
  +--> ACP stdio/server/client boundary
  |
  +--> future GUI / app-server WebSocket/SSE
  |
  +--> surfaces/headless/headless.go
         - currently still consumes gateway.TurnHandle.Events()
         - this is not yet fully ACP-native
```

外部 ACP agent 入口目前有两条相关路径：

```text
external ACP controller / subagent
  |
  | impl/agent/acp/controller/updates.go
  |   - normalizeACPUpdateEvent
  |   - content chunks may get Message
  |   - tool/plan often UIOnly Protocol-only
  |
  | impl/agent/acp/subagent/runner.go
  |   - acpUpdateEvent
  |   - currently creates VisibilityCanonical with Protocol/Text only for some updates
  v
eventsource / kernel / gateway / eventstream
```

这里最关键的判断是：

`ports/session.Event` 应保留为 **内部 durable semantic model**。它是 runtime 和 store 的 source of truth，不应该为了 ACP schema 美观而被污染。

`protocol/acp/eventstream.Envelope` 应成为 **TUI/GUI/app-server/headless 的公共事件协议**。它已经天然是 ACP-native，并且足够接近 wire/event stream 边界。

`surfaces/transcript.Event` 应是 **共享 UI view model 或 TUI 私有 view model**，不是持久模型、不是 ACP wire schema。

`ports/gateway.Event` 应逐步变成 **内部 adapter DTO / 过渡桥**，不宜继续承担长期 public contract。

---

## 3. Contract Audit：10 条架构契约逐条审查

### 1. 一个 durable Agent SDK session 是运行时上下文的 source of truth

**结论：大体满足，但被 gateway DTO 与外部 ACP ingress 路径削弱。**

证据：

`README.md:64-70` 明确说 canonical session events 是 replay source，`session.Event.Message` 表示 model-visible messages，`session.Event.Tool` 表示 tool execution state。

`docs/agent-sdk-acp-architecture-plan.md:5-8` 也把 durable Agent SDK session model 定义为唯一持久上下文，ACP client-facing event protocol 由 Gateway 发出，但不是 durable replacement。

`internal/kernel/gateway_replay.go:10-54` 的 `ReplayEvents` 从 session store 读取事件后再投影到 gateway events，说明 replay 来源是 session store。

`impl/agent/local/chat/history.go` 的 `messagesFromContext` 从 session events 重建模型上下文，尤其 `messageFromInvocationEvent` 优先调用 `session.ModelMessageOf`。

缺口：

`ports/gateway.Event` 当前仍是 public port 类型，且字段足够完整，容易被未来 GUI/app-server 误认为 source of truth。

`impl/agent/acp/subagent/runner.go:497-614` 的 `acpUpdateEvent` 对部分 ACP updates 创建 canonical-looking event，但没有 canonical `Message` / `Tool`。这会让“durable session source of truth”在外部 ACP participant 场景下变成半真半假。

### 2. 持久层应该保存 canonical model semantics，而不是 UI transcript cache

**结论：内置 local agent 路径满足度高；外部 ACP/subagent 路径有 P0 缺口。**

证据：

`ports/session/event_validation.go:40-64` 的 `ValidateDurableCoreEvent` 要求 user/assistant/system 必须有 `Event.Message`，tool_call/tool_result 必须有 `Event.Tool` 或模型 tool-result message。

`impl/session/file/event_log.go:81-122` 读取 event log 时会反序列化 `session.Event` 并做 durable core validation。

`impl/session/file/event_log.go:124-141` 拒绝旧格式 `user_message`、`assistant_message`、`tool_call`、`tool_result` 等 legacy semantic sidecars。

`impl/session/memory/store.go:149-192` 和 `impl/session/file/store.go:209-274` 都在 AppendEvent 时 canonicalize、validate，并跳过 transient events。

测试证据也很强：`ports/session/session_test.go:582` 的 `TestValidateDurableCoreEventRejectsProtocolOnlyMessage`、`:602` 的 `TestValidateDurableCoreEventRejectsProtocolOnlyToolResult`、`impl/session/file/store_test.go:90` 的 `TestStoreRejectsProtocolOnlyCoreToolResult` 都在保护这个契约。

缺口：

`impl/agent/acp/subagent/runner.go:497-614` 仍可能把 ACP projection 形状构造成 `VisibilityCanonical` event。即使 store validation 最终拒绝，这也说明 ingest 层没有完成 canonical normalization。

### 3. `session.Event.Message` 是模型可见消息状态

**结论：核心代码强满足；ACP subagent 有冲突。**

证据：

`ports/session/semantic.go:20-28` 的 `ModelMessageOf` 只返回 `Event.Message`，并且注释明确说明不从 `Protocol`、`_meta`、`Text` 或 projection-only payload 推断模型消息。

`impl/agent/local/chat/response.go:145-157` 创建 final assistant response event 时写入 `Message`。

`impl/agent/local/runtime_events.go:15-33` 的 `buildUserEvent` 对 user event 写入 `Message`，同时可附带 ACP protocol projection。

冲突：

`impl/agent/acp/subagent/runner.go:497-614` 对 `agent_message_chunk`、`agent_thought_chunk` 一类 update 主要设置 `Text` 和 `Protocol`，没有设置 `Event.Message`，但 visibility 是 canonical。这和“Message 是模型可见状态”的约束冲突。

### 4. `session.Event.Tool` 是工具执行状态

**结论：local agent 满足；external ACP tool event 不满足。**

证据：

`ports/session/event.go:26-39` 对 `EventTool` 的注释明确写着它是 durable SDK tool-execution payload，ACP wire shapes 由 projector 派生。

`impl/agent/local/chat/tool_event.go:230-241` 的 `toolEventPayload` 会构造 durable `EventTool`，包括 ID、Name、Kind、Title、Status、Input、Output、Content。

`protocol/acp/projector/projector.go:365-433` 的 `toolCallForEvent` 优先从 `Event.Tool` 推导 ACP `ToolCall`。

冲突：

`impl/agent/acp/subagent/runner.go:497-614` 对 ACP `tool_call` / `tool_call_update` 只放 `Protocol.ToolCall` 或 `Protocol.Update`，没有 canonical `Event.Tool`。

`protocol/acp/projector/gateway.go:189-254` 的 gateway bridge 也主要从 gateway tool payload 重建 `EventProtocol`，不重建 `Event.Tool`。作为投影桥可以接受，但不能进入 durable canonical path。

### 5. `session.Event.Protocol.Update` 是 ACP/client projection contract，不应成为本地 Agent SDK replay source

**结论：核心满足，但 fallback 边界需要继续收紧。**

证据：

`ports/session/event.go:59-81` 明确注释：`Protocol` 是 client-facing ACP/control projection，不是 local model replay source。

`ports/session/event_canonicalize.go:50-96` 会在 canonical `Message` / `Tool` 存在时清除 redundant protocol projection，防止模型/tool 语义重复。

`ports/session/semantic.go:20-28` 明确拒绝从 `Protocol` 推导 model message。

缺口：

`ports/session/event.go:174-198` 的 `EventText` 会 fallback 到 `Protocol` content。作为 display helper 可以，但必须持续避免任何 replay 逻辑使用它作为模型语义。

`internal/kernel/event_projection.go:658-700` 的 assistant/reasoning projection 会在没有 `Message` 时从 protocol/meta fallback。这对 UI display 合理，但如果上游错误地产生 protocol-only canonical event，会掩盖问题。

### 6. Gateway 应该向 TUI、`caelis acp`、外部 ACP client 发出标准 ACP `session/update` 和 `request_permission`

**结论：部分满足。事件链路存在，但 schema 和 request handling 还不够严格。**

证据：

`protocol/acp/projector/projector.go:28-34` 定义 projector 把 canonical session events 转换为 ACP `session/update` notifications 与 `request_permission` payloads。

`protocol/acp/projector/projector.go:66-81` 的 `ProjectEvent` 会先处理 permission，再处理 explicit/inferred updates。

`internal/kernel/handle.go:409-426` 的 `publishWithACPProjection` 会把 gateway events 投影为 ACP eventstream。

`ports/gateway/types.go:488-499` 的 `ACPEventStreamHandle` 暴露 `ACPEvents() <-chan eventstream.Envelope`。

缺口：

`protocol/acp/projector/projector.go:102-130` 的 `ProjectPermissionRequest` 直接读取 `event.Protocol.Approval` alias，而不是 durable 字段 `Permission`。这使 request_permission 投影依赖内存 alias，持久 round-trip 后容易丢。

官方 ACP 的 request permission 是 client method，并包含 `_meta`、`options`、`sessionId`、`toolCall`；客户端取消 prompt turn 时必须返回 cancelled。([Agent Client Protocol][4]) 当前本地 `protocol/acp/schema/update.go:140-159` 的 `RequestPermissionRequest` 缺 `_meta`。

### 7. Caelis display hints 应进入 ACP `_meta`，但 `_meta` 不应成为模型关键数据唯一副本

**结论：原则满足，代码有几个污染点。**

证据：

`ports/session/protocol.go:61-76` 的 `ProtocolUpdate` 注释明确说 Caelis-specific data 应进入 `Event.Meta["_meta"].caelis`，不应该放在 protocol payload 本身。

`ports/gateway/event_meta.go:5-28` 定义了 Caelis `_meta` namespace 常量，并说明 renderer 可以消费 `_meta.caelis`，但 provider-visible tool JSON 不应依赖它。

风险：

`internal/kernel/event_projection.go:923-946` 的 `canonicalToolName` 会先读 `_meta.caelis.runtime.tool.name`，再读 `Event.Tool.Name`。这让 meta 有能力覆盖 canonical tool name。

`impl/agent/local/chat/history.go:364-377` 的 `toolNameFromEvent` 同样优先读 `_meta.caelis.runtime.tool.name`。

`protocol/acp/projector/projector.go:738-788`、`:1078-1133` 里 terminal output/info 仍使用 top-level `terminal_output` / `terminal_info` meta，而不是统一 `_meta.caelis.runtime.terminal`。

官方 ACP schema 明确把 `_meta` 作为 reserved metadata 字段，且实现不应对未知 keys 做假设；同时很多 ACP update/request 类型都有 `_meta`。([GitHub][5]) 所以 Caelis 扩展应该严格 namespaced，并且不能让 `_meta` 成为 canonical 字段的权威来源。

### 8. `VisibilityUIOnly` 是 transient live rendering；持久 final canonical events 必须包含完整模型可见状态

**结论：大体满足，但 `Overlay` 和 external ACP finalization 需要补测试与文档。**

证据：

`ports/session/event_visibility.go:10-16` 的 `IsTransient` 把 `VisibilityUIOnly`、`VisibilityOverlay` 和 `EventTypeNotice` 视为 transient。

`impl/session/memory/store.go:524-526` 的 `shouldPersistEvent` 返回 `!session.IsTransient(event)`。

`impl/session/file/helpers.go:149-162` 也跳过 transient persisted events。

缺口：

`ports/session/event_visibility.go:29-36` 的 `IsInvocationVisibleEvent` 排除了 UIOnly、Notice、Mirror，但没有排除 Overlay。也就是说 Overlay 是 transient，但可能参与 invocation-visible 过滤。这个语义不是不能成立，但必须被文档和测试固定，否则长期会混乱。

外部 ACP streaming/final 的边界还不清：controller path 的 chunks 多为 UIOnly 可以接受；subagent path 的 canonical protocol-only event 则不接受。

### 9. 外部 ACP agent 和内置 Agent SDK agent 在 Gateway 边界汇合；外部 ACP 输入进入存储前要规范化为 canonical session events

**结论：这是当前最明显未满足契约。**

证据：

`docs/agent-sdk-acp-architecture-plan.md:106-111` 已经写明 ACPIngress 应把 external ACP updates 规范化为 canonical session events，然后才进 durable history。

但代码里没有一个清晰独立的 `protocol/acp/ingress` 或 `impl/agent/acp/ingress` normalizer。实际逻辑分散在：

`impl/agent/acp/controller/updates.go:16-139` 的 `normalizeACPUpdateEvent`。

`impl/agent/acp/subagent/runner.go:497-614` 的 `acpUpdateEvent`。

尤其后者对 ACP content/tool/plan updates 没有 materialize `Message`、`Tool`、`PlanPayload`，却使用 canonical visibility。这就是文档与实现的实质缺口。

### 10. TUI/ACP clients 消费同一套 ACP-native event stream，可以装饰，但不应发明 built-in-only 协议

**结论：方向正确，但尚未完全收敛。**

证据：

`surfaces/transcript/acp_projector.go:40-111` 的 `ProjectACPEventToEvents` 已经以 `eventstream.Envelope` 为输入。

`surfaces/transcript/replay.go:10-31` 的 `ProjectReplayEvents` 也接受 `[]eventstream.Envelope`。

`app/gatewayapp/controladapter/adapter_turn.go:13-35` 会优先使用 `gateway.ACPEventStreamHandle.ACPEvents()`。

`surfaces/tui/app/control_service_eventstream.go` 与 `render_dispatch.go` 已经走 eventstream。

缺口：

`surfaces/headless/headless.go:33-70` 仍使用 `gateway.TurnHandle.Events()` 和 `gateway.EventEnvelope`，没有完全加入 ACP-native stream。

`surfaces/tui/app/gateway_event_test_helpers_test.go:10-27` 仍通过 `acpprojector.ProjectGatewayEventEnvelope` 把 gateway event 转 transcript event，说明测试和部分兼容路径还没有完全迁移。

---

## 4. Major Risks

### P0-1：外部 ACP subagent canonicalization 不完整

**证据**

`impl/agent/acp/subagent/runner.go:497-614` 的 `acpUpdateEvent` 创建 `session.Event`，设置了 `VisibilityCanonical`、ACP scope、controller kind、`Protocol.Update`，但对 agent/thought content 只设置 `Text + Protocol`，对 tool_call/tool_update 只设置 `Protocol.ToolCall/Update`，对 plan 只设置 `Protocol.Plan/Update`，没有 `Event.Message`、`Event.Tool` 或 `PlanPayload`。

`impl/agent/acp/subagent/runner_test.go:61` 的 `TestRunnerHandleUpdatePublishesStructuredToolAndPlanEvents` 目前主要验证 `Protocol.ToolCall`，没有验证 durable `Event.Tool`。

**为什么是风险**

这会使外部 ACP agent 的事件看起来是 canonical，但不能作为本地 Agent SDK replay source。最坏情况有两个：
一是 store validation 直接拒绝，导致外部 agent 历史无法持久化；二是某些路径没有写 store，只进入 UI，导致 replay/live 行为不一致。无论哪种，都违反你最核心的“ACP projection 不污染 durable model semantics”。

**建议修复**

引入一个明确的 `protocol/acp/ingress` 或 `impl/agent/acp/ingress` normalizer：

```text
ACP schema.Update
  -> live eventstream.Envelope for streaming/display
  -> final durable session.Event with Message / Tool / PlanPayload
```

它应该替代 `controller/updates.go` 与 `subagent/runner.go` 中重复的 ACP update -> session.Event 构造逻辑。规则要很硬：

content final chunk 如果进入 durable history，必须 materialize `Event.Message`。

tool_call / tool_call_update 如果进入 durable history，必须 materialize `Event.Tool`。

plan 如果是 durable semantic plan，必须 materialize `Event.PlanPayload`；如果只是 client projection，则只能是 UIOnly / eventstream-only。

### P0-2：公共事件协议尚未冻结，`gateway.Event` 与 `eventstream.Envelope` 双轨会导致长期漂移

**证据**

`ports/gateway/types.go:395-431` 的 `gateway.Event` 同时持有 `Protocol`、`Narrative`、`ToolCall`、`ToolResult`、`Plan`、`ApprovalPayload`、`Participant`、`Lifecycle`、`Usage`。

`protocol/acp/eventstream/event.go:41-68` 的 `eventstream.Envelope` 又持有 `Update`、`Permission`、`Notice`、`ApprovalReview`、`Participant`、`Lifecycle`、`Usage`、scope、cursor、turn 等字段。

`protocol/acp/projector/gateway.go:14-18` 注释已经承认 `ProjectGatewayEventEnvelope` 是 compatibility bridge while surfaces migrate from kernel.Event。

**为什么是风险**

未来 GUI/app-server 如果同时支持 gateway event 和 ACP eventstream，就会遇到字段重复、优先级不明、语义不等价的问题。比如 tool status 在 session/tool、gateway payload、ACP update、transcript tool state 里都有一份；一旦某个路径少字段或 fallback，测试很难覆盖全组合。

**建议修复**

v1 前明确：

`eventstream.EnvelopeV1` 是唯一 client-facing event protocol。

`gateway.Event` 标记为 internal adapter DTO 或 deprecated public Go compatibility type。

新 surface 禁止直接消费 `gateway.EventEnvelope`。

headless 迁移到 `ACPEvents()`。

---

### P1-1：`EventProtocol` alias 易丢失，permission/participant/handoff round-trip 不稳

**证据**

`ports/session/protocol.go:138-151` 中 `EventProtocol` 的 `UpdateType`、`ToolCall`、`Plan`、`Approval`、`Participant`、`Handoff` 多数是 `json:"-"` alias。

`ports/session/protocol.go:153-176` 的 marshal/unmarshal 只持久化 `Method`、`Update`、`Permission`。

`internal/kernel/event_projection.go:616-638` 的 `canonicalParticipantPayload` 只看 `event.Protocol.Participant`。如果 event 是从 JSONL 读回来的，alias 不一定存在。

`protocol/acp/projector/projector.go:102-130` 的 `ProjectPermissionRequest` 直接读 `event.Protocol.Approval`，不是 `event.Protocol.Permission`。

**为什么是风险**

alias 在内存里方便兼容，但持久化后可能消失。这样 participant/handoff/approval 的 live path 与 replay path 会不等价。

**建议修复**

把 participant/handoff 升级为 typed durable payload，例如 `EventParticipant`、`EventHandoff`，或至少让 `CloneEventProtocol` / unmarshal 能从 durable `Method + Update + Permission` 完整恢复所有 projection helper。

`ProjectPermissionRequest` 必须先 normalize `EventProtocol`，然后读 durable `Permission`。

---

### P1-2：ACP schema 与官方 ACP v1 仍有字段差距

**证据**

本地 `protocol/acp/schema/update.go:90-138` 定义 `ContentChunk`、`ToolCall`、`ToolCallUpdate`、`PlanUpdate`。其中 `ToolCall` / `ToolCallUpdate` 有 `Meta`，但 `ContentChunk` 没有 `_meta` 和 `messageId`。

官方 ACP 的 `user_message_chunk`、`agent_message_chunk`、`agent_thought_chunk` 包含 `_meta`、`content` 和可选 `messageId`。([Agent Client Protocol][4]) ([Agent Client Protocol][4])

本地 `RequestPermissionRequest` 缺 `_meta`，而官方 request permission schema 包含 `_meta`、`options`、`sessionId`、`toolCall`。([Agent Client Protocol][4])

本地 eventstream 有 `KindUsage = "caelis/usage"` 和 `UsageSnapshot`，但官方 ACP 有标准 `usage_update`，包含 `_meta`、`cost`、`size`、`used`。([Agent Client Protocol][4])

**为什么是风险**

这会让 Caelis 看起来 ACP-native，但对标准 ACP client 来说仍有细微不兼容。尤其 `messageId` 对 streaming chunk 合并、并发 assistant chunks、多 agent transcript 都很重要。

**建议修复**

为本地 schema 增加 ACP v1 官方字段，至少包括：

`ContentChunk.Meta`

`ContentChunk.MessageID`

`RequestPermissionRequest.Meta`

标准 `UsageUpdate`

并增加 official-schema golden/conformance tests。

---

### P1-3：`_meta` namespace 与 canonical 字段权威性还不够干净

**证据**

`internal/kernel/event_projection.go:923-946` 的 `canonicalToolName` 优先读 `_meta.caelis.runtime.tool.name`。

`impl/agent/local/chat/history.go:364-377` 的 `toolNameFromEvent` 也优先读 meta tool name。

`protocol/acp/projector/projector.go:738-788` 使用 top-level `terminal_output`，`:1120-1133` 使用 top-level `terminal_info`。

**为什么是风险**

你的契约说 `_meta` 是 display hints，不应该成为模型关键数据唯一副本。tool name、tool id、terminal output 是否属于模型关键数据要分层决定；但目前 meta-first 会让 display/runtime 反过来覆盖 canonical。

**建议修复**

Canonical 字段优先级固定为：

```text
Event.Tool.Name / Event.Message.ToolCalls[].Name
  > Protocol projection
  > _meta display hint
```

所有 Caelis 扩展统一放到：

```json
{
  "_meta": {
    "caelis": {
      "version": 1,
      "runtime": {
        "terminal": {},
        "display": {},
        "tool": {}
      }
    }
  }
}
```

top-level `terminal_info` / `terminal_output` v1 前迁移或只保留兼容读，不再写。

---

### P1-4：tool lifecycle 状态投影有损

**证据**

`impl/agent/local/chat/tool_event.go:119-135` 支持 running、waiting_input、waiting_approval、failed、interrupted、cancelled、terminated、completed 等丰富状态。

`protocol/acp/projector/projector.go:906-917` 的 `acpToolStatus` 会把 local started/running/waiting_approval 压成 `in_progress`，把 cancelled/interrupted/terminated/timed_out 压成 `failed`。

**为什么是风险**

标准 ACP tool status 更粗，这是合理的 wire compatibility；但如果 Caelis 自己的 TUI/GUI/app-server 也只看 ACP status，就会丢掉 approval、cancel、interrupt、timeout 的 UI/diagnostic 语义。

**建议修复**

标准 ACP 字段继续保持兼容；Caelis 细粒度状态放进 `_meta.caelis.runtime.tool.status_detail`，并且 canonical `Event.Tool.Status` 仍然是 durable 权威。

---

### P1-5：headless 仍未消费 ACP-native stream

**证据**

`surfaces/headless/headless.go:33-70` 当前仍从 `gateway.TurnHandle.Events()` 读取 `gateway.EventEnvelope`，并使用 `gateway.AssistantText`、Gateway approval payload。

**为什么是风险**

你的目标是 TUI、GUI、app-server、headless、ACP stdio/client 共享同一套 ACP-native event stream。headless 继续使用 gateway event，会让它成为另一套协议消费者，也会让测试覆盖偏向 gateway DTO。

**建议修复**

把 headless 改成优先消费 `ACPEvents()`。若 handle 暂不支持 ACP stream，可短期用 projector bridge，但 bridge 应只存在于 adapter 层，并在 v1 前移除或降级为 legacy fallback。

---

### P2-1：`surfaces/transcript.Event` 是良好的 UI view model，但命名和文档容易让人误用为协议

**证据**

`surfaces/transcript/event.go:47-92` 的 `Event` 包含 `Anchor`、`Narrative`、`Tool`、`Approval`、`State`、`Usage` 等 UI-friendly 字段。

`surfaces/transcript/acp_projector.go:40-111` 明确从 `eventstream.Envelope` 投影 transcript event。

**为什么是风险**

GUI/app-server 可能会想直接复用 transcript event 作为 wire schema。短期看方便，长期会把 UI 渲染 anchor、panel 状态、final folding 等细节冻进协议。

**建议修复**

文档里明确：

`eventstream.EnvelopeV1` 是 protocol。

`transcript.Event` 是 view model。

如果 GUI 也要复用 transcript，把包名或文档改成 `surfaces/uimodel/transcript`，但仍声明它不是 wire protocol。

---

### P2-2：`FinalAssistantAccumulator` 缺少 `messageId` 维度

**证据**

`protocol/acp/schema/final_accumulator.go:16-68` 目前主要按 content/tool/plan barrier 处理 agent message chunks。

**为什么是风险**

官方 ACP content chunks 支持 `messageId`。没有 messageId 时，多 assistant stream、participant/subagent interleave、future GUI concurrent rendering 都会更难处理。

**建议修复**

在 `ContentChunk` 增加 `MessageID` 后，accumulator 应按 `(sessionID, turnID, scope, participantID, messageID)` 分组。

---

### P2-3：`arch_lint` 主要约束 import 方向，不约束事件协议使用

**证据**

`scripts/arch_lint.go:113-178` 主要检查 `internal/kernel`、`ports`、`protocol`、`impl`、`surfaces`、`app/gatewayapp` 的 import 边界和测试例外。

**为什么是风险**

import 干净不代表协议边界干净。比如 surfaces 不一定 import forbidden package，但仍可能消费 `gateway.Event` 或依赖 `EventProtocol` alias。

**建议修复**

增加语义 lint：

禁止 `surfaces/*` 新代码直接引用 `gateway.EventEnvelope`，除 transitional helper 白名单。

禁止非 compatibility 包写入 `EventProtocol.ToolCall` / `Plan` / `Approval` alias。

禁止新代码写 top-level `terminal_info` / `terminal_output` meta。

---

## 5. Protocol Recommendation

本小姐的明确建议是：**把 `protocol/acp/eventstream.Envelope` 升级为 `EnvelopeV1`，作为 TUI / GUI / app-server / headless 的稳定公共 event protocol。**

### 为什么不是 `session.Event`

`session.Event` 是 durable canonical model。它要服务 Agent SDK replay、store round-trip、provider/tool runtime，不应该暴露给 GUI/app-server 作为 client 协议。它可以有 provider metadata、canonical model message、tool raw output、compaction、approval semantic 等内部字段，这些不应全部成为外部 contract。

### 为什么不是 `gateway.Event`

`gateway.Event` 当前承担了太多过渡职责：它既像 session projection，又像 transcript payload，又带 ACP protocol，还带 usage/approval/lifecycle。这种类型最容易变成“所有人都塞一点字段”的大泥球。它在 v0.x 可以作为 in-process adapter，但不应该是 v1 public stream。

### 为什么是 `eventstream.Envelope`

`protocol/acp/eventstream/event.go:41-68` 的结构已经最接近你想要的公共事件层：

它以 ACP `session/update` / `request_permission` 为核心。

它包含 cursor、session、handle、run、turn、scope、actor、participant、final 等跨 UI/app-server 必需字段。

它支持 Caelis 自定义事件：notice、participant、lifecycle、approval_review、usage、error。

它已经被 `surfaces/transcript` 和 TUI live path 消费。

官方 ACP 也是 JSON-RPC 2.0 风格：方法有 request/response，notifications 是 one-way；初始化时 client/agent 协商 protocolVersion 和 capabilities。([Agent Client Protocol][1]) ([Agent Client Protocol][2]) 这正好支持 Caelis 在 ACP 标准消息上叠加 capability-advertised Caelis extensions。

### 建议的 v1 schema 方向

建议把公共事件定义成类似：

```go
type EnvelopeV1 struct {
    Protocol          string `json:"protocol"`             // "caelis.eventstream"
    Version           int    `json:"version"`              // 1
    ACPProtocolVersion int   `json:"acp_protocol_version"` // 1 when ACP-compatible

    Kind       Kind   `json:"kind"`   // session/update, session/request_permission, usage_update, caelis/*
    Cursor     string `json:"cursor"` // stable resume cursor
    EventID    string `json:"event_id,omitempty"`
    ProjectionID string `json:"projection_id,omitempty"`

    SessionID string `json:"session_id"`
    HandleID  string `json:"handle_id,omitempty"`
    RunID     string `json:"run_id,omitempty"`
    TurnID    string `json:"turn_id,omitempty"`

    Scope         Scope  `json:"scope,omitempty"` // main, participant, subagent
    ScopeID       string `json:"scope_id,omitempty"`
    Actor         string `json:"actor,omitempty"`
    ParticipantID string `json:"participant_id,omitempty"`

    Final      bool      `json:"final,omitempty"`
    OccurredAt time.Time `json:"occurred_at"`

    Update     schema.Update                    `json:"update,omitempty"`
    Permission *schema.RequestPermissionRequest `json:"permission,omitempty"`

    Usage      *schema.UsageUpdate              `json:"usage,omitempty"`
    Lifecycle  *Lifecycle                       `json:"lifecycle,omitempty"`
    Participant *Participant                    `json:"participant,omitempty"`
    Notice     *Notice                          `json:"notice,omitempty"`
    Error      *Error                           `json:"error,omitempty"`

    Meta map[string]any `json:"_meta,omitempty"`
}
```

不一定要照抄这个字段名，但要锁定这些语义。

### cursor 规范

短期可以继续用 `session.Event.ID` 作为 durable replay cursor，但 live stream 应补一个 monotonic projection cursor，避免 UIOnly / pass-through event 没有 durable ID。

建议区分：

`event_id`：durable canonical event id，可能为空。

`cursor`：client resume cursor，必须单调、stream 内唯一。

`projection_id`：一次投影生成的 envelope id，可选，用于 dedup。

SSE 映射：

```text
id: <cursor>
event: <kind>
data: <EnvelopeV1 JSON>
```

WebSocket 映射：

```json
{
  "type": "event",
  "data": { "...EnvelopeV1..." }
}
```

ACP stdio/client 映射：

`KindSessionUpdate` -> JSON-RPC notification `session/update`。

`KindRequestPermission` -> JSON-RPC request `session/request_permission` 或对应 client method。

`usage_update` 优先使用 ACP standard update。

`caelis/*` 事件作为 extension notification，必须通过 initialize capabilities 或 `_meta.caelis` 宣告。

### streaming 与 final canonical

live streaming：

`agent_message_chunk final=false`

`agent_thought_chunk final=false`

`tool_call_update status=in_progress`

durable replay：

只输出能代表最终 canonical 状态的 final chunks / final tool update。

`surfaces/transcript/replay.go:42-67` 已经有“replay keeps only final assistant/thought chunks”的方向，这个规则应上升为 protocol doc。

### tool lifecycle

Canonical：

`session.Event.Tool.Status` 是完整生命周期。

ACP standard：

映射到 `pending`、`in_progress`、`completed`、`failed`。

Caelis detail：

`_meta.caelis.runtime.tool.status_detail = "waiting_approval" | "cancelled" | "interrupted" | ...`

这样兼容 ACP client，又不给 Caelis UI 丢细节。

### terminal output

不要再使用 top-level `terminal_info` / `terminal_output`。建议统一：

```json
{
  "_meta": {
    "caelis": {
      "version": 1,
      "runtime": {
        "terminal": {
          "id": "...",
          "stream": "stdout",
          "content_type": "text/plain",
          "truncated": false
        }
      }
    }
  }
}
```

模型关键 output 仍在 `Event.Tool.Output` / `Event.Tool.Content`，terminal meta 只是 rendering hint。

---

## 6. Test Plan：最值得补的 15 个测试

1. **ACP subagent content finalization -> canonical `Event.Message`**
   放在 `impl/agent/acp/subagent`。输入 `agent_message_chunk` final/update，断言进入 durable history 的 event 有 `Message`，不是 protocol-only。

2. **ACP subagent tool_call/tool_update -> canonical `Event.Tool`**
   放在 `impl/agent/acp/subagent`。断言 tool id/name/kind/title/status/input/output/content 都进入 `Event.Tool`，`Protocol.Update` 只是 projection。

3. **External ACP multi-tool turn store round-trip**
   放在 `eval` 或 `impl/session/file` regression。模拟外部 ACP assistant 发出多个 tool calls 与 results，重建 model message sequence 必须稳定。

4. **Permission request durable round-trip**
   放在 `ports/session` + `protocol/acp/projector`。构造只含 `EventProtocol.Permission` 的事件，经过 marshal/unmarshal 后 `ProjectPermissionRequest` 仍能成功，不依赖 `Approval` alias。

5. **Participant/handoff durable round-trip**
   放在 `impl/session/file` 或 `internal/kernel`。保存 participant/handoff event，重启读取后 `canonicalParticipantPayload` / handoff projection 仍完整。

6. **ACP schema conformance golden：content chunk `_meta` / `messageId`**
   放在 `protocol/acp/schema`。确认本地 `user_message_chunk`、`agent_message_chunk`、`agent_thought_chunk` 支持官方字段。

7. **ACP schema conformance golden：`request_permission._meta`**
   放在 `protocol/acp/schema` 和 `protocol/acp/projector`。确认 `_meta` 保留、clone、投影、JSON round-trip 不丢。

8. **Usage projection 使用标准 `usage_update`**
   放在 `protocol/acp/projector` 或 `internal/kernel`。确认 usage 能以 ACP standard update 输出，同时旧 `caelis/usage` 若保留则只作为兼容路径。

9. **Terminal metadata namespace 测试**
   放在 `protocol/acp/projector`。断言新事件只写 `_meta.caelis.runtime.terminal`，不再写 top-level `terminal_info` / `terminal_output`。

10. **Visibility matrix 持久化测试**
    放在 `ports/session` 和 `impl/session/file`。覆盖 `VisibilityUIOnly`、`Overlay`、`Mirror`、`Notice`：哪些 persist、哪些 replay transcript、哪些 model invocation visible，全部固定。

11. **Overlay invocation visibility 决策测试**
    放在 `ports/session`。如果 Overlay 不应参与 model context，就让 `IsInvocationVisibleEvent` 排除它；如果应该参与，测试名和文档必须说明原因。

12. **Replay/live eventstream equivalence**
    放在 `internal/kernel`。同一批 canonical session events，live `ACPEvents()` 与 `ReplayEvents -> eventstream` 的 final semantic envelopes 应一致。

13. **Reasoning/thinking signature round-trip**
    放在 `eval`。不仅验证 reasoning text，也验证 provider signature / metadata 是否在 canonical 位置保留，而不是只靠 `_meta` display fallback。

14. **Compaction semantic replay 测试**
    放在 `eval` 或 `impl/agent/local/chat/history`。验证 compaction 后 rebuilt model context 使用 compacted canonical semantics，而不是 transcript display text。

15. **Headless consumes ACP-native stream**
    放在 `surfaces/headless`。把 RunOnce 改为消费 `eventstream.Envelope` 后，测试 assistant text、tool output、approval request 都从 ACP-native stream 得到。

---

## 7. Documentation Plan

### README

`README.md` 已经写得很接近目标，但建议补三点：

第一，明确产品级稳定协议是 `protocol/acp/eventstream.EnvelopeV1`，而不是 `ports/gateway.Event`。

第二，把当前 gateway path 描述为 compatibility/in-process adapter。

第三，在 `README.md:220-223` 的 replay 描述旁补一句：replay emits ACP-native final envelopes derived from durable canonical session events。

### `docs/agent-sdk-acp-architecture-plan.md`

这份文档已经把目标边界讲清楚，但现在的问题是它描述了目标状态，而代码还没有完全达到。

建议拆成：

`Current Architecture`

`Target Architecture`

`Known Gaps Before v1`

其中 `Known Gaps` 必须点名：

external ACP subagent ingress 尚未完整 canonicalize。

`gateway.Event` 仍是 transitional public DTO。

ACP schema 与官方 v1 仍有字段差距。

headless 尚未完全 ACP-native。

### `ports/session` package docs

建议新增或加强 `doc.go`，固定：

`Event.Message` 是模型可见消息唯一来源。

`Event.Tool` 是工具执行状态唯一来源。

`Event.Protocol` 只可作为 ACP/client projection。

visibility matrix：canonical、mirror、ui_only、overlay、notice 分别是否 persist、是否 replay、是否 model-visible、是否 transcript-visible。

### `protocol/acp/eventstream` package docs

必须写清楚：

这是 Caelis public client event stream。

它是 ACP-native，但不是官方 ACP JSON-RPC 本体的简单复制。

哪些 `Kind` 是 ACP standard，哪些是 `caelis/*` extension。

cursor/resume/final/streaming/scope/participant/tool lifecycle/usage/lifecycle 的稳定语义。

SSE / WebSocket / stdio ACP 的映射。

### `surfaces/transcript` package docs

明确它是 UI view model：

输入是 `eventstream.Envelope`。

输出是 TUI/GUI 可共享的 transcript rendering model。

它不是 durable store schema。

它不是 app-server wire protocol。

### ACP compatibility table

建议在 docs 里增加一张表：

```text
ACP field/update             Caelis support       Notes
session/update               yes                  eventstream.KindSessionUpdate
user_message_chunk           partial              needs _meta/messageId
agent_message_chunk          partial              needs _meta/messageId
agent_thought_chunk          partial              needs _meta/messageId
tool_call                    yes                  Caelis extensions in _meta.caelis
tool_call_update             yes                  local status detail in _meta.caelis
request_permission           partial              needs _meta, alias cleanup
usage_update                 partial              currently caelis/usage
session/load/resume          planned/partial      replay semantics must align
```

官方 ACP `session/load` 要求 agent 恢复 session context/history，并通过 notifications 流式发送整个 conversation history；`session/resume` 则继续已有 session 而不重复 previous messages。([Agent Client Protocol][4]) ([Agent Client Protocol][4]) 这部分很适合直接映射 Caelis replay/resume 文档。

---

## 8. Roadmap

### 短期：v0.x 下一轮收敛

先修 P0，不要继续堆 surface。

1. 新增 ACP ingress normalizer，统一 `controller/updates.go` 与 `subagent/runner.go` 的 ACP update -> session event 逻辑。

2. 修复 `acpUpdateEvent`：任何 durable canonical assistant/tool/plan event 必须有 `Message` / `Tool` / `PlanPayload`。

3. 修复 permission alias：`ProjectPermissionRequest` 使用 durable `Protocol.Permission`，不依赖 `Approval` alias。

4. 给 `ContentChunk`、`RequestPermissionRequest` 增加 `_meta`，给 content chunks 增加 `messageId`。

5. headless 优先消费 `ACPEvents()`。

6. 把 top-level `terminal_info` / `terminal_output` 写入迁移到 `_meta.caelis.runtime.terminal`。

7. 在 README 和 architecture plan 里标注 `gateway.Event` 是 transitional。

### 中期：v0.x 后半段

1. 发布 `eventstream.EnvelopeV1` 文档和 golden schema。

2. app-server SSE/WebSocket 直接暴露 `EnvelopeV1`，不要暴露 `gateway.Event` 或 transcript event。

3. 把 usage 投影收敛到 ACP standard `usage_update`，`caelis/usage` 作为 legacy extension。

4. 把 `surfaces/transcript` 定位为 shared UI view model；如果 GUI 要复用，改包文档或包名，避免误认为协议。

5. 增强 `scripts/arch_lint.go`：禁止新 surface 依赖 gateway event，限制 `EventProtocol` alias 写入。

6. 为 external ACP participant replay、multi-tool、approval、compaction、reasoning signature 增加 regression tests。

### v1 freeze 前

1. 冻结三层事件模型：

```text
ports/session.Event              durable canonical semantics
protocol/acp/eventstream.EnvelopeV1 client event protocol
surfaces/transcript.Event         UI view model
```

2. `gateway.Event` 从 public stable contract 中移除，或标记为 internal/legacy adapter。

3. 移除或封存 `EventProtocol.ToolCall`、`Plan`、`Approval`、`Participant`、`Handoff` 这类 `json:"-"` transitional alias。

4. 锁定 cursor/resume 语义，保证 app-server/TUI/GUI/headless 都能从同一 cursor 机制恢复。

5. 锁定 `_meta.caelis.version = 1` namespace，所有扩展字段要有 backwards-compatible 规则。

6. 做一次 ACP v1 conformance audit，确保 protocolVersion/capabilities、session/update、request_permission、usage_update、load/resume 语义有清楚映射。

---

## 9. Open Questions

1. **外部 ACP agent 的最终 assistant/tool 输出是否应该进入父 Agent SDK 的模型上下文？**
   如果是，就必须 canonicalize 成 `Event.Message` / `Event.Tool`。如果不是，就不应该用 `VisibilityCanonical`，而应该是 `Mirror`、`UIOnly` 或 eventstream-only。

2. **`gateway.Event` 在 v1 是否仍是 public Go SDK API？**
   如果是，就要承认它是第四层稳定协议，并付出长期维护成本。我的建议是不要。

3. **Caelis 的 `caelis/*` events 要不要通过 ACP stdio 暴露给外部 client？**
   如果要，必须通过 capabilities 宣告 extension；如果不要，就只在 app-server/TUI eventstream 中暴露，ACP stdio 只发标准 ACP。

4. **reasoning/thinking signature 的 canonical 位置在哪里？**
   现在有 `Message`、provider metadata、`_meta.caelis.sdk` 等多个可能位置。v1 前需要决定哪些是模型 replay 必需，哪些只是 display/debug。

5. **`VisibilityOverlay` 是否应该参与 model invocation？**
   当前 `IsTransient` 认为 Overlay 不持久，但 `IsInvocationVisibleEvent` 没排除 Overlay。这个设计可以成立，但必须明确，否则很容易被误用。

6. **participant/handoff 是 durable semantic event，还是 control-plane projection？**
   如果是 durable，就需要 typed payload，而不能只靠 `EventProtocol.Participant` / `Handoff` alias。

7. **transcript 是否要成为 GUI 共享 view model？**
   如果要，建议正式命名为 shared UI model，并加稳定性声明；如果不要，就把它限定为 TUI rendering layer。

8. **cursor 的强保证是什么？**
   是只在单次 live stream 内唯一，还是跨重启、跨 replay、跨 app-server reconnect 都可恢复？这个会影响 `EnvelopeV1` 的字段设计。

---

最后给你一句硬判断：**Caelis 的核心方向是对的，尤其 `session.Event.Message` / `Event.Tool` 作为 durable semantics 这条线非常清楚；真正要警惕的是“为了方便 UI/ACP 投影而让 protocol-only event 混入 canonical path”。** 只要把 external ACP ingress 和 gateway/eventstream 双轨这两个点收住，后面 GUI、app-server、headless 复用就会顺很多。

[1]: https://agentclientprotocol.com/protocol/v1/overview "Overview - Agent Client Protocol"
[2]: https://agentclientprotocol.com/protocol/v1/initialization "Initialization - Agent Client Protocol"
[3]: https://github.com/agentclientprotocol/agent-client-protocol/blob/main/README.md "agent-client-protocol/README.md at main · agentclientprotocol/agent-client-protocol · GitHub"
[4]: https://agentclientprotocol.com/protocol/v1/schema "Schema - Agent Client Protocol"
[5]: https://github.com/agentclientprotocol/agent-client-protocol/blob/main/schema/v1/schema.json "agent-client-protocol/schema/v1/schema.json at main · agentclientprotocol/agent-client-protocol · GitHub"
