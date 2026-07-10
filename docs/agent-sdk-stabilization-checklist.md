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
| P0-4 Compound commit idempotency | partial | A committed-but-reported-error retry cannot apply an event-derived state delta twice; runtime facts needed for retry have stable identities |
| P0-5 Tool unknown-outcome model continuity | partial | Unknown side effects produce a canonical result paired with the original call, are visible after replay, and are reconciled without blind execution |
| P0-6 Approval committed-error liveness | closed | A durable matching resolution always wakes a live waiter, including `session.CommittedError` and idempotent retry paths |
| P0-7 Subagent spawn saga | open | Spawn intent and identity are durable; task/binding lifecycle is consistent; post-spawn failures compensate or persist unknown outcome; restart never blindly respawns |

## P1 stability blockers

| ID | Status | Exit condition |
| --- | --- | --- |
| P1-1 ACP semantic completeness | partial | Permission, cancel, participant, and handoff have one normalized codec path and built-in/external conformance, matching the completed update codec |
| P1-2 Control ownership completion | partial | Surface/source strings are translated by Control into neutral SDK owner/principal/role values; system Agents reuse the common Runtime safety pipeline |
| P1-3 Durable continuation and placement | partial | Contract is either safe checkpoint/lease-based continuation or explicitly live-process attachment; production host exercises session lease lifecycle |
| P1-4 Execution capability wiring | partial | Control derives and validates actual model, tool, and sandbox requirements; unsupported output/features do not silently degrade |
| P1-5 Runtime liveness and observability | partial | Control-owned dynamic watchdog exists; TraceSink cannot block execution indefinitely; stuck guardrails are bounded |
| P1-6 Schema and compatibility | partial | Raw durable JSON migrates before typed decode and unknown-field corpus proves preservation; supported API is compared tag-to-tag with explicit waivers |
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
