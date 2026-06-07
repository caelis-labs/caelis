# Validation Plan

This document defines the verification gates for the rewrite. It complements
the roadmap by making success measurable.

## Default Commands

Use the project Makefile when possible because it routes Go caches into
`.tmp/cache`:

```bash
make test
make arch-lint
git diff --check
```

For targeted Go commands outside Make, keep cache writes inside the workspace:

```bash
GOMODCACHE="$PWD/.tmp/cache/gomod" \
GOCACHE="$PWD/.tmp/cache/gocache" \
GOTMPDIR="$PWD/.tmp/cache/gotmp" \
go test ./session/... ./model/...
```

Run `make quality` before merge-ready milestones. Run `make release-dry-run`
only when packaging, versioning, npm, or release metadata changed.

## Phase 1 Validation

Phase 1A is architecture design only. The validation gates are document and
contract checks:

```bash
git diff --check
rg -n 'TBD|TODO|FIXME' docs/refactor --glob '!validation-plan.md'
```

Acceptance:
- every package named in the roadmap is assigned one owner;
- dependency rules form a strict DAG;
- built-in layering is documented before built-ins are written;
- [phase-1-preflight.md](phase-1-preflight.md) has no unanswered item;
- no new document is added under `docs/refactor/` unless it replaces an
  existing document and is linked from `README.md`;
- public Caelis contracts use Caelis domain types, not adk-go concrete APIs;
- optional skeleton code has no deferred-implementation panic paths;
- no old `ports/impl` split is reintroduced in the target architecture.

If Phase 1B creates an optional behaviorless contract skeleton, add:

```bash
# All packages compile (even if empty)
go build ./...

# Architecture lint passes
make arch-lint

# No circular dependencies
go list ./... | while read pkg; do
  go list -f '{{range .Imports}}{{.}} {{end}}' "$pkg"
done | grep -v internal | sort  # verify DAG
```

Additional acceptance for a skeleton:
- every package in the target tree exists with a `doc.go`;
- every interface and pure type compiles;
- architecture lint enforces the dependency DAG;
- no file store, provider, tool, sandbox, runner, gateway, ACP, or TUI
  behavior exists.

## Phase 2+ Validation

### Session Store Round Trip

Purpose: prove the durable store can rebuild model-visible context.

Required coverage:

- user text and multimodal content;
- assistant text;
- assistant reasoning with provider replay metadata;
- assistant tool-use parts;
- multiple tool calls in one assistant turn;
- matching tool results;
- tool errors;
- compaction/system context;
- participant and delegated subagent visibility;
- `ui_only`, `overlay`, and `mirror` filtering.

Acceptance:

- rebuilt `[]model.Message` equals the runtime-produced semantic sequence;
- no ACP transcript text is needed to rebuild model context;
- no transient stream chunk is needed for reload.

Target packages: `session/...`, `session/file/`

```bash
go test ./session/...
```

### Gateway Replay

Purpose: prove session events project to stable gateway events.

Required coverage:

- replay after cursor;
- replay limit;
- missing cursor error;
- active run plus replay;
- control-plane state with controller and participants;
- approval requested/reviewed events;
- usage snapshots.

Acceptance:

- gateway `EventEnvelope` fields preserve event kind, run/turn/session
  identity, origin, protocol payload, usage, narrative, tool call, tool
  result, plan, participant, and lifecycle facts.

Target packages: `gateway/...`, `gateway/kernel/`

```bash
go test ./gateway/...
```

### ACP Projection

Purpose: prove standard ACP remains the client projection contract.

Required coverage:

- user message chunks;
- assistant thought chunks;
- assistant message chunks;
- tool calls;
- tool call updates;
- request permission;
- plan updates;
- terminal content and stream ids in `_meta`;
- participant and subagent origin metadata.

Acceptance:

- ACP projection derives from gateway/session semantics;
- Caelis `_meta` improves display but is not the only copy of model-critical
  state;
- live and replay projection keep the same semantic ordering.

Target packages before Presentation exists: `acp`, `acp/client`,
`acp/projector`, `acp/terminal`

```bash
go test ./acp ./acp/client ./acp/projector ./acp/terminal
```

### TUI Transcript And UX

Purpose: preserve the delivered terminal experience in the new package
structure.

Required coverage:

- golden render tests for normal transcript, reasoning, tools, terminal
  output, approvals, plans, and subagents;
- resize/follow-tail behavior;
- command dispatch;
- command completion;
- model/provider connection wizard;
- resume and compact flows;
- ACP controller and local command behavior.

Acceptance:

- no visible regression unless explicitly accepted;
- TUI consumes gateway/ACP eventstream data rather than local runtime
  internals;
- transcript reducer can be tested without Bubble Tea process state.

Target packages: `tui/...`, `headless/...`, `acp/server/...`, `app/...`

```bash
go test ./tui/... ./headless/... ./acp/server/... ./app/...
```

### Runtime Agent Loop

Purpose: preserve model/tool behavior.

Required coverage:

- streaming and non-streaming final assistant response;
- invalid tool-call repair;
- single tool call;
- concurrent multi-tool call;
- tool progress events;
- tool error result;
- pending user submission drain;
- overflow compaction recovery;
- interruption/cancellation.

Acceptance:

- event order remains stable;
- persisted events remain valid semantic events;
- no duplicate user input or assistant tool call after retry/recovery.

Target packages: `agent/llmagent/`, `runner/`

```bash
go test ./agent/... ./runner/...
```

### Layer 4 Gap Closure Gate

Purpose: prove the new Agent SDK can carry Caelis runtime responsibilities
before Gateway, TUI, headless, or ACP server packages depend on it.

