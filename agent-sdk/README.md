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
| `judgment` | Typed choice, probability and relevance evaluation contracts; `judgment/typesafe` implements System One |
| `model` | Model contracts and provider implementations |
| `policy` | Policy presets and permission helpers |
| `runtime` | Local Runtime, Turn mechanics, controller and participant contracts |
| `sandbox` | Sandbox contracts and local implementations |
| `session` | Session contracts and bundled file/memory stores |
| `skill` | Skill discovery and built-in Skill tooling |
| `task` | Asynchronous Job and collaborator execution contracts |
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

## Code Mode executable entry

`builtin.BuildCoreTools` includes `js_exec`, which reexecutes the current Go
binary. The embedder must dispatch the worker argument before configuration,
Host startup, CLI parsing or signal setup, then exit with its status:

```go
if len(os.Args) == 2 && os.Args[1] == gojaexec.WorkerArgument {
    os.Exit(gojaexec.WorkerMain())
}
```

These Code Mode helpers remain bundled/experimental packages outside the
`supported-packages.txt` compatibility surface. Import
`agent-sdk/tool/codemode/gojaexec` for this entry. A library cannot install
an executable entry on behalf of its host; executables without this hook do not
support the default Goja executor. Core CLI supplies it. Registering only the
public `codemode.Tool` also injects the existing Task tool; shell is unnecessary.
The host can supply canonical selected names via
`AgentSpec.Metadata[codemode.MetadataSelectedTools]` (`[]string`); the SDK does
not choose product integrations. See [Code Mode](../docs/code-mode.md) for
permissions, output selection and Run-scoped yield limits.

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
These are change-scoped checks. `make commit-check` runs formatting, lint and
diff checks; select owning tests and additional gates from
[Testing](../docs/testing.md).
