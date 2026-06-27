# AGENTS.md

## Working Style
- Preserve unrelated user changes; check the worktree before broad edits.
- Avoid import aliases unless they disambiguate or match local convention.
- Read nearby docs, package comments, and tests before editing unfamiliar code. For session, gateway, ACP, replay, or surface work, read `docs/architecture.md` first.

## Coding Preferences
- Follow existing boundaries, helpers, and tests; scope edits to changed behavior.
- Add abstractions only when they remove real complexity or match an established pattern.
- Prefer public contracts in `ports/*`; keep private glue in `internal/*`; surfaces must not own model, tool, sandbox, or persistence semantics.
- Avoid growing central orchestration files. For coherent features in large/high-touch files, prefer a nearby module with docs and tests.
- Document new exported types, interfaces, and non-obvious contracts.
- Persist semantic model state, not UI transcript cache. `_meta` is display/debug unless documented as replay metadata.
- Normalize external ACP input before storage; keep transient UI/subagent traces out of durable parent context unless carried by canonical payloads.
- Before `v1.0.0`, prefer clean schema and boundary fixes over compatibility fallbacks.

## Validation
- Run `gofmt` on touched Go files, focused `go test` packages for changed behavior, and `git diff --check`.
- Run `make arch-lint` after import, package ownership, gateway/eventstream, or session protocol changes.
- Persistence or replay changes need round-trip tests comparing rebuilt model context with runtime-produced context.
- Projection/UI reload tests do not replace model-context round-trip tests.
- UI or text-output changes should include/update golden or regression coverage and review the rendered/output diff.
- Tests should prefer whole-object/event comparisons and structured helpers over field-by-field assertions or ad hoc JSON/string digging.
- Use `make quality` for release-ready changes and `make regression` when projection, TUI behavior, command execution, or ACP integration changes broadly.

## Release
- Keep release mechanics in `docs/release.md`; update that doc when the process changes.
- When asked to release, follow `docs/release.md` and verify the worktree contains only intended changes.