This gate applies to the temporary Phase 5X plan in
[refactor-roadmap.md](refactor-roadmap.md). It is stricter than package unit
coverage because the risk is not compilation; the risk is accidentally moving
Layer 4 semantics into Gateway.

Required package coverage:

- `session/...`: event validation, canonicalization, controller and
  participant bindings, structured state, file-store restart, corrupt log
  rejection, model-context rebuild;
- `policy/...`: workspace-write path rules, sensitive home config roots,
  command hard-deny rules, escalation approval, read-only denial;
- `sandbox/...`: route selection, fail-closed sandbox requirement, host route
  diagnostics, backend filesystem and command execution;
- `tool/...`: filesystem read/write/patch/list/glob/search, command execution,
  task control, spawn delegation, truncation, display-only diff payloads;
- `agent/...` and `runner/...`: prompt assembly input, invalid tool-call
  repair, concurrent tool execution, observer events, compaction, overflow
  retry, cancellation, durable event persistence;
- `acp/...`: standard schema, client transport, terminal lifecycle,
  request_permission, projector, external ACP normalization.

Required deterministic E2E coverage:

- runtime model request equals durable replay context after a normal turn;
- runtime model request equals durable replay context after tool call/result;
- restart from `session/file` produces the same `[]model.Message`;
- mirror and ui-only events are excluded from model context;
- display-only tool content does not become model-visible content;
- `RUN_COMMAND` uses the selected sandbox backend and never silently falls
  back to host when sandbox execution was required;
- policy denial prevents the base tool from running;
- approval allow/deny produces the correct tool result and approval payload;
- long-running command emits transient observer events and persists only the
  canonical final state;
- `TASK` wait/write/cancel operate on a durable task snapshot;
- `SPAWN` creates scoped child work and bridges approvals;
- model overflow triggers one compaction event and one retry without duplicate
  user input or tool-use parts;
- ACP projection from persisted events matches the standard `session/update`
  and `session/request_permission` golden payloads;
- external ACP input normalizes to canonical session events before storage.

Required real-provider smoke coverage:

- use local `~/.caelis/config.json` when present;
- resolve the provider through `runner.ModelRegistry`, not by manually
  preparing an agent with an LLM instance;
- perform at least one plain model turn and one restart/replay turn;
- skip with an explicit test message when local credentials are absent, so
  package-level tests remain portable.

Commands:

```bash
GOMODCACHE="$PWD/.tmp/cache/gomod" \
GOCACHE="$PWD/.tmp/cache/gocache" \
GOTMPDIR="$PWD/.tmp/cache/gotmp" \
go test ./session/... ./policy/... ./sandbox/... ./tool/... ./agent/... ./runner/... ./acp/...

GOMODCACHE="$PWD/.tmp/cache/gomod" \
GOCACHE="$PWD/.tmp/cache/gocache" \
GOTMPDIR="$PWD/.tmp/cache/gotmp" \
go test ./test/e2e/layer4 -v -count=1

make arch-lint
git diff --check
```

Acceptance:

- no Critical or High Layer 4 gap from Phase 5X remains open;
- all deterministic E2E tests identify the fake boundaries they use;
- real-provider tests are labeled as provider smoke, not full tool/runtime
  coverage;
- no Layer 4 package imports `gateway`, `tui`, `headless`, `acp/server`,
  `app/gatewayapp`, or old `ports`, `impl`, `protocol`, `surfaces` packages;
- a reviewer approves the final Layer 4 closure before Phase 6 starts.

### Policy, Approval, And Sandbox

Purpose: preserve enforcement boundaries.

Required coverage:

- read-only filesystem tools;
- destructive filesystem tools;
- command execution constraints;
- sensitive path policy;
- approval request generation;
- manual approval;
- automatic approval review;
- host route;
- platform sandbox route;
- Windows workspace-write ACL behavior on Windows runners.

Acceptance:

- policy profile and approval mode stay separate;
- sandbox constraints are backend-neutral before backend execution;
- explicit sandbox route fails closed when the backend cannot enforce it;
- host route remains explicit and diagnosable.

Target packages: `policy/...`, `sandbox/...`

```bash
go test ./policy/... ./sandbox/...
```

### ACP Agents And Subagents

Purpose: preserve multi-agent workflows.

Required coverage:

- register built-in ACP agent;
- register custom ACP agent;
- installable adapter list;
- main-controller handoff;
- sidecar prompt;
- delegated subagent `SPAWN`;
- participant detach on error;
- context visibility between main, sidecar, and delegated agents;
- agent profile binding to self, model, and ACP target.

Acceptance:

- external ACP input normalizes into canonical session events before
  storage;
- delegated private work does not enter main model context accidentally;
- shared final dialogue remains visible where intended.

Target packages: `acp/...`, `runner/`

```bash
go test ./acp/... ./runner/...
```

## Suggested Targeted Commands

Use these as phase-level checks before broader `make test` once the
corresponding packages exist:

```bash
# Phase 2: Core domain contracts and pure helpers
go test ./model/... ./session/... ./tool/... ./sandbox/... ./policy/... ./agent/...

# Phase 3: Persistence, providers, sandbox, policy
go test ./session/... ./model/... ./sandbox/... ./policy/...

# Phase 4: Built-ins and skills
go test ./tool/... ./skill/...

# Phase 5: Agent and runner
go test ./agent/... ./runner/...

# Phase 6: Gateway
go test ./gateway/...

# Phase 7: ACP
go test ./acp ./acp/client ./acp/projector ./acp/terminal

# Phase 8: TUI and integration
go test ./tui/... ./headless/... ./acp/server/... ./app/... ./cmd/caelis/...

# Full suite
make test
```
