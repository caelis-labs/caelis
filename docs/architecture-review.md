# Architecture Review

Use `.agents/skills/caelis-deep-review` for recurring architecture and
technical-debt review.

## Temporary Roadmap

Snapshot from the 2026-07-02 deep-review pass. Treat this as an execution
checklist until the items are either implemented or replaced by a newer review.

- [x] `P1` Move ACP passthrough out of the reusable `ports/eventsource`
  contract. Failure mode: runtime/control source handles traffic
  surface-facing `eventstream.Envelope` values as a public port contract,
  making future non-ACP runtime/control events and Agent Manage Loop work harder
  to isolate. Bounded repair: keep durable canonical events unchanged, move ACP
  passthrough to an internal bridge owned by the control/kernel publication
  path, and preserve external ACP live fidelity. Implemented by moving the
  source-event bridge to `internal/acpbridge`.
- [x] `P1` Split product command/control semantics out of
  `protocol/acp/control`. Failure mode: protocol packages own app commands,
  plugin/model/sandbox controls, and slash routing. Bounded repair: move command
  registry/router ownership toward app/control while retaining ACP schema,
  eventstream, projection, and client protocol contracts in `protocol/acp`.
  Implemented by moving the command catalog to `ports/controlcommand`, prompt
  request/result parsing contracts to `ports/controlprompt`, shared slash
  orchestration to `internal/controlpromptrouter`, and the connect wizard
  completion payload to `internal/connectwizard`. `protocol/acp/control`
  remains a transitional control contract/presenter package, not the owner of
  product command routing.
- [ ] `P1` Narrow `ports/gateway.Service` consumers. Failure mode: one stable
  port still spans session, turn, replay, control-plane, and request policy.
  Bounded repair: move callers onto the smallest existing subinterfaces first,
  then split replay/control-plane app contracts only where consumers prove it.
- [ ] `P1` Turn system-managed agents into a small registry instead of Guardian
  special cases. Failure mode: Guardian, Reviewer, and future Agent Manage Loop
  agents would duplicate profile, binding, and run-plan rules. Bounded repair:
  make the app-private `systemManagedAgentSpec` registry the single source for
  status, binding policy, and run planning without changing Guardian behavior.
- [ ] `P2` Consolidate transcript/TUI display fallback normalization. Failure
  mode: approval review, retry, terminal, and tool-result display derivation is
  spread across transcript projection and TUI rendering. Bounded repair: move
  shared display normalizers into `surfaces/transcript` or `ports/displaypolicy`
  and leave TUI responsible for layout only.

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
