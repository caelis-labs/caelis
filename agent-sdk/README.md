# Caelis Agent SDK

Import prefix: `github.com/caelis-labs/caelis/agent-sdk`

The Agent SDK is the reusable Agent-building layer inside the root Caelis Go
module. It has no separate module, version, release, or test lifecycle.

Product hosts inject model access, Session storage, sandbox execution, tools,
policy, and task implementations. The SDK owns reusable Runtime mechanics and
contracts; Caelis configuration, Agent selection, review routing, orchestration,
and handoff remain in the product Control layer.

## Packages

| Package | Role |
| --- | --- |
| `agent-sdk` | Cross-domain Agent, Run, capability, approval, usage, and error contracts |
| `approval` | Approval review contracts |
| `display` | Tool and Runtime display helpers |
| `model` | Model contracts and provider implementations |
| `policy` | Policy presets and permission helpers |
| `runtime` | Local Runtime, Turn mechanics, controller and participant contracts |
| `sandbox` | Sandbox contracts and local implementations |
| `session` | Session contracts and bundled file/memory stores |
| `skill` | Skill discovery and built-in Skill tooling |
| `task` | Task and subagent contracts |
| `tool` | Tool registry contracts and built-in tools |

`tool.Definition.Name` is the sole executable identity and lookup is exact and
case-sensitive. Product presentation may recognize exact built-in names, but
display labels never create aliases or execution authority. MCP tools retain
their source identity while exposing a bounded model-visible name.

## Quickstart

Use a Caelis module tag and import only the capabilities your host needs:

```bash
go get github.com/caelis-labs/caelis@<version>
```

[`runtime/quickstart_external_test.go`](runtime/quickstart_external_test.go)
shows a minimal external consumer. Only paths listed in
[`supported-packages.txt`](supported-packages.txt) are supported external imports;
other non-`internal` packages are bundled implementations or experimental
helpers.

## Dependency boundary

SDK packages must not depend on product `control/*`, `app/*`, `surfaces/*`, the
retired `protocol/acp/*` or `ports/*` trees, or repository `internal/*` packages
outside `agent-sdk`. Product hosts and wire adapters depend inward on SDK
contracts, never the reverse.

ACP-compatible controller, participant, event, permission, cancellation, and
transfer semantics are reusable SDK contracts. `acp-go-sdk` owns standard wire
behavior; Caelis Control, Host-private adapters, and Surfaces own their product
projection and compatibility code.

The complete ownership, durability, and lifecycle rules live in
[Agent SDK Boundary](../docs/agent-sdk-boundary.md). Exported package comments
and typed errors are the consumer contract; diagnostic error text is unstable.

## Development

From the repository root:

```bash
go test ./agent-sdk/...
make sdk-boundary-check
make sdk-proxy-smoke
make arch-lint
```

`sdk-boundary-check` compiles the current worktree from a separate consumer
module. `sdk-proxy-smoke` verifies an exact published tag through a Go proxy.
These are change-scoped checks; `make commit-check` runs the shared repository
lint, full test, and build gates without repeating the SDK suite.
