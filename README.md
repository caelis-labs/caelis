# Caelis

**A collaboration workspace for AI agents.**

[![Latest release](https://img.shields.io/github/v/release/caelis-labs/caelis)](https://github.com/caelis-labs/caelis/releases/latest)
[![Quality](https://github.com/caelis-labs/caelis/actions/workflows/quality.yml/badge.svg)](https://github.com/caelis-labs/caelis/actions/workflows/quality.yml)
[![npm](https://img.shields.io/npm/v/@caelis/caelis)](https://www.npmjs.com/package/@caelis/caelis)

[English](README.md) · [简体中文](README.zh-CN.md)

Any [ACP-compatible agent](https://agentclientprotocol.com/) can join the same
Caelis collaboration network. The built-in runtime, native collaborators, and
external ACP agents work as **participants** with shared mailbox and messaging
semantics—not just isolated subtasks that report back once.

Use Caelis to explore repositories, implement changes, run tests, and review
work with one agent or several working together. Participants can discover and
message each other, keep their own conversations, and continue working when new
input arrives. You follow their progress and send input from a terminal workspace,
or use Caelis through a one-shot command or ACP client.

The collaboration network is scoped to a local Caelis Session, not a hosted or
cross-Session messaging service. External agents connect through ACP stdio; their
support for injected MCP tools, steering, and history determines which
collaboration features are available.

[Website](https://caelis.dev) · [Releases](https://github.com/caelis-labs/caelis/releases) · [Documentation](#documentation)

## Quick start

Install on macOS or Linux, then open a repository:

```bash
curl -fsSL https://caelis.dev/install.sh | sh
cd /path/to/your/project
caelis
```

On first launch:

1. Run `/connect` to sign in with ChatGPT Codex, configure an API or local
   model provider, or connect an ACP agent.
2. Use `/model` to choose the main agent's model and reasoning effort.
3. Ask for a concrete outcome: `Map this repository and explain how to run its tests.`

The launch directory is your workspace. Sessions are stored locally; use
`/resume` to return to earlier work.

### Other installation methods

| Method | Command |
| --- | --- |
| Windows PowerShell | `irm https://caelis.dev/install.ps1 \| iex` |
| npm | `npm install -g @caelis/caelis` |
| npm without install | `npx @caelis/caelis --help` |
| Source | `git clone https://github.com/caelis-labs/caelis.git && cd caelis && make install` |

Release binaries support macOS, Linux, and Windows on x64 and ARM64. Building
from source requires the Go version declared in [`go.mod`](go.mod).

## Work with participants

Start with native participants using the current model, or mix in other models
and ACP agents:

1. Use `/connect` to add the providers or ACP agents you want. For an external
   agent, install its executable separately and make it available on the Host's
   `PATH`; choose **Custom** for another ACP stdio command.
2. Open `/team` to configure participant profiles such as `breeze`, `orbit`,
   and `zenith`, or create a custom role in the Team overlay.
3. Ask the main agent to coordinate the work, for example:

   ```text
   Review this change with two participants: one for correctness and one for
   test coverage. Have them exchange relevant findings, then summarize the
   issues and recommended fixes. Do not edit files yet.
   ```

The controller starts participant conversations with `StartThread` and observes
public results with `ReadThread` and `WaitThread`. All participants use
`ListThreads` and `SendMessage` for discovery and communication through the same
Control-owned mailbox service. Queued mail is not proof of delivery; collaboration
does not grant participants permission to orchestrate or change authority.

Click a participant link or the running/done count in the footer to open its
workspace. Switch between participants, use an overlay or split view, and send
text or images directly without replacing the main conversation. **F6** switches
composer focus; **F7** shows or hides the pane without stopping work.

See the [Participant guide](docs/participants.md) for configuration and workspace
controls, and [External ACP agents](docs/external-acp-agents.md) for capability
requirements and delivery guarantees.

## What else you get

- **Repository tools:** inspect and edit files, search code, and run commands
  with visible tool requests and approval modes.
- **Model choice:** ChatGPT Codex sign-in, API-key and local providers, and
  ACP agents in one model picker.
- **Workspace extensions:** MCP servers, skills, and plugins; project MCP
  configuration requires workspace trust.
- **Durable sessions and memory:** resume conversations and use built-in
  `Remember` and `Recall`. Memory needs no separate installation and invokes no
  model unless you explicitly bind the Memory Steward in `/team`.
- **Interactive or scriptable:** a TUI, text, versioned JSON, streaming JSONL,
  and an ACP server backed by the same Session and Control services.

## Common commands

| Goal | Command |
| --- | --- |
| Start the TUI | `caelis` |
| Observe an existing Session in another terminal | `caelis -session <session-id>` |
| Run one prompt | `caelis -p "Summarize this repository."` |
| Return one structured result | `caelis -p "Review the changes." -format json` |
| Stream ACP envelopes | `caelis -p "Run the tests." -format jsonl` |
| Read a prompt from stdin | `printf '%s\n' "Explain this code." \| caelis -format text` |
| Serve Caelis over ACP | `caelis acp` |
| Repair recognized compatibility data and check health | `caelis doctor` |
| Inspect the managed local Host | `caelis service status` |
| Show all options | `caelis -h` |

Use `-session` to target a durable Session, `-store-dir` to choose another data
root, `-control-url` to attach to a specific Host, and `-embedded` for explicit
single-process operation.

In the TUI, `/resume` or `Ctrl+O` opens the Session list, with active Sessions
marked `running`. Selecting a Session changes the view while its work continues
on the Host. Each terminal can observe the same Session, see later Turns, send
prompts, and answer approvals. The first valid approval answer takes effect;
the other observers close that approval when its result arrives.

`/quit` (also `/exit`), `Ctrl+D`, or two presses of `Ctrl+C` close the TUI without
cancelling an accepted Turn. Press `Esc` in the Session workspace to interrupt
the current Turn; inside the Session list, `Esc` closes the list. Continued work
after terminal exit requires a managed or remote Host. An `-embedded` Host ends
with its owning process. Reattaching from another terminal requires the same
Host and data root.

## Safety and local data

Caelis starts in `auto-review` mode. Guardian reviews tool requests and fails
closed when it cannot make a valid decision. Use `/mode manual` when you want to
approve each request yourself. External agents retain their own execution
capabilities; Caelis handles the permission requests they expose through ACP.

ChatGPT subscription access uses a community-compatible Codex OAuth flow rather
than a documented third-party OpenAI integration. Browser or device login stores
the refresh credential in the selected Store with private file permissions.

Release builds store Sessions and credentials under `~/.caelis`; development
builds default to `~/.caelis-dev/default`. Credential files are private to the
user. `-store-dir` changes the data root, not the workspace. Model requests go to
your selected provider or external agent; local storage does not imply offline
inference.

Managed local startup failures include a stable `CAELIS_STARTUP_*` code. In
particular, `CAELIS_STARTUP_WORKSPACE_IDENTITY_CONFLICT` is repaired by
`caelis doctor`; normal startup does not rewrite durable Session data.

## Documentation

- [Terminal UI](surfaces/tui/README.md): themes and responsive rich diffs.

- [Participants](docs/participants.md): configure collaborators and use their workspaces.
- [External ACP agents](docs/external-acp-agents.md): connection, capabilities, and messaging contracts.
- [Agent SDK](agent-sdk/README.md): embed or extend the reusable Go runtime.
- [Architecture](docs/architecture.md): repository ownership and dependency boundaries.
- [Application Runtime](docs/application-runtime.md): application enrollment, Session profiles, callback receipts, and resources.
- [Testing](docs/testing.md): default and change-scoped validation.
- [Release](docs/release.md): publish and verify official artifacts.

## Develop

```bash
make install
make commit-check
```

`make commit-check` checks Go formatting, runs full lint, and checks diff
whitespace. Run focused tests for changed behavior; PR CI runs the full test/build
gate. Use `make quality` for an optional full local run. See
[Testing](docs/testing.md) for prerequisites, coverage, and platform checks.

## License

[Apache-2.0](LICENSE).
