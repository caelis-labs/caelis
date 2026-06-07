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
- `acp/` is the single canonical ACP package (schema, client, server, projector, normalize).
  `protocol/acp/` is a deprecated compatibility layer being absorbed into `acp/`.
- `orchestrator/` owns multi-agent orchestration: SPAWN delegation, ACP child lifecycle,
  context visibility, permission bridging, stream merge. Imports `acp/`, `agent/`, `runner/`,
  `session/`, `tool/`, `policy/`. Must not import `gateway/`, `app/`, `tui/`, `headless/`.
- `runner/` owns single invocation execution. Does not import `orchestrator/`; receives
  orchestration interfaces via dependency injection.
- `gateway/` is the surface-facing service contract (composition/config/API facade).
  It does not own turn lifecycle or multi-agent orchestration.
- `app/` is the composition root that wires `orchestrator/`, `runner/`, `gateway/`, and
  all Layer 4 dependencies.
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
