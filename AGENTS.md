# AGENTS.md

## Working Style
- Preserve unrelated user changes; check the worktree before broad edits.
- Avoid import aliases unless they disambiguate or match local convention.
- Read nearby docs, package comments, and tests before editing unfamiliar code. For session, gateway, ACP, replay, runtime, control, or surface work, read `docs/architecture.md` first, then the linked normative document for that boundary.

## Coding Preferences
- Follow existing boundaries, helpers, and tests; scope edits to changed behavior.
- Add abstractions only when they remove real complexity or match an established pattern.
- Target architecture: presentation surfaces -> control layer -> Agent Runtime / SDK. Current package names are transitional.
- Surfaces consume ACP-shaped `eventstream.Envelope` payloads plus documented `_meta` extensions; they must not own model, tool, sandbox, policy, persistence, or runtime semantics.
- ACP is the native interoperability language for built-in and external Agents as well as the surface protocol. Reusable normalized ACP semantics may live in `agent-sdk`; root `protocol/acp/*` owns product wire transport, compatibility, and projection.
- The control layer owns orchestration: lifecycle, Agent assembly, permissions, Guardian/Reviewer/system agents, future Agent Manage Loop coordination, endpoint selection, and handoff authorization. Agents must not autonomously commit handoff.
- Agent Runtime / SDK packages should be reusable below the application and must not depend on presentation, product assembly, or one transport implementation.
- Do not build a deterministic workflow graph/node engine. Dynamic orchestration belongs to the control-layer Agent Manage Loop.
- Prefer reusable public contracts in `agent-sdk/*` and coherent product-control packages in `control/*`; existing `ports/*` packages are transitional and frozen except for migrations or fixes to their current contracts. Keep private glue in `internal/*`, and avoid mixing app-control contracts with reusable runtime contracts.
- `agent-sdk/*` is a package tree in the root Go module, not a nested module. SDK packages must not depend on `control/*`, `app/*`, `surfaces/*`, `protocol/acp/*`, product-host `ports/*`, or repository `internal/*` packages outside the `agent-sdk` package tree.
- `ports/controlclient` is the current transport-neutral product client contract and `internal/controlclient` is its current implementation, but both paths are transitional. New Control capabilities belong in coherent `control/*` packages; do not add new operations to `ports/*`, `protocol/acp/control.Service`, or Surface-private APIs. `surfaces/appserver` only maps HTTP/SSE and authentication.
- All current clients consume the Control-owned Session feed. Keep Turn/task ingress in `internal/controlclient/turningress`; do not let a Surface discover `StreamSubscriber`, rebuild replay, or publish a second permission path.
- `Envelope.Cursor` is the only public resume token. Delivery, parent relation, scope, position, and approval identity use typed Envelope fields; `_meta` must not become a correlation, ordering, durability, or authorization source.
- Session semantic writes are fenced. Runtime writes carry the current execution lease guard for the complete asynchronous producer lifetime; overlapping Control writes use an explicit allowed purpose and matching fence when required. Never catch `ErrLeaseConflict` and retry unfenced. `SnapshotState` is a pure read; durable State repair uses a revision-checked guarded mutation.
- Client-hosted ACP terminal execution is currently unsupported unless a complete handler is installed. Preserve the declared capability and the documented Zed compatibility anchor until a focused compatibility decision changes both behavior and tests.
- Avoid growing central orchestration files. For coherent features in large/high-touch files, prefer a nearby module with docs and tests.
- Document new exported types, interfaces, and non-obvious contracts.
- Persist semantic model state, not UI transcript cache. `_meta` is display/debug unless documented as replay metadata.
- Normalize external ACP input before storage; keep transient UI/subagent traces out of durable parent context unless carried by canonical payloads.
- Before `v1.0.0`, prefer clean schema and boundary fixes over compatibility fallbacks.

## Architecture Review
- Use `.agents/skills/caelis-deep-review` for recurring Caelis architecture review, long-term technical debt inventory, boundary drift checks, and large code-quality scans.
- Deep review findings should rank concrete risk over theoretical purity: P0 for correctness/security/replay corruption, P1 for boundary drift that blocks near-term extension, P2 for useful cleanup.
- For architecture cleanup, choose one bounded high-ROI slice and validate it before widening scope.

## Validation
- Run `gofmt` on touched Go files, focused `go test` packages for changed behavior, and `git diff --check`.
- Before committing, run `make commit-check`; it includes formatting, `golangci-lint`, `arch-lint`, the SDK package-boundary gate, vet, tests, and build.
- Run `make arch-lint` after import, package ownership, gateway/eventstream, or session protocol changes.
- Run `make client-protocol-check` after changing OpenAPI, generated clients, Envelope wire shapes, or `ports/controlclient` JSON contracts.
- Lease, concurrency, persistence, broker, or lifecycle changes require focused `go test -race` coverage; release candidates run the Agent SDK race suite documented in `docs/release.md`.
- Persistence or replay changes need round-trip tests comparing rebuilt model context with runtime-produced context.
- Projection/UI reload tests do not replace model-context round-trip tests.
- UI or text-output changes should include/update golden or regression coverage and review the rendered/output diff.
- Tests should prefer whole-object/event comparisons and structured helpers over field-by-field assertions or ad hoc JSON/string digging.
- Use `make regression` when projection, TUI behavior, command execution, or ACP integration changes broadly.
- Run `make docs-links` after adding, removing, or renaming maintained documentation.

## Release
- Keep release mechanics in `docs/release.md`; update that doc when the process changes.
- When asked to release, follow `docs/release.md` and verify the worktree contains only intended changes.
- Keep normative contracts and current limitations in maintained docs; keep completed implementation plans and acceptance history in Git, tests, tags, and CI rather than a permanent Roadmap document.
