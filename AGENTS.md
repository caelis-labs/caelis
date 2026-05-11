# AGENTS.md

## Working Rules

- Keep the main agent responsible for architecture, task decomposition,
  integration, validation, and final judgment.
- Use sub-agents only for bounded sidecar work with clear ownership; do not
  delegate final design decisions or cross-cutting release judgment.
- Prefer `rg`/`rg --files` for repository search.
- Preserve unrelated user changes in the worktree.

## Architecture Direction

- Keep the internal core centered on one durable Session workspace, ACP-native
  event semantics, and multiple agents working in the same session context.
- Treat ACP protocol shapes such as `session/update` and
  `session/request_permission` as first-class event semantics. UI layers may
  project or decorate them, but should not replace them with UI-only protocol
  concepts.
- Prefer small public extension ports over hard-wired implementations. Approval
  review, session storage, model providers, sandbox policy, tools, skills, and
  prompt assembly should be replaceable through narrow interfaces.
- Put reusable extension contracts in `sdk/*` when they are intended for
  outside implementations. Use `internal/*` only for private glue that should
  not become a public integration point.
- Keep `sdk/runtime` focused on runtime orchestration and abstract callbacks. It
  should depend on generic ports such as approval requesters, not know whether a
  decision came from a human, a guardian agent, or a future policy engine.
- Model approval as a pluggable port: human/manual approval and agent
  Auto-Review are implementations of the same approval contract. Shared
  approval payload normalization and response conversion belong with that
  contract, not in surface adapters.
- Keep generic ACP adapters independent from `gateway/core`. They may depend on
  `sdk/*` ports and ACP protocol packages. Composition packages may wire gateway
  defaults into ACP adapters, but generic adapters should not import gateway
  orchestration types unless the dependency is explicitly part of a migration
  step.
- Keep `gateway/core` responsible for local turn/session orchestration,
  canonical event projection, and lifecycle coordination. It should not import
  TUI packages or generic ACP surface adapters.
- Keep `app/gatewayapp` as the default local composition layer. It wires default
  implementations and persisted config, but reusable architecture contracts
  should live below it.
- Avoid broad framework rewrites. When moving toward the port-based architecture,
  make narrow, validated extractions that reduce duplicated semantics or remove
  dependency inversion.

## Commit Messages

- Treat commit history as the project changelog. Every commit should explain the
  user-visible or maintainer-visible reason for the change.
- Use concise conventional-style subjects when possible: `feat:`, `fix:`,
  `docs:`, `refactor:`, `test:`, `build:`, or `chore:`.
- Put release-relevant context in the commit body when the subject is not enough.
- Do not maintain `CHANGELOG.md`; release notes are generated from git history.

## README Policy

- Keep `README.md` stable and version-agnostic.
- Do not update README for version bumps, tag creation, package publication, or
  changelog-only work.
- Update README only when core architecture, public commands, installation
  shape, runtime behavior, or documented user workflows materially change.
- Do not pin npm install examples to a concrete package version.

## Versioning

- The release version source of truth is the annotated git tag `vX.Y.Z`.
- Do not add or update a repository `VERSION` file.
- Binary version metadata is injected from the tag by `Makefile`/GoReleaser
  ldflags. Untagged or dirty source builds report `dev`.
- Source npm manifests stay on the development placeholder version. The release
  workflow rewrites npm package versions from the pushed tag before publishing.

## Release Flow

1. Confirm the worktree only contains intended changes.
2. Confirm `main` is the release branch and is up to date with `origin/main`.
3. Run `make quality`.
4. Run `git diff --check`.
5. Run `make release-dry-run` when packaging behavior changed.
6. Commit and push the release-ready code to `main`.
7. Create an annotated tag on the exact published `main` commit:
   `git tag -a vX.Y.Z -m vX.Y.Z`.
8. Push the tag to trigger `.github/workflows/release.yml`.
9. Verify the release workflow, GitHub Release, npm package versions, and that
   `HEAD`, `origin/main`, and `vX.Y.Z^{}` identify the intended commit.
