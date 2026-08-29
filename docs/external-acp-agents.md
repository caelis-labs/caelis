# External ACP Agents

This document owns the maintained product contract for local external ACP
Agent onboarding, authentication recovery, default model behavior, and the
user-owned endpoint catalog. Layer ownership remains defined by
[Caelis Architecture](architecture.md).

## Onboarding Commands

External ACP onboarding has three Host-scoped, principal-bound AppServer
commands. `prepare` records a durable intent before process startup, Session
creation, or authentication can begin. It returns an
opaque preparation reference and exact content digest in the command receipt.
If the Agent requires authentication, the preparation becomes `needs_auth` and
publishes only wire-safe method identities; the initiating presentation layer
must choose one exact method and invoke `prepare-auth` as a second idempotent
command. A ready preparation contains normalized discovery evidence and may be
consumed by `connect`, which commits the external Agent and ModelProfile with
the expected Host configuration revision.

Preparation records are secret-free, principal-owned, time bounded, and not a
second operation ledger. They allow an intent-only operation receipt to be
recovered observationally after restart without repeating launcher, process,
authentication, or Session effects. Cleanup that cannot be proven produces an
unknown outcome and is never retried blindly. `connect` uses configuration
compare-and-save and canonical roll-forward; a committed write is not undone
when assembly refresh or durability observation later reports a warning.
Within the single Control Host, an exact-operation gate spans intent creation,
effect execution or observational recovery, and receipt completion. Restart
recovery uses the durable intent plus the preparation record; it does not rely
on a cross-process execution lease. A concurrent exact duplicate therefore
cannot observationally recover a terminal preparation before the original
creator persists its exact receipt, including a public post-commit durability
warning.

The product Surface keeps only a short-lived ready-preparation convenience
cache for the guided wizard. The durable preparation and command ledger remain
authoritative, so embedded and HTTP clients use the same request, revision,
idempotency, authentication-challenge, and recovery semantics. The retired
request-local discovery cache, hidden context-selected authentication path,
and Session-scoped connect routes are not product authorities.

Terminal login execution is an explicit caller capability, not wire data. A
bound embedded presentation may attach a local terminal runner to the
`prepare-auth` request context. HTTP carries the same method ID and receipt
contract but cannot carry that executable callback; selecting a terminal
method over HTTP therefore fails with `failed_precondition` before any login,
process restart, or child preparation effect. The principal-owned
`needs_auth` preparation remains unchanged and can be continued by a capable
embedded presentation with a new operation ID. Agent-managed authentication
methods work through both transports without this local capability.

## Authentication Recovery

Caelis preserves structured JSON-RPC call errors so the ACP
`auth_required` code (`-32000`) is detected by code rather than formatted
message text. Locally synthesized connection failures remain transport errors
and never acquire a peer JSON-RPC code. Authentication methods come only from
the Agent's `initialize.authMethods` response; persisted terminal identity is
used only to direct a non-interactive runtime back through `/connect`.

The bridge owns one authenticated-operation recovery path:

1. call the ACP operation;
2. if the response is not `auth_required`, return it unchanged;
3. select an exact declared method ID, reusing the persisted endpoint
   selection when available;
4. execute the method according to its declared type;
5. retry the original operation once.

This path covers Session open and resume as well as prompts and negotiated
steering on active controller, participant, and spawned-Agent Sessions.

Stable agent-managed methods call `authenticate` with the declared `methodId`
on the current ACP connection. A missing method type normalizes to `agent`, as
defined by stable ACP v1.

Preview terminal methods are out of band. During `/connect`, the client
advertises `clientCapabilities.auth.terminal`. Caelis closes the discovery
Agent process, launches the same configured executable with its base arguments
and environment plus only the method-declared argument and environment
additions, waits for a successful exit, then starts a new Agent process,
initializes it, and retries the Session request. A terminal method is never
sent to `authenticate`.

Controller and spawned-Agent runtime connections deliberately do not advertise
terminal authentication. They may recover a persisted agent-managed method
in-band. If a persisted terminal login expires, runtime returns an actionable
error directing the user through `/connect`, where the initiating Surface can
own the interactive process. This guidance uses the persisted terminal method
even when the runtime `initialize` response omits that method because terminal
capability was not advertised.

Recovery is single-shot. A second `auth_required` after successful
authentication is returned rather than looping or repeating an external
side effect. If authentication succeeds but `session/resume` then fails for a
non-authentication reason, the controller treats that as an unavailable stale
Session and falls back to `session/new`; method selection, login, and repeated
`auth_required` failures remain hard recovery errors.

