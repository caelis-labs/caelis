哼，真拿你没办法……本小姐已经静态扫过你上传的 `caelis-main` 项目了。这个项目的瘦身不能靠“删功能”，而要靠**收敛重复逻辑、压缩静态数据、稳定架构边界、把大文件拆成可复用策略**。下面这份计划按“不损失产品能力和用户体验”的前提来定。

我先说结论：**核心分层应该保留**，也就是 `cmd/caelis -> internal/cli -> app/gatewayapp -> kernel.Service` 这条路径不能砍；`kernel/` 和 `ports/` 作为稳定契约也不能粗暴合并进实现层。真正该瘦的是 `surfaces/tui`、`impl/model/providers`、`impl/agent/local`、`app/gatewayapp/local.go`、`ports/session + internal/kernel/event_projection` 里过度膨胀和重复的部分。

我这边静态统计到的基线大概是这样：

| 指标        |                                                    当前情况 |
| --------- | ------------------------------------------------------: |
| Go 文件     |                                                   415 个 |
| 生产 Go 代码  |                                              约 98,806 行 |
| 测试 Go 代码  |                                              约 52,200 行 |
| 仓库文件总大小   |                                               约 6.48 MB |
| 最大非代码数据   | `impl/model/catalog/models_dev_snapshot.json`，约 1.81 MB |
| 生产代码最大热点  |                 `surfaces` 约 38,050 行，`impl` 约 36,380 行 |
| 两大热点占生产代码 |                                                   约 75% |

另外我尝试跑 `go test ./...` 时受限于当前容器只有 Go 1.23.2，而项目要求 Go 1.25.1，并且容器无法联网下载 toolchain，所以这里没法完成测试执行验证。后续计划里的验收标准仍然应该以你本地或 CI 的 `make quality` 为准。

ChatGPT ---

第一阶段先做“零功能损失”的瘦身。最明显的是 `impl/model/catalog/models_dev_snapshot.json`。它现在是 1.81 MB，属于 models.dev 的完整快照，但运行时真正需要的是 provider、model、context window、output limit、reasoning、tool call、image、JSON output 这些能力字段。按现有数据生成 compact JSON，大概可以降到 795 KB；如果改成 gzip embed，再启动时解压解析，可以降到约 50 KB 级别。这个改动对用户体验没有影响，因为 lookup 结果保持一致，远程刷新、local override、builtin fallback 都可以照旧保留。

建议改法是新增一个生成脚本，比如：

```text
internal/modelcataloggen/
  generate_snapshot.go
```

输出：

```text
impl/model/catalog/models_dev_snapshot.compact.json.gz
```

然后把 `model_catalog_remote.go` 里的 `//go:embed models_dev_snapshot.json` 改成读取压缩快照。验收标准是：`LookupModelCapabilities`、`LookupSuggestedModelCapabilities`、provider overlay、本地 override、远程 fetch fallback 全部测试不变。这个 PR 可以作为第一刀，收益非常高，风险很低。

顺手还要清理 README 里的坏链接。当前 README 指向 `docs/README.md` 和 `docs/architecture.md`，但 `docs/` 下实际只有 `agent-sdk-acp-architecture-plan.md`。这不影响运行，但会增加维护噪声。要么补齐文档，要么把链接改到真实文件。小事归小事，可这种小毛病不处理，项目看起来就不够漂亮，笨蛋。

---

第二阶段收敛 TUI 渲染层。这里是最大代码热点：`surfaces/tui/app` 单目录生产代码约 29,007 行，其中几个大文件特别突出：

```text
surfaces/tui/app/acp_transcript.go      约 3,011 行
surfaces/tui/app/blocks.go              约 2,895 行
surfaces/tui/app/driver_bridge.go       约 2,432 行
surfaces/tui/gatewaydriver/gateway_driver.go 约 2,414 行
surfaces/tui/app/view_render.go         约 1,550 行
surfaces/tui/app/stream_blocks.go       约 1,548 行
surfaces/tui/app/model_completion.go    约 1,297 行
surfaces/tui/app/model_input.go         约 1,292 行
```

这里不能砍 TUI 能力，因为 TUI 是主体验。但可以把“事件解释”和“终端渲染”解耦。现在很多逻辑在处理同一类事情：ACP event、tool call、task output、diff、approval、reasoning fold、terminal panel、subagent transcript。建议引入一个中间层：

```text
surfaces/tui/displaymodel/
  event_model.go
  tool_model.go
  diff_model.go
  transcript_model.go
```

目标是让流程变成：

```text
kernel/protocol/session event
        ↓
DisplayEvent / DisplayBlock / ToolPanelViewModel
        ↓
Bubble Tea rendering
```

