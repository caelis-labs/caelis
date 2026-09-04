# Caelis

**A terminal-native AI coding agent for real repositories.**

[![Latest release](https://img.shields.io/github/v/release/caelis-labs/caelis)](https://github.com/caelis-labs/caelis/releases/latest)
[![Quality](https://github.com/caelis-labs/caelis/actions/workflows/quality.yml/badge.svg)](https://github.com/caelis-labs/caelis/actions/workflows/quality.yml)
[![npm](https://img.shields.io/npm/v/@caelis/caelis)](https://www.npmjs.com/package/@caelis/caelis)

Caelis can inspect and edit files, search code, run commands, keep durable
sessions, and delegate bounded tasks. You choose the model or external agent;
Caelis keeps tool execution visible and governed by an approval policy.

Use the same runtime through an interactive TUI, a one-shot command, or an
[Agent Client Protocol (ACP)](https://agentclientprotocol.com/) server.

[Website](https://caelis.dev) · [Releases](https://github.com/caelis-labs/caelis/releases) · [Documentation](#documentation)

## Start in 60 seconds

Install on macOS or Linux, then open a repository:

```bash
curl -fsSL https://caelis.dev/install.sh | sh
cd /path/to/your/project
caelis
```

On first launch:

1. Run `/connect` to sign in with ChatGPT Codex, configure an API or local
   model provider, or connect an ACP agent.
2. Use `/model` to choose the model and reasoning effort.
3. Ask for a concrete outcome, such as `Map this repository and explain how to
   run its tests.`

The launch directory is the workspace. Sessions are stored locally and can be
resumed later.

Other installation methods:

| Method | Command |
| --- | --- |
| Windows PowerShell | `irm https://caelis.dev/install.ps1 \| iex` |
| npm | `npm install -g @caelis/caelis` |
| npm without install | `npx @caelis/caelis --help` |
| Source | `git clone https://github.com/caelis-labs/caelis.git && cd caelis && make install` |

Building from source requires the Go version declared in [`go.mod`](go.mod).

## Why Caelis

- **Terminal-native:** streaming output, tools, approvals, tasks, and history in
  one TUI.
- **Model-neutral:** ChatGPT Codex sign-in, API-key and local providers, and
  ACP-compatible agents share one model picker.
- **Guarded execution:** filesystem, search, shell, and extension tools pass
  through explicit approval modes.
- **Bounded delegation:** named subagent profiles keep child work observable
  without flattening it into the parent transcript.
- **Workspace extensions:** MCP servers, skills, and plugins are assembled for
  the workspace; project MCP configuration requires workspace trust.
- **Durable Memory:** the capability is enabled by default and exposes only
  `Remember` and `Recall` from the Memory package embedded in the Host. It uses
  zero model tokens unless you explicitly bind the Memory Steward in
  `/subagent`; no separate Memory installation or endpoint is required.
- **Scriptable:** text, versioned JSON, and streaming JSONL use the same durable
  Session and Control paths as the TUI.

## Common commands

| Goal | Command |
| --- | --- |
| Start the TUI | `caelis` |
| Run one prompt | `caelis -p "Summarize this repository."` |
| Return one structured result | `caelis -p "Review the changes." -format json` |
| Stream ACP envelopes | `caelis -p "Run the tests." -format jsonl` |
| Read a prompt from stdin | `printf '%s\n' "Explain this code." \| caelis -format text` |
| Serve Caelis over ACP | `caelis acp` |
| Repair recognized compatibility data and report current health | `caelis doctor` |
| Inspect the managed local Host | `caelis service status` |
| Show all options | `caelis -h` |

Managed local startup failures include a stable `CAELIS_STARTUP_*` code. In
particular, `CAELIS_STARTUP_WORKSPACE_IDENTITY_CONFLICT` is repaired by
`caelis doctor`; normal startup does not rewrite durable Session data.

Use `-session` to target a durable Session, `-store-dir` to choose another data
root, `-control-url` to attach to a specific Host, and `-embedded` for explicit
single-process operation.

## Safety and local data

Caelis starts in `auto-review` mode. Guardian reviews tool requests and fails
closed when it cannot make a valid decision. Use `/mode manual` when you want to
approve each request yourself.

ChatGPT subscription access uses a community-compatible Codex OAuth flow rather
than a documented third-party OpenAI integration. Browser or device login stores
the refresh credential in the selected Store with private file permissions.

Release builds store Sessions and credentials under `~/.caelis`; development
builds default to `~/.caelis-dev/default`. Credential files are private to the
user. `-store-dir` selects a different data root; it does not change the
workspace directory.

## Documentation

- [External ACP agents](docs/external-acp-agents.md): connect and operate local
  ACP-compatible agents.
- [Agent SDK](agent-sdk/README.md): embed or extend the reusable Go runtime.
- [Architecture](docs/architecture.md): repository ownership and dependency
  boundaries.
- [Testing](docs/testing.md): default and change-scoped validation.
- [Release](docs/release.md): publish and verify official artifacts.

## Develop

```bash
make install
make commit-check
```

`make commit-check` runs lint, the full untagged test suite, and build. See
[Testing](docs/testing.md) for checks selected by the affected boundary.
