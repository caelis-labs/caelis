---
name: caelis-deep-review
description: Review Caelis code for bugs, regressions, architecture drift, misplaced ownership, duplicated authority or data paths, extension bloat, and dead code. Use for read-only review of Caelis changes, especially cross-layer or high-risk work; do not use as implementation guidance or as part of the generic /review workflow.
---

# Caelis Deep Review

Report findings first. Default to read-only analysis and keep the review
proportional: inspect local changes locally, and trace wider only when risk or
ownership crosses a boundary.

## Evidence

Read `docs/architecture.md`, then only the normative document relevant to the
change. Treat implementation, package comments, tests, and enforced dependency
rules as evidence; report a maintained document that disagrees with them.

- `docs/agent-sdk-boundary.md` for SDK, Runtime, handoff, or orchestration;
- `docs/acp-projection-architecture.md` for ACP/eventstream or Surfaces;
- `docs/control-convergence.md` for Control ownership, clients, feeds, or
  app-server direction;
- `scripts/arch_lint.go` when dependency rules matter;
- `docs/release.md` only for release readiness.

## Review

1. Find concrete bugs and regressions first, especially in concurrency,
   persistence, replay, permission, sandbox, cancellation, and lifecycle paths.
2. Identify the semantic owner before judging placement. Flag behavior added
   to the wrong layer when it creates a second authority or bypasses the
   intended dependency direction.
3. Look for duplicate APIs, feeds, replay paths, state mirrors, caches,
   permission paths, identities, compatibility fallbacks, and sources of
   durable truth.
4. Assess extension cost: central-file branching, pass-through wrappers,
   special modes, speculative abstractions, and hidden cross-layer coupling.
5. Check cleanup completeness: dead code, obsolete compatibility paths,
   duplicate helpers or contracts, and stale tests or documentation.

Distinguish pre-existing debt from regressions introduced by the reviewed
change. Do not turn architectural taste into a finding without a concrete
failure mode or credible extension cost. Prefer deletion or reuse of an
established path over a broad refactor.

For high-risk work, trace the smallest production-shaped path end to end and
identify missing risk-specific test evidence. Do not run broad release gates
unless the review scope or user asks for them.

## Findings

- `P0`: correctness, security, data loss, replay/model-context corruption,
  permission/sandbox failure, or user-visible lifecycle breakage.
- `P1`: wrong-layer ownership, duplicated authority or data path, coupling, or
  bloat that blocks near-term extension.
- `P2`: useful cleanup with lower immediate risk.

For each P0/P1 include file:line, failure mode, impact, and a bounded repair.
Prefer a few high-confidence findings over a long nit list. If there are no
findings, say so and name the evidence checked and residual limitations.