这样 `acp_transcript.go` 和 `blocks.go` 不再各自判断 tool kind、tool status、metadata、diff、terminal output，而是消费统一的 view model。你已经有 `internal/displaypolicy/policy.go`，它其实是个好苗子，可以扩展成真正的 display policy 层，而不是让 TUI 文件里到处散落格式化规则。

这里还有一个具体重复点：`surfaces/tui/acpprojector/format.go` 和 `surfaces/tui/tuidiff/diff.go` 都有 diff/hunk/LCS 类逻辑。建议统一为一个 diff model：`tuidiff.BuildModel` 负责结构化 diff，`acpprojector` 只做协议文本投影，不再自己维护一套简化 diff 算法。这样不会影响用户看到的 diff，反而更一致。

这一阶段的验收标准不是“少了多少行”，而是这些体验必须完全保留：ACP transcript 渲染、BASH/SPAWN/TASK 面板、approval 状态、reasoning folding、tool output truncation、dark/light theme、自适应宽度、markdown/glamour 渲染、slash command completion。建议配套 golden/snapshot 测试锁定输出。

目标可以定得现实一点：第一轮把 TUI 生产代码减少 4,000 到 7,000 行，或者至少让最大单文件都压到 1,200 行以下。

---

第三阶段重构 slash command 和 connect wizard。现在 slash command 的定义、dispatch、completion、帮助文案散落在 `defaults.go`、`driver_bridge.go`、`gateway_driver.go`、README 文案和测试里。功能很多，但代码形态不够集中。

建议建立统一命令注册表：

```go
type CommandSpec struct {
    Name        string
    Usage       string
    Description string
    Dispatch    CommandHandler
    Complete    CommandCompleter
    Visible     func(Context) bool
}
```

然后 `/help`、`/agent`、`/connect`、`/model`、`/approval`、`/sandbox`、`/doctor`、`/compact` 都从同一个 registry 生成帮助、补全和派发逻辑。动态 ACP child commands 也可以作为 registry 的 runtime extension。

这样做的好处是不会改变用户输入体验，但可以减少重复 switch、重复文案、重复补全判断，还能降低以后加命令时的维护成本。`/connect` wizard 的状态机也建议从“字符串 payload 拼接”改成明确的 step state，例如：

```go
type ConnectWizardState struct {
    Provider string
    BaseURL string
    AuthMode string
    TokenRef string
    Model string
    ContextWindow int
    MaxOutputTokens int
    ReasoningLevels []string
}
```

当前测试里有很多类似 `connect-model:provider|url|...` 的内部字符串断言，这说明内部协议已经影响测试形态了。把它换成结构化状态后，测试更短，也更不脆弱，同时不会影响用户看到的 `/connect` 交互。

---

第四阶段处理 model provider 代码。`impl/model/providers` 生产代码约 6,377 行，测试约 3,223 行，是另一个高价值区域。这里不能删 provider，因为产品能力会受损；应该做的是把 OpenAI-compatible 派生 provider 收敛成 profile。

现在 `deepseek.go`、`mimo.go`、`minimax.go`、`volcengine.go`、`volcengine_coding_plan.go`、`openrouter.go`、`codefree.go`、`openai_compat.go` 之间很可能存在请求格式、SSE、usage、reasoning payload、structured output 的重复变体。建议抽象成：

```go
type CompatProfile struct {
    ProviderID string
    DefaultBaseURL string
    ReasoningStrategy ReasoningStrategy
    StructuredOutputStrategy StructuredOutputStrategy
    UsageParser UsageParser
    HeaderPolicy HeaderPolicy
}
```

然后大部分兼容 provider 只声明 profile，核心 HTTP/SSE/request transform 共用一套。这个方向不减少任何模型能力，反而会让新 provider 接入更快。验收必须覆盖：streaming、non-streaming、tool call、invalid tool args、structured output、reasoning effort、usage、finish reason、provider error、backpressure/retry。

`codefree_auth.go` 和 `codefree.go` 也建议拆开：认证、凭据存储、OAuth callback、chat completion 不要在同一 provider 包里互相缠绕。不是为了拆而拆，而是让 chat 路径不会被 auth 细节污染。

这一阶段的目标是减少 2,000 到 4,000 行 provider 相关代码，同时保持 provider matrix 测试覆盖不下降。

---

第五阶段处理 local runtime，特别是 `impl/agent/local/task_runtime.go`。这个文件约 2,918 行，里面混着 BASH、TASK、SPAWN/subagent、持久化、stream、approval、rehydration、snapshot、stdin write、cancel/wait 等逻辑。它是功能密集区，不能粗暴删，但非常适合抽象状态机。

