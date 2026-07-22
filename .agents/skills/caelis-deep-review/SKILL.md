---
name: caelis-deep-review
description: Caelis architecture and code-quality review for layering, ownership, extension risk, Go design, ACP/eventstream, Control, Runtime/SDK, persistence, Surfaces, and multi-Agent collaboration.
---

# Caelis Deep Review

Review the current repository with findings first. Default to analysis only;
edit code only when the user also requests a change. Keep depth proportional to
the scope: a local change gets a local review, while cross-layer or release work
earns broader tracing and validation.

## Read

Always read `AGENTS.md` and `docs/architecture.md`. Then read only the normative
document relevant to the change:

- `docs/agent-sdk-boundary.md` for SDK, Runtime, handoff, or orchestration;
- `docs/acp-projection-architecture.md` for ACP/eventstream or Surfaces;
- `docs/control-client-m2-design.md` for client commands, feed, replay, or
  app-server;
- `scripts/arch_lint.go` when dependency rules matter;
- `docs/release.md` only for release readiness.

## Design Lens

```text
Presentation surfaces -> Control layer -> Agent Runtime / SDK
```

- Surfaces render `eventstream.Envelope` values and collect input. They do not
  own Runtime, model, tool, sandbox, policy, persistence, or orchestration.
- Control owns assembly, lifecycle, permissions, Guardian/Reviewer/system
  Agents, endpoint selection, handoff authorization, and future Agent Manage
  Loop decisions.
- `agent-sdk/*` owns reusable Runtime semantics and must not depend on product
  `app/*`, `surfaces/*`, `protocol/acp/*`, product `ports/*`, or non-SDK
  repository internals.
- ACP is the shared semantic language for built-in and external Agents and the
  payload vocabulary projected to clients. SDK-normalized semantics flow
  outward to `protocol/acp`; product wire types do not flow inward.
- `control/client` owns the product-client contract and implementation,
  including commands, Session list/bootstrap/reconnect, feed/replay, and
  approval recovery. `internal/controlclient/turningress` remains private
  main-Turn ingress glue, and HTTP/SSE remains a thin Surface adapter.
  Transitional `protocol/acp/control.Service` must not grow into a second
  product API.
- Canonical durable facts, not transcript caches or undocumented `_meta`, are
  replay truth. Typed Envelope fields own scope, relation, delivery, position,
  approval identity, and resume semantics.
- Agents may suggest delegation or handoff, but only Control commits ownership
  changes. Caelis does not build a deterministic workflow graph/node engine;
  dynamic orchestration belongs to the Agent Manage Loop.
- Unsupported capabilities should fail closed and be declared unsupported;
  incomplete optional functionality is not a reason to add a compatibility
  bypass.

## Review Rules

1. Preserve user changes and record the worktree boundary before broad work.
2. Identify the semantic owner and dependency direction before judging package
   placement.
3. Review changed code with nearby contracts, package comments, and tests.
   Search wider only when ownership, duplication, or risk crosses that boundary.
4. Prefer deletion and reuse of an established path over a new mode, wrapper,
   mirror, cache, or special case.
5. Keep new coherent behavior out of central orchestration files when a nearby
   focused module can own it.
6. Require comments for exported contracts and non-obvious ordering,
   concurrency, persistence, compatibility, and failure guarantees.
7. Compare maintained docs and exported comments with implementation. Keep
   completed plans and acceptance history in Git, tests, tags, and CI.
8. For high-risk concurrency, persistence, replay, permission, or effect work,
   expand to an end-to-end trace and test the smallest production-shaped
   counterexample. Do not impose that cost on unrelated reviews.

## Findings

- `P0`: correctness, security, data loss, replay/model-context corruption,
  permission/sandbox failure, or user-visible lifecycle breakage.
- `P1`: wrong-layer ownership, duplicated authority or data path, coupling, or
  bloat that blocks near-term extension.
- `P2`: useful cleanup with lower immediate risk.

For each P0/P1 include file:line, failure mode, impact, and a bounded repair.
Prefer a few high-confidence findings over a long nit list. If there are no
findings, say so and name the evidence and residual limitations checked. End
with at most three next slices only when follow-up work remains.

## Validation

Keep validation proportional:

- focused `go test -count=1` for changed behavior;
- focused `go test -race -count=1` for concurrency/lifecycle/persistence;
- model-context round trips for persistence or replay;
- `make client-protocol-check` for product wire changes;
- `make regression` for broad ACP, projection, TUI, or command changes;
- `make docs-links` for documentation topology changes;
- `make commit-check` plus `git diff --check` before a requested commit;
- the full `docs/release.md` gates only for release readiness.
