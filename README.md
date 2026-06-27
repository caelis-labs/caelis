# caelis

`caelis` is a terminal-first agent runtime. The active local path is:
`cmd/caelis -> internal/cli -> app/gatewayapp -> ports/gateway.Service`.

The project treats a session workspace plus ACP-native event semantics as the
stable product boundary. Public `ports/*` packages name the core contract and
extension points; `impl/*` packages hold concrete local implementations; surface
adapters project the shared state into the Bubble Tea TUI, ACP stdio, and the
headless one-shot runner.

The client event protocol is `protocol/acp/eventstream.Envelope`. TUI, GUI,
app-server, headless, and ACP bridges should consume that ACP-native stream and
project their own view models from it. `ports/gateway.Event` remains a
transitional in-process DTO for legacy adapters and tests; it is not the target
protocol for new surfaces.

## What It Does

- Starts an interactive TUI when launched from a TTY with no prompt input.
- Runs a headless single-shot turn when given `-p` or piped stdin.
- Persists sessions, provider config, and app config under `~/.caelis` by default.
- Supports approval-aware tool execution; sandbox route, status, and doctor
  output report whether execution is isolated or using the host.
- Connects external agents through the Agent Client Protocol (ACP) as
  participants, subagents, or main-controller handoffs.
- Keeps async `RUN_COMMAND` and `SPAWN` work addressable through `TASK wait`,
  `TASK write`, and `TASK cancel`, including stdin writes to interactive shell
  commands.
- Projects built-in and ACP tools through one transcript renderer so `Ran`,
  `Read`, `Search`, `Wrote`, `Patched`, `SPAWN`, `TASK`, and approval states stay
  visually consistent.
- Adapts TUI colors to dark and light terminal backgrounds, terminal color
  profile, `NO_COLOR`, explicit theme selection, and optional accent overrides.
- Assembles prompts from built-in instructions, workspace `AGENTS.md`, global
  `~/.agents/AGENTS.md`, and discovered local skills.

## Current Layout

- `cmd/caelis`: the single binary entrypoint. It delegates immediately to the
  internal CLI runner.
- `internal/cli`: flat-flag CLI runner. It routes doctor, ACP stdio, headless,
  and interactive TUI modes through the local app stack.
- `ports/`: public extension ports for agent orchestration, approval, assembly,
  compaction, config, controller, delegation, gateway, model, policy, prompt,
  sandbox, session storage, skill, stream, subagent, task, and tool contracts.
- `impl/`: concrete implementations such as local agents, ACP-backed agents,
  session stores, model providers, sandbox backends, policy presets, tools,
  prompt/config/stream adapters, and approval strategies.
- `internal/kernel`: concrete local kernel implementation for sessions, turns,
  replay, active runs, and control-plane operations.
- `app/gatewayapp`: local composition root that wires runtime, gateway resolver,
  prompt assembly, config store, model catalog, sandbox, tools, approval, and
  session storage.
- `surfaces/headless`: one-shot CLI surface over the public gateway contract.
- `surfaces/tui`: terminal UI surface facades for the app, gateway driver, and
  driver contract.
- `protocol/acp`: ACP schema, JSON-RPC, client, server, transport, terminal, and
  projector packages.
- `surfaces/acpserver`: exposes the local stack as an ACP stdio agent.
- `eval/`: build-tagged cross-stack and live evaluation tests for kernel, app,
  ACP, CLI, local-config model, and TUI gateway-driver flows.
- `npm/`: npm wrapper package plus platform-specific binary packages.

Architecture plan:
[docs/agent-sdk-acp-architecture-plan.md](docs/agent-sdk-acp-architecture-plan.md)

## Pre-v1 Upgrade Notes

Current builds use canonical session events as the replay source:
`session.Event.Message` for model-visible messages and `session.Event.Tool` for
tool execution state. Replay emits ACP-native final envelopes derived from
those durable events, including standard `usage_update` session updates for
token usage. Older local data that only stored v2 semantic sidecar
payloads (`user_message`, `assistant_message`, `system_context`, `tool_call`,
`tool_result`) or embedded document `events` arrays is not migrated at read time;
the file session store returns an unsupported legacy format error instead of
silently replaying partial history.

Other pre-v1 compatibility paths are intentionally cut: compact
`replacement_history` / `retained_user_inputs` metadata is not replayed,
uppercase model config JSON is rejected, `-permission-mode` is replaced by
`-approval-mode`, CodeFree credentials are read from the Caelis credential path
instead of imported from `~/.codefree-cli`, and connect wizard completions use a
structured JSON payload rather than the old pipe-delimited payload.

## Install

From npm:

```bash
npm i -g @onslaughtsnail/caelis
```

or without a global install:

```bash
npx @onslaughtsnail/caelis --help
```

Supported npm platforms: macOS/Linux/Windows (`x64`, `arm64`).

From source:

```bash
go install ./cmd/caelis
```

The binary name is `caelis` in release artifacts and npm packages. Local source
builds can also be run with `go run ./cmd/caelis`.

## CLI Entry

`cmd/caelis` uses one flat flag set. Run `go run ./cmd/caelis -h` to inspect the
current flags.

Subcommands:

- `caelis acp`: serve the local stack as an ACP stdio agent.
- `caelis doctor`: print runtime, session, model, and sandbox diagnostics.

Common flags:

- `-p`: single-shot prompt text
- `-format`: `text` or `json` for headless output
- `-interactive`: force the TUI path even when stdin is piped
- `-session`, `-store-dir`, `-workspace-key`, `-workspace-cwd`
- `-approval-mode`: `auto-review` or `manual`
- `-provider`, `-api`, `-model`, `-base-url`, `-token`, `-token-env`
- `-auth-type`, `-header-key`
- `-model-alias`, `-context-window`, `-max-output-tokens`
- `-system-prompt`: append session-specific system guidance
- `-doctor`: print runtime, session, and sandbox diagnostics

Interactive TUI:

```bash
caelis \
  -provider openai \
  -model gpt-5 \
  -approval-mode auto-review
```

Headless single-shot:

```bash
caelis \
  -provider openai \
  -model gpt-5 \
  -approval-mode auto-review \
  -p "Summarize the repository layout."
```

Headless from stdin:

```bash
printf '%s\n' "Summarize the repository layout." | caelis \
  -provider openai \
  -model gpt-5 \
  -format text
```

If no model is configured yet, start the TUI and use `/connect`.

## TUI And ACP Agents

The TUI is the primary local interface. It keeps prompt turns, external ACP
participants, subagent tasks, tool calls, output panels, approvals, plans, and
standard ACP usage updates in one transcript pipeline.

Current built-in slash commands:

- `/help`
- `/agent list`
- `/agent add <builtin>`
- `/agent install <adapter>`
- `/agent use <agent|local>`
- `/agent remove <agent>`
- dynamic ACP child commands for registered agents, for example `/codex <prompt>`
  and follow-up `@handle <prompt>`
- `/connect`
- `/model use <alias>` or `/model del <alias>`
- `/approval [auto-review|manual]`
- `/status`
- `/doctor` (Windows sandbox repair/readiness)
- `/new`
- `/resume [session-id]`
- `/compact`
- `/exit`
- `/quit`

Notes:

- `/agent` manages ACP-backed participants and main-controller handoff without
  bypassing the gateway control plane.
- ACP tool identity keeps the protocol `kind` and `title` separate. The TUI maps
  known kinds into existing display verbs, such as `execute -> Ran`, `read ->
  Read`, `search/fetch -> Search`, and `edit/delete/move -> Patched`.
- Exploration-style tools are compact when safe; terminal and mutation tools stay
  prominent and use condensed output panels for long-running work.
- Completion is available for slash commands, `/agent` arguments, `#path`,
  `$skill`, and `/resume` session ids.
- The default theme auto-detects terminal background and color depth. Set
  `CAELIS_THEME=dark|light|nord|solarized|dracula` to force a theme,
  `CAELIS_THEME=auto` to return to background-aware defaults, `CAELIS_ACCENT`
  to override the focus/accent color, or `NO_COLOR=1` to disable styling.

## Runtime And Permissions

`caelis` exposes one CLI approval switch:

- `-approval-mode auto-review`: use model-backed approval review for
  sensitive requests when the sandbox route requires escalation.
- `-approval-mode manual`: require an explicit user decision for sensitive
  requests.

Sandbox backend selection is resolved by the local runtime: macOS uses seatbelt,
Linux prefers bubblewrap and falls back to Landlock when available, and Windows
uses the current-user workspace-write sandbox by default. The TUI reports
sandbox readiness in `/status`; Windows workspace ACL state is repaired lazily
before sandboxed commands run. The CLI keeps `caelis sandbox reset` and
`caelis sandbox clean` for sandbox state cleanup. Sandbox permission failures
are surfaced with backend-neutral denial metadata and the raw path-bearing error
needed for recovery.

## Sessions

Interactive sessions are stored under `~/.caelis/sessions` by default. The TUI
starts a fresh session unless `-session` is provided. Resume state is projected
through the same gateway event stream used by live turns, including ACP
participants, child tasks, plan updates, and tool panels.

## Development

Caelis currently requires Go `1.25.1` as declared in `go.mod`.

```bash
make quality
make test
make build
make arch-lint
make size-report
```

- `make quality`: runs formatting check, `golangci-lint`, tests, `go vet`, and
  `go build ./...`
- `make test`: runs `go test ./...`
- `make arch-lint`: checks the repository layer boundaries.
- `make size-report`: prints code size, package, embedded resource, binary, npm,
  and dependency metrics.
- `make build`: runs `go build ./...`

The Makefile defaults Go and lint caches to `.tmp/cache` so local quality checks
do not need writable global Go cache directories. Override the cache roots only
when you need to share or relocate them:

```bash
CACHE_ROOT=/tmp/caelis-cache \
make quality
```

## Release And Packaging

- Release identity comes from the annotated git tag, such as `vX.Y.Z`.
- Binary version metadata is injected from the git tag at build/release time.
- npm package manifests are rewritten from the tag inside the release workflow.
- Go release archives are produced from `./cmd/caelis` by GoReleaser.
- npm publishes a thin launcher package from `npm/` plus platform-specific binary
  packages from `npm/packages/*`.
- The npm wrapper is file-whitelisted so published artifacts do not include
  workspace files such as `.env`, `.git`, `.superpowers`, caches, or temporary
  build outputs.

Local release dry run:

```bash
make release-dry-run
```

Release hygiene checklist:

- Keep commit messages descriptive; release notes are generated from git history.
- Keep README stable and update it only when the architecture or public usage
  contract changes.
- Run `make quality`, `git diff --check`, and a release dry run before
  publishing.
- Push `main` before creating a tag.
- For a published release, verify the tag workflow, GitHub Release, and npm
  package versions after publication.

Tagged releases are driven by annotated `vX.Y.Z` tags pushed at the exact `main`
commit intended for publication.
