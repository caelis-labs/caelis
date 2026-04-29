# caelis

Current release target: `v0.1.0`.

`caelis` is a terminal-first agent runtime with one local stack:
`sdk -> gateway -> app/gatewayapp -> adapters -> tui/headless`.

The project now treats the gateway event contract as the stable product boundary.
The SDK owns runtime, session, model, sandbox, tool, delegation, and ACP
integration primitives; adapters project that state into the Bubble Tea TUI or a
headless one-shot runner.

## What It Does

- Starts an interactive TUI when launched from a TTY with no prompt input.
- Runs a headless single-shot turn when given `-p` or piped stdin.
- Persists sessions, provider config, and app config under `~/.caelis` by default.
- Supports approval-aware tool execution in `default` permission mode and direct
  host execution in `full_control`.
- Connects external agents through the Agent Client Protocol (ACP) as
  participants, subagents, or main-controller handoffs.
- Projects built-in and ACP tools through one transcript renderer so `Ran`,
  `Read`, `Search`, `Wrote`, `Patched`, `SPAWN`, `TASK`, and approval states stay
  visually consistent.
- Assembles prompts from built-in instructions, workspace `AGENTS.md`, global
  `~/.agents/AGENTS.md`, and discovered local skills.

## Current Layout

- `cmd/cli`: flat-flag CLI entrypoint. Chooses TUI or headless mode; there are no
  `console` or `acp` subcommands.
- `sdk/`: reusable foundation for runtime, session, model/provider, tool,
  sandbox, delegation, plugin, stream, and ACP contracts. Root packages stay
  contract-first; concrete implementations live in subpackages such as
  `sdk/runtime/local`, `sdk/session/file`, `sdk/tool/builtin`, and
  `sdk/controller/acp`.
- `gateway/`: product-facing API surface. `gateway/core` owns session, turn, event
  replay, approval, and control-plane orchestration. `gateway/host` owns host and
  remote-session lifecycle.
- `app/gatewayapp`: local composition root that wires the SDK runtime, gateway
  resolver, prompt assembly, config store, model catalog, and session store.
- `gateway/adapter/headless`: one-shot adapter over the root `gateway` contract.
- `gateway/adapter/tui/runtime`: gateway-to-TUI bridge used by the interactive
  application.
- `tui/`: presentation layer, including `tui/tuiapp`, `tui/tuikit`,
  `tui/modelcatalog`, `tui/acpprojector`, and `tui/tuidiff`.
- `acp/` and `acpbridge/`: ACP schema, client/server transport, fixtures, and
  bridge helpers used by external-agent flows.
- `npm/`: npm wrapper package plus platform-specific binary packages.

Architecture overview: [docs/architecture.md](docs/architecture.md)

Deeper design documents:
[docs/current_sdk_foundation_scope.md](docs/current_sdk_foundation_scope.md),
[docs/unified_gateway_foundation_spec.md](docs/unified_gateway_foundation_spec.md)

## Install

From npm:

```bash
npm i -g @onslaughtsnail/caelis@0.1.0
```

or without a global install:

```bash
npx @onslaughtsnail/caelis@0.1.0 --help
```

Supported npm platforms: macOS/Linux (`x64`, `arm64`).

From source:

```bash
go install ./cmd/cli
```

The binary name is `caelis` in release artifacts and npm packages. Local source
builds can also be run with `go run ./cmd/cli`.

## CLI Entry

`cmd/cli` uses one flat flag set. Run `go run ./cmd/cli -h` to inspect the
current flags.

Common flags:

- `-p`: single-shot prompt text
- `-format`: `text` or `json` for headless output
- `-interactive`: force the TUI path even when stdin is piped
- `-session`, `-store-dir`, `-workspace-key`, `-workspace-cwd`
- `-permission-mode`: `default` or `full_control`
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
  -permission-mode default
```

Headless single-shot:

```bash
caelis \
  -provider openai \
  -model gpt-5 \
  -permission-mode default \
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
usage updates in one transcript pipeline.

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
- `/sandbox [auto|seatbelt|bwrap|landlock]`
- `/status`
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

## Runtime And Permissions

`caelis` exposes one CLI permission switch:

- `-permission-mode default`: use the local sandbox runtime when available and
  require approval for host escalation.
- `-permission-mode full_control`: execute directly on the host.

Sandbox backend selection is resolved by the local runtime. The TUI exposes
`/sandbox [auto|seatbelt|bwrap|landlock]` for inspection and selection. Sandbox
permission failures are surfaced with backend-neutral denial metadata and the raw
path-bearing error needed for recovery.

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
```

- `make quality`: runs formatting check, `golangci-lint`, tests, `go vet`, and
  `go build ./...`
- `make test`: runs `go test ./...`
- `make build`: runs `go build ./...`

When running in restricted environments, set writable cache roots:

```bash
GOMODCACHE=/tmp/caelis-gomodcache \
GOCACHE=/tmp/caelis-gocache \
GOLANGCI_LINT_CACHE=/tmp/caelis-golangci-lint-cache \
make quality
```

## Release And Packaging

- `VERSION` carries the Go release tag, including the leading `v`.
- npm package manifests carry the same version without the leading `v`.
- Go release archives are produced from `./cmd/cli` by GoReleaser.
- npm publishes a thin launcher package from `npm/` plus platform-specific binary
  packages from `npm/packages/*`.
- The npm wrapper is file-whitelisted so published artifacts do not include
  workspace files such as `.env`, `.git`, `.superpowers`, caches, or temporary
  build outputs.

Synchronize npm manifests:

```bash
node ./npm/scripts/set-version.mjs 0.1.0
```

Local release dry run:

```bash
make release-dry-run
```