建议把它拆成两个层次：

```text
impl/agent/local/taskruntime/
  manager.go          // TASK wait/write/cancel 的统一入口
  state.go            // task snapshot/state transition
  bash_target.go      // BASH/SPAWN shell target
  subagent_target.go  // ACP subagent target
  persistence.go      // Entry <-> runtime state
  stream.go           // stream frame stitching
```

核心是统一 BASH 和 subagent 的生命周期接口：

```go
type TaskTarget interface {
    Start(ctx context.Context) (Snapshot, error)
    Wait(ctx context.Context, yield time.Duration) (Snapshot, error)
    Write(ctx context.Context, input string) (Snapshot, error)
    Cancel(ctx context.Context) (Snapshot, error)
    Rehydrate(entry task.Entry) (Snapshot, error)
}
```

这样 `TASK wait/write/cancel` 不需要到处判断 target kind。状态映射、tool result payload、stream append、persist entry 都可以共享。用户层面的 `BASH`、`SPAWN`、`TASK wait`、`TASK write`、`TASK cancel` 行为不变，但代码会更清晰。

这部分不要一口气改完。建议先加状态机测试，再做 strangler refactor：先让旧代码调用新状态机，再逐步移走分支逻辑。

---

第六阶段收敛 session/protocol 投影。这里要非常小心，因为它是产品语义边界。`ports/session/session.go` 约 1,601 行，`internal/kernel/event_projection.go` 约 1,147 行。它们承担了 session canonical event、ACP projection、tool payload、clone/normalize、legacy fallback 等职责。

从你的 `docs/agent-sdk-acp-architecture-plan.md` 看，项目已经明确了方向：**durable store 应该保存 canonical model/tool/session semantics，ACP 是 client-facing projection，不是 durable source of truth**。这很好，千万别为了瘦身把这个边界弄丢。

真正该删的是“旧分歧兼容逻辑”。文档里已经说 pre-v1.0.0 session 文件可以不兼容，那么进入 v1 之前应该做一次 schema 收敛：

1. 给 session store 加明确 schema version。
2. 把 canonical `Event.Message`、`Event.Tool`、`Event.State` 作为唯一模型上下文来源。
3. ACP `Protocol.Update` 只作为 projection payload 或外部 ACP normalization 结果。
4. 删除“从 Protocol 反推 Message/Tool”的旧 fallback，或者只保留在迁移器里。
5. Replay projector 只从 canonical event 投影 ACP。
6. 所有旧路径测试改成 migration test，而不是运行时常驻分支。

这个阶段可能不会让文件数量少很多，但会明显降低复杂度。验收标准必须非常硬：session round-trip 后重建的 model messages 一致；reasoning、tool call、tool result、usage、approval、plan、participant/subagent replay 都能从 canonical event 投影出来；UI-only transient chunk 不参与 reload。

---

第七阶段整理 `app/gatewayapp/local.go`。这个文件约 2,013 行，是 composition root，但现在承担太多：stack wiring、ACP agent registry、model alias/config、sandbox status、subagent、compaction、provider choices 等都在里面。

这里不要把 composition root 删掉，它是好架构的一部分。应该把它瘦成“装配层”，把业务服务下沉：

```text
app/gatewayapp/
  stack.go              // NewLocalStack + public Stack facade
  model_service.go      // Connect/UseModel/DeleteModel/ListModelChoices
  acp_agent_service.go  // Register/List/Install/Unregister ACP agents
  sandbox_service.go    // backend selection/status
  session_service.go    // StartSession/Resume/Compact/Usage
  runtime_config.go     // self runtime args, app config
```

注意，这种拆分本身不等于减少总行数；真正的瘦身来自删除重复 normalization、重复 alias resolution、重复 config hydration。`Stack` 应该保留为对外门面，但每个方法只委托给对应 service。这样可扩展性保留，后续添加 provider、agent adapter、sandbox backend 时不会继续把 `local.go` 撑爆。

---

第八阶段测试代码瘦身，但不能牺牲覆盖率。测试现在约 52,200 行，几个大测试文件非常大：

```text
impl/agent/local/runtime_test.go                  约 6,159 行
surfaces/tui/app/gateway_event_dispatch_test.go   约 4,187 行
impl/model/providers/providers_test.go            约 3,223 行
surfaces/tui/gatewaydriver/gateway_driver_test.go 约 3,117 行
internal/kernel/gateway_test.go                   约 2,355 行
```

这里的原则是：**不要删测试，只删重复测试样板**。可以做三件事：

第一，把 provider tests 做成 matrix fixture：同一套 streaming/tool/usage/reasoning/error 场景跑不同 profile，而不是每个 provider 写一大段相似测试。

