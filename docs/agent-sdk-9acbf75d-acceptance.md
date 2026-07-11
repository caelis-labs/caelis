# Agent SDK Candidate Re-acceptance

Status: **stable-dependency candidate implementation repaired after independent
rejection of `30ee5f02`; candidate tag and release evidence deferred**.

This note re-accepts the repaired worktree after the independent rejection at
`30ee5f02` (which itself followed the earlier rejection at `43d89bc5`). It does
not alter the frozen
[v0.25.0 acceptance](agent-sdk-v0.25.0-acceptance.md), and it does not authorize
a tag, push, or release. The live item-by-item evidence is maintained in the
[stabilization checklist](agent-sdk-stabilization-checklist.md).

## Verdict

All seven P0 correctness blockers and all eight P1 implementation blockers are
closed with failure-specific tests for the interleavings found in the
`30ee5f02` rejection. The Agent SDK remains a package tree in the root Go module
and is independently consumable through its supported package surface. ACP
remains the native built-in/external glue protocol; Control or the user remains
the only handoff authority; no LLM handoff tool, deterministic workflow engine,
nested module, or transcript-as-model-truth path was added.

The implementation is ready to become a candidate once the final gate index below
is re-run against a clean HEAD. A new candidate tag and the same-tag release run
are intentionally deferred until separately authorized.

## Closure matrix

| ID | Status | Failure-specific evidence |
| --- | --- | --- |
| P0-1 | closed | Explicit allow is the only execution path; malformed and unknown decisions fail closed |
| P0-2 | closed | Durable/public recursive values do not share mutable descendants |
| P0-3 | closed | Source-revision compaction CAS/replay, two-Runtime shared-store interleavings, committed file-lease outcomes, and fenced built-in/ACP cancellation |
| P0-4 | closed | Transaction identity prevents repeated event-derived deltas; provider ToolCall IDs are scoped per invocation; PLAN digest binds explanation and entries; overflow rebind shares one journal step counter so reused `ollama-call-0` cannot reissue `tool-step-1` |
| P0-5 | closed | Atomic canonical synthetic result plus terminal journal is recovered into model context without blind retry |
| P0-6 | closed | Idempotent delivery and committed-error reread wake the live approval waiter |
| P0-7 | closed | Invalid anchors fail before durable `spawned`; compensation resumes across cancel/terminal/detach; ACP children key by TaskID; AgentID is restart-stable from TaskID |
| P1-1 | closed | Controller and subagent permission bridges preserve RawInput/RawOutput/Content through the final runtime approval |
| P1-2 | closed | Source is audit-only; projection and narrative behavior use native stream and canonical protocol semantics |
| P1-3 | closed | StartSubagent, Compact, Continue, and Wait use one leased placement guard with heartbeat and cancel-on-loss; Continue records parent intent before remote effect; soft watchdog remains Runner-scoped; remote-success/parent-final dual-write residual documented |
| P1-4 | closed | Control derives and validates model, tool, and sandbox execution requirements |
| P1-5 | closed | System agents reuse Runtime safety/lifecycle; dynamic watchdog; transient detach is deadline-bounded |
| P1-6 | closed | Raw JSON migrates before typed decode; unknown fields and tag-to-tag supported API compatibility are gated (28 reviewed waivers) |
| P1-7 | closed | Supported-only behavioral quickstart resolves exact `v0.25.0` without replace through one proxy into an isolated empty module cache; pipe and comma fallbacks fail closed |
| P1-8 | closed | Release waits for same-SHA quality and non-empty race/regression/link/proxy gates; real new-tag evidence is deferred |

## Repair commits after `30ee5f02`

The local repair slice after the second independent rejection addresses:

- overflow-retry journal identity continuity;
- spawn validation-before-commit and ACP TaskID/AgentID isolation;
- controller permission RawOutput/Content preservation;
- unified lease heartbeat/cancel-on-loss for Run and ExecutePlaced, Continue/Wait fencing;
- spawn single task builder, Stack.withPlaced, continuation rollback helper;
- proxy `|direct` rejection and transient detach timeouts;
- checklist/acceptance/boundary evidence sync (this document).

Earlier repair commits indexed from the first re-acceptance remain:

- `c781f08c` classifies committed file-lease acquire/heartbeat/release outcomes.
- `55a4a582` preserves lease fencing during built-in and ACP cancellation.
- `19c4b18e` scopes provider-local tool identities per invocation.
- `a5fe612a` makes spawn compensation and restart recovery resumable.
- `01373491` binds PLAN transactions to the complete persisted state.
- `55804315` preserves permission semantics and ACP-safe recovery statuses.
- `89c9deeb` makes event Source audit-only.
- `aa63499c` places synchronous Control operations behind runtime fencing.
- `1589c0bd` requires isolated, proxy-only public consumer evidence.

## Final gate index

The re-acceptance run must record all of the following against the same clean
HEAD before a candidate tag is considered:

- full selected Agent SDK/control/ACP `go test -race -count=1`;
- `make regression` with all five selectors reporting non-empty test lists;
- `make docs-links`, `make arch-lint`, and `make sdk-boundary-check`;
- isolated `SDK_PROXY_VERSION=v0.25.0 SDK_PROXY_URL=https://proxy.golang.org make sdk-proxy-smoke`;
- `make commit-check`, `make release-dry-run`, and `git diff --check`.

Passing these gates establishes implementation readiness. It is not a release:
no new tag or artifact publication occurs without explicit authorization.
