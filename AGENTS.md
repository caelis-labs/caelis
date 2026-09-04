# AGENTS.md

## Working Style

- Preserve unrelated user changes and inspect the worktree before broad edits.
- Avoid import aliases unless they disambiguate or match local convention.
- Read nearby documentation, package comments, and tests before editing. For
  Session, Runtime, Control, ACP, replay, gateway, or Surface work, start with
  `docs/architecture.md` and follow its boundary links.

## Architecture

- Dependency direction is `Surfaces -> Control -> Agent Runtime / SDK`.
- Surfaces render typed `control/appserver/eventstream.Envelope` values and
  collect input. They do not own model, tool, sandbox, policy, persistence,
  replay, permission, or lifecycle semantics.
- Control owns product configuration, Agent assembly, endpoint and Session
  lifecycle, permissions, orchestration, controller selection, and handoff.
- `agent-sdk/*` owns reusable Runtime contracts and must not depend on product
  Control, Host, transport, presentation, or repository-internal packages.
- Standard ACP wire behavior comes from `acp-go-sdk`. Product projection,
  compatibility, and permission translation stay with their Control,
  Host-private, or Surface owner; do not recreate `protocol/acp/*` or `ports/*`.
- `control/appserver` is the aggregate product-client boundary. Task observation
  remains independent from the Session feed. Control-owned stream recorders
  append Surface-bound events to the shared spool before appserver projection.
- `app/*` owns process composition and private adapters, not a second product
  semantics layer. Lower layers depend on focused contracts, never a concrete
  Host or a wide function bag.
- Keep one semantic owner and one authoritative data path. Typed Envelope fields
  own identity, relation, position, approval, and resume; `_meta` is display or
  compatibility data only.
- Fence semantic Session writes for the complete producer lifetime. Never retry
  `ErrFenceConflict` through an unfenced path or turn an unknown effect outcome
  into a blind retry.
- Dynamic orchestration and handoff authorization belong to Control. Do not add
  an SDK workflow graph or let an Agent transfer its own authority.

## Code Quality

- Follow existing owners, helpers, and tests; scope edits to changed behavior.
- Add abstractions only when they remove real complexity or match an established
  pattern. Avoid growing central orchestration files.
- Remove superseded paths, mirrors, wrappers, tests, and docs. A compatibility
  path must name its owner, scope, and removal condition.
- Document exported APIs and non-obvious caller-visible contracts. Normalize
  external ACP input before storage, and keep transient UI or child traces out
  of durable parent context.

## Validation

- Run `gofmt` on touched Go files, focused owning tests, and `git diff --check`.
- Before committing, run `make commit-check` for lint, full tests, and build.
- Select additional gates by impact: `make arch-lint` for ownership/import
  changes, `make client-protocol-check` for wire changes, focused race tests for
  lifecycle/concurrency/persistence, and `make regression` for broad Surface or
  ACP behavior.
- Persistence and replay changes require model-context round trips; UI reload
  tests are not substitutes. Visible output changes require rendered or golden
  review.
- Run `make docs-links` after maintained documentation links change. Run
  `npm --prefix npm test` for npm launcher or release-script changes, and use
  `make release-dry-run` only for packaging evidence.

## Release

- `docs/release.md` is the release procedure. A local commit does not authorize
  a push, tag, or publication.
- Keep current contracts and compatibility removal conditions in maintained
  docs; keep plans, completed migrations, and acceptance history in issues, Git,
  tests, tags, and CI.
