---
name: caelis-deep-review
description: Caelis workspace-private architecture and code-quality review. Use for long-term technical debt inventory, architecture boundary review, Go quality review, layering responsibility checks, extension-risk analysis, or changes touching presentation surfaces, control orchestration, Agent Runtime modules, ACP/eventstream projection, session persistence, tools, skills, model providers, Guardian/Reviewer agents, Agent Manage Loop direction, or external ACP agent collaboration.
---

# Caelis Deep Review

Review Caelis architecture and code quality from the current repository state.
Default to analysis only. Do not edit code unless the user separately asks for a
fix.

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

Look for concrete risks, not theoretical purity:

- surface packages owning model/tool/sandbox/session/policy semantics;
- duplicated projection, terminal, transcript, or replay paths;
- orchestration accumulating in one god package or file;
- `ports/*` contracts mixing reusable runtime contracts with app-control
  contracts;
- `impl/*` depending on control or presentation concerns;
- durable model facts represented only by protocol mirrors or `_meta`;
- system-managed agents implemented as ad hoc subagent/UI special cases;
- extension blockers for Guardian, Reviewer, Agent Manage Loop, or external ACP
  agent collaboration;
- duplicated or drifting ACP semantic contract owners across SDK and product
  wire packages;
- Go issues around context, concurrency, errors, resources, interfaces, tests,
  and package ownership.

## Output

Rank findings by impact:

- `P0`: correctness, security, data loss, replay/persistence, or model-context
  breakage.
- `P1`: boundary drift or coupling that blocks likely near-term extension.
- `P2`: useful cleanup with lower immediate risk.

For each P0/P1 finding, include file:line, failure mode, why it matters, and a
bounded repair direction. End with the top 1-3 next slices by ROI/risk.

## Validation Expectations

- Pre-commit gate: `make commit-check`.
- Release gate: `make release-dry-run`.
- Use focused `go test` packages before broad gates when proposing or reviewing
  a bounded implementation slice.
