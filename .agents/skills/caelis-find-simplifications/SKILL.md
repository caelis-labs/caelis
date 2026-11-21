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

1. Follow each live route from registration and dispatch to its observable
   consumers. Include indirect registrations, external SDK contracts, and old
   wire or durable readers. Record uncertain consumers as unresolved.
2. Name the current and proposed owner. Compare inputs, defaults, identity,
   authority, lifetime, ordering, and observable output before calling two
   paths equivalent. In particular, compare frozen declarations with live
   admission, business results with model and display projections, and effect
   execution with observation. Share a decoder only as far as its callers
   agree, preserving distinctions such as absent, invalid, and explicit zero.
3. Account for deleted branches, state, API, tests, and prose against new glue.
   A live consumer supports consolidation when it can use the surviving owner
   with the same contract. Removal needs evidence that the behavior has no
   supported consumer or that its retirement is authorized. Defer when a
   maintained contract, unresolved consumer, or integration cost outweighs
   the reduction.

## Verify An Authorized Change Before Merge

For each affected contract, pick an observation at the production entry point
and identify what a wrong deletion would break. Capture the baseline and use
the same probe after the change. Select focused coverage for the affected cases:

- Admission, refresh, or scheduling. Exercise a replaced or revoked target,
  callback name precedence, or the ordered and concurrent sequences that must
  still work. Check actual dispatch counts and order as well as returned results.
- Results or compatibility. Follow representative success, error, and legacy
  data to the affected readers. Where relevant, check durable/model round trips
  and rendered output, including mixed content, redaction, and combined status.
- Model-facing instructions or generated examples. Check the assembled prompt
  budget alongside the behavioral guidance, and execute advertised syntax
  through the real adapter when it changes.

Use [caelis-validate-change](../caelis-validate-change/SKILL.md) for gate
selection. Check that evidence covers the final diff against the intended base,
including fixes and integration changes. In a PR, check the final head, the
base used for integration, required checks, and review findings. Reverify any
affected evidence when the code or integration base moves.

## Learn From Review And Repair

Compare the initial diff, review or failing check, corrective diff, and the
regression evidence. Identify the assumption or missing observation that let
the change through. A fix belongs in the owning code and test. Update this
skill when requested and the evidence exposes a reusable decision or
verification gap. Tighten an existing rule where possible, then try it on a
real candidate to see whether it changes the selection or the evidence.
Keep case history in the PR or Git. Reuse an existing gate for recurring
validation, and keep fixture repairs and one-off environment workarounds local.

## Report

Rank a small candidate set by correctness risk, authority reduction, extension
cost, and implementation size. For each include evidence locations, consumer
classification, target ownership, bounded deletion or consolidation, behavior
given up, acceptance evidence, and dependencies on other candidates.

Do not manufacture a quota, create a permanent roadmap, add TODOs, open issues,
or implement candidates unless the user asks for that state change.
