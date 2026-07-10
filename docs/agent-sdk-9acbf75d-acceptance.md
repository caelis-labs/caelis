# Agent SDK `9acbf75d` Local Candidate Acceptance

Status: **stable-dependency implementation ready locally; candidate-tag and
published-release evidence not yet available**.

This note reviews the stabilization candidate whose last implementation/CI
commit is `9acbf75d`. The documentation commit containing this note is not a
new release candidate. No tag, push, pull request, or release was created.
The [v0.25.0 acceptance](agent-sdk-v0.25.0-acceptance.md) remains frozen history.

## Findings first

The reproduced P0 failures are closed by committed, failure-specific tests.
Durable identities are restart-safe; session leases fence every Runtime-owned
mutation; approval committed-error recovery is cancellation-independent; tool
recovery has one canonical status; the subagent spawn path is a CAS-claimed,
roll-forward saga; and compound transaction retries bind identity to semantic
digests. Exact model-context rebuild tests cover the persistence/replay changes.

The P1 production gaps are also closed locally. Lease/watchdog decorators retain
streams, live attachment, approval, and participant capabilities; participant
prompts use the same placement envelope as main runs; Source is audit-only and
principals fail closed; ACP coordination uses shared semantic owners;
RUN_COMMAND declares async sessions; API compatibility uses a rolling prior
release; current-source and tagged consumers are separate; and regression
selectors cannot pass with zero tests.

Operational evidence is deliberately narrower. The published `v0.25.0`
artifact passed the revised tagged consumer smoke, but no new candidate tag
exists. Therefore P1-7 and P1-8 are closed as implementation mechanisms while
their next-candidate tag/proxy/workflow evidence is deferred to release time.

## Final matrix

| ID | Status | Closing evidence |
| --- | --- | --- |
| P0-1 Policy fail-closed | closed, retained | malformed/missing/unknown policy decisions remain denied; `c2349278` |
| P0-2 Recursive value isolation | closed, retained | nested session/context/task values and rollback isolation; `c2349278` |
| P0-3 Session/compaction concurrency | closed | restart-safe identities `d5e1fb36`; lease identity/fencing and shared-store replay `1cf73e53` |
| P0-4 Compound commit idempotency | closed | semantic transaction digest, stale-revision retry ordering, conflict tests; `bbdaf21c` |
| P0-5 Tool unknown-outcome continuity | closed | succeeded/failed/unknown journal-event-model agreement and exact replay; `3149f872` |
| P0-6 Approval committed-error liveness | closed | cancel-after-commit memory/file adapter interleaving, matching/conflicting retry; `eecb2e37` |
| P0-7 Subagent spawn saga | closed | CAS-only external spawn, every durable phase, committed-error/restart/unknown tests; `e85b803c` |
| P1-1 ACP semantic completeness | closed locally | permission request/response plus cancel/participant/handoff conformance and construction lint; `879e58f4` |
| P1-2 Control ownership completion | closed locally | Source-invariance and empty/unknown-principal rejection; `bd2c3135` |
| P1-3 Durable continuation and placement | closed locally | optional-capability passthrough and fenced participant watchdog envelope; `aa906e04` |
| P1-4 Execution capability wiring | closed locally | RUN_COMMAND CommandExec+AsyncSessions preflight and deep clone; `57a2de1a` |
| P1-5 Runtime liveness/observability | closed locally | prior watchdog/trace/guardrail work plus participant/decorator production coverage; `f5cce303`, `d2a8ee11`, `9dc683a0`, `aa906e04` |
| P1-6 Schema/API compatibility | closed locally | raw migration retained; rolling previous-tag baseline and exact waiver tests; `2fd67d36`, `9acbf75d` |
| P1-7 Public consumer contract | mechanism closed; next-tag evidence deferred | current worktree quickstart and `v0.25.0` tag-owned proxy fixture both pass; `9acbf75d` |
| P1-8 Release enforcement | mechanism closed; next-tag evidence deferred | same-SHA dependency retained; named non-empty race/regression/docs/proxy gates contract-tested; `9acbf75d` |

## Exact failure sequences and fixes

### P0 concurrency, recovery, and saga failures

- Fresh Runtime instances previously reused counter-derived durable IDs. The
  default generator now uses cryptographic identities for runs, turns, pause
  tokens, participant work, store events, and controller epochs. Three fresh
  file-backed Runtimes and repeated rebuilds complete without journal collision,
  and live/rebuilt `[]model.Message` values compare equal (`d5e1fb36`).
