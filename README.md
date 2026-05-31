# Caelis

Caelis is an ACP-native agent runtime and gateway. It runs as an interactive TUI,
a headless CLI, or an ACP stdio server that can be used by clients such as Zed.

The project keeps a small core around canonical session events, runtime
orchestration, approval policy, prompt materialization, and ACP projection.
Providers, stores, sandboxes, tools, skills, prompts, and external ACP agents are
replaceable adapters or registry contributions.

## Why Caelis

- ACP-native by design: Caelis can orchestrate external ACP agents and expose
  itself as an ACP agent.
- One shared product kernel: TUI, future APP, CLI, and ACP surfaces consume the
  same app services and view-model payloads.
- Durable canonical state: replay and resume are based on model-visible session
  semantics, not UI transcript cache.
- Replaceable infrastructure: model providers, sandbox backends, JSONL/SQLite
  stores, tools, prompt sources, and skills can evolve independently.
- Practical local workflow: interactive turns, headless prompts, approvals,
  sandbox diagnostics, async tasks, and external agent delegation live in one
  binary.

## Quick Start

Install from npm:

```bash
npm i -g @onslaughtsnail/caelis
```

Run the interactive TUI:

```bash
caelis
```

If no model is configured, start the TUI and use `/connect`.

Run a one-shot prompt:

```bash
caelis -p "Summarize this repository."
```

Serve Caelis as an ACP stdio agent:

```bash
caelis acp
```

Print diagnostics:

```bash
caelis doctor
```

Run from source:

```bash
go run ./cmd/caelis -h
go run ./cmd/caelis
go run ./cmd/caelis -p "Summarize this repository."
go run ./cmd/caelis acp
```

Build from source:

```bash
go install ./cmd/caelis
```

Caelis currently requires the Go version declared in `go.mod`.
