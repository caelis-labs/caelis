Code Mode 真实验收预冻结方案（待 Bot 读回，未运行）

本轮目录工作到此收敛。保留已经实现的当前源 Lookup、小范围安全回归及历史四档计数；Definition 次数仅是本地成本诊断，不作为用户主要收益门槛。后续不扩目录、不做性能专项。真实验收必须绑定新审查 head，不能使用历史79cf作为采样head。

只做两个任务、每任务一组 native/JS 配对。每 Session 连续两 Turn，合计四个独立 Session/八 Turn；小任务 native→JS，编排任务 JS→native。两任务分别报告，不能合并成总体收益率。一轮不能证明稳定收益。无额外连通探测、挑样重试、模型替换；永久额度错误、unknown、模型/Runtime错误或不完整采集停止并保留原结果。

1. 小任务作为不劣化观察：沿用两文件17/25，续轮18/25，读取后写同形状 answer.json；真实 Context7 解析 Goja library ID并查询原 Runtime.Interrupt goroutine 问题。两路同短问题、同文档查询义务，续轮重读/重查；缺 API 证据明确 unavailable，不算完整 API 支持通过。相同 canonical names 对两路可见，首次缺 Schema 才 exact describe，续轮有效 Schema 复用；不强制模型先 search。
2. 编排价值任务建议：隔离本地只读 MCP 服务提供三个批次共36条合成检查记录（severity/status/owner/issue_id/updated_at/较长 evidence）。实际工具为 list_batches/read_batch；必须取得列表并读取所有批次，再按共同规则筛选 open+high/critical、同 issue_id 留最新记录、按 owner 汇总，写 actionable.json（保留 issue_id、来源批次、更新时间、计数），仅输出所需结果。续轮关闭一条并增加一条新记录，重新读取工具，输出新报告。它是实际模型→真实Runtime→本地MCP→临时文件任务，输入明确为合成，不冒充生产数据或真实外部账号验收。这个任务检验中间结果筛选/编排价值；它不是2文件加法的泛化结论。待 Bot 确认后固定完整记录、oracle和hash，再实现薄验收入口。

两路每个任务的输入、业务操作、shell/进程禁令、工作区边界、输出 JSON 与内容选择义务完全相同。native 走既有工具入口，JS 扩展走 js_exec；高频 fs 两路均可直接调用原生function，不强迫所有fs走JS。不要求workflow.js或保存脚本，inline/file能力分开。模型实际选择是否编排及生成JS均保留，不能硬编码替代其脚本。先描述已知name与tools.call(name,args)形状，只有未知名才search，空候选分支安全。

统一现有 magpie@local/magpie/codex/gpt-6-luna 参数（medium/max_output_tokens32768，精确配置摘要在冻结时再核对）；stream=true，每次生成独立600秒预算，不另套更短总context/HTTP timeout/首事件截止，原生取消有效。每Turn最多24次模型生成（含selector），两任务不共用600秒。无自动重试/换模型，不调整credits，不接生产Session，不GUI（共享桌面仍由DTW119使用）。

采集与预先判读：

- 业务：原工具调用/参数/receipt、实际读写计数与文件oracle；unknown不重放，所有失败保留。API unavailable单列。
- 往返：每Turn、每配对分别列主请求、selector请求和合计，另列外层/子工具调用数；不要只看外层js_exec次数。
- tokens：provider实际prompt/cached/fresh/output，缺项记不可用。小任务目标是不增加主请求与fresh；编排任务检验相同业务下能否降低主请求/合计往返与fresh，绝对值和百分比逐Turn/逐对报告。两类目标未达如实记，不追加样本或事后放宽。native fresh=0时相对值N/A，绝对值仍列。
- 前缀：保存每Session实际 instructions/tools SDK与HTTP字节并核对稳定性。只有provider提供对应分项时才给实际token归因；缺分项就记prefix tokens unavailable。不能把字节换成token，也不能把公共JSON前缀当KV缓存证明。独立Session不保证provider缓存隔离。
- 输出成本：保存原子工具返回、投影后进入下一模型请求的工具结果bytes/形状及各后续请求实际usage。区分全Journal与模型选定输出；下一请求总token变化不是某一结果的因果归因。

运行门禁：先冻结精确新产品head、新harness SHA、完整prompts/输入/oracle/模型参数/顺序/次数/工具Schema及hash并由Bot读回；原fair A清单/样本保留。当前只有预方案，没有执行清单；本文件不启动真实请求。
