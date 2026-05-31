# AGENTS.md
## Working Rules
- Main agent owns architecture, decomposition, integration, validation, and final judgment.
- Sub-agents are allowed only for bounded sidecar work with clear ownership.
- Prefer `rg` / `rg --files`; preserve unrelated user changes.
- Avoid unnecessary import aliases.
## Architecture Contract
- One durable Agent SDK session is the source of truth for runtime context.
- Store canonical model semantics, not UI transcript cache: user content,
  assistant reasoning/text, tool calls/results, replay signatures, provider
  metadata, compaction/system context, approvals, and lifecycle state.
- Reloaded model input must match the runtime semantic message sequence, except
  when system prompt, tools, or skills intentionally changed.
- `session.Event.Message` is durable model-visible message state.
- `session.Event.Tool` is durable tool execution state: ids, names, args,
  status, output, content, truncation, and replay boundaries.
- `session.Event.Protocol.Update` is the ACP client projection contract, not
  the local Agent SDK replay source.
- Gateway emits standard ACP `session/update` and `request_permission` for TUI,
  `caelis acp`, and external ACP clients.
- Caelis display hints belong in ACP `_meta`; `_meta` must not be the only copy
  of model-critical data unless explicitly defined as replay metadata.
- `VisibilityUIOnly` chunks are transient live rendering events; persisted final
  canonical events must contain complete model-visible state.
- Built-in agents and external ACP agents meet at the Gateway boundary; external
  ACP input must normalize into canonical session events before storage.
- TUI and ACP clients consume the same ACP-native event stream and may decorate
  it, but must not invent a built-in-only protocol.
## Layer Boundaries
- Public extension contracts live in `ports/*`; private glue lives in `internal/*`.
- `internal/kernel` owns local turn/session orchestration, canonical projection,
  replay validation, approvals, participants, and lifecycle coordination.
- `internal/adapters/*` provides concrete implementations and must not import
  surfaces or `internal/kernel`.
- `protocol/acp` owns ACP schema, JSON-RPC, client/server, terminal, and projector code.
- `surfaces/*` adapt UI/CLI/ACP interactions to kernel or app services and must
  not own model, sandbox, tool, or persistence semantics.
- `internal/app/local` is the default composition root for concrete implementations and config.
- Before `v1.0.0`, prefer clean schema and boundary fixes over compatibility
  fallbacks or legacy replay guesses.
## Validation
- Persistence changes require store round-trip tests comparing rebuilt model
  context with runtime-produced context.
- ACP/TUI reload tests verify projections, but do not replace model-context
  round-trip tests.
- Run affected `go test` packages and `git diff --check`.
## Release Flow
1. Confirm the worktree only contains intended changes.
2. Confirm `main` is current with `origin/main`.
3. Run `make quality`, then `git diff --check`.
4. Run `make release-dry-run` when packaging changed.
5. Commit and push release-ready code to `main`.
6. Tag the exact published commit: `git tag -a vX.Y.Z -m vX.Y.Z`.
7. Push the tag and verify the workflow, GitHub Release, npm versions, `HEAD`,
   `origin/main`, and `vX.Y.Z^{}`.
