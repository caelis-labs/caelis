# Agent SDK Stabilization Checklist

Status: **stable-dependency candidate readiness rejected at `43d89bc5`;
blocking P0 and P1 work reopened**.

This is the live execution board for work after `v0.25.0`. The full evidence,
failure interleavings, and frozen verdict are in
[Agent SDK v0.25.0 Acceptance Review](agent-sdk-v0.25.0-acceptance.md).
The current evidence index is
[Agent SDK 9acbf75d Acceptance](agent-sdk-9acbf75d-acceptance.md).

An item is closed only when its specific failure sequence has a regression,
fault, race, or model-context round-trip test. Existing broad green gates are
necessary but are not sufficient closing evidence.

Status values:

- `open`: the acceptance invariant is not implemented;
- `in progress`: one bounded implementation slice is under verification;
- `partial`: useful mechanisms exist but the acceptance invariant still fails;
- `closed`: the exact invariant and failure interleaving have committed tests;
- `deferred`: an explicit architecture decision narrows the contract and records
  the remaining risk and owner.

## P0 stable-dependency blockers

| ID | Status | Exit condition |
| --- | --- | --- |
| P0-1 Policy fail-closed | closed | Only explicit allow can execute; all malformed, missing, registry, and unknown decisions fail closed |
| P0-2 Recursive value isolation | closed | Durable/public values cannot share mutable nested descendants; failed mutations roll back |
| P0-3 Session/compaction concurrency | closed | Checkpoint replay/CAS and shared-store fencing remain covered; file lease committed outcomes return durable revisions, and built-in/ACP cancellation preserves the active lease fence |
| P0-4 Compound commit idempotency | closed | Provider-local tool IDs are scoped by run/turn/step while canonical pairing keeps the raw ID; PLAN transaction digests cover entries and persisted explanation |
| P0-5 Tool unknown-outcome model continuity | closed | Unknown side effects produce a canonical result paired with the original call, are visible after replay, and are reconciled without blind execution |
| P0-6 Approval committed-error liveness | closed | A durable matching resolution always wakes a live waiter, including `session.CommittedError` and idempotent retry paths |
| P0-7 Subagent spawn saga | closed | Compensation resumes across cancel/phase/detach failures; spawning restart becomes durable unknown outcome; identity binds full spawn semantics; invalid anchors fail before participant commit; ACP cancel failures propagate |

## P1 stability blockers

| ID | Status | Exit condition |
| --- | --- | --- |
| P1-1 ACP semantic completeness | partial | External and controller/subagent permission bridges must use one lossless semantic codec; recovery tool status must map to valid ACP wire enums |
| P1-2 Control ownership completion | partial | Source must be audit-only, including projection suppression; system Agents continue to reuse the common Runtime pipeline |
| P1-3 Durable continuation and placement | partial | StartSubagent and manual Compact must enter through the same leased/watchdog placement envelope as ordinary production runs |
| P1-4 Execution capability wiring | closed | Control derives and validates actual model, tool, and sandbox requirements; unsupported output/features do not silently degrade |
| P1-5 Runtime liveness and observability | closed | Watchdog/TraceSink/guardrail bounds remain covered, and production built-in/ACP cancellation durably persists under lease fencing before non-cooperative work returns |
| P1-6 Schema and compatibility | closed | Raw durable JSON migrates before typed decode and unknown-field corpus proves preservation; supported API is compared tag-to-tag with explicit waivers |
| P1-7 Public consumer contract | partial | Proxy evidence must use a clean isolated module cache and a proxy-only route so cached or direct fallback resolution cannot pass |
| P1-8 Release enforcement | partial | Same-SHA/non-empty workflow mechanics remain closed, but the consumer sub-gate needs strict proxy evidence and a real candidate tag remains deferred |

## Execution Order

Use small, independently committable slices:

1. P0-6 approval waiter liveness.
2. P0-3 compaction revision/CAS and covered-sequence replay.
3. P0-4 compound transaction idempotency and stable runtime identities.
4. P0-5 canonical unknown-outcome recovery and `tool.Recoverer` wiring.
5. P0-7 subagent spawn intent, compensation, and recovery.
6. ACP/Control/system-Agent contract completion.
7. Control watchdog, capability wiring, schema/API compatibility, and release
   enforcement.

### Independent re-acceptance at `43d89bc5`

An independent fault review rejected the local candidate despite green broad
gates. Reproduced blockers are:

- file lease acquire/heartbeat commits can be reported as ordinary errors,
  leaving live leases or cancelling healthy runs;
- production cancellation writes discard the active lease fence;
- provider-local tool-call IDs collide across fresh Runtime instances;
- subagent compensation is not restart-safe across cancel, terminal, and
  detach boundaries, and spawn identity/anchor validation is incomplete;
- PLAN compound digests omit the persisted explanation mutation;
- permission mapping remains duplicated/lossy, recovery emits invalid ACP tool
  statuses, Source still affects projection, placement has raw-engine bypasses,
  and the proxy smoke can pass from a shared cache with network disabled.