- A same-owner second acquisition previously shared and could release the first
  lease; an expired owner could also write after takeover. Every acquisition is
  unique and receives a persistent monotonic fence. Memory/file append, batch,
  compound, controller, and participant mutations validate it atomically. A
  non-cooperative stale success is rejected and the new owner recovers
  `unknown_outcome` (`1cf73e53`).
- Resolver cancellation immediately after a committed approval could suppress
  recovery and waiter delivery. Confirmation now uses `context.WithoutCancel`
  with a bounded timeout, redelivers only the matching durable decision, and
  rejects a conflict (`eecb2e37`).
- Empty `RecoveryResult.Result` previously produced model status
  `unknown_outcome` even when recovery proved success/failure. Minimal canonical
  payloads now derive status from `RecoveryStatus`; only unknown carries the
  no-blind-retry instruction (`3149f872`).
- Spawn could duplicate under an Upsert-only store or compensate after
  irreversible canonical dialogue. Spawn now requires `task.CASStore`, claims a
  durable phase before the external call, reloads committed errors, and rolls
  forward after canonical commit (`e85b803c`).
- Compound retries previously remembered only a boolean transaction ID. Stores
  now persist and compare mutation/event digests before expected-revision CAS;
  changed semantics and legacy bool-only ambiguity fail closed (`bbdaf21c`).

### P1 composition and dependency failures

- Production decorators previously erased `StreamProvider` and other optional
  interfaces, while participant prompts used raw Runtime. Both decorators now
  preserve the required small interfaces and track the outer live runner;
  SessionControl receives the watchdog-decorated runtime (`aa906e04`).
- `spawn`/`acp` substrings in Source previously changed visibility/echo and an
  empty task principal became Controller. Neutral role/kind now owns behavior;
  Source remains provenance and empty/unknown principals are rejected
  (`bd2c3135`).
- External ACP permissions and built-in coordination previously had parallel
  mappings. Permission ingress/response use `protocol/acp/semantic`, SDK session
  owns participant/handoff constructors, and arch-lint blocks new direct
  coordination literals (`879e58f4`). Handoff construction remains a fact
  format; only Control commits transfer.
- RUN_COMMAND used async `sandbox.Runtime.Start` without declaring
  `AsyncSessions`, and cloned definitions shared requirements. Declarations,
  preflight tests, and deep cloning now match execution (`57a2de1a`).
- A fixed `v0.25.0` API comparison missed an API added in one release and
  removed in the next. The gate selects the latest reachable previous tag,
  skipping an exact candidate tag; exact/stale/duplicate waiver behavior is
  tested (`9acbf75d`).
- The proxy gate previously mixed an old module with the current fixture. The
  worktree gate now compiles the current fixture; the proxy gate extracts the
  target tag's own fixture/package snapshot and verifies exact no-replace
  resolution (`9acbf75d`).
- TUI regression regexes previously matched nothing. Every group now lists
  matched tests before execution and rejects empty/unmatched selectors; the two
  TUI groups execute their current real regression names (`9acbf75d`).

## Validation evidence

Focused slice validation included `gofmt`, `git diff --check`, relevant
`go test -count=1`, concurrency-sensitive `-race`, `make arch-lint`, and
`make sdk-boundary-check`. Persistence slices include memory/file conformance,
fault interleavings, whole-object comparison, and live/rebuilt model-context
round trips.

Before this note, the following broader gates passed:

- `go test -race -count=1 ./agent-sdk/session ./agent-sdk/runtime`
- `go test -race -count=1 ./internal/controlplane ./protocol/acp/semantic`
- `make arch-lint`
- `make sdk-boundary-check`
- `make regression` (all five groups printed non-empty test lists)
- `make sdk-proxy-smoke` against `v0.25.0` via
  `https://proxy.golang.org,direct`, exact version and no replace
- `git diff --check`

The final handoff must additionally record `make docs-links`, `make
commit-check`, and `make release-dry-run` results after documentation is
committed. A snapshot dry run is local build evidence, not tagged/published
operational evidence.

## Deferred P2 and operations

- Extract coherent modules when future work again touches the large subagent,
  ACP manager, or architecture-lint files; no unrelated rewrite is justified.
- Continue observing the previously non-reproduced participant transient test
  flake.
- Run release snapshots with repository-external cache/temp roots when parallel
  jobs could clean `.tmp/cache/gotmp`.
- At the next release, capture candidate-tag quality workflow, exact proxy,
  assets, checksums, and published module evidence. Until then, do not describe
  this local candidate as a published stable release.
