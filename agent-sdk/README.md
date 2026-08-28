# Caelis Agent SDK

Import prefix: `github.com/caelis-labs/caelis/agent-sdk`

`agent-sdk` is an ordinary package tree in the root
`github.com/caelis-labs/caelis` Go module. It has no separate module, version,
release, or test lifecycle.

The Agent SDK is the reusable agent-building layer for Caelis products. Local TUI,
headless CLI, ACP server, future desktop GUI, and cloud-hosted agent environments
should assemble runnable agents through SDK contracts instead of linking against
product-host or presentation packages.

## Purpose

Product hosts inject environment-specific implementations for model access, session
storage, sandbox execution, tools, policy, and task orchestration. The SDK owns
turn mechanics, low-level runtime coordination, and the cross-domain contracts
those hosts wire together. Caelis product assembly, Agent selection, policy and
review routing, the Agent Manage Loop, and handoff decisions stay in the host
Control layer.

ACP-compatible controller, participant, event, permission, cancellation, and
transfer semantics are intentionally reusable SDK contracts. ACP is the native
collaboration language shared by built-in and external Agents. `acp-go-sdk`
owns standard ACP wire contracts and connections. Control owns its
Surface-facing update and permission DTOs plus canonical projection in
`control/appserver/eventstream` and `control/appserver/projection`; the
retired product `protocol/acp` tree is guarded against recreation, and Caelis
compatibility metadata remains private to its Control, Host, adapter, or
Surface owner.

## Package Layout

| Package | Role |
| --- | --- |
| `agent-sdk` (root) | Cross-domain public contracts: agent specs, turn requests, runtime events, capabilities, approvals, neutral handoff/transfer values, usage, and stable errors |
| `agent-sdk/approval` | Approval review contracts |
| `agent-sdk/display` | Tool and runtime display helpers |
| `agent-sdk/model` | Model contracts and provider implementations |
| `agent-sdk/policy` | Policy presets and permission helpers |
| `agent-sdk/runtime` | Local agent runtime, turn mechanics, reusable ACP-compatible endpoint/controller contracts, and low-level control-plane mechanisms |
| `agent-sdk/sandbox` | Sandbox runtime contracts and local implementations |
| `agent-sdk/session` | Session contracts and bundled file/memory stores; the file store uses a SQLite secondary index |
| `agent-sdk/skill` | Skill discovery and builtin skill tooling |
| `agent-sdk/task` | Task and subagent contracts |
| `agent-sdk/tool` | Tool registry contracts and builtin tools |

`tool.Definition.Name` is the sole execution identity. Built-in tools use
PascalCase model-visible names: `Read`, `ViewImage`, `Write`,
`Patch`, `Glob`, `Grep`, `RunCommand`, `Task`, `Plan`, `Skill`, `WebSearch`,
and `WebFetch`. `ViewImage` is exposed only when the selected model declares
image-input support from maintained endpoint metadata or an explicit model
override; generic compatible endpoints remain conservative. `Spawn` and
`ToolSearch` are injected only when their capabilities are available.
Tool lookup is exact and case-sensitive; external MCP tool names are preserved
unchanged. Product projectors and Surfaces may attach private presentation
semantics to the exact built-in names, but those labels do not create aliases
or additional executable identities.

## Quickstart

Use a Caelis module tag and import only the SDK capabilities the host needs:

```bash
go get github.com/caelis-labs/caelis@<version>
```

The executable
[`quickstart_external_test.go`](runtime/quickstart_external_test.go) shows a
minimal host-local Agent using only supported imports. The external-consumer
gate compiles that example and every path in
[`supported-packages.txt`](supported-packages.txt) outside the repository.

## Dependency Boundary

SDK packages must not depend on Caelis product-host or presentation code,
including:

- `app/*`
- `surfaces/*`
- `protocol/acp/*`
- the retired product-host `ports/*` tree
- repository `internal/*` packages outside the `agent-sdk` package tree

Those packages belong to the Caelis product host and surface layers. The ban on
importing root `protocol/acp/*` packages preserves dependency direction; it does
not ban reusable ACP semantics in the SDK. SDK consumers should depend only on
`github.com/caelis-labs/caelis/agent-sdk/...` imports and their own host-specific
implementations of SDK contracts. Their selected version is the Caelis root
module version.

## Architecture and Stability

The accepted package boundary, ACP-native orchestration model, and durability
invariants are documented in
[Agent SDK Boundary](../docs/agent-sdk-boundary.md).

Exported package comments and typed errors are the consumer contract. Prefer
the narrowest interface a host needs and treat diagnostic `Error()` text as
unstable.

Module or repository extraction is not a current goal. Package independence is
enforced by dependency closure, architecture lint, explicit public contracts,
and external consumer tests against the root module. Handoff is authorized by
the host Control layer, never by an LLM-facing tool, and Caelis does not provide
a deterministic workflow engine.

Only the exact import paths in
[`supported-packages.txt`](supported-packages.txt) are compiled as the supported
external SDK surface. Other non-`internal` packages are bundled Caelis
implementations or experimental helpers. Before v1, routine commits do not run
a declaration-level source-compatibility gate; durable data and protocol
contracts remain governed by their normative documents. `make
sdk-boundary-check` verifies the SDK dependency closure and compiles the
allowlist from a separate consumer module.

Supported consumers should depend on the smallest capability they need. The
session package exposes separate lifecycle, reader, appender, binding, and
state contracts; sandbox exposes runner, async-runner, filesystem, descriptor,
and backend-reporting capabilities. Endpoint and subagent bridges reuse the
root approval option/response/tool-call and cancellation contracts rather than
defining transport-specific copies.

## Development

From the Caelis repository root:

```bash
go test ./agent-sdk/...
make sdk-boundary-check
make sdk-proxy-smoke
make arch-lint
```

`sdk-boundary-check` builds the current worktree quickstart in a separate module.
`sdk-proxy-smoke` separately extracts the target tag's own fixture and supported
package list, resolves that exact version through a Go proxy, and rejects local
`replace` directives.

`make commit-check` runs the package-boundary checks together with root-module
formatting, lint, vet, tests, and builds. Root commands cover SDK packages once;
there is no separate SDK test pass.
