---
name: caelis-maintain-docs
description: Write, review, trim, move, or audit maintained Caelis prose, including repository documentation, package comments, exported Go docs, tests, prompts, tool descriptions, diagnostics, visible CLI or TUI strings, and repository skills. Use for document placement, contract coverage, current-state wording, duplicated rationale, reasoning-transcript leakage, generated prose ownership, or link validation; review read-only unless edits are explicitly requested.
---

# Maintain Caelis Prose

Preserve the complete contract, give each fact one maintained owner, and remove
authoring-session narration. Length is a discovery signal, not a defect.

## Discover The Owner

1. Confirm the requested prose scope and write authority. Inspect the live
   worktree so unrelated user changes remain untouched.
2. Read applicable `AGENTS.md` instructions. Discover the current prose and
   contract owners through repository navigation, package comments, inbound
   links, generators, and executed gates; never assume a fixed documentation
   filename or hierarchy from memory.
3. Classify the fact by purpose before editing:
   - standing instruction needed in every task;
   - current architecture or repository map;
   - future ownership and convergence direction;
   - reusable package or public API contract;
   - protocol, persistence, or lifecycle contract;
   - testing or release procedure;
   - decision rationale or incident evidence.
4. Keep one authoritative home. Elsewhere retain only the locally required
   contract and link or refer to the owner without duplicating its explanation.
   If current sources disagree, report the conflict instead of silently choosing.

## Preserve Complete Propositions

Before trimming or rewriting, enumerate every relevant actor, action, condition,
timing, ordering, obligation, exception, negative guarantee, ownership rule,
side effect, failure mode, and consequence. Remove repetition and decoration
only when those facts survive and the result is clearer.

Add prose when types and code cannot safely express a caller-visible contract,
race ordering, durable or wire promise, security restriction, compatibility
condition, surprising failure, or extension rule. Do not restate obvious code.

## Write From The Repository View

Write maintained current-state prose that a reader at `HEAD` can verify without
a chat transcript, review thread, uncommitted plan, or private design ledger.

- Replace PR, commit, phase, audit-item, and reviewer choreography with the
  shipped rule or a durable issue reference.
- Replace change narration such as old/new implementation history with present
  behavior or a counterfactual failure that explains the invariant.
- Delete control-flow and test walkthroughs; retain only non-obvious contracts,
  fixture reasons, platform accommodations, and observable verification.
- Turn uncertain future work into an owned issue or actionable TODO only when
  the task authorizes it; otherwise report the gap.
- Keep migration plans and completed acceptance history out of maintained
  normative prose. Preserve compatibility removal conditions and known current
  limitations while they remain active.

Treat prompts, tool descriptions, diagnostics, and visible strings as behavior.
Inspect their final assembled output and require owning golden, snapshot, or
product-path evidence when wording changes semantics.

## Edit Owner First And Validate

Edit generators or source comments before derived catalogs, generated clients,
fixtures, or snapshots, then regenerate through the repository-owned path.
When moving or deleting prose, repair all inbound references atomically.

Run the narrow current gates for the touched surfaces, the repository's link or
generated-freshness checks, behavior coverage for visible text, and a diff
whitespace check. Report the scope, changed owners, deliberate keeps, conflicts,
generated artifacts, and exact checks run. Do not introduce translation,
documentation-site, archive, or decision-record machinery unless the repository
already owns that workflow or the user explicitly requests it.
