# External ACP Agents

This document owns the product contract for local external ACP Agent onboarding,
authentication, model selection, input, disconnect, and endpoint compatibility.
Layer ownership lives in [Architecture](architecture.md).

## Connect

Run `/connect` and choose an ACP agent. The guided flow:

1. prepares the selected local command and discovers its capabilities;
2. completes one declared authentication method when required;
3. selects the Agent's default or an advertised remote model;
4. commits the Agent and resulting ModelProfile against the current Host
   configuration revision.

Preparation, authentication, and connect are Host-scoped, principal-bound,
idempotent commands. Durable intent is recorded before process, authentication,
Session, or configuration effects. A committed configuration write is not rolled
back because later refresh or durability observation reports a warning.

Preparation records are secret-free, time-bounded recovery evidence, not a
second operation ledger. Ambiguous process or protocol cleanup remains
`unknown_outcome` and is never retried blindly.

Terminal authentication is a caller capability rather than wire data. An embedded
interactive client may run a declared terminal login. HTTP clients cannot carry
that executable callback and fail before starting the login; Agent-managed ACP
authentication works through either transport.

## Authentication recovery

Caelis detects ACP `auth_required` by its structured JSON-RPC code, not message
text. Authentication methods come from `initialize.authMethods`.

For an authenticated operation, the bridge:

1. calls the operation;
2. on `auth_required`, selects one declared method;
3. performs agent-managed authentication in-band or directs terminal login back
   through interactive `/connect`;
4. retries the original operation once.

A second `auth_required` is returned without another side effect. Session open,
resume, prompt, and negotiated steering use this path. If authentication succeeds
but resume proves the remote Session unavailable, the controller may create a new
Session only while it can still prove that the current prompt was not submitted.

See the upstream
[ACP authentication methods RFD](https://agentclientprotocol.com/rfds/auth-methods)
for standard wire behavior.

## Input and collaborator Sessions

An idle external collaborator receives `session/prompt` on its existing ACP
Session. A running collaborator receives `_session/steering` only when the Agent
advertised that capability; otherwise active input is rejected until the
current Turn finishes. Spawn reports this fixed capability once as
`supports_steering`; Task read/wait do not repeat it.

`SendMessage {to, message}` is the model-facing address adapter over those
standard methods. Caelis binds trusted source identity and dispatches one
Agent-communication input. Accepted input is projected as standard ACP
`session/update` with `user_message_chunk`; display-only source metadata may use
`_meta.caelis.agent_communication`, while typed event identity remains
authoritative. It has no delivery MessageID, durable mailbox, target completion
claim, or Task mutation. Ambiguous post-dispatch outcomes are not blindly
retried.

Task observes subsequent collaborator output on demand. A terminal response
retrieved through Task read/wait should not also be sent as a message. Durable
participant placement preserves the collaborator handle, ACP Session ID, and
Task identity across Runtime or Host restart; later input resumes that exact
Session rather than substituting `session/new`.

A nested Spawn performed inside a third-party participant stays behind that
participant boundary. Caelis may render its final standard tool result, but it
does not create another parent Task workspace or flatten the nested transcript.

## Main Session controller

The product ModelProfile catalog is the single `/model` surface for provider and
ACP backends. Selecting an ACP profile transfers the selected Session controller
from the SDK Kernel to that Agent. Caelis continues to own durable Session state,
feed/replay, permissions, and handoff.

The durable controller binding freezes the Agent, remote model, configuration,
effort, remote Session ID, controller epoch, and context-sync position. Runtime
reattachment uses that binding rather than resolving the current profile again.
When a remote Session is gone, the bridge may create a replacement and transfer
canonical context only before the new prompt has been submitted.

An ACP-backed Host default is stored as a dormant binding for new Sessions and
starts no Agent process until work activates it. ACP main Turns do not require a
local provider. Local-only capabilities such as Runtime compaction are omitted or
rejected while the external Agent controls the Session.

The latest standard ACP `usage_update` is retained as the main context gauge.
Subagent gauges remain on their Tasks and contribute once to Session totals.

## Disconnect

`/disconnect acp` removes the Agent, its ACP profiles, and bindings in one
revision-aware Host command. The selected Agent is the explicit removal target;
no second confirmation is required.

Control immediately revokes the removed placement from live Runtime catalogs,
detaches matching participants, and repairs affected main-controller bindings.
An accepted in-flight operation may finish with already resolved values, but later
work cannot select or display the deleted profile. A post-commit repair warning
never restores disconnected configuration.

`/disconnect provider` is separate and never removes an ACP Agent connection.

## Models

ACP model catalogs are optional. If an Agent advertises none, Caelis creates one
product-only `Agent default` profile and sends no synthetic model ID to the
Agent.

Guided onboarding selects the remote model but does not impose a reasoning
effort. The Agent-advertised choices become profile capabilities; fixed Agent
bindings and participant attachment choose an explicit effort later.

## Endpoint catalog

The built-in catalog contains stable commands for official ACP stdio modes plus a
Custom command:

| Catalog ID | Command |
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

The executable must already be visible on the Host process PATH. Caelis persists
the logical command and arguments but does not install, update, version-pin, or
repair third-party adapters. Use Custom for any other ACP stdio command.

Executable discovery does not authorize subagent use. A profile must still be
bound explicitly through `/subagent bind`.

## Compatibility

Compatibility stays inside the Host-private ACP bridge and is selected from
observed message shape or advertised capabilities, never a guessed peer version.

| Path | Enabled condition | Removal event |
| --- | --- | --- |
| Flat Session configuration options | Standard options fail normalization; standard shapes always win | Supported peers and upgrade fixtures no longer emit the flat shape |
| Legacy `models` and `session/set_model` | No standard model option is advertised and the requested model exists in the legacy catalog | Every supported selectable peer uses standard model configuration and no fixture needs the legacy channel |
| Prompt image `name` | Standard image content is valid and a non-empty top-level name supplies display metadata only | Supported peers use standard image URI/reference metadata |

Older persisted connections may still use `package_exec` or `managed`
launchers. Runtime keeps them read-compatible, but new onboarding cannot create
or repair them. A Codex connection that points into the retired Store-owned
`acp-agents` cache is migrated to the built-in `hosted_adapter` only when the
Host confirms that `codex` is available on `PATH`; its stable connection, Agent,
profile, and binding identities are preserved while stale discovery is dropped.
The cache is reclaimed only after neither external connections nor live ACP
preparations still reference it.
Remove the general legacy launcher reader only after every supported upgrade
source can be migrated to a user-owned executable or a built-in hosted adapter.