第二，把 TUI rendering tests 改成更稳定的 golden/view-model tests。先测 `DisplayEvent` 和 `DisplayBlock`，再少量测最终 ANSI 渲染，避免每次 UI 文案变化都要更新一堆长测试。

第三，把 runtime test 的 session、tool、sandbox、stream fake 提成共享 fixture，减少每个 case 重新搭一套 stub。

目标是测试代码减少 8,000 到 12,000 行，但覆盖的行为矩阵不能变少。瘦测试不是为了少写测试，是为了让测试更像“规格”，别误会，本小姐不是心疼你维护测试才这么说的。

---

为了防止以后又胖回去，需要加“体重秤”。建议新增：

```text
scripts/size_report.sh
scripts/arch_lint.go
```

`size_report` 输出这些指标：

```text
生产 Go 行数
测试 Go 行数
最大 20 个文件
最大 20 个包
嵌入资源大小
release binary size
npm package size
直接依赖数量
```

`arch_lint` 检查这些边界：

```text
kernel/ 不允许 import impl/ 或 surfaces/
ports/ 不允许 import impl/ 或 surfaces/
impl/ 不允许 import surfaces/
surfaces/ 可以依赖 kernel/ports/protocol，但不直接依赖内部实现，除非经 gatewaydriver/app facade
app/gatewayapp 是主要 composition root
cmd/caelis 只进入 internal/cli
```

CI 里加预算，不要一上来卡得太狠。比如先只报警，后面再改成失败：

```text
单个生产 Go 文件超过 2,000 行：warning
单个生产 Go 文件超过 3,000 行：fail
嵌入资源超过 500 KB 且未压缩：fail
生产 Go 总行数连续增长超过 5%：warning
```

---

推荐 PR 顺序是这样，风险从低到高：

1. **PR-1：加 size report 和 architecture lint**
   不改产品行为，只建立基线。

2. **PR-2：压缩/compact model catalog snapshot**
   立刻减少仓库体积和二进制嵌入资源，风险最低。

3. **PR-3：统一 diff model**
   合并 `acpprojector` 与 `tuidiff` 的重复 diff 逻辑，保持渲染一致。

4. **PR-4：建立 TUI display model**
   先把 tool display、reasoning、approval、terminal panel 统一成 view model，再让旧 renderer 消费它。

5. **PR-5：统一 slash command registry**
   收敛 `/help`、dispatch、completion、dynamic agent commands、README 文案来源。

6. **PR-6：provider profile 化**
   OpenAI-compatible 派生 provider 统一到 `CompatProfile`，保留全部 provider 能力。

7. **PR-7：task runtime 状态机化**
   把 BASH/SPAWN/subagent/TASK 生命周期统一到 `TaskTarget`。

8. **PR-8：session schema v1 cleanup**
   在 schema version 和 migration test 准备好之后，删除旧的 protocol-only 运行时 fallback。

9. **PR-9：测试 fixture/golden 化**
   最后瘦测试样板，确保前面改动已经被稳定规格覆盖。

---

不要做这些事：

不要为了瘦身删除 TUI、headless、ACP server、ACP subagent、approval、sandbox、TASK、provider、prompt assembly。那不是瘦身，是砍产品。

不要把 `ports/` 和 `kernel/` 合并进 `impl/`。短期行数可能变少，长期扩展性会崩。

不要直接删除大测试文件。先提炼 fixture、matrix、golden，再减少重复样板。

不要让 session store 变成 ACP transcript cache。这个项目的正确边界是 canonical session event 在内，ACP projection 在外。

---

最终目标可以这样定：

| 类别          |              第一轮目标 |                       中期目标 |
| ----------- | -----------------: | -------------------------: |
| 仓库静态体积      |        立减约 1 MB 以上 |           嵌入资源控制在 500 KB 内 |
| 生产 Go 行数    |          减少 10% 左右 |               减少 15% 到 25% |
| TUI 最大文件    |     全部压到 1,500 行以内 |             全部压到 1,200 行以内 |
| Provider 代码 | 减少 2,000 到 4,000 行 |  新 provider 主要靠 profile 添加 |
| 测试代码        |        不降覆盖，减少重复样板 | 大测试转 fixture/matrix/golden |
| 架构边界        |            lint 可见 |                      CI 强制 |

这份计划的核心不是“变小就行”，而是让代码变得更像一个可长期演进的产品：核心 contract 留住，composition root 留住，扩展端口留住，把重复判断、重复格式化、重复 provider 变体和大块静态数据拿掉。哼，这样才配得上一个像样的工程嘛。
