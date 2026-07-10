---
name: review
description: Use when asked to review code changes, patches, diffs, pull requests, staged work, unstaged work, or workspace changes for correctness, regressions, maintainability, code bloat, and design smells.
metadata:
  short-description: Strict code review of changes
---

# Review

Use this skill for code review of a change set (diff, PR, staged/unstaged/untracked, or scoped workspace edits).

Default to analysis only. Do not edit code unless explicitly asked.

## Stance

Be strict, direct, and ambitious about quality.

- Findings-first. Do not rubber-stamp "looks fine" when structure got messier.
- Hunt real defects **and** maintainability regressions: bloat, smells, wrong-layer logic, and missed simplifications.
- Prefer a few high-conviction findings over a long list of nits.
- If behavior can stay the same while the design gets clearly simpler, say so and push for that path.
- Look for "code judo": reframes that delete whole branches, helpers, modes, or layers instead of polishing the same complexity.

Do not be rude, but do not soften major maintainability issues into mild suggestions.

## Priority Order

1. Correctness, security, data loss, replay/persistence, model-context, or permission breakage.
2. Missed simplification that would delete substantial incidental complexity.
3. Spaghetti growth: special-case branches bolted onto busy paths.
4. Boundary / abstraction / ownership drift that will block near-term extension.
5. File-size and decomposition problems (especially crossing ~1k lines without cause).
6. Test gaps for risky behavior.
7. Legibility nits only if nothing higher remains.

## What To Flag Aggressively

- Bugs, regressions, and untested risky paths.
- Changes that make an existing flow harder to reason about even if tests pass.
- New ad-hoc conditionals, one-off flags, or feature checks scattered into shared code.
- God-file growth; large files absorbing unrelated logic that should be a nearby module.
- Thin wrappers, identity abstractions, cast-heavy or optional-heavy contracts that hide the real invariant.
- Duplicated helpers when a canonical one already exists.
- Logic in the wrong layer (surface/control/runtime/SDK/product wire).
- Prompt or agent guidance that trains bad habits (over-escalation, bypasses, scenario catalogs).
- Partial-state updates or non-atomic multi-step persistence where a cleaner atomic path is obvious.

## What Not To Do

- Do not flood the review with pure style nits when structural issues exist.
- Do not demand abstractions "for the future" with no present complexity win.
- Do not rewrite the author's approach unless a simpler structure is clearly better.
- Do not make code changes during review unless explicitly asked.
- Do not default to JSON.

## Method

1. Establish scope: which files and behaviors actually changed.
2. Read surrounding owners, contracts, and nearby tests before judging unfamiliar code.
3. Check failure modes: cancel, error, empty, concurrency, permission, replay.
4. Check structure: is this the simplest honest design, or complexity rearranged?
5. Check validation: focused tests for the changed risk; note unrun critical coverage.

## Output Format

Start with findings, ordered by severity (`P0` / `P1` / `P2` or Critical / High / Medium / Low).

For each finding include:

- location (`file:line` or symbol when line is unavailable)
- failure mode or design smell
- why it matters (user-visible, system-visible, or maintenance blast radius)
- smallest practical fix or simplification direction

Then a short residual section:

- open questions only if they material change the verdict
- unrun tests or validation gaps
- if no findings: say so clearly, plus residual risk

Keep the summary secondary to findings. End with top 1-3 next slices by ROI/risk when multiple issues exist.

## Approval Bar

Do not approve merely because behavior seems correct. Treat these as presumptive blockers unless clearly justified:

- the change preserves a lot of incidental complexity when a simpler reframe is available
- a file grows past a healthy size boundary without decomposition
- ad-hoc branching tangles an already busy flow
- feature logic leaks into a shared path
- unnecessary wrappers/casts/optionality obscure the contract
- tests miss the main risk the change introduces
