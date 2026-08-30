# Caelis

**A terminal-native AI coding agent for working on real repositories.**

[![Latest release](https://img.shields.io/github/v/release/caelis-labs/caelis)](https://github.com/caelis-labs/caelis/releases/latest)
[![Quality](https://github.com/caelis-labs/caelis/actions/workflows/quality.yml/badge.svg)](https://github.com/caelis-labs/caelis/actions/workflows/quality.yml)
[![npm](https://img.shields.io/npm/v/@caelis/caelis)](https://www.npmjs.com/package/@caelis/caelis)

Caelis can inspect and edit files, search code, run commands, keep long-lived
sessions, and delegate bounded tasks. You choose the model or external agent;
Caelis keeps tool execution visible and governed by an approval policy.

Use the same runtime as an interactive TUI, a one-shot command for scripts and
CI, or an [Agent Client Protocol (ACP)](https://agentclientprotocol.com/)
server for other agent clients.

[Website](https://caelis.dev) · [Releases](https://github.com/caelis-labs/caelis/releases) · [Documentation](#documentation)

## Start in 60 seconds

Install on macOS or Linux:

```bash
curl -fsSL https://caelis.dev/install.sh | sh
```

Then open a repository:

```bash
cd /path/to/your/project
caelis
```

On first launch:

1. Run `/connect` to sign in with ChatGPT Codex, configure an API or local
   model provider, or connect an ACP agent.
2. Use `/model` to choose the model and reasoning effort for the session.
3. Ask for a concrete outcome, for example: `Map this repository and explain
   how to run its tests.`

The directory where you start Caelis is the workspace. Sessions are saved
locally, so you can leave and resume without rebuilding the context from
scratch.

## Why Caelis

- **Built for the terminal.** Work in a full-screen TUI with streaming output,
  tool activity, approvals, tasks, and session history in one place.
- **Bring your own intelligence.** Use ChatGPT Codex sign-in, API-key or local
  model providers such as Ollama, and ACP-compatible agents through one model
  picker.
- **Act with guardrails.** Filesystem, search, shell, and extension tools run
  through explicit approval modes. Automatic review fails closed if its
  Guardian cannot make a valid decision.
- **Delegate without losing the thread.** Configure Caelis subagent profiles
  for fast bounded work, general implementation, or deeper analysis, then
  follow their tasks from the parent session.
- **Extend the workspace.** Add tools through MCP servers, skills, and plugins.
  Workspace-provided MCP configuration is activated only after that workspace
  is trusted.
- **Use it beyond the TUI.** Headless text, versioned JSON, and streaming JSONL
  outputs make the same agent usable from shell scripts, CI, and other tools.

## Install

### macOS and Linux

```bash
curl -fsSL https://caelis.dev/install.sh | sh
```

### Windows PowerShell

```powershell
irm https://caelis.dev/install.ps1 | iex
```

### npm

```bash
npm install -g @caelis/caelis
```

Or run it without a global install:

```bash
npx @caelis/caelis --help
```

### From source

Caelis requires the Go version declared in [`go.mod`](go.mod).

```bash
git clone https://github.com/caelis-labs/caelis.git
cd caelis
make install
```

## Use

| Goal | Command |
| --- | --- |
| Start the interactive TUI | `caelis` |
| Run one prompt | `caelis -p "Summarize this repository."` |
| Return one versioned result | `caelis -p "Review the current changes." -format json` |
| Stream ACP envelopes as JSONL | `caelis -p "Run the tests." -format jsonl` |
| Read the prompt from stdin | `printf '%s\n' "Explain this code." \| caelis -format text` |
| Serve Caelis as an ACP agent | `caelis acp` |
| Check runtime and configuration | `caelis doctor` |
| Show every CLI option | `caelis -h` |

Useful flags:

- `-session <id>` resumes or targets a session.
- `-store-dir <path>` overrides the default data directory.
- `-interactive` forces the TUI when stdin is piped.
- `-no-animation` reduces TUI motion; the equivalent environment variable is
  `CAELIS_TUI_NO_ANIMATION=true`.
- `-control-url <url>` attaches to an existing Caelis Control Host.
- `-embedded` forces an in-process Host instead of attaching to the managed
  local Host.

## Models, agents, and safety

`/connect` keeps three choices separate: account sign-in, model-provider
configuration, and local ACP agents. Runtime startup never creates or
overwrites credentials or model profiles. `/disconnect provider` removes a
provider profile; `/disconnect acp` removes an external agent connection.

ChatGPT subscription access uses the `codex` provider and its guided sign-in.
The flow opens a browser when possible and uses device-code sign-in for
headless, SSH, and CI environments. This is a community-compatible OAuth
integration rather than a documented third-party OpenAI integration. Its
refresh credential is stored at `~/.caelis/providers/codex/auth.json` by
default with `0600` permissions.

Caelis starts in `auto-review` mode. Guardian reviews tool requests and Caelis
executes only validated approvals; if Guardian is unavailable, the current
turn stops without silently approving the action. Use `/mode manual` before a
sensitive turn when you want to approve each request yourself.

Use `/subagent` to bind the built-in delegation profiles to connected models
or ACP agents: Breeze for quick bounded work, Orbit for general implementation
and review, and Zenith for deep or high-risk analysis.

## Local data

Caelis stores its state under `~/.caelis` by default, including interactive
sessions under `~/.caelis/sessions`. Pass `-store-dir` to use another location.
The workspace itself is always the process's current directory.

## Documentation

- [External ACP agents](docs/external-acp-agents.md): connect and operate other
  ACP-compatible coding agents through Caelis.
- [Agent SDK](agent-sdk/README.md): embed or extend the reusable Go agent
  runtime.
- [Repository architecture](docs/architecture.md): understand ownership and
  dependency boundaries before contributing.
- [Testing](docs/testing.md): choose focused and repository-wide validation.
- [Release process](docs/release.md): build and publish official artifacts.

## Develop

```bash
make install
make commit-check
```

`make commit-check` runs the repository's formatting, lint, architecture,
protocol, documentation, test, and build gates.
