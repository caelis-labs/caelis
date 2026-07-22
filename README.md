# caelis

`caelis` is a terminal-first agent runtime with an interactive TUI, a headless
one-shot CLI mode, and an ACP stdio server for external agent clients.

It stores local state under `~/.caelis` by default and supports model provider
configuration, session persistence, approval-aware tool execution, built-in
filesystem/search/shell tools, subagent tasks, and ACP-backed participants.

Official site: <https://caelis.dev>

## Install

From the official install script on macOS or Linux:

```bash
curl -fsSL https://caelis.dev/install.sh | sh
```

From the official install script on Windows PowerShell:

```powershell
irm https://caelis.dev/install.ps1 | iex
```

From npm:

```bash
npm i -g @caelis/caelis
```

or without a global install:

```bash
npx @caelis/caelis --help
```

From source:

```bash
go install ./cmd/caelis
```

Local source builds can also be run with:

```bash
go run ./cmd/caelis --help
```

## Use

Start the interactive TUI:

```bash
caelis
```

Run a single headless prompt:

```bash
caelis -p "Summarize this repository."
```

Read a prompt from stdin:

```bash
printf '%s\n' "Explain the current changes." | caelis -format text
```

Serve Caelis as an ACP stdio agent:

```bash
caelis acp
```

Print runtime, model, session, and sandbox diagnostics:

```bash
caelis doctor
```

Inspect all current flags:

```bash
caelis -h
```

Common flags:

- `-p`: headless prompt text.
- `-format`: `text` or `json` for headless output.
- `-interactive`: force TUI mode when stdin is piped.
- `-session`: resume or target a session id.
- `-store-dir`: override the default store directory.
- `-workspace-cwd`: set the workspace directory.
- `-approval-mode`: `auto-review` or `manual`.
- `-provider`, `-model`, `-api`, `-base-url`, `-token`, `-token-env`: model
  provider configuration.

If no model is configured yet, start the TUI and run `/connect`.

Use `/subagent` to configure the fixed Caelis delegation profiles: Breeze for
fast bounded work, Orbit for general implementation and review, and Zenith for
deep or high-risk analysis. Each binding selects one connected `ModelProfile`
and an explicit reasoning effort. `self` separately uses the current Session
profile and effort; an unbound fixed profile is not exposed in Spawn or direct
run catalogs. Provider and ACP connections both produce `ModelProfile` choices,
while raw model and external Agent IDs remain hidden from the model-facing Spawn
catalog. Guardian and Reviewer accept provider profiles only.

To use a ChatGPT subscription as the primary model path, choose the `codex`
model provider in `/connect` and complete the guided sign-in. Caelis opens a
browser when one is available and automatically uses device-code sign-in for
headless/SSH/CI environments or when the browser cannot be opened. This is a
community-compatible OAuth integration rather than a documented third-party
OpenAI integration. It uses one account, the fixed ChatGPT Codex endpoint, and
does not implement account pools or rotation. The refresh credential is stored
under `~/.caelis/providers/codex/auth.json` by default with `0600` permissions
so Caelis processes sharing the same state root can reuse one unexpired login.

## Data

The default data root is:

```text
~/.caelis
```

Interactive sessions are stored under `~/.caelis/sessions` unless `-store-dir`
is provided.

## Architecture

See [docs/architecture.md](docs/architecture.md) for the layer map and
[docs/agent-sdk-boundary.md](docs/agent-sdk-boundary.md) for the reusable Agent
SDK package boundary and ACP-native orchestration model. See
[docs/agent-sdk-usage.md](docs/agent-sdk-usage.md) for the SDK quickstart and
consumer contracts, [docs/acp-projection-architecture.md](docs/acp-projection-architecture.md)
for ACP-to-Surface projection, and
[docs/control-client-m2-design.md](docs/control-client-m2-design.md) for the
accepted Control client command/feed contract. Release mechanics live in
[docs/release.md](docs/release.md).

## Development

Caelis requires the Go version declared in `go.mod`.

```bash
make install
make commit-check
```
