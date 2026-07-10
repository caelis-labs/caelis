---
name: caelis-deep-review
description: Caelis workspace-private architecture and code-quality review. Use for long-term technical debt inventory, architecture boundary review, Go quality review, layering responsibility checks, extension-risk analysis, code bloat/smell scans, or changes touching presentation surfaces, control orchestration, Agent Runtime modules, ACP/eventstream projection, session persistence, tools, skills, model providers, Guardian/Reviewer agents, Agent Manage Loop direction, or external ACP agent collaboration.
---

# Caelis Deep Review

Review Caelis architecture and code quality from the current repository state.
Default to analysis only. Do not edit code unless the user separately asks for a
fix.

Be strict and ambitious about structure. Defects first, then maintainability:
bloat, smells, wrong-layer ownership, and missed simplifications that would
delete whole categories of complexity.

## Read First

- `AGENTS.md`
- `docs/architecture.md`
- `docs/acp-projection-architecture.md`
- `scripts/arch_lint.go` when boundary rules matter

## Architecture Lens

Use this as a direction, not a hard package map:

```text
Presentation surfaces -> Control layer -> Agent Runtime / SDK
```

- ACP is Caelis's native interoperability language for built-in and external
  Agents as well as the surface protocol. Reusable normalized ACP semantics may
  live in `agent-sdk`; the root `protocol/acp` packages own product wire,
  compatibility, and projection.
- The control layer assembles runnable agents, owns lifecycle/policy/review
  orchestration, endpoint selection, Agent Manage Loop decisions, and handoff
  authorization. Agents may suggest but must not commit handoff.
- Agent Runtime packages should be reusable below the application and should
  not depend on presentation, product assembly, or one transport
  implementation.
- Internal runtime/control events do not all have to be raw ACP wire schemas,
  but collaboration crossing built-in/external Agent boundaries should use the
  shared ACP semantics.
- Caelis does not target a deterministic workflow graph or node executor;
  dynamic orchestration belongs to the Control-layer Agent Manage Loop.

## Review Focus

Prioritize concrete risk over theoretical purity, and structural health over
cosmetic style:

### Correctness and product risk (P0)

- security, permission, sandbox, destructive-operation mistakes
- replay/persistence or model-context breakage
- concurrency, lifecycle, cancellation, error-handling, resource leaks
- durable model facts represented only by protocol mirrors or `_meta`

### Boundary and extension risk (P1)

- surface packages owning model/tool/sandbox/session/policy semantics
- duplicated projection, terminal, transcript, or replay paths
- orchestration accumulating in one god package or file
- `ports/*` contracts mixing reusable runtime contracts with app-control
  contracts
- SDK packages depending on product host packages
- system-managed agents implemented as ad hoc subagent/UI special cases
- extension blockers for Guardian, Reviewer, Agent Manage Loop, or external ACP
  agent collaboration
- duplicated or drifting ACP semantic contract owners across SDK and product
  wire packages

### Quality, bloat, and design smell (P1/P2 by blast radius)

- file-size explosions (especially past ~1k lines without decomposition)
- special-case branches bolted onto already busy flows
- thin wrappers / identity abstractions that add indirection without clarity
- copy-pasted helpers when a canonical owner already exists
- feature logic leaking into shared paths
- complexity rearranged rather than deleted ("refactor" with no fewer concepts)
- prompt or agent guidance that trains bad operational habits

## Stance

- Be direct and demanding. Do not rubber-stamp working-but-messier code.
- Prefer a few high-conviction findings over long nit lists.
- Look for code judo: reframes that remove layers, modes, or branches entirely.
- If a simpler structure preserves behavior, push for that structure.
- Do not invent future abstractions with no present complexity win.

## Output

Rank findings by impact:

- `P0`: correctness, security, data loss, replay/persistence, or model-context
  breakage.
- `P1`: boundary drift, coupling, bloat, or smell that blocks near-term
  extension or will keep accumulating cost.
- `P2`: useful cleanup with lower immediate risk.

For each P0/P1 finding, include file:line, failure mode or smell, why it
matters, and a bounded repair direction. End with the top 1-3 next slices by
ROI/risk.

## Validation Expectations

- Pre-commit gate: `make commit-check`.
- Release gate: `make release-dry-run`.
- Use focused `go test` packages before broad gates when proposing or reviewing
  a bounded implementation slice.