See the upstream
[ACP authentication methods RFD](https://agentclientprotocol.com/rfds/auth-methods)
for the wire contract.

## Agent Input and Steering

Caelis uses the standard ACP input methods for external Agents. An idle child
receives `session/prompt` on its existing ACP Session. A running child receives
`_session/steering` only when the Agent advertised steering support during
`initialize`; otherwise active input is rejected explicitly. Caelis does not
advertise, call, or accept a private Agent-message method.

`SendMessage {to,message}` is a model-facing address adapter over those standard
input methods. Caelis classifies the operation as Agent communication before it
crosses the endpoint boundary and prefixes the standard ACP prompt with the
trusted sender identity. It has no delivery MessageID, durable mailbox,
target-lifecycle acknowledgement, or Task mutation. Each invocation dispatches
one input. The target Agent chooses when active steering is consumed, while the
existing child endpoint owner serializes transport operations and isolates
ambiguous outcomes. A transport loss or malformed post-dispatch response is
therefore reported as unknown and is not blindly retried.

Task observes child output, not the input operation. Input admission leaves the
Task unchanged. If the child later emits output or a terminal result, the
activity observer derives the next Task generation from those events and keeps
the same absolute stream cursor across endpoint resume or Runtime rehydration.
ACP Assistant, reasoning, tool, plan, and terminal notifications remain
first-class child events; their output `messageId` correlation is unrelated to
SendMessage input identity.

A Caelis-hosted child can address its parent or sibling through a trusted
Host-bound input sender. The Host derives the source from the exact durable
participant binding, routes an active parent through an explicit Agent-
communication submission, starts a parent Turn with that same semantic kind
when idle, and routes siblings through the same child input capability. The
parent Session persists `EventTypeContext` plus the trusted Actor and projects a
dedicated non-User transcript event. Standard ACP has no reverse
Agent-to-client routing method, so a third-party ACP host that does not supply
such a topology-aware sender cannot use `to=parent`; Caelis fails explicitly
rather than reviving a private wire extension.

After a Host or Session Runtime restart, the durable parent participant binding
still owns the child handle, placement, ACP Session ID, and Task identity. A
later idle input may recreate the endpoint process, resume that exact ACP
Session, and issue `session/prompt`; it never substitutes `session/new` or uses
Task state as input authority. Built-in managed child Sessions remain hidden
from ordinary lifecycle clients, and resume still requires the exact parent and
delegation binding.

`SendMessage` is the incremental channel for updates and questions. A child's
terminal answer remains its final response and is retrieved by the parent with
`Task read` or `Task wait`; sending the same terminal answer through both paths
creates duplicate narrative. The tool result confirms only that one ordinary
input dispatch returned successfully. It carries no target state or Turn
identity; Task read/stream derives those facts later from child output.

When an external participant itself invokes Spawn, that nested child remains
behind the participant process boundary. Caelis consumes its live child stream
locally and exposes at most one terminal `tool_call_update` per child Turn. The
FinalResponse is standard ACP tool result `content` on the participant's Spawn
call. That call is not mounted as a client terminal; no nested child narrative,
Task workspace, subagent roster entry, or subagent output overlay crosses into
the parent TUI. Live and replayed durable
participant results use the same ordinary tool panel and never infer a child
workspace from the Spawn name. This does not change the product-owned Main
Spawn workspace and Task-stream contract.

## Disconnect Safety

`/disconnect acp` is a Host-scoped AppServer command with an idempotency key and
an expected Host configuration revision. Selecting the Agent in that explicit
command authorizes removal without a second confirmation prompt. Control removes
the external Agent, its ACP profiles, and every binding to those profiles,
including a system-Agent binding, in one configuration compare-and-save. A
committed configuration write is never undone with an unconditional rollback;
later assembly refresh failures are reported as committed warnings.

`/disconnect provider` is the separate provider-profile operation. It removes
every binding to that provider profile and refreshes live Runtime model catalogs,
placement, and affected durable Session model selection immediately; it never
removes an ACP Agent connection.

`/disconnect acp` changes the Host configuration available to future Session
Runtime activations. It does not scan durable Sessions, rewrite their controller or
participant history, or invalidate a Runtime that was already assembled. Such
an activated Runtime may continue to use the external Agent endpoint captured
in its immutable configuration snapshot until the Runtime is released, the
user switches away and later reactivates the Session, or the Host restarts.
After that boundary, a new activation reads the current configuration and the
disconnected Agent is unavailable.

No assembly lease or fresh Host admission check exists on participant Spawn,
attachment, or controller handoff. Those operations resolve from the active
Session Runtime snapshot. Configuration writers coordinate only for the short
atomic configuration compare-and-save; revision conflicts are reported to the
writer and do not block an already active Runtime.

## Agent Default Model

ACP model catalogs are optional. When a Session advertises no models, Caelis
creates one product-only `Agent default` profile so the connection remains
selectable. Its synthetic profile ID is never sent over ACP: runtime leaves the
Session model selection empty and preserves the Agent's own default.

Guided TUI onboarding selects the exact remote model but does not preselect a
reasoning effort. The resulting ModelProfile records the Agent-advertised effort
choices and current value as its capability and profile default. Fixed-handle
bindings and participant attachment still choose one explicit canonical effort;
the later selection, not onboarding, determines the effort sent for that work.

## External ACP Wire Compatibility

External-Agent wire compatibility stays in the Host-private ACP bridge and never
becomes a second product protocol. Compatibility is selected from the observed
message shape or advertised Session state, not from a guessed peer version or a
calendar deadline.

| Compatibility path | Owner | Enabled condition and standard precedence | Removal event |
| --- | --- | --- | --- |
| Flat Session configuration options | `internal/acpagentbridge/client` lifecycle-response ingress | The SDK standard config-option union is decoded and normalized first. The flat decoder runs only when one response option cannot be normalized as a standard select or boolean option; standard options always win. | Remove when the supported external-Agent interoperability set and supported upgrade fixtures contain no lifecycle response that emits the flat option shape. |
| Legacy `models` and `session/set_model` | Host-private `internal/acpagentbridge/client` wire adaptation and `internal/acpagentbridge/sessionconfig` selection | A standard model Session config option has priority even if `models` is also present. The legacy catalog and mutation method are used only when no standard model config option is advertised and the requested model appears in `models.availableModels`. | Remove both fields and the mutation method together when every supported Agent that exposes model selection uses the standard model config option and no supported upgrade fixture requires the legacy channel. |
| Prompt image `name` | `internal/acpagentbridge` prompt adaptation, with `surfaces/acp` recovering the lossless params supplied to its typed callback by the SDK | Standard ACP image decoding and validation owns `type`, `data`, `mimeType`, and `uri`. A non-empty top-level `name` is retained only as Host-private attachment display metadata; malformed or unrelated fields are ignored and never replace standard image content. | Remove when supported Caelis peers use the standard image `uri` for attachment naming or reference and no supported interoperability fixture requires the top-level `name` extension. |

## Endpoint Catalog and Installation Ownership

The guided `/connect` catalog contains a curated set of official CLIs with
native ACP stdio entry points, plus one generic Custom command entry:

| Catalog ID | PATH command and arguments |
| --- | --- |
| `grok` | `grok agent stdio` |
| `kimi` | `kimi acp` |
| `opencode` | `opencode acp` |
| `copilot` | `copilot --acp` |
| `qoder` | `qoder --acp`, falling back to `qodercli --acp` |
| `gemini` | `gemini --acp` |
| `qwen-code` | `qwen --acp` |
| `auggie` | `auggie --acp` |
| `cline` | `cline --acp` |
| `factory-droid` | `droid exec --output-format acp-daemon` |
| `goose` | `goose acp` |
| `kilo` | `kilo acp` |

A native preset is only unversioned command metadata; one of its executable
names must already be visible on the Caelis Host process `PATH`. The catalog is
not a mirror of every ACP Registry entry. Adding another preset requires a
stable official installed command, while a package version or Registry-only
distribution remains outside this contract.

The Custom command entry is the generic integration point for an ACP Registry
adapter or any other ACP stdio executable. Users install, select, update, and
remove those adapters themselves. Caelis validates that the command is visible
in the same execution environment during preparation and persists the logical
command and arguments, so later process starts use the current user-managed
executable. Caelis does not fetch the ACP Registry, run `npx`, invoke a package
manager, copy adapters into its store, pin adapter versions, or repair an
adapter installation. Codex and Claude therefore have no built-in adapter in
this contract; a user-installed adapter remains available through Custom
command.

Catalog or `PATH` presence is discovery evidence only. It does not authorize an
Agent to run as a subagent. Product use still requires an explicit
`/subagent bind` handle, and dispatch resolves that binding rather than treating
executable detection as availability authority.

Persisted connections created by older Caelis versions may still contain
`package_exec` or `managed` launchers. Runtime keeps those stored launchers
read-compatible so an upgrade does not silently break an existing connection,
but `/connect` cannot create either form and Caelis no longer updates or repairs
their installations. This compatibility path remains only until an explicit
configuration migration can convert every supported upgrade source to a
user-owned executable command; it must not be used as precedent for new
onboarding behavior.
