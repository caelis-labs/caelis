# AGENTS.md

## Working preferences

- Inspect the worktree and preserve unrelated user changes.
- Read nearby docs, package comments, and tests before editing. For Session,
  Runtime, Control, ACP, replay, gateway, or Surface work, start with
  [Architecture](docs/architecture.md) and follow its boundary links.
- Keep changes focused. Reuse existing owners and helpers; add abstractions only
  when they remove real complexity. Avoid growing central orchestration files.
- Avoid import aliases unless they disambiguate or match local convention.
- Keep one semantic owner and one authoritative data path. Dependency direction
  is `Surfaces -> Control -> Agent Runtime / SDK`; Surfaces present, Control
  orchestrates, and the SDK stays independent of product and transport code.
- Remove superseded code, tests, and docs. Compatibility paths must name their
  owner, scope, and removal condition.
- Document exported APIs and non-obvious contracts. Maintained docs describe the
  current product; plans and completed migration history belong in issues and Git.

## Validation and release

- Run `gofmt` on touched Go files, focused owning tests, and `git diff --check`.
- Run `make commit-check` before committing. Select additional checks from
  [Testing](docs/testing.md) by affected contract, not as a blanket checklist.
  Persistence/replay changes need model-context round trips; visible output
  changes need rendered or golden evidence.
- Follow [Release](docs/release.md). A commit does not authorize a push, tag,
  or publication; those need explicit user authorization.
