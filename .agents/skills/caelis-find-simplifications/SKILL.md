---
name: caelis-find-simplifications
description: Find evidence-backed simplifications and architecture-convergence candidates in Caelis. Use for explicit package or repository surveys of dead code, duplicate authority or data paths, speculative APIs, central orchestration growth, pass-through facades, compatibility residue, or hand-rolled infrastructure; do not auto-run during ordinary change review and do not edit or create issues unless requested.
---

# Find Caelis Simplifications

Turn a broad simplification request into a few bounded candidates that remove
real surface area or restore one semantic owner. Keep the survey read-only
unless the user separately authorizes implementation.

## Establish The Search Domain

1. Confirm the requested packages, subsystems, or repository breadth and inspect
   the live worktree before searching.
2. Read applicable `AGENTS.md` instructions. Discover current contracts,
   generators, package comments, tests, and gates dynamically; do not assume a
   remembered documentation layout.
3. Evaluate the code against Caelis's long-term semantic direction, not merely
   its current physical packages. Treat transitional locations and facades as
   migration evidence rather than precedent.
4. For broad work, divide the survey by independent semantic domains and keep
   production paths distinct from tests, examples, generated artifacts, and
   prose.

## Find Strong Candidates

Prefer candidates where evidence shows that cost exceeds current value:

- two APIs, feeds, replay paths, permission paths, identities, caches, or state
  machines mirror the same fact;
- a central composition or orchestration object gains feature-specific branches
  instead of delegating to a coherent owner;
- a public method, option, event, fallback, wrapper, or package has no production
  consumer, while tests or prose only preserve its existence;
- Host-scoped authority, activated Session Runtime state, and stateless assembly
  logic are coupled through one concrete object;
- an adapter depends on composition internals instead of focused Control
  contracts, or a Surface owns runtime, replay, persistence, or policy semantics;
- a compatibility path lacks an active reader need, owner, scope, or removal
  condition;
- a maintained dependency or standard-library facility can delete a hand-rolled
  implementation and its dedicated tests without relocating the same complexity.

Start with the largest or highest-change production surfaces when the request
asks for breadth. Use `rg` to classify exact symbols, wire strings, config keys,
and dynamic registrations, then read every relevant call site.

## Prove Or Reject

For each candidate:

1. Separate production consumers from tests, examples, generated artifacts,
   documentation, and ambiguous runtime discovery paths.
2. Name the current semantic owner and the desired owner. Explain the failure,
   duplicated authority, or credible extension cost caused by the current form.
3. Calculate net reduction: code, API, state, tests, and prose removed minus new
   glue. A forwarding wrapper or package move with equal complexity is not a win.
4. State behavior or compatibility given up and why that trade remains valid.
5. Reject or downgrade the idea when a production caller exists, a maintained
   contract still justifies it, the change only improves tree aesthetics, or
   unrelated churn overwhelms the reduction.

## Report

Rank a small candidate set by correctness risk, authority reduction, extension
cost, and implementation size. For each include evidence locations, consumer
classification, target ownership, bounded deletion or consolidation, behavior
given up, acceptance evidence, and dependencies on other candidates.

Do not manufacture a quota, create a permanent roadmap, add TODOs, open issues,
or implement candidates unless the user asks for that state change.
