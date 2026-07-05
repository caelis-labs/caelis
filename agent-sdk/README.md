# Caelis Agent SDK

Go module: `github.com/caelis-labs/caelis/agent-sdk`

The Agent SDK is the reusable agent-building layer for Caelis products. Local TUI,
headless CLI, ACP server, future desktop GUI, and cloud-hosted agent environments
should assemble runnable agents through SDK contracts instead of linking against
product-host or presentation packages.

## Purpose

Product hosts inject environment-specific implementations for model access, session
storage, sandbox execution, tools, policy, and task orchestration. The SDK owns
turn mechanics, runtime orchestration, and the cross-domain contracts those hosts
wire together.

## Package Layout

| Package | Role |
| --- | --- |
| `agent-sdk` (root) | Cross-domain public contracts: agent specs, turn requests, runtime events, capabilities, approvals, handoff values, usage, and stable errors |
| `agent-sdk/approval` | Approval review contracts |
| `agent-sdk/display` | Tool and runtime display helpers |
| `agent-sdk/model` | Model contracts and provider implementations |
| `agent-sdk/policy` | Policy presets and permission helpers |
| `agent-sdk/runtime` | Local agent runtime, control-plane helpers, and controller contracts |
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
- repository `internal/*` packages outside the SDK module

Those packages belong to the Caelis product host and surface layers. SDK consumers
should depend only on `github.com/caelis-labs/caelis/agent-sdk/...` imports and
their own host-specific implementations of SDK contracts.

## Development

From the SDK module root:

```bash
go test -count=1 ./...
```

The Caelis repository also runs `make sdk-standalone-check` to copy this module
outside the product tree and verify module independence, release artifact
hygiene, and buildability. Set `SDK_STANDALONE_RUN_TESTS=1` when the copied SDK
module should also run its full test suite.
