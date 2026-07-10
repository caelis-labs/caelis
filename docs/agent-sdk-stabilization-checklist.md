# Agent SDK Stabilization Checklist

Status: **reopened after v0.25.0 acceptance**.

This is the live execution board for work after `v0.25.0`. The full evidence,
failure interleavings, and frozen verdict are in
[Agent SDK v0.25.0 Acceptance Review](agent-sdk-v0.25.0-acceptance.md).

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
| P0-3 Session/compaction concurrency | closed | Checkpoint persistence uses the source revision; replay keeps every fact with `Seq > summarized_through_seq`; two shared-store Runtimes are coordinated by lease/CAS |
| P0-4 Compound commit idempotency | closed | A committed-but-reported-error retry cannot apply an event-derived state delta twice; runtime facts needed for retry have stable identities |
| P0-5 Tool unknown-outcome model continuity | closed | Unknown side effects produce a canonical result paired with the original call, are visible after replay, and are reconciled without blind execution |
| P0-6 Approval committed-error liveness | closed | A durable matching resolution always wakes a live waiter, including `session.CommittedError` and idempotent retry paths |
| P0-7 Subagent spawn saga | closed | Spawn intent and identity are durable; task/binding lifecycle is consistent; post-spawn failures compensate or persist unknown outcome; restart never blindly respawns |

## P1 stability blockers

| ID | Status | Exit condition |
| --- | --- | --- |
| P1-1 ACP semantic completeness | closed | Permission, cancel, participant, and handoff have one normalized codec path and built-in/external conformance, matching the completed update codec |
| P1-2 Control ownership completion | closed | Surface/source strings are translated by Control into neutral SDK owner/principal/role values; system Agents reuse the common Runtime safety pipeline |
| P1-3 Durable continuation and placement | closed | Contract is either safe checkpoint/lease-based continuation or explicitly live-process attachment; production host exercises session lease lifecycle |
| P1-4 Execution capability wiring | closed | Control derives and validates actual model, tool, and sandbox requirements; unsupported output/features do not silently degrade |
| P1-5 Runtime liveness and observability | closed | Control-owned dynamic watchdog exists; TraceSink cannot block execution indefinitely; stuck guardrails are bounded |
| P1-6 Schema and compatibility | closed | Raw durable JSON migrates before typed decode and unknown-field corpus proves preservation; supported API is compared tag-to-tag with explicit waivers |
| P1-7 Public consumer contract | partial | A behavioral quickstart uses only supported imports, or required reference packages are explicitly supported; actual tagged module passes a no-replace consumer smoke |
| P1-8 Release enforcement | partial | Publish waits for quality on the same SHA and CI records focused race, regression, link, and proxy-consumer evidence |

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

### P1 closing evidence

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
- **P1-3 live-attach contract slice:** the ambiguous `Resume` API was removed.
  `AttachLiveRun` now names and documents the actual process-local contract;
  after restart it returns `RunNotAttachableError` and never treats durable
  journal state as a replay point. Memory and file stores now implement the
  same lease CAS contract, including cross-instance conflict, heartbeat
  revision, release, and expiry takeover. The production Gateway uses a
  Control-owned wrapper that holds the lease for the full asynchronous Runner
  lifetime and cancels execution if heartbeat ownership is lost.
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
  snapshots and compares the current supported API with the `v0.25.0` release
  tag. Additions pass; every removed or changed old declaration requires an
  exact package plus SHA-256 waiver and a concrete reason. Missing, duplicate,
  ambiguous, and stale waivers fail. The 18 reviewed pre-v1 changes cover the
  honest live-attach rename, spawn/transaction identities, neutral task
  principals/roles, capability cleanup, independent nested schemas, and
  execution requirements. Quality checkout now fetches tags and
  `sdk-boundary-check` runs this gate on the same candidate snapshot.

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
