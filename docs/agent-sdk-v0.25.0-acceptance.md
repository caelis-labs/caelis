# Agent SDK v0.25.0 Acceptance Review

Status: **release accepted; stable-dependency readiness not accepted**.

- Review date: 2026-07-10
- Release tag: `v0.25.0`
- Release commit: `5579efa5b42c97114593fe3b02c531629e08c57c`

This is the frozen acceptance record for the stabilization work implemented
after baseline `ba814f51`. It separates three questions that earlier documents
mixed together:

1. Was the Caelis release published correctly?
2. Were the intended architecture boundaries implemented?
3. Is `agent-sdk` safe to declare a stable dependency layer?

The answers are respectively **yes**, **mostly**, and **not yet**.

The live follow-up board is
[Agent SDK Stabilization Checklist](agent-sdk-stabilization-checklist.md). The
normative architecture remains in
[Agent SDK Boundary](agent-sdk-boundary.md); this review does not redefine it.

## Executive Verdict

`v0.25.0` is a substantial hardening release. It establishes the root-module
SDK boundary, fail-closed policy behavior, recursive value isolation, session
revision and event sequence contracts, file-store WAL transactions, durable
execution journals, API governance, ACP semantic ownership, and Control-owned
handoff coordination.

Those mechanisms are real and their existing suites pass. Acceptance also
found failure interleavings that those suites do not cover. Five P0 conditions
still block treating the SDK as a stable persistence/runtime dependency:

- a compaction checkpoint can hide a concurrent event that it did not
  summarize;
- an idempotent compound retry can apply its state callback twice;
- tool recovery records `unknown_outcome` in the journal but not in the next
  model context;
- an approval committed with an unknown reporting outcome can leave the live
  waiter asleep forever;
- subagent spawn and parent attachment form an uncompensated, non-idempotent
  multi-store saga.

Green build, race, regression, and release checks therefore prove the tested
paths, but do not override the failed semantic invariants below.

## Release and Gate Evidence

### Published release

