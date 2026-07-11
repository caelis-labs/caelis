# Agent SDK Stabilization Checklist

Status: **stable-dependency candidate implementation repaired after independent
rejection of `30ee5f02`; candidate tag and release evidence remain deferred**.

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
| P0-4 Compound commit idempotency | closed | Provider-local tool IDs are scoped by run/turn/step while canonical pairing keeps the raw ID; PLAN digests cover entries/explanation; overflow recovery reuses one journal step counter so retry cannot reissue `tool-step-1` for the same provider call ID |
| P0-5 Tool unknown-outcome model continuity | closed | Unknown side effects produce a canonical result paired with the original call, are visible after replay, and are reconciled without blind execution |
| P0-6 Approval committed-error liveness | closed | A durable matching resolution always wakes a live waiter, including `session.CommittedError` and idempotent retry paths |
| P0-7 Subagent spawn saga | closed | Validation fails closed before durable `spawned`; compensation resumes across cancel/phase/detach; ACP child runs key by TaskID; AgentID is restart-stable from TaskID |

## P1 stability blockers

| ID | Status | Exit condition |
| --- | --- | --- |
| P1-1 ACP semantic completeness | closed | External and controller/subagent permission bridges preserve RawInput/RawOutput/Content through the final runtime approval; recovery keeps durable status while projecting only valid ACP tool enums |
| P1-2 Control ownership completion | closed | Source is audit-only, including projection/narrative classification; system Agents continue to reuse the common Runtime pipeline |
| P1-3 Durable continuation and placement | closed | StartSubagent, Compact, Continue, and Wait use one leased placement envelope with heartbeat and cancel-on-loss; Continue records parent user intent before the remote child effect. Soft watchdog review remains Runner-scoped (not reimplemented for sync placed ops). Residual: remote-success/parent-final dual-write is not a full saga |
| P1-4 Execution capability wiring | closed | Control derives and validates actual model, tool, and sandbox requirements; unsupported output/features do not silently degrade |
| P1-5 Runtime liveness and observability | closed | Watchdog/TraceSink/guardrail bounds remain covered; production cancellation is fenced; transient detach honors a deadline so finish cannot block the event stream forever |
| P1-6 Schema and compatibility | closed | Raw durable JSON migrates before typed decode and unknown-field corpus proves preservation; supported API is compared tag-to-tag with explicit waivers |
| P1-7 Public consumer contract | closed | Exact-version/no-replace quickstart downloads into a fresh isolated module cache through one proxy with no direct/off/comma/pipe fallback |
| P1-8 Release enforcement | closed | Same-SHA/non-empty workflow and strict proxy consumer sub-gates are enforced; creating evidence for a new candidate tag remains an authorized release action and is deferred |

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
gates. Reproduced blockers are summarized in the historical evidence below and
were first repaired through `30ee5f02`.

### Independent re-rejection at `30ee5f02`

A second independent acceptance rejected `30ee5f02` even though standard gates
passed. Reopened/partial findings were:

- **P0-4:** overflow retry rewrapped tools with a fresh journal counter, so a
  reused provider-local `ollama-call-0` could reissue `tool-step-1`;
- **P0-7:** invalid anchors were persisted as `spawned` before compensation;
  ACP runner keyed children by remote SessionID; AgentID came from a process
  counter that restarts at `helper-001`;
- **P1-1:** controller permission reconstruction dropped RawOutput/Content;
- **P1-3:** `ExecutePlaced` held the lease without heartbeats; Continue/Wait
  bypassed placement; Watchdog passthrough lacked a placed lifecycle;
- **P1-7:** proxy smoke rejected comma fallback but not `|direct`;
- **P1-5:** transient finish hooks could block forever on detach;
- docs claimed closed rows while live evidence lagged.

### Repair evidence after `30ee5f02`

- **P0-4 overflow journal identity:** `runWithOverflowRecovery` shares one
  `atomic.Uint64` tool-step sequence across attempt rebinds. A regression wraps
  tools twice with the same sequence and proves `tool-step-1:ollama-call-0` and
  `tool-step-2:ollama-call-0` for the identical provider call ID.
- **P0-7 spawn validation before commit:** invalid anchors compensate without a
  durable `spawned` commit, so restart cannot roll-forward past validation. The
  invalid-anchor saga test asserts the durable status is never
  `spawned`/`committed`.
- **P0-7 ACP child isolation:** process-local child runs key by TaskID; lookup
  rejects SessionID mismatch; AgentID prefers the durable TaskID so restart
  cannot reissue a short counter ID over an old participant binding.
- **P1-1 controller permission codec:** controller approval reconstruction
  copies RawOutput and Content exactly as the subagent bridge already did.
- **P1-3 placement lifecycle:** one `sessionLeaseGuard` heartbeats both async
  runners and sync `ExecutePlaced` callbacks and cancels work on lease loss.
  Gateway Start/Continue/Wait/Compact share `withPlaced`. Soft watchdog review
  is not duplicated for placed ops (Runner event stream only). Continue appends
  parent user intent before remote child execution; final assistant dual-write
  remains a documented residual, not full spawn-style saga atomicity.
- **P1-7 proxy pipe fallback:** `sdk_proxy_smoke.sh` rejects `|` fallbacks; the
  workflow regression proves `https://127.0.0.1:1|direct` fails closed.
- **P1-5 transient detach timeout:** finish-hook detach uses a bounded context;
  `context.WithoutCancel` is no longer allowed to strip deadlines. A blocked
  backend test proves deadline-bounded return.

### Historical P0/P1 implementation evidence

Earlier sub-slices remain useful background and are retained below without
overriding the live matrix.

- **P0-3 file lease committed-outcome sub-slice:** acquire and heartbeat now
  translate a document rename followed by index/sync failure into
  `session.CommittedError` while returning the exact durable lease revision;
  release uses the same committed classification.
- **P0-3/P1-5 cancellation-fencing sub-slice:** every built-in and ACP cancel
  hook derives an uncancelled context from the original leased run context,
  preserving its mutation guard.
- **P0-4 scoped tool-identity / PLAN digest sub-slices:** canonical event
  idempotency uses run/turn/ordinal scope; PLAN digests cover entries and
  explanation.
- **P0-7 recoverable-compensation sub-slice:** compensation first persists
  intent, proves child cancellation, detaches, then writes terminal
  `compensated`.
- **P1-1..P1-8 historical slices:** permission codec ownership, Source
  audit-only, live-attach rename, capability wiring, watchdog soft review,
  raw migration, tag-to-tag API waivers (28 reviewed pre-v1 removals),
  isolated proxy consumer smoke, and same-SHA release workflow gates.

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
