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

Consume one versioned JSON result:

```bash
caelis -p "Summarize this repository." -format json
```

Stream target-filtered ACP Envelopes as JSONL and receive one terminal result
or post-flag-parsing error record:

```bash
caelis -p "Summarize this repository." -format jsonl
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
- `-format`: `text`, `json`, or `jsonl` for headless output.
- `-interactive`: force TUI mode when stdin is piped.
- `-no-animation`: reduce TUI motion (also available as
  `CAELIS_TUI_NO_ANIMATION=true`).
- `-session`: resume or target a session id.
- `-store-dir`: override the default store directory.
- `-control-url`: attach to an existing Control Host.
- `-embedded`: force an in-process Host instead of managed local Host attach.

The workspace is always the process's current directory. Caelis derives its
internal workspace identity from the canonical absolute path; there is no
workspace flag to keep in sync.

In `auto-review`, Guardian model requests use the provider-neutral bounded retry
policy. If Guardian still cannot produce one validated decision, Caelis stops
the current Turn with `guardian_unavailable`, executes no requested action, and
does not silently fall back to manual approval. Retry the Turn after Guardian is
available, or use `/mode manual` before starting sensitive work.

Provider setup and API-key replacement are available only through `/connect`.
Runtime startup never creates or overwrites model profiles or credentials.
`/model use` changes the global default by storing one existing ModelProfile ID
and one supported reasoning effort; it does not rewrite the profile definition.

Use `/subagent` to configure the fixed Caelis delegation profiles: Breeze for
fast bounded work, Orbit for general implementation and review, and Zenith for
deep or high-risk analysis. Each binding selects one connected `ModelProfile`
and an explicit reasoning effort. `self` separately uses the current Session
profile and effort; an unbound fixed profile is not exposed in Spawn or direct
run catalogs. Provider and ACP connections both produce `ModelProfile` choices,
while raw model and external Agent IDs remain hidden from the model-facing Spawn
catalog. Guardian accepts provider profiles only; Reviewer accepts provider
and ACP profiles.

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

Caelis follows one dependency direction:

```text
TUI / Headless / ACP
    -> control/appserver and focused Control contracts
        -> Agent Runtime / SDK
```

`control/appserver` is the transport-neutral product entry point shared by
in-process and HTTP clients. `app/*` owns the private Control Host composition,
transport adapters, and concrete components; presentation code never depends
on that Host implementation. The in-process adapter receives the concrete Host
only at its composition root and injects focused services into leaf
capabilities. Session-bound adapter paths select only the focused services
needed from an authorized Runtime lease; neither the Host nor a Runtime
aggregate reaches leaf services. Host presentation reads are normalized to
`control/appserver` types before the local adapter; ACP wire projection remains
outside Host composition. `agent-sdk/*` remains reusable below the product
Control layer.

See [docs/architecture.md](docs/architecture.md) for the repository map,
[docs/agent-sdk-boundary.md](docs/agent-sdk-boundary.md) for the reusable Agent
SDK boundary, and [docs/acp-projection-architecture.md](docs/acp-projection-architecture.md)
for ACP-to-Surface projection. The [Agent SDK README](agent-sdk/README.md) is
the SDK consumer entry point. Release mechanics live in
[docs/release.md](docs/release.md).

## Development

Caelis requires the Go version declared in `go.mod`.

```bash
make install
make commit-check
```
