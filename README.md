# caelis

`caelis` is a terminal-first agent runtime with an interactive TUI, a headless
one-shot CLI mode, and an ACP stdio server for external agent clients.

It stores local state under `~/.caelis` by default and supports model provider
configuration, session persistence, approval-aware tool execution, built-in
filesystem/search/shell tools, subagent tasks, and ACP-backed participants.

## Install

From npm:

```bash
npm i -g @onslaughtsnail/caelis
```

or without a global install:

```bash
npx @onslaughtsnail/caelis --help
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

## Data

The default data root is:

```text
~/.caelis
```

Interactive sessions are stored under `~/.caelis/sessions` unless `-store-dir`
is provided.

## Development

Caelis requires the Go version declared in `go.mod`.

```bash
make install
make commit-check
```