| Check | Result | Evidence |
| --- | --- | --- |
| Git identity | pass | `HEAD`, `origin/main`, and `v0.25.0^{}` resolve to `5579efa5b42c97114593fe3b02c531629e08c57c` |
| GitHub Release | pass | Non-draft, non-prerelease [v0.25.0 release](https://github.com/caelis-labs/caelis/releases/tag/v0.25.0) with six platform archives and `checksums.txt` |
| GitHub quality | pass | [quality run 29085285050](https://github.com/caelis-labs/caelis/actions/runs/29085285050) succeeded for the release commit |
| GitHub publish | pass | [release run 29085293963](https://github.com/caelis-labs/caelis/actions/runs/29085293963) succeeded for the release commit |
| npm | pass | `@caelis/caelis@0.25.0` and all six OS/architecture packages are published with provenance; optional dependencies are pinned to `0.25.0` |
| Go proxy | pass | `github.com/caelis-labs/caelis@v0.25.0` resolves to the release commit |
| Asset smoke | pass | A downloaded archive matched its published checksum and reported `v0.25.0` plus the expected commit |

### Local acceptance gates

The clean release commit passed:

- `make commit-check`;
- `go test -race ./agent-sdk/policy/... ./agent-sdk/session/... ./agent-sdk/runtime/...`;
- `make regression`;
- `make release-dry-run`, including all six GoReleaser targets.

The release evidence has three CI enforcement gaps:

1. `.github/workflows/release.yml` can publish without waiting for a successful
   quality run for the same SHA. For this release, quality and release ran
   concurrently.
2. Focused race, regression, documentation-link, and cross-platform SDK tests
   are not release-gated CI jobs. A checklist claim that they ran locally is not
   CI evidence.
3. `sdk-boundary-check` compiles a local-replace consumer. It does not smoke the
   actual tag from the Go proxy without a `replace` directive.

These are P1 release-process gaps, not evidence that the published v0.25.0
assets are malformed.

## Previous Blocker Acceptance

| ID | Acceptance | What is confirmed | What remains |
| --- | --- | --- | --- |
| P0-1 policy fail-open | **closed** | Missing decider, registry failure, unknown profile, empty/unknown action, and invalid constraints fail closed; only explicit allow reaches a tool | No current authorization bypass found |
| P0-2 mutable value isolation | **closed** | `agent-sdk/internal/jsonvalue` owns recursive validation/clone; session, context, task, policy, approval, and durable store paths have nested-isolation and rollback tests | `tool.CloneResult` shallow-copies `[]model.Part`; public helper cleanup remains P2 |
| P0-3 session concurrency and compaction | **partial / P0** | Event `Seq`, session `Revision`, append CAS, same-Runtime run conflict, compaction coverage, and store lease DTOs exist | Runtime/compaction do not consistently carry revision or a cross-runtime lease; replay cuts by checkpoint position rather than covered sequence |
| P0-4 atomic persistence and idempotency | **partial / P0** | File WAL makes the tested event/document/state transactions crash-atomic; event ID/key dedupe works | A deduplicated compound retry still invokes `UpdateState`; ordinary runtime facts do not all have stable retry identities |
| P0-5 tool side-effect unknown outcome | **partial / P0** | Durable execution states, effect classes, cancellation-request distinction, and crash detection exist; Runtime does not mechanically replay the call | Recovery writes only a journal fact, so model replay removes the unmatched tool call and loses the unknown outcome; `tool.Recoverer` is not invoked |
| P1-1 ACP and Control ownership | **partial** | Update semantics have one SDK owner and one product wire codec; assembly, routing, endpoint lifecycle, and handoff commit moved to Control | Permission/cancel/participant/handoff conformance is incomplete; SDK subagent code still interprets Caelis surface source strings |
| P1-2 durable run, approval, recovery | **partial** | Run/Turn/Step/PauseToken journals and typed interrupted recovery exist | Approval has the P0 liveness defect below; `Resume` only reattaches a continuation live in the current process; leases have no production caller |
| P1-3 public API governance | **partial** | Sixteen supported imports, an API declaration snapshot, external-module compilation, and dependency closure are enforced | The snapshot is not a tag-to-tag compatibility check; the quickstart imports bundled paths outside the allowlist; the supported surface remains large |
| P1-4 runtime safety and observability | **partial** | Bounded single-consumer events, capability DTOs, lifecycle interceptors, TraceSink, and ordered guardrails exist | Capability wiring is incomplete; TraceSink can block; non-cooperative guardrails leak goroutines; no Control watchdog exists; system Agents bypass the Runtime pipeline |
| P1-5 schema and compatibility | **partial** | Versioned durable records, adjacent migrations, typed errors, and a v0/v1 replay corpus exist | File replay decodes into a typed Event before migration, so unknown JSON fields are already lost despite the documented preservation rule |

## Open P0 Details

### P0-3: compaction can omit a concurrent unsummarized event

Evidence:

- compaction reads a snapshot, generates a checkpoint, then appends it without
  `ExpectedRevision` in `agent-sdk/runtime/compaction_runtime.go`;
- ordinary runtime append also omits revision CAS in
  `agent-sdk/runtime/runtime_turn.go`;
- `compact.PromptEventsFromLatestCompact` selects the highest-coverage
  checkpoint but returns events by slice position after that checkpoint in
  `agent-sdk/runtime/compact/compact.go`;
- the active-run conflict map in `agent-sdk/runtime/runtime.go` is local to one
  Runtime instance, and `SessionLeaseService` is not used by Runtime.

Failure sequence:

1. Runtime A reads events with sequences 1 through 10 and summarizes them.
2. Runtime B appends event 11.
3. Runtime A appends checkpoint 12 with `summarized_through_seq=10`.
4. Replay chooses checkpoint 12 and keeps only array entries after it.
5. Event 11 is newer than the checkpoint coverage but appears before the
   checkpoint in storage, so it disappears from model context.

Acceptance criteria:

- persist a checkpoint with CAS against the revision used to create it, then
  retry from a new snapshot or abandon on conflict;
- rebuild after a checkpoint by `Seq > SummarizedThroughSeq`, independent of
  array position;
- define and exercise the host lease/CAS rule for two Runtime instances sharing
  one session;
- add a model-context round-trip test for the exact interleaving above.

### P0-4: compound idempotent retry can apply state twice

`PrepareEventsForAppend` correctly recognizes an identical event retry and
returns no newly persisted event. `PrepareAppendTransaction` nevertheless calls
`UpdateState` unconditionally and increments the session revision. A first
commit that returns `session.CommittedError`, followed by a same-identity retry,
can therefore produce one event but apply an incremental state callback twice.

Acceptance criteria:

- give the compound transaction a stable identity, or otherwise prove whether
  its event and state mutation were already applied;
- never rerun an event-derived state delta when every input event deduplicated;
- keep a separately identified pure-state transaction possible;
- add memory and file fault tests for committed-but-reported-error retry;
- assign stable identities to runtime user/tool/plan/compact facts needed for
  safe caller retry.

### P0-5: unknown tool outcome is absent from model truth

`recoverIncompleteToolExecutions` appends a journal-visible
`unknown_outcome`. It does not append a canonical tool result. On the next
turn, `normalizeToolCallHistory` drops the unmatched tool call, so the model
does not learn that a non-idempotent side effect may already have happened. A
model may independently issue the same action again even though Runtime did not
mechanically replay it.

Acceptance criteria:

- atomically persist a canonical synthetic tool result paired with the original
  tool-call ID and its terminal journal transition;
- include effect class, `unknown_outcome`, and an explicit no-blind-retry
  instruction in model-visible semantics;
- call a configured `tool.Recoverer` when safe, retaining unknown on reconcile
  failure;
- compare the rebuilt whole model context with the live/recovered context and
  prove that a history-aware model does not blindly repeat the action.

### P0-6: committed approval can leave a live run asleep

`Runtime.ResolveApproval` appends the resolved PauseToken before it sends to the
live waiter. If the store commits and returns `session.CommittedError`, the
method returns before delivery. A same-decision retry reads an already-resolved
token and returns success without delivering it. The run waits until its parent
context is cancelled.

Acceptance criteria:

- make decision delivery an idempotent helper used by both the new-resolution
  and already-resolved branches;
- on `session.IsCommitted(err)`, reload the token and deliver when the matching
  decision is durable;
- add a file-store post-commit fault test with a real live approval waiter.

### P0-7: subagent spawn is an uncompensated durable saga

`taskRuntime.StartSubagent` performs the external `runner.Spawn` side effect
before it has a durable parent task. It then persists the task entry, participant
binding, transient attached update, and optional canonical side-agent dialogue
in separate operations. Failure after any step returns an error without a stable
spawn transaction identity or guaranteed child cancellation. Retrying can
orphan or duplicate a live child and leave task/binding/history facts split.

The existing `ParticipantLifecycleService` is used for ACP participants but not
for this subagent path. Using it closes only the binding/lifecycle slice; the
spawn/task/binding saga still needs recovery or compensation.

Acceptance criteria:

- create a stable spawn operation identity and persist intent before the
  external side effect;
- use the atomic participant lifecycle capability for the parent binding and
  update, with revision CAS;
- compensate a failed post-spawn commit by cancelling the returned child
  anchor; persist `unknown_outcome` when termination cannot be proved;
- recover prepared/started spawn operations after restart without blind spawn;
- fault-test every boundary: before spawn, after spawn, after task persistence,
  after binding, and after canonical dialogue persistence.

## P1 Follow-up

The P0 work takes precedence. The following P1 items are still required before
the SDK is described as stable across local and cloud hosts:

1. **Complete ACP semantic conformance.** Centralize permission encode/decode in
   `protocol/acp/semantic` and add built-in/external conformance for permission,
   cancellation, participant lifecycle, and handoff. Keep ACP as the native
   glue protocol; do not replace it with four ADK-style top-level abstractions.
2. **Finish Control peel-off.** Control must translate surface origins into
   neutral owner/principal/participant-role values. SDK task code must not
   interpret `slash_agent`, `user_side_agent`, or similar product strings.
3. **Choose an honest continuation contract.** Either implement a safe durable
   checkpoint/lease-based continuation or rename/narrow `Resume` to live-process
   attachment. Never resume through an unknown side-effect boundary.
4. **Wire execution requirements.** Control should derive model, tool, and
   sandbox requirements and validate the actual configured instances. Remove or
   implement declarations such as tool resumability that have no consumer.
5. **Use one safety pipeline for system Agents.** Guardian/Reviewer currently
   call `AgentFactory.NewAgent(...).Run(...)` directly and bypass Runtime
   capability validation, journal, guardrail, lifecycle, trace, policy, and tool
   wrappers. A Control-owned system-agent executor may keep hidden-session
   policy, but it must reuse the common invocation safety pipeline.
6. **Implement a Control-owned dynamic watchdog.** Observe lifecycle, usage,
   repeated tool signatures, elapsed time, and progress. Support soft
   thresholds, user confirmation, checkpoint, and cancellation. Do not restore
   fixed SDK step budgets or introduce a deterministic workflow graph.
7. **Bound observers and guards.** A TraceSink must not block execution forever;
   non-cooperative guardrails need an outstanding-call cap or stuck circuit
   breaker, and context normalization must happen before guardrails run.
8. **Migrate raw durable JSON.** Apply migrations before typed unmarshal and add
   unknown-field sentinels at top-level and nested journal payloads.
9. **Tighten public compatibility.** Compare supported API against the previous
   release tag with explicit breaking-change waivers, and provide a behavioral
   quickstart using only supported imports or explicitly support the necessary
   reference packages.
10. **Make release quality a publish prerequisite.** Gate publishing on quality
    for the same SHA; add focused race/regression/link checks and a no-replace Go
    proxy consumer smoke.

## P2 Maintainability Inventory

- `agent-sdk/runtime/task_subagent.go` mixes task lifecycle, participant
  persistence, surface source policy, context, and projection in more than 1,100
  lines; extract the persistence coordinator and source-policy translation
  first.
- `internal/acpagentbridge/controller/manager.go` combines process/client
  lifecycle, controller and participant operations, reconnect, permission,
  model/mode/config, and status in more than 1,300 lines.
- `app/gatewayapp/approval_reviewer.go` combines prompt selection, transcript
  budgeting, JSON extraction, policy decision, and accounting in more than 1,100
  lines.
- `scripts/arch_lint.go` is a large sequential rule tree; split rules by owned
  boundary without weakening coverage.
- `tool.CloneResult` must deep-copy nested `model.Part` values before consumers
  rely on it as a public isolation helper.
- Remove misleading legacy package names and aliases from the pre-v1 supported
  surface when a clean replacement already exists.

These cleanups should follow concrete ownership seams and tests. They do not
justify a new abstraction layer or a broad rewrite.

## Readiness Exit Gate

Do not change this review's verdict to ready until all P0 acceptance tests pass
and the P1 contract is explicitly narrowed or implemented. The minimum evidence
is:

- two-Runtime shared-store CAS/lease and compaction interleaving tests;
- compound committed-error idempotency tests;
- live/rebuilt unknown-outcome model-context equality;
- approval committed-error waiter liveness;
- subagent spawn fault/recovery matrix;
- built-in/external ACP permission and lifecycle conformance;
- one supported-import behavioral consumer against the actual release tag;
- release publish gated on the same SHA's quality result.

## Next-stage Handoff Prompt

```text
在 caelis 仓库继续 Agent SDK 稳定化工作。先完整阅读 AGENTS.md、
docs/architecture.md、docs/agent-sdk-boundary.md、
docs/agent-sdk-v0.25.0-acceptance.md 和
docs/agent-sdk-stabilization-checklist.md，并使用 caelis-deep-review。

目标：关闭 v0.25.0 验收中所有 P0，再处理明确列出的 P1，使 agent-sdk
成为同一 root Go module 内可独立依赖的稳定 package 层；不要拆 module 或仓库。

不可变约束：
- ACP 继续作为 built-in/external Agent 的原生胶水协议；SDK 拥有可复用语义，
  protocol/acp 拥有产品 wire/compat/projection。
- handoff 只能由用户或 Control/Agent Manage Loop 决策和提交；不得添加 LLM-facing
  handoff tool。
- 不实现 deterministic workflow graph/node/edge engine，不照搬 ADK 的四类顶层抽象。
- 持久化 canonical model/tool/task facts，不把 UI transcript 或未声明 _meta 当模型真相。
- 保持 agent-sdk 对 app/surfaces/protocol/acp/product ports/root internal 的单向依赖禁令。

按可独立验收的切片推进，每个切片先写失败测试、再修实现、再提交：
1. P0-6 approval committed-error 唤醒：幂等 deliver，IsCommitted 后重读并唤醒 live waiter。
2. P0-3 compaction/concurrency：checkpoint revision CAS；replay 按 covered Seq；两个 Runtime
   共享 store 的 lease/CAS 与 round-trip 交错测试。
3. P0-4 compound idempotency：transaction identity；dedupe 时不重复应用 event-derived
   state delta；普通 runtime facts 使用稳定 retry identity。
4. P0-5 tool unknown outcome：原 call ID 配对的 canonical synthetic ToolResult 与 terminal
   journal 原子提交；接线 Recoverer；证明重建模型上下文可见且不会盲目重试。
5. P0-7 subagent spawn saga：持久化 intent、稳定 spawn identity、atomic participant lifecycle、
   失败补偿/unknown outcome、逐故障点恢复测试。

P0 全绿后再处理 P1：
- 完成 permission/cancel/participant/handoff 的 ACP semantic codec 与 built-in/external conformance；
- 将 slash/user_side_agent 等 source 字符串翻译移到 Control，SDK 只接收中性 owner/role；
- 明确 durable continuation：安全实现 checkpoint+lease，或将 Resume 收窄为 live attach；
- Control 统一推导并校验 model/tool/sandbox ExecutionRequirements；
- Guardian/Reviewer 复用同一 Runtime invocation safety/journal/lifecycle pipeline；
- 实现 Control-owned dynamic watchdog，支持软阈值、确认、checkpoint、cancel，不加固定 SDK
  step budget，不引入 workflow engine；
- TraceSink 非阻塞化，guardrail 增加 stuck/outstanding 保护，并先 normalize context；
- file replay 在 typed unmarshal 前迁移 raw JSON，补 unknown-field corpus；
- tag-to-tag API diff + waiver、supported-only behavioral quickstart、no-replace proxy smoke；
- release workflow 必须等待同 SHA quality，CI 补 focused race/regression/link checks。

不要因为旧测试全绿就把条目标 closed。每个 closed 必须有针对本次失败交错的 fault/race/
model-context round-trip 测试。保持改动小而完整，优先拆 task_subagent 的 persistence coordinator，
不要借机大重写。每个切片运行 gofmt、focused tests、必要的 -race、make arch-lint、
make sdk-boundary-check；P0 persistence/replay 切片还要 make regression，提交前统一运行
make commit-check 和 git diff --check。同步更新 checklist；不要改写这份冻结的 v0.25.0 验收记录。

最终交付：逐项 closed/partial/open 矩阵、关键 file:line、测试与门禁证据、剩余风险，以及每个
切片的 commit SHA。不要发布新 release，除非用户另行明确授权。
```
