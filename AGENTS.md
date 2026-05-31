# AGENTS.md

## 协作规则

- 主 agent 负责架构判断、拆分、集成、验证和最终决策。
- 子 agent 只用于边界清晰的旁路任务；主 agent 必须整合结论。
- 优先使用 `rg` / `rg --files`；保留无关用户改动。
- 手工编辑用 `apply_patch`，避免无意义 import alias 和顺手重构。

## 产品定位

Caelis 是 ACP-native 的 agent runtime 与 gateway。核心只保留会话事实、
runtime 编排、ACP 出入站、审批/策略、上下文重建和可替换契约；模型、
sandbox、store、tool、prompt、skill、外部 ACP agent 都应通过 adapter、
registry 或 plugin 装配。

TUI、未来 APP、headless CLI、`caelis acp` 是平级 surface。它们共享
`internal/app/services`、`internal/app/viewmodel`、canonical session events
和 ACP 投影，但不共享 UI 实现，也不能各自发明产品语义。

## 架构分层

- `core/*`：稳定公共契约，包括 runtime、session、model、tool、sandbox、
  plugin、config；不得依赖 `internal/*` 或 surface。
- `core/session.Event`：持久事实源，保存模型可见消息、工具调用/结果、
  lifecycle、approval、provider metadata、replay/compaction 语义。
- `internal/engine/*`：本地 session/turn 编排、上下文重建、审批、任务、
  participant/controller 生命周期；不得引入 UI 语义。
- `internal/app/services`：所有 surface 共用的产品 API。
- `internal/app/viewmodel`：surface-neutral payload，不放具体 UI 组件。
- `internal/app/local`：默认 composition root，负责装配 adapters/registries。
- `internal/adapters/*`：model/store/sandbox/tool/acp-agent 的具体实现。
- `protocol/acp/*`：ACP schema、JSON-RPC、client/server、transport 和 projector。
- `surfaces/*` 与 `internal/surface/*`：只做输入适配和渲染投影。

## 开发约束

- ACP 是核心产品边界，不是可选兼容层。
- 持久化存 canonical 语义，不存 UI transcript cache 作为 replay 源。
- ACP `_meta` 只能承载显示提示或明确的 replay metadata，不能成为唯一的模型关键数据。
- 外部 ACP 输入必须先归一化为 canonical event，再进入存储和 replay。
- 不恢复旧版 broad `ports`、legacy `kernel`、`app/gatewayapp`、router/preset/tool stack。
- `v1.0.0` 前优先修正边界和 schema，不为旧形状堆兼容分支。

## 验证

涉及持久化、ACP 投影、prompt、task 或 replay 的改动必须补对应 round-trip
或 parity 测试。提交前运行受影响 `go test`、`go run ./scripts/arch_lint.go`
和 `git diff --check`。