The historical implementation evidence below describes mechanisms that remain
useful, but it no longer constitutes closing evidence for the reopened rows.

### Reopened P0 repair evidence

- **P0-3 file lease committed-outcome sub-slice:** acquire and heartbeat now
  translate a document rename followed by index/sync failure into
  `session.CommittedError` while returning the exact durable lease revision;
  release uses the same committed classification. File fault tests reload the
  committed lease, release heartbeat revision 2 without a stale-revision
  conflict, and prove a committed release removed the lease. The Control
  placement wrapper accepts a committed Acquire only when the returned or
  durably reread lease matches the requested session/owner and has a usable
  revision/fence; committed heartbeats no longer cancel a healthy run.
- **P0-3/P1-5 cancellation-fencing sub-slice:** every built-in and ACP cancel
  hook derives an uncancelled context from the original leased run context,
  preserving its mutation guard. A real production `LeasedRuntime` test holds
  a non-cooperative Agent after cancellation and observes durable
  `cancel_requested` before it returns; the live external ACP controller path
  proves the same ordering and fence preservation.
- **P0-4 scoped tool-identity sub-slice:** canonical event idempotency uses
  run/turn/ordinal scope while preserving the provider ToolCall ID inside the
  paired model call/result. Tool-execution journals use a per-run step ordinal
  in addition to the raw call ID. Two fresh file-backed Runtimes can therefore
  persist separate `ollama-call-0` turns, one turn can use that same provider ID
  in consecutive tool steps, and a third Runtime rebuilds both paired facts in
  model context. P0-4 remains partial until the PLAN transaction digest covers
  its complete persisted state mutation.
- **P0-7 recoverable-compensation sub-slice:** compensation first persists
  intent, then proves child cancellation, persists that proof, atomically
  detaches any participant, and only then writes terminal `compensated`.
  Restart tests cover cancel success followed by phase-write failure and
  terminal/detach boundaries without respawn or roll-forward. A restart from
  `spawning` CAS-transitions to durable `unknown_outcome`; the spawn request
  digest binds Agent, prompt, context prelude, effective mode, approval mode,
  parent call, and participant role. Anchor/result validation rejects missing
  participant identity before attachment and compensates the external child.
  The production ACP runner now returns remote Cancel notification failures and
  records an interrupted/unknown local state instead of claiming cancellation.
- **P0-4 PLAN mutation-digest sub-slice:** PLAN compound transactions encode
  the complete versioned plan state—entries and explanation—into the explicit
  mutation digest rather than relying on non-persisted `Event.Text`. Memory and
  file regressions prove an identical retry deduplicates, while the same
  transaction/entries with a different explanation returns
  `EventConflictError` and leaves the original state intact.

### Historical P1 implementation evidence

- **P1-1:** `protocol/acp/semantic` owns permission request/response and cancel
  wire conversion plus participant/handoff lifecycle conversion. Both the
  event projector and ACP Runtime bridge use that permission codec. Exact
  wire/normalized round trips cover nested tool identity, raw input/content,
  allow/reject outcomes, cancellation identity, participant lifecycle, and
  handoff lifecycle. Runtime participant production, external ACP manual
  permission and cancellation, and Control-owned atomic handoff commit have
  conformance regressions against those normalized semantics.
- **P1-2 source-policy slice:** SDK subagent role and task-control authorization
  now consume neutral `session.ParticipantRole` and `session.ActorKind` values.
  Product `Source` strings are audit provenance only and cannot change role,
  model-context visibility, or control authorization. Unknown roles are
  rejected before spawn and unknown principals fail closed. Guardian model
  attempts now execute through Core Runtime in isolated staging sessions, with
  guardrails, typed Run/Turn/Model lifecycle, capability validation, and
  terminal Run/Turn journals. Only validated prompt/assistant pairs enter the
  reusable durable Guardian session, preserving malformed-retry isolation.
- **P1-3 live-attach/placement contract slice:** the ambiguous `Resume` API was removed.
  `AttachLiveRun` now names and documents the actual process-local contract;
  after restart it returns `RunNotAttachableError` and never treats durable
  journal state as a replay point. Memory and file stores now implement the
  same lease CAS contract, including cross-instance conflict, heartbeat
  revision, release, and expiry takeover. The production Gateway uses a
  Control-owned wrapper that holds the lease for the full asynchronous Runner
  lifetime and cancels execution if heartbeat ownership is lost. Every
  acquisition has a distinct identity and monotonic fencing token; Runtime
  writes are checked atomically by memory/file stores. Stream, live-attach,
  approval, and participant capabilities survive lease/watchdog decoration,
  and participant prompts use the same fenced watchdog envelope as main runs.
- **P1-4 execution-requirements slice:** built-in tools declare their concrete
  sandbox dependencies, and the production Control host derives their union
  from the final augmented tool set. After surface request defaults are merged,
  Control validates the actual model and sandbox descriptors before Runtime is
  started; Runtime retains the same model/output checks as a defensive SDK
  boundary. Unknown output modes, schema mode without a schema, duplicate or
  malformed tools, and undeclared model/sandbox capabilities fail closed. The
  unimplemented `tool.Resumable` and `OutputModeToolOnly` declarations were
  removed before v1 rather than advertising behavior with no consumer.
