## 一、P0 性能热点：这些会直接拖慢体感

### 1. 输入补全在按键路径里同步执行，属于最危险的“打字卡顿源”

证据在这些路径：

`surfaces/tui/app/model_input.go:819-846`
普通输入后同步调用 `refreshMention`、`refreshSkill`、`updateResumeCandidates`、`updateSlashArgCandidates`、`refreshSlashCommands`。

`surfaces/tui/app/model_input.go:851-877`
粘贴时也同步走同一套刷新链。

`surfaces/tui/app/model_completion.go:84-118`
`refreshMention` 会同步调用 `cfg.FileComplete` / `cfg.MentionComplete`。

`surfaces/tui/app/model_completion.go:178-200`
`refreshSkill` 同步调用 `cfg.SkillComplete`。

`surfaces/tui/app/model_completion.go:313-349`
`updateResumeCandidates` 同步拉取 resume 候选，最多请求 200，再过滤。

`surfaces/tui/gatewaydriver/completion.go:297-377`
文件补全内部会 `filepath.WalkDir(base)`，虽然有 150ms timeout 和深度限制，但它仍然发生在输入交互链上。

这会导致一个非常典型的 UX 断点：用户敲一个字符，本来应该是 0.1 秒级的直接反馈，却可能被文件遍历、session 查询、skill 查询或 slash 参数候选拖住。Bubble Tea 社区的优秀做法是：输入状态先立即更新，补全结果通过异步 `Cmd` 回来，并用 generation token 丢弃过期结果。([Charm](https://charm.land/blog/commands-in-bubbletea/))

优化动作：

把 mention / skill / slash arg / resume completion 全部改成异步命令。输入发生时立即更新 textarea，然后发起带 `queryID` 的补全请求。结果回来时，如果 `queryID` 不等于当前输入版本，直接丢弃。加 80–120ms debounce，避免每个字符都触发全量检索。文件补全建立 workspace prefix cache，`WalkDir` 只做冷启动或后台刷新。resume 候选从 session 元数据索引读，不要实时加载完整 session。

验收标准：

打字路径 p95 小于 16–30ms；补全结果允许 100–250ms 后出现，但输入光标不能卡。粘贴大段文本时不要逐字符触发补全刷新。

------

### 2. session store 是全 JSON 文档读写，长会话会越来越慢

这是项目里最大的结构性热点之一。

证据：

`impl/session/file/store.go:152-195`
`List` 会遍历所有 session document path，读取完整 JSON document，然后过滤排序。

`impl/session/file/store.go:197-243`
`AppendEvent` 每追加一个事件，都会读完整 document，把 event append 进去，然后写完整 document。

`impl/session/file/store.go:355-395`
`ReplaceState` / `UpdateState` 也是读整份 document、改 state、写整份 document。

`impl/session/file/store.go:524-600`
`readDocumentAt` 是 `os.ReadFile + json.Unmarshal`，`writeDocument` 是 `json.MarshalIndent`，写临时文件、`Sync`、rename、chmod、`syncDir`。

`impl/session/file/store.go:633-710`
查找和列出 session path 也依赖 `WalkDir`。

这个设计可靠性不错，但性能会随“会话长度”和“会话数量”线性恶化。更糟的是 agent 流式输出、工具事件、状态更新都可能不断追加事件，导致每个事件都重写越来越大的 JSON。到了长会话，这就是 O(n²) 倾向。

社区里更常见的优秀设计是：**append-only event log + 元数据索引 + 周期性 snapshot/compaction**。终端 agent 这类产品尤其适合把事件写成 JSONL/WAL，把 session summary、更新时间、模型、cwd、title、token usage 单独放 metadata index。列表页只读 index，恢复时先读 snapshot，再 replay 增量 event。

优化动作：

把 `AppendEvent` 改成 append-only JSONL，不再每次重写完整 document。保留 `session.meta.json` 或 SQLite/BoltDB/Badger 这类轻量索引用于列表和 resume 候选。每 N 个事件或每 M KB 写一次 snapshot。`UpdateState` 改成 coalesced write，短时间内多次 state update 合并。`MarshalIndent` 只用于导出/调试，正常存储用紧凑 JSON。

验收标准：

长会话追加事件耗时不随历史长度显著增长。`ListSessions(limit=10/50)` 不读取完整 transcript，只读 index。恢复 10k events 的 session 不应先阻塞 UI，而是展示进度并分批 replay。

------

### 3. `/resume` 逐事件回放，会制造 TUI 消息风暴

证据：

`surfaces/tui/app/driver_bridge.go:1157-1167`
resume 成功后调用 `ReplayEvents`，然后对 `resumeTranscriptReplayEvents(events)` 的每个事件逐个 `sender.Send`。

`surfaces/tui/app/driver_bridge.go:1173-1185`
过滤出 TUI resume 需要的事件。

问题不是“能不能恢复”，而是长会话恢复时会把历史事件重新当成实时事件灌给 TUI。这样会触发 document 更新、viewport dirty、render scheduler、缓存校验等一串工作。会话越长，恢复越像一次压力测试。

优化动作：

增加 `ReplayTranscriptBatchMsg`，把历史 transcript 转成批量 document mutation。进入 TUI 时先渲染最近 N 条或最近可视窗口，旧内容 lazy materialize。恢复期间展示 “Loading transcript · 1320 / 9821 events” 之类的进度。不要让历史 replay 走实时 stream smoothing 的路径。

验收标准：

恢复 1k、10k、50k events 分别做 benchmark。恢复期间 UI 可取消，可滚动最近内容，不出现长时间空白。

------

### 4. TUI 每帧构造完整 frame，streaming 时容易放大成本

证据：

`surfaces/tui/app/model_view.go:23-90`
每次 `View()` 都计算 layout、渲染 viewport、drawer、prompt reservation、hint row、status header、separator、input bar、footer。

`surfaces/tui/app/model_view.go:90-110`
之后 `strings.Join` 拼完整 frame，执行 normalize，再叠加 prompt/input/command palette，然后再次 normalize。

`surfaces/tui/app/model_view.go:113-133`
每帧记录 render duration 和 bytes，并返回 `tea.NewView(view)`。

`surfaces/tui/app/model_view.go:136-138`
mouse mode 默认是 `tea.MouseModeCellMotion`。

`surfaces/tui/app/model_util.go:121-180`
诊断会记录每帧耗时、bytes、慢帧、输入延迟；如果配置了 `DiagnosticsDebugFile` 或 `CAELIS_TUI_RENDER_DEBUG_FILE`，会每帧 `json.MarshalIndent` 并 `os.WriteFile`。

这部分项目已经做了诊断，非常好。但目前风险在于：streaming、spinner、viewport 变化会驱动频繁 `View()`，而 `View()` 里做的是全 frame 字符串构造。Bubble Tea 的 `WithFPS` 只是设置渲染器最大 FPS，默认/上限行为并不能替代减少每帧工作量。([pkg.go.dev](https://pkg.go.dev/charm.land/bubbletea/v2))

优化动作：

底部区域、header、footer、hint、status、input bar 做按 key 缓存，只在尺寸、状态、文本变化时重算。normalize 尽量只做一次，或拆成局部 normalize。`DiagnosticsDebugFile` 改成 1Hz 采样、退出时写一次，或 ring buffer append，不要每帧写文件。mouse mode 在不需要 cell motion 时降级，减少终端输入事件压力。慢帧阈值已有 40ms，可以继续保留，但要加 p95/p99 输出。

验收标准：

idle frame 基本不重算大块字符串。streaming 时 render p95 小于 16–25ms，慢帧占比显著下降。打开 debug diagnostics 不应把 UI 自己拖慢。

------

### 5. viewport 已有增量缓存，但仍存在长 transcript 的线性复制/哈希

证据：

`surfaces/tui/app/view_layout.go:65-78`
`renderedStyledLines` 会遍历所有 document blocks，再拼 stream lines。

`surfaces/tui/app/view_layout.go:85-133`
`syncViewportContent` 根据 dirty block/context/cache 决定 full rebuild 或 incremental rebuild。

`surfaces/tui/app/view_layout.go:279-287`
`viewportLinesFingerprint` 对每一行做 FNV hash。

`surfaces/tui/app/view_layout.go:314-343`
内容版本变化时会 hash `viewportStyledLines`，变化后 `append([]string(nil), lines...)` 再 `SetContentLines`。

`surfaces/tui/app/view_layout.go:345-360`
materialize stale content 时也会 clone 全部 lines，然后重新设置 viewport。

这说明你已经做了不少对的事情：dirty block、incremental、deferred sync、stale materialization。但长 transcript 下，全量 hash、全量 clone、全量 SetContentLines 仍然会成为热点。

优化动作：

把 line fingerprint 改成 rolling segment hash，block 级 hash 聚合，不要每次扫所有行。tail-only append 走专门路径，不 clone 全部 lines。viewport backing 做 virtualized provider，只 materialize visible window + overscan。对 selection/follow-tail 场景保留现有 deferred sync，但 stale materialization 要分批执行。

验收标准：

长 transcript 追加 tail chunk 时，不出现和 transcript 总行数成正比的工作。滚动到历史区域时允许 lazy render，但必须给出稳定反馈。

------

### 6. 流式输出 smoothing 有设计，但高频流仍会被 per-tick 排序、grapheme、wrap 放大

证据：

`surfaces/tui/app/state.go:28-37`
默认 stream smoothing tick 是 16ms，目标 lag 160ms，normal/catchup CPS 与每帧 reveal 数有限制。

`surfaces/tui/app/render_scheduler.go:31-47`
scheduler 会拦截 LogChunk 和 gateway narrative/terminal stream 事件。

`surfaces/tui/app/render_scheduler.go:62-75`
enqueue 只和队尾同 lane/key 的事件合并。

`surfaces/tui/app/render_scheduler.go:152-182`
drain 时处理 pending events，并同步 viewport。

`surfaces/tui/app/stream_blocks.go:435-442`
stream delta 会 split 成 grapheme clusters。

`surfaces/tui/app/stream_blocks.go:488-560`
每次 drain 会从 map 取 keys 并 sort。

`surfaces/tui/app/stream_blocks.go:651-665`
主输出会根据 wrapWidth、当前 raw 和稳定行策略计算 reveal cluster 数。

这是很细腻的实现，但还可以继续压热路径。

优化动作：

单 active stream 时走 fast path，不构造 keys、不 sort。维护 lane order slice，只有新增 lane 时才更新顺序。合并不只看队尾，而是合并同 key 的所有 pending delta。先按 bytes 合并，再批量做 grapheme split。backlog 过高时自动降级：减少动画、增大 chunk、暂停 markdown streaming render。把 backlog size、drain cost、revealed clusters/frame 加入 diagnostics。

验收标准：

高频 token stream 下没有消息队列堆积。CPU 占用不随 delta 数线性飙升，而更接近按帧成本增长。

------

### 7. Markdown/Glamour 在 streaming 场景仍可能反复失效

证据：

`surfaces/tui/app/blocks.go:71-94`
narrative block render cache 按 width/theme/raw/rolePrefix/streaming 建 key。

`surfaces/tui/app/blocks.go:131-145`
AssistantBlock streaming 时走 `activeBuffer.RenderRows`，非 streaming 才使用 cached glamour rows。

`surfaces/tui/app/blocks.go:213-229`
assistant narrative streaming/final 分别走不同 glamour 渲染路径。

这块已经比“每帧全 markdown 渲染”好很多。但 raw 在流式期间持续变化，任何以完整 raw 为 key 的 cache 都会天然失效频繁。尤其是 markdown wrap、inline style、ANSI/grapheme 组合起来，长回答会明显变重。

优化动作：

streaming 阶段优先 cheap renderer，只做基础 wrap/ANSI，不做完整 glamour。final chunk 到来后后台生成完整 markdown render，然后替换。对 markdown AST 做 block-level cache：只有最后一个 paragraph/code block 在 streaming 时变化，前面稳定 block 不应重渲染。长 code block、长 table、长 plain text 分别设置渲染上限和 lazy expansion。

验收标准：

长 markdown 回答 streaming 时不卡；final 美化可以稍晚，但不能影响输入和滚动。

------

### 8. HTTP provider 缺少统一的网络超时/空闲保护

证据：

`impl/model/providers/http_client.go:5-10`
如果没有传 client，就返回裸 `&http.Client{}`。

`impl/model/providers/openai_compat.go:107-114`
非 stream 只有 `requestTimeout > 0` 时才加 context timeout；stream 主要依赖 caller context。

`impl/model/providers/openrouter.go:190-207`
同样是非 stream 在 timeout 存在时才加 context timeout。

`impl/model/providers/discovery.go:36-45`
discovery 路径反而做得更好，有默认 45s timeout，并在必要时 clone client。

Go 的 `http.Client` 应该复用，且 `Client.Timeout` 为 0 时表示没有总超时；这个 timeout 覆盖连接、重定向和读取 body，并会像 context cancel 一样取消请求。([Go](https://go.dev/src/net/http/client.go)) 对流式请求不能简单设置很短总超时，否则会误杀长回答；但必须设置连接/TLS/header 超时、空闲读 watchdog、用户取消路径。

优化动作：

提供统一 `ProviderHTTPClientFactory`，默认 Transport 设置 `DialContext` timeout、TLS handshake timeout、response header timeout、idle conn timeout。streaming 请求不设短总 deadline，但设 inactivity timeout，例如 N 秒无任何 chunk 就提示并允许重试/取消。所有 provider 都统一取消语义：Esc / Back / Ctrl-C 后 UI 立即显示 “cancelling provider request…”，超时仍未结束则给强制中断提示。

验收标准：

网络卡住不会让 TUI 看起来死机。用户取消后 100ms 内有反馈。provider 无 chunk 超过阈值时能明确提示。

------

## 二、P1 性能热点：这些不一定立刻炸，但会在大项目/长会话里变成坑

### 9. 文件工具 LIST/GLOB/READ 在大目录和大文件下成本偏高

证据：

`impl/tool/builtin/filesystem/list.go:78-130`
`LIST` 会 `ReadDir` 全部 items，对每个 item 调 `Info()`，构建完整数组、排序，再按 limit 返回。

`impl/tool/builtin/filesystem/glob.go:117-140`
`GLOB` 会 walk 整个 root，收集所有 match，排序后再截断。

`impl/tool/builtin/filesystem/read.go:99-139`
`READ` 达到 line limit 后，还会 `io.Copy(hasher, reader)` 把剩余内容读完用于 revision hash。

这些工具对 agent 很关键，因为模型会频繁调用。大 monorepo、大 node_modules、大日志文件下，这些路径会拖慢整个 agent turn。

优化动作：

`LIST` 增加 `metadata=false` 默认值，不需要 size/mtime 时不调 `Info()`。排序前允许 early stop 或分桶 top-N。`GLOB` 增加 limit+1 early terminate，并尊重 ignore 文件。`READ` 不要为了小片段读取 hash 整个剩余文件；可以改成文件 stat + range hash + mtime/size revision，或只在用户要求 exact revision 时完整 hash。

验收标准：

在 100k 文件目录里，LIST/GLOB 能快速返回前 N 个结果，并明确提示 “truncated”。读取大文件前 200 行不应扫描完整 GB 级文件。

------

### 10. task store 也是整 index 读写，任务多时会退化

证据：

`impl/task/file/store.go:70-129`
`Upsert` 读取 session index，写 final blobs，扫描替换 task，排序所有 task，再写完整 index。

`impl/task/file/store.go:132-160`
`Get` 会 `ReadDir(rootDir)`，读取每个 `*.index.json`，扫描 tasks 找 ID。

`impl/task/file/store.go:270-299`
index 读写是完整 JSON。

`impl/task/file/store.go:325-351`
blob 写入会重写排序后的全部 blob records 到 `.blobs.jsonl`。

优化动作：

增加 taskID → sessionID 的全局索引。task index 改 append/upsert log，后台 compaction。blob 不要每次重写全部记录，改 append-only + manifest。`Get(taskID)` 不能扫所有 session。

验收标准：

task 数量和 session 数量上来后，单 task 查询仍然接近 O(1)。

------

### 11. 工具事件更新按 callID 反向扫描，长消息列表下会变热

证据：

`surfaces/tui/app/blocks.go:290-365`
`applyToolEventUpdate` 会从 events 末尾向前扫描，按 callID 合并 open/final tool event。

这在短会话里没问题，但工具输出多、subagent 多、长 session replay 时，会变成重复扫描。

优化动作：

document 层维护 `toolCallID -> block/event index`。新增事件时登记，后续 update 直接定位。批量 resume/replay 时暂时关闭逐条 index rebuild，最后一次性 build。

验收标准：

工具事件更新不随 transcript 长度线性增长。

------

### 12. generic tool panel cache 会 hash 完整文本

证据：

`surfaces/tui/app/tool_panel_cache.go:43-63`
cache key 包含 `hashString64(request.Text)`。

`surfaces/tui/app/tool_panel_cache.go:65-72`
terminal panel 会先 tail 到 max lines，是好的；但非 terminal 文本会直接返回 full text。

`surfaces/tui/app/tool_panel_cache.go:79-82`
hash 会扫完整 text。

优化动作：

terminal panel 的 bounded-tail 设计保留。generic output 也要有 max render bytes / visible window。hash 在事件更新时做 rolling hash，render 时不要重复扫完整文本。长 JSON/tool result 提供 folded tree 或 “open full output”。

验收标准：

巨大工具结果不会让每帧 render/hash 都扫完整内容。

------

## 三、用户体验断点：这些地方用户会“以为坏了”

### 1. 长工具输出不能内部滚动，鼠标滚轮可能“没反应”

证据：

`surfaces/tui/app/model_input.go:110-163`
鼠标滚轮会尝试路由到 panel scroll。

`surfaces/tui/app/blocks.go:1220-1226`
`MainACPTurnBlock.ScrollToolPanel` 和 `CanScrollToolPanel` 返回 false。

`surfaces/tui/app/blocks.go:1329-1335`
Participant turn 版本也返回 false。

测试里也能看到这是有意设计：terminal panel 显示 tail、忽略内部 scroll，完成后显示开头/结尾和隐藏提示，点击可展开。这不是 bug，但 UX 上会让用户困惑：滚轮无反馈等于“我是不是没点中”。

优化动作：

当用户在工具 panel 上滚轮但不可滚动时，显示一次性 hint：`Tool output is clipped · click to expand · c to copy · /open-log to view full output`。长输出 panel 上固定显示 affordance，不要只靠点击。展开后支持搜索、复制、保存到文件。对 terminal/subagent live output 保留 tail-follow，但允许 “pin / detach full log”。

CLI 社区优秀设计强调：长命令要告诉用户当前步骤、仍在运行，并给出可操作反馈；信息性错误也应该包含标题、说明和解决办法。([Thoughtworks](https://www.thoughtworks.com/en-us/insights/blog/engineering-effectiveness/elevate-developer-experiences-cli-design-guidelines)) 这里的“滚轮没反应”本质就是缺少反馈。

------

### 2. 运行中粘贴图片会静默忽略

证据：

`surfaces/tui/app/model_input.go:767-770`
`ImagePasteMsg` 在 `m.running` 时直接 `return m, nil`。

这会造成很糟糕的断点：用户以为图片进去了，其实没有。

优化动作：

运行中图片粘贴不要静默丢弃。至少显示 hint：`Image paste is queued until the current run finishes` 或 `Images cannot be attached while running · press Esc to interrupt first`。更好是支持 queued attachment，等当前 run 完成后进入 composer。

验收标准：

任何用户输入被拒绝时，都必须有可见反馈。

------

### 3. slash command 在 running 状态有提示，但可用/不可用边界还可以更清楚

证据：

`surfaces/tui/app/model_input.go:724-752`
运行中如果输入 slash command，会提示 `slash commands are unavailable while running`；普通文本会作为 follow-up 提交。

这点已经做得不错。但从 UX 看，running 状态下到底哪些动作可用，需要更明确。

优化动作：

running carousel 里增加上下文动作：`type to queue follow-up · Esc interrupt · / commands locked`。当用户输入 `/` 时，直接在 composer 附近显示原因和替代动作。允许少量安全 slash command 在 running 时执行，比如 `/copy`, `/status`, `/interrupt`。

------

### 4. approval modal 的命令预览可能截断关键风险信息

你们已经有 approval 流程，这是好事。但如果命令、路径、参数较长，截断的 preview 会让用户无法判断风险。

优化动作：

approval modal 加 “expand details”。展示 command、cwd、env diff、sandbox mode、network/file permissions、estimated destructive risk。支持复制完整命令。危险操作高亮原因，而不是只显示一坨压缩 JSON。

验收标准：

用户不需要猜 “这个 approve 到底会执行什么”。

------

### 5. 状态栏信息太紧凑，认知成本偏高

你们测试里有类似 `12.6k/88k(14%)` 的 context usage 格式。这对工程师能看懂，但终端产品应该更降低认知成本。

优化动作：

改成 `12.6k / 88k · 14%`，必要时加短 label：`ctx 12.6k / 88k · 14%`。model、cwd、approval mode、sandbox mode 这类状态不要挤在一起，优先保留当前 turn 最相关信息。

------

### 6. 首次使用和故障恢复可以更主动

README 里说明项目支持 `~/.caelis` 配置、sessions、approval/sandbox、ACP、headless、AGENTS.md、skills 等，但终端里首次启动如果没有 API key、没有 model、没有 workspace prompt，用户很容易卡住。

优秀 CLI 会把 terminal 内帮助、简短输出、stdout/stderr 语义、退出码和状态变化做清楚；命令改变状态时应告诉用户发生了什么，Ctrl-C 也应该尽快反馈。([CLI指南](https://clig.dev/))

优化动作：

首次启动 wizard：检测 provider key、默认模型、cwd、AGENTS.md、skills。缺失时给出一屏内可执行建议。`/doctor` 命令输出 provider connectivity、config path、session path、terminal color profile、NO_COLOR/no animation 状态。网络失败、模型失败、工具失败统一用 “原因 + 影响 + 下一步” 格式。

------

### 7. 色彩/无障碍方向是对的，但要补测试矩阵

README 提到适配 dark/light terminal、terminal color profile、NO_COLOR、显式 theme/accent。这个方向和社区规范一致：`NO_COLOR` 约定要求命令行软件在环境变量存在且非空时禁用 ANSI color，当然配置或 CLI 参数可以覆盖。([no-color.org](https://no-color.org/))

优化动作：

加 snapshot 测试覆盖：NO_COLOR、NoAnimation、窄终端、低色彩终端、浅色背景、SSH/tmux、Windows Terminal。所有重要状态不能只靠颜色表达，要有文字或符号冗余。

------

## 四、优化打磨清单，本小姐按优先级给你排好了

| 优先级 | 项目                          | 落点                                                         | 验收标准                                                  |
| ------ | ----------------------------- | ------------------------------------------------------------ | --------------------------------------------------------- |
| P0     | 输入补全异步化                | `model_input.go`, `model_completion.go`, `gatewaydriver/completion.go` | 输入 p95 < 30ms；补全结果可延迟但不阻塞光标               |
| P0     | session append-only 化        | `impl/session/file/store.go`                                 | `AppendEvent` 不随 transcript 长度增长；列表只读 metadata |
| P0     | resume 批量回放               | `driver_bridge.go`                                           | 长 session 恢复有进度、可取消、不逐事件触发 render storm  |
| P0     | viewport tail append 优化     | `view_layout.go`                                             | tail streaming 不全量 hash/clone 所有 lines               |
| P0     | provider 网络保护             | `impl/model/providers/*`                                     | 连接/header/inactivity 超时明确；取消 100ms 内有 UI 反馈  |
| P1     | stream scheduler fast path    | `render_scheduler.go`, `stream_blocks.go`                    | 单 stream 不排序；高频 delta 自动合并/降级                |
| P1     | markdown streaming cheap path | `blocks.go`, `view_layout.go`                                | streaming 期间不反复完整 glamour；final 后再美化          |
| P1     | tool panel 长输出虚拟化       | `tool_panel_cache.go`, `blocks.go`                           | 大工具输出不每帧 hash 全文；可展开/复制/搜索              |
| P1     | tool callID 索引              | `blocks.go`, `document.go`                                   | 工具事件更新 O(1) 定位                                    |
| P1     | 文件工具 early-stop           | `filesystem/list.go`, `glob.go`, `read.go`                   | 大目录/大文件下快速返回 limit+truncated                   |
| P1     | task store 建索引             | `impl/task/file/store.go`                                    | `Get(taskID)` 不扫所有 session                            |
| P1     | diagnostics 限频              | `model_util.go`                                              | debug 文件写入不影响 frame time                           |
| P2     | running 状态输入反馈          | `model_input.go`, `state.go`                                 | 图片粘贴、slash 不可用、排队 follow-up 都有明确提示       |
| P2     | approval 详情展开             | approval modal/render path                                   | 用户能看到完整命令、cwd、权限、风险说明                   |
| P2     | 状态栏微文案                  | status formatting                                            | `ctx 12.6k / 88k · 14%` 这类更可读格式                    |
| P2     | `/doctor` 和首次启动向导      | CLI/TUI command                                              | provider/config/session/terminal 状态一屏诊断             |
| P2     | 无障碍 snapshot               | render tests                                                 | NO_COLOR、NoAnimation、窄屏、浅色背景都稳定               |

------

## 五、建议立刻加的 benchmark / profiling 套件

别急着凭感觉改，本小姐给你一套验收靶子：

`BenchmarkViewLargeTranscript`
构造 1k/10k/50k blocks，测 `Model.View()` p50/p95/p99、allocs、bytes rendered。

`BenchmarkSyncViewportTailAppend`
长 transcript 下不断 append tail chunk，测是否仍然全量 hash/clone。

`BenchmarkStreamDrainHighRate`
模拟 1000/5000/20000 token delta，测 scheduler backlog、drain cost、frame count。

`BenchmarkCompletionTypingLargeRepo`
模拟每 30ms 输入一个字符，大目录下测 completion 是否阻塞输入。

`BenchmarkAppendEventLongSession`
1k/10k/50k events 下追加一个 event，比较当前 JSON rewrite 和 append-only log。

`BenchmarkResumeReplayLongSession`
测逐事件 replay 与 batch replay 的 UI message 数、render 次数、总耗时。

`BenchmarkFilesystemListGlobLargeTree`
模拟 100k 文件树，测 LIST/GLOB 是否 early stop。

运行层面加 pprof：CPU profile 看 streaming/render，heap profile 看长 transcript，block/mutex profile 看 store lock 和 file I/O。项目如果未来要跨环境观测，可以接 OpenTelemetry，把 traces/metrics/logs 统一导出；OpenTelemetry 本身就是用于生成、收集和导出 telemetry 的工具链。([OpenTelemetry](https://opentelemetry.io/docs/what-is-opentelemetry/))

------

最后给你一句狠的：**这个项目现在不是“缺优化”，而是已经到了该把交互热路径和存储模型分层的阶段**。先改补全异步、session append-only、resume batch、viewport tail path，这四个一做，体感会明显上一个台阶。剩下的再逐步打磨工具输出、文件工具、状态提示和诊断体系。
