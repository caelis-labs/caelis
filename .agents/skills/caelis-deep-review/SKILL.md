---
name: caelis-deep-review
description: Review Caelis changes read-only for bugs, regressions, architecture drift, misplaced ownership, duplicated authority or data paths, extension bloat, and incomplete cleanup. Use for local changes, commits, branches, or pull requests, especially cross-layer or high-risk work; do not use for implementation, broad simplification surveys, or generic review orchestration.
---

# Caelis Deep Review

Report findings first. Remain read-only and keep the review proportional:
inspect local changes locally, then trace wider only when risk or ownership
crosses a boundary.

## Establish The Scope

1. Confirm the repository, requested comparison, and whether the target is the
   worktree, staged changes, a commit range, a branch, or a pull request.
2. Inspect staged, unstaged, and untracked paths. Resolve the exact base and
   head for committed work; refresh them after a retarget, merge, or rebase.
3. Read the applicable root and subtree `AGENTS.md` instructions. Discover the
   current maintained contracts from repository navigation, package comments,
   generators, and executed gates instead of relying on remembered document
   filenames.
4. Treat current package placement as migration evidence, not automatic proof
   of the intended owner. Apply the long-term semantic direction in the
   repository instructions and report conflicts between code, prose, tests,
   and enforced dependency rules.

Do not fetch unrelated branches or run broad release gates merely to increase
the amount of evidence.

## Review

1. Find concrete bugs and regressions first, especially in concurrency,
   persistence, replay, permission, sandbox, cancellation, and lifecycle paths.
2. Trace both sides of each changed contract through the smallest real entry
   path. Check errors, cancellation, ownership, fencing, cleanup, and alternate
   callers that can bypass prompts, schemas, facades, or adapters.
3. Identify the semantic owner before judging placement. Flag a second
   authority, product API, data path, lifecycle controller, or durable source
   of truth; do not flag directory aesthetics alone.
4. Trace retained and derived state through caches, feeds, replay, projections,
   prompts, and UI views to the authoritative success point. Check that bounds
   cover the final emitted or retained result, including wrappers and metadata.
5. Inspect the exact prompts, tool schemas, diagnostics, Envelopes, and visible
   output affected by the change. Tests must exercise the shipped entry path
   and fail on the intended regression rather than restating implementation.
6. Check cleanup completeness: obsolete compatibility readers, fallbacks,
   wrappers, mirrors, helpers, tests, generated artifacts, and prose. A required
   compatibility path must name its owner, scope, and removal condition.

Classify each concern as introduced regression, pre-existing debt, or target
convergence gap. Only the first is automatically a finding against the change;
the others require a concrete failure mode, bypass, or credible extension cost.
Prefer deletion or reuse of an established path over a broad refactor.

For high-risk work, trace the smallest production-shaped path end to end and
identify missing risk-specific test evidence. Do not run broad release gates
unless the review scope or user asks for them.

## Findings

- `P0`: correctness, security, data loss, replay/model-context corruption,
  permission/sandbox failure, or user-visible lifecycle breakage.
- `P1`: wrong-layer ownership, duplicated authority or data path, coupling, or
  bloat that blocks near-term extension.
- `P2`: useful cleanup with lower immediate risk.

For each P0/P1 include file:line, failure mode, impact, evidence, and a bounded
repair. Keep inline locations on the tightest changed range; use a review-level
finding for cross-cutting ownership defects. Omit purely mechanical issues
already guaranteed by a passing gate. Prefer a few high-confidence findings
over a long nit list. If there are none, say so and name the scope, evidence
checked, and unrun physical, platform, or release gates.