- **P1-5 non-blocking trace sub-slice:** lifecycle trace delivery is now
  observer-only asynchronous work. Each lifecycle preserves start/terminal
  ordering, slow or panicking sinks cannot hold the execution path, and a
  process-wide outstanding cap bounds permanently stuck sink calls; saturated
  telemetry is dropped rather than backpressuring model, tool, approval, or
  handoff execution.
- **P1-5 guardrail-liveness sub-slice:** Runtime normalizes a nil caller context
  before session or guardrail work. Non-cooperative guardrails retain their
  outstanding slot after timeout until they actually return, and each Runtime
  has a hard outstanding-call cap. Once saturated, later invocations fail with
  `resource_exhausted` (or follow an explicitly configured fail-open policy)
  without spawning another leaked goroutine.
- **P1-5 dynamic-watchdog sub-slice:** the production Control host wraps each
  local run with a dynamic reviewer that observes elapsed/no-progress time,
  typed Runtime lifecycle status, canonical provider usage, and repeated
  normalized tool signatures from either Runner event view. Soft thresholds
  request a Control review rather than enforcing an SDK step count. Reviews can continue, append
  an idempotent durable journal checkpoint, or checkpoint and cancel; cancel is
  ignored unless the reviewer explicitly records confirmation. The default
  production policy checkpoints soft-threshold evidence and does not
  auto-cancel. Confirmed cancellation, declined confirmation, timer-only
  stalls, canonical `SourceEvents`, and checkpoint-before-cancel ordering have
  dedicated regressions.
- **P1-6 raw-migration sub-slice:** file event-log replay and committed WAL
  recovery now migrate `json.RawMessage` before decoding `session.Event`.
  Event, journal, run/turn/step, tool-execution, and pause-token schemas advance
  independently, including the current-event/legacy-nested-journal case. A raw
  corpus proves top-level and every nested journal sentinel survives migration;
  file round-trip coverage proves the migrated journal remains outside exact
  canonical model history.
- **P1-6 tag-to-tag API sub-slice:** `scripts/sdk_api_compat` parses declaration
  snapshots and automatically compares the current supported API with the most
  recent reachable prior release tag (skipping the current candidate tag).
  Additions pass; every removed or changed old declaration requires an
  exact package plus SHA-256 waiver and a concrete reason. Missing, duplicate,
  ambiguous, and stale waivers fail. The 18 reviewed pre-v1 changes cover the
  honest live-attach rename, spawn/transaction identities, neutral task
  principals/roles, capability cleanup, independent nested schemas, and
  execution requirements. Quality checkout now fetches tags and
  `sdk-boundary-check` runs this gate on the same candidate snapshot.
- **P1-7 supported-consumer slice:** the executable quickstart now imports only
  the root Agent contracts plus supported `model` and `session` packages; a
  regression parses its imports against the allowlist. `sdk_proxy_smoke.sh`
  creates a clean external module, imports all 16 supported packages, executes
  the behavioral quickstart, and verifies the resolved Caelis module has the
  exact `v0.25.0` version and no `replace`. The current-source gate separately
  compiled the worktree quickstart, so a new supported API is not tested
  against an old tag fixture. The baseline smoke passed against
  `https://proxy.golang.org,direct`; no new candidate tag was created.
- **P1-8 release-enforcement slice:** `quality.yml` is now reusable and records
  focused Agent SDK race, regression, maintained-document link, and tagged
  no-replace consumer gates. `release.yml` invokes that workflow at the caller
  SHA, supplies the candidate tag to the consumer smoke, and makes every
  publish step wait on its success. Every regression selector must list at least
  one real test before execution; an empty or unmatched selector fails. Workflow
  contract regressions and the link checker have focused tests; no tag or
  release was created.

Do not combine unrelated P0s into one broad rewrite. Update this board in the
same commit as the closing evidence. Do not edit the frozen v0.25.0 acceptance
record to make an item look closed.

## Historical Implementation Record

The first stabilization implementation spans `ba814f51..5579efa5`. It added the
mechanisms summarized in the acceptance review, and all repository gates passed
at the release commit. The earlier 18-slice self-reported log was removed from
this live board because commit subjects and broad green gates did not prove the
failure interleavings found during independent acceptance. Git remains the
authoritative slice history.

## Non-negotiable Guardrails

- Keep `agent-sdk` in the root module; do not add a nested module or adapter
  module to simulate independence.
- Keep ACP semantic ownership flowing SDK -> product wire/projection.
- Only Control or explicit user action may authorize controller handoff.
- Do not add a deterministic workflow graph/executor or an LLM-facing handoff
  tool.
- Persist canonical model/tool/task facts; do not promote UI transcript,
  protocol mirrors, or undocumented `_meta` into model truth.
- Persistence and replay changes require whole-model-context round-trip tests.
