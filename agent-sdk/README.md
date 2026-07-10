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
collaboration language shared by built-in and external Agents. The product
`protocol/acp` packages own wire transport, compatibility, and surface
projection rather than those reusable semantics.

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
| `agent-sdk/session` | Session contracts and file, memory, and sqlite stores |
| `agent-sdk/skill` | Skill discovery and builtin skill tooling |
| `agent-sdk/task` | Task and subagent contracts |
| `agent-sdk/tool` | Tool registry contracts and builtin tools |

## Dependency Boundary

SDK packages must not depend on Caelis product-host or presentation code,
including:

- `app/*`
- `surfaces/*`
- `protocol/acp/*`
- `ports/gateway` and other product-host `ports/*` packages
- repository `internal/*` packages outside the `agent-sdk` package tree

Those packages belong to the Caelis product host and surface layers. The ban on
importing root `protocol/acp/*` packages preserves dependency direction; it does
not ban reusable ACP semantics in the SDK. SDK consumers should depend only on
`github.com/caelis-labs/caelis/agent-sdk/...` imports and their own host-specific
implementations of SDK contracts. Their selected version is the Caelis root
module version.

## Architecture and Stability

The accepted boundary, ACP-native orchestration model, durability risks, and
migration plan are documented in
[Agent SDK Boundary and Evolution Plan](../docs/agent-sdk-boundary.md).

Module or repository extraction is not a current goal. Package independence is
enforced by dependency closure, architecture lint, explicit public contracts,
and external consumer tests against the root module. Handoff is authorized by
the host Control layer, never by an LLM-facing tool, and Caelis does not provide
a deterministic workflow engine.

## Development

From the Caelis repository root:

```bash
go test ./agent-sdk/...
make sdk-boundary-check
make arch-lint
```

`make commit-check` runs the package-boundary checks together with root-module
formatting, lint, vet, tests, and builds. Root commands cover SDK packages once;
there is no separate SDK test pass.
