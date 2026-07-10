# Agent SDK Stabilization Checklist

Status: active from baseline `ba814f51`.

This is the durable execution checklist for the P0/P1 blockers in
[Agent SDK Boundary and Evolution Plan](agent-sdk-boundary.md). Update it in
the same commit as each completed slice. A blocker is `closed` only when its
required invariants, focused tests, and applicable gates are all recorded.

Status values:

- `open`: known work remains and no closing slice is under verification;
- `in progress`: implementation or required verification is incomplete;
- `closed`: all listed invariants have committed evidence;
- `blocked`: a reproducible external or architectural blocker is recorded.

## P0 release blockers

| ID | Status | Required closing evidence | Current evidence / next action |
| --- | --- | --- | --- |
| P0-1 Policy fail-open | closed | Missing decider, unknown profile, registry failure, empty/unknown action, and invalid constraints cannot execute; external policy conformance coverage; race/focused tests | Typed `policy.DecisionError` / `policy.ProfileError`, explicit-action normalization, and fail-closed runtime resolution. External-package conformance tests: `agent-sdk/policy/policy_test.go`; runtime resolution tests: `agent-sdk/runtime/runtime_test.go`. Slice 1 race and repository gates passed. |
| P0-2 Mutable value isolation | closed | One JSON-compatible validator/recursive clone owner; nested isolation for event tool input/output, metadata, state, context, task/result; failed-update rollback; race coverage | `agent-sdk/internal/jsonvalue` owns validation/clone semantics; session/context/policy/approval/task/tool/runtime payload paths use recursive isolation. Nested mutation, invalid value, and rollback tests cover session, memory/file stores, context, policy, task, and tool. Slice 1 race and repository gates passed. |
| P0-3 Session concurrency and compaction replay | closed | Monotonic event `Seq`, session `Revision`, expected-revision CAS, same-session conflict signal, compaction `summarized-through Seq`, highest-valid-coverage replay, lease/heartbeat adapter contract | Shared append preparation assigns monotonic `Event.Seq`, increments `Session.Revision`, and enforces typed revision conflicts. Runtime returns `RunConflictError` for a concurrent same-session run. Compaction records covered Seq and ignores later lower-coverage checkpoints. `SessionLeaseService` reserves cloud lease/heartbeat CAS without placement policy. Memory/file and compaction/runtime tests plus race and boundary gates passed. |
| P0-4 Atomic persistence and idempotency | in progress | Atomic event batch + state delta contract; file adapter has explicit degraded capability or safe transaction; stable event/idempotency-key dedupe/conflict; participant/PLAN/handoff compound mutations cannot split | Stable caller-provided Event IDs now dedupe identical retries or return typed conflict, including after file-store reopen. Remaining: replace file log/document crash window with an explicit WAL/transaction contract and close all compound-mutation fault paths. |
| P0-5 Tool side-effect unknown outcome | open | Durable Run/Turn/Step/ToolExecution journal and required state machine; stable execution key; effect class and optional recovery; crash recovery produces `UnknownOutcome`; cancellation request differs from termination | Implement after storage CAS/atomic contract. |

## P1 stability blockers

| ID | Status | Required closing evidence | Current evidence / next action |
| --- | --- | --- | --- |
| P1-1 ACP contract and Control ownership | open | One SDK semantic DTO owner; product wire only encodes/projects; product assembly/profile/process/UI/selection/handoff commit moved to Control; built-in/external conformance suite | Preserve normalized endpoint/controller/participant/cancel/permission/transfer values in SDK. Requires `make regression`. |
| P1-2 Durable Run, approval, and recovery | open | Durable Run/Turn/Step/PauseToken; `Resume(runID)` and `ResolveApproval`; endpoint reattach/recover or typed interrupted/unknown outcome; task revision/lease/heartbeat/CAS | Build on P0-3/P0-5 journal. |
| P1-3 Public API governance | open | Supported-package allowlist; internal helpers; narrow store/executor interfaces; consolidated approval/request/cancel contracts; external examples/contract tests/API diff gate; allowlist-based SDK boundary check | `agent-sdk/internal/jsonvalue` begins hiding implementation detail; the allowlist and compatibility gate remain open. |
| P1-4 Runtime safety, capabilities, observability | open | Run limits; model/tool/executor capability negotiation; typed lifecycle interceptors and read-only TraceSink; deterministic guardrail order/timeout/fail policy; bounded single-consumer event queue and defined close behavior | Audit after durable run semantics. |
| P1-5 Schema and compatibility | open | Versioned durable Event/Run/ToolExecution schemas and migrations; cross-version replay corpus with exact model-context equality; typed error codes; quickstart and platform/Go/concurrency/cancellation/persistence docs | Complete after durable schemas stabilize. |

## Slice log

| Slice | Scope | Focused verification | Broad gates | Commit |
| --- | --- | --- | --- | --- |
| 1 | P0-1 fail-closed policy + P0-2 recursive value isolation | `go test ./agent-sdk/...`; targeted failure tests added first | Focused race suite, `make arch-lint`, `make sdk-boundary-check`, `make commit-check`, and `git diff --check` passed | `fix: harden policy and JSON value isolation` |
| 2 | P0-3 Seq/Revision/CAS + compaction coverage replay | `go test ./agent-sdk/...`; memory/file CAS and idempotency, concurrent run, and out-of-order checkpoint tests | Focused race suite, `make arch-lint`, `make sdk-boundary-check`, `make commit-check`, and `git diff --check` passed | `fix: add session CAS and sequence contracts` |

## Non-negotiable guardrails

- Keep `agent-sdk` in the root module; never add a nested module or adapter
  module to simulate isolation.
- Keep ACP semantic ownership flowing SDK -> product wire/projection.
- Only Control or explicit user action may authorize controller handoff.
- Do not add a deterministic workflow graph/executor.
- Persist canonical model/tool/task facts; do not promote UI transcript,
  protocol mirrors, or undocumented `_meta` into model truth.
