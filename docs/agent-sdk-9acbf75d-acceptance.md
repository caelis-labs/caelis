# Agent SDK `9acbf75d` Candidate Re-acceptance

Status: **stable-dependency candidate readiness rejected at `43d89bc5`**.

This note supersedes the earlier local self-assessment of candidate
`9acbf75d`. Independent fault testing was performed against the documentation
HEAD `43d89bc5`. No tag, push, pull request, or release was created. The
[v0.25.0 acceptance](agent-sdk-v0.25.0-acceptance.md) remains frozen history;
the live repair state is tracked in the
[stabilization checklist](agent-sdk-stabilization-checklist.md).

## Verdict

The root-module Agent SDK package boundary is real, and dependency, build,
race, regression, link, and release-snapshot gates pass. Those broad gates do
not cover several reproducible persistence, fencing, identity, and recovery
interleavings. The candidate is therefore not ready to tag or describe as a
stable dependency layer.

Only four P0 rows are fully closed. P0-3 and P0-7 are open; P0-4 is partial.
Several P1 mechanisms are useful but do not yet satisfy their end-to-end
contracts.

## Current matrix

| ID | Status | Independent conclusion |
| --- | --- | --- |
| P0-1 Policy fail-closed | closed | Explicit allow remains the only execution path |
| P0-2 Recursive value isolation | closed | Durable/public nested values remain isolated |
| P0-3 Session/compaction concurrency | open | File lease post-commit errors are misclassified; production cancel drops lease fencing |
| P0-4 Compound commit idempotency | partial | Tool-call identity is provider-local; PLAN digest omits persisted explanation |
| P0-5 Tool unknown-outcome continuity | closed for model continuity | ACP projection status remains a P1 defect |
| P0-6 Approval committed-error liveness | closed | Matching durable approval still wakes the waiter |
| P0-7 Subagent spawn saga | open | Compensation and spawning recovery are not restart-safe; identity and anchor validation are incomplete |
| P1-1 ACP semantic completeness | partial | Permission mapping is duplicated/lossy; recovery emits invalid ACP tool statuses |
| P1-2 Control ownership completion | partial | Source still changes projection suppression |
| P1-3 Durable continuation and placement | partial | StartSubagent and manual Compact bypass the leased/watchdog envelope |
| P1-4 Execution capability wiring | closed locally | No independent counterexample reproduced |
| P1-5 Runtime liveness/observability | partial | Cancel fencing failure prevents durable cancellation under production leases |
| P1-6 Schema/API compatibility | closed locally | No independent counterexample reproduced |
| P1-7 Public consumer contract | partial | Shared module cache plus `direct` fallback permits false-positive proxy evidence |
| P1-8 Release enforcement | partial | Same-SHA/non-empty mechanics hold; proxy sub-gate is incomplete and real-tag evidence is deferred |

## Reproduced blocking findings

### P0: file lease committed outcomes

The file document store can rename the durable document and then fail index or
directory synchronization with a private committed-write error. Lease acquire
and heartbeat do not translate that outcome to `session.CommittedError` or
return the durable lease. Acquire can therefore leave an unreported live lease;
a committed heartbeat can be treated as lease loss and cause a healthy run to
cancel, after which release with the stale revision conflicts.

### P0: cancellation loses the lease fence

Production Runtime and ACP cancellation paths use `context.Background()` when
persisting `cancel_requested`. Under the real leased Runtime this discards the
active fencing value and reliably returns `session.ErrLeaseConflict`.
Cooperative work may later write a terminal cancellation, but a
non-cooperative execution has no durable cancellation request.

### P0: tool-call identity is not session-global

Runtime event IDs use raw provider ToolCall IDs such as `tool_call:<id>` and
`tool_result:<id>`. Fresh Runtime instances in one session can receive the same
provider-local ID and the second turn conflicts. Ollama makes this deterministic
because each response starts at `ollama-call-0`.

### P0: spawn compensation is not recoverable

Compensation currently performs child cancel, terminal saga persistence, and
participant detach in an order that cannot recover every crash boundary.
Terminal-before-detach can strand a participant; cancel-before-terminal can
later roll a cancelled child forward; `spawning` restart remains prepared
instead of unknown. Spawn identity omits semantic request fields, empty or
invalid anchors are accepted, and the production ACP runner can discard remote
cancel errors.

### P0: PLAN digest omits persisted mutation

PLAN explanation is persisted by the compound state callback but removed from
the event representation used to compute the transaction digest. Reusing one
transaction and entries with a different explanation incorrectly deduplicates
instead of returning a conflict.

## P1 residual findings

- External subagent and controller bridges still hand-map permissions and lose
  `RawOutput` or `Content` instead of using one lossless semantic codec.
- Recovery statuses such as `succeeded` and `unknown_outcome` are projected as
  ACP tool statuses even though the wire enum accepts only `pending`,
  `in_progress`, `completed`, and `failed`.
- Gateway Source prefixes still suppress projection, so Source is not purely
  audit provenance.
- StartSubagent and manual Compact call the raw engine outside the production
  leased/watchdog placement envelope.
- The proxy smoke inherits a shared module cache and permits `direct`; after a
  cached run it succeeds with `SDK_PROXY_URL=off`, so it proves exact version
  and no replace but not proxy consumability.

## Gate evidence and limitation

Independent revalidation passed `make commit-check`, fresh focused race runs,
all five non-empty regression groups, documentation links, architecture lint,
SDK boundary checks, six-platform release snapshot/checksum generation, and
`git diff --check`. The repository was clean at `main@43d89bc5`, ahead of its
remote, with no tag at HEAD.

These results prove the exercised gates, not the missing fault interleavings.
Candidate acceptance must remain rejected until the reopened rows have focused
regressions and their fixes pass the complete gates again.

## Repair order

1. Correct lease committed-outcome recovery, cancellation fencing, and scoped
   tool identities.
2. Make spawn compensation/recovery restart-safe and bind/validate the entire
   spawn request.
3. Include the full PLAN state mutation in the compound digest.
4. Converge ACP permission/status semantics, audit-only Source behavior,
   placement envelopes, and strict isolated proxy evidence.

Do not create a candidate tag or claim stable-dependency readiness before this
matrix is re-accepted from failure-specific evidence.
