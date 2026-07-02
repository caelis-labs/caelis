# Architecture Review

Use `.agents/skills/caelis-deep-review` for recurring architecture and
technical-debt review.

## Review Questions

- Is this presentation, control, or Agent Runtime / SDK responsibility?
- Is a surface consuming `eventstream.Envelope`, or reaching into
  runtime/control internals?
- Is orchestration accumulating in a central file instead of a coherent module?
- Are `ports/*` contracts mixing runtime SDK and app-control semantics?
- Does `impl/*` depend on presentation or controller details?
- Are model-visible facts stored as canonical session payloads?
- Would the design scale to Guardian, Reviewer, Agent Manage Loop, and external
  ACP-agent collaboration?

## Priority

- `P0`: correctness, security, data loss, replay/persistence, or model-context
  breakage.
- `P1`: boundary drift that blocks likely near-term extension.
- `P2`: useful cleanup with lower immediate risk.

Each P0/P1 finding should include file:line, failure mode, why it matters, and a
bounded repair direction.

## Validation

- Before commits: `make commit-check`.
- Before release: `make release-dry-run`.
- For focused implementation slices, run targeted `go test` packages before the
  broad gate.
