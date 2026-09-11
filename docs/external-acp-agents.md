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
advertised `_meta.steering.supported`. This is a negotiated custom extension,
not a standard ACP v1 method. Direct running input remains unsupported without
it. StartThread reports this capability as `supports_steering`.

Control appends a tagged collaboration setup block to the child's initial
`session/prompt`, after the task body and sender. It names the assigned handle,
the reserved parent address `parent`, and the participant role. It supplies the
stable MCP discovery key `caelis-collaboration` and asks children to report
meaningful progress through `SendMessage`, process returned mail, and end the
current Turn when finished or blocked. New messages resume the same Session. The setup is
not appended to later idle prompts, steering, or reconnects to the same Session.
Its text is English; task and message bodies retain the sender's language.
Credentials, mailbox contents, and Session or Task identifiers stay outside it.
Built-in spawned Sessions receive their identity through system-prompt assembly
and do not inherit controller-only tool guidance. Transferred parent context
follows the initial task body.

Product Agents share one Control-owned mailbox service within their owning work
Session. Children expose only `ListThreads` and `SendMessage`; the controller also
has `ReadThread` and `WaitThread`. Built-in children and child MCP servers
register only their tool set. `ListThreads` discovers all collaborators.

`SendMessage {to, message, reply_to?}` first commits a message to the recipient's
persistent mailbox, then independently takes up to 32 messages from the caller's
own mailbox. The result contains `{id, status:"queued"}` and optional `messages`;
it never echoes the outgoing body. Empty or failed inbox checks omit `messages`
and do not change the successful send acknowledgement. A failed send does not
take inbox messages. Incoming batches are bounded by encoded JSON size within
the shared 4 MiB response limit; remaining mail stays queued. Automatic delivery,
SendMessage replies, and controller waits share the atomic read-and-delete path:
a message is consumed by only one path. There are no automatic retries if a
response is lost or a dispatch outcome is unknown. Message text is limited to
65,536 bytes; `reply_to`, when present, is a canonical message UUID.

`ReadThread` returns a participant's latest public result and observation cursor,
not its reasoning or complete conversation. Supplying `after` suppresses already
observed output. `WaitThread` waits up to 60 seconds for incoming mail or new
terminal/attention states among at most eight selected threads. It returns when
any target needs attention or mail arrives, rather than waiting for all targets.
The result contains the wake reason, consumed messages and thread observations;
timeout does not cancel work. Neither tool exposes the parent transcript or
cross-Session routing. Thread observations use one `cursor` with `handle`,
`state`, and optional name or output, without internal Task IDs or a duplicate
revision. Received mail retains `id`, `from`, `message`, and optional `reply_to`;
its implicit recipient is omitted. Wait results omit empty message and thread
lists. TUI keeps display-only decoding of retained `ReceiveMessages` results for
historical transcripts; that compatibility can be removed when those records
are no longer supported. The tool itself is not exposed or callable.

Only the controller receives `StartThread`. It creates a persistent participant
conversation and starts its initial prompt, returning its handle, status and steering capability without
folding its result into the creation result. Follow-up messages reuse that
conversation. Participant removal is an internal Control operation; it is not
exposed as a model tool and preserves Session history. Running or unresolved
participants must settle before removal.

Taking a message or claiming it for automatic delivery removes it from the
mailbox. A failed dispatch or lost tool response can lose the message; there is
no acknowledgement, retry, or redelivery protocol. Repeating SendMessage creates
another message. Pull and automatic delivery share the same atomic removal.
Automatic delivery is serial per recipient and independent across recipients,
with a ten-second deadline per attempt. Each attempt atomically claims the
pending messages that fit the encoded batch budget and submits them as one
input admission, preserving their order and individual source identities. The
same rule applies to the parent: a running local controller accepts the batch
at its next safe model boundary, and a running ACP controller receives one
negotiated steering request. Local safe boundaries follow a completed model
response or tool step; mail does not cancel an in-flight tool. Accepted local
batches are committed atomically before entering model context. A final empty
drain closes input admission so late mail can select the next Turn without
being acknowledged into a completed Run.
The 32-message pull limit does not split automatic delivery; messages beyond the
encoded budget remain queued. A deadline does not establish whether the peer
executed the input and does not trigger a retry.
Agents without steering can take mail through MCP during their current turn;
otherwise the Host waits for a known terminal activity before submitting the
queued batch as one prompt on the existing Session. An unresolved execution
does not qualify as idle. Confirmed Session closure discards its pending mail,
including mail recovered after Host restart; transient discovery failures do
not authorize cleanup.

Native tools and the `caelis collaboration mcp --stdio` bridge call the same
AppServer service. The bridge uses Host-issued `CAELIS_COLLABORATION_URL` and
`CAELIS_COLLABORATION_TOKEN` environment values, never general Host credentials.
External child creation and resume inject this stdio server through ACP
`mcpServers` when the Host has a child Control endpoint. The built-in Codex
adapter translates stdio MCP declarations into per-thread app-server overrides;
HTTP and SSE injection are not supported by that adapter. Bridge exit does not
cancel queued messages.

The Host keeps random bearer grants in memory. Each grant binds one work
Session, immutable participant instance, and exact ACP Session. A pending grant
must bind after a successful handshake within two minutes. An active grant has
a fixed 24-hour deadline; tool calls and repeated binding cannot extend it.
Before another idle turn, a connection with less than one hour remaining is
replaced and resumes the same ACP Session with a new grant. Expired credentials
are rejected even during a long turn. A peer that cannot resume reports a
reconnection error rather than silently starting another Session.

Every call checks the current participant instance and work-Session lifecycle;
mailbox waits recheck authorization as they wait. The first call may wait up to
two seconds for the initial participant commit. Connection close or failure,
participant removal, replacement activation and Host shutdown invalidate the
affected grant. A closed work Session cannot use existing grants. Host restart
requires new grants. A bearer copied elsewhere still represents its original
identity; it never grants access to a caller-selected Session.

Credential values remain outside model prompts, tool schemas, results and
canonical history. Tool schemas are stable across credential changes. Pending
mail survives connection replacement, but consumed mail is never retried.

Delivered input follows the ordinary Agent-communication context path. The
model receives the task body followed by mail reference lines (`Message-ID` and
optional `In-Reply-To`) and a single `From` footer, without an embedded mailbox
JSON object or repeated recipient identity. Typed source identity and the original
display body remain separate from this text. Accepted input is projected as ACP `session/update` with `user_message_chunk`; display-only
source metadata may use `_meta.caelis.agent_communication`, while typed event
identity remains authoritative. Caelis mailbox IDs are separate from peer-owned
ACP message IDs. Delivered context remains in canonical Session history after
its mailbox entry is removed. The external history reader removes collaboration
setup and mail footers from child display and attributes preceding content blocks
to the footer's sender. Its legacy header reader is display-only; remove that
reader once supported external histories no longer contain header-format prompts.

Direct user input quotes each text block as a JSON string inside a
`<caelis_user_input version="1">` prompt envelope. ACP peers need not retain
custom content metadata, so this text encoding distinguishes user content from
mail syntax across `session/load`. The child model receives the envelope; the
Task transcript decodes it once and displays the original text with a user
source. Images remain ordinary ACP content blocks. The envelope carries no
principal identifier and grants no authority. Replay joins fragmented envelope
text before decoding and never interprets its decoded body as mail or setup.
Unmarked retained history still uses the legacy display parser; an already
stored ambiguous message has no reliable evidence for retroactive attribution.

An admitted `session/prompt` remains open until its execution reaches a Turn
terminal. If ACP forwarding fails, the bridge can no longer reliably service
permissions: it requests Control cancellation and waits for the actual terminal,
rather than silently draining a Run that may be waiting for approval. Cancellation
requests do not prove cancellation completion. If observation is lost, or
cancellation cannot be settled within 30 seconds, the prompt reports an error,
not a successful `end_turn` or `cancelled` response. A completed Turn wins a race
with observation cancellation and returns `end_turn`; unrelated forwarding
failures remain errors, even when their cleanup successfully cancels the Turn.

A standard RPC internal error, request-cancellation error, or unrecognized peer
error does not prove that remote execution stopped. The child Task records
`unknown_outcome`, retaining the response phase and numeric RPC code when
available without exposing peer text or data. Such an activity rejects
follow-up input, including after Runtime reload: `session/resume` restores access
to a Session, not proof that its old execution is idle. Automatic execution
reconciliation is not available through standard ACP resume; unresolved Tasks
remain isolated rather than retrying a prompt blindly. Proven admission
rejections and normal completed Turns retain ordinary follow-up behavior.

The Host records ACP prompt-response, settlement and cleanup failures in
`<Store>/logs/runtime.jsonl`, with Task, activity, parent-call and Session
identities, RPC code, submission classification when available, and a bounded
original error chain. This private sink is independent from model context and
Task output. It uses owner-only file access and the existing 2 MiB rotation with
one `.1` backup. Error details may contain sensitive peer data or paths; review
logs before sharing. Prompts, launch environments and child stderr are not
attached to these records.

ReadThread and WaitThread observe subsequent public results. Thread identity
and ACP Session placement survive Runtime or Host restart; later input resumes
that exact Session rather than substituting `session/new`.

Task addresses individual asynchronous Jobs. Its model-facing operations reject
participant handles. Job input and cancellation depend on the producer's
capabilities; RunCommand is the current built-in Job producer. The standalone
SDK direct-input SendMessage tool is available only when an embedder explicitly
assembles it. Product assembly always uses the Control mailbox tools; Runtime
never injects a legacy SendMessage tool implicitly.

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

`/disconnect acp` supports selecting multiple Agents. Each Agent, its ACP
profiles, and bindings are removed in one revision-aware Host command. Enter
submits the selected targets without a second confirmation. Commands run in
selection order and stop at the first error; confirmed removals remain applied.

Control immediately revokes the removed placement from live Runtime catalogs,
detaches matching participants, and repairs affected main-controller bindings.
An accepted in-flight operation may finish with already resolved values, but later
work cannot select or display the deleted profile. A post-commit repair warning
never restores disconnected configuration.

`/disconnect provider` lists configured provider models grouped by Provider and
supports the same multi-selection flow. It never removes an ACP Agent connection.

## Models

ACP model catalogs are optional. If an Agent advertises none, Caelis creates one
product-only `Agent default` profile and sends no synthetic model ID to the
Agent.

Guided onboarding selects the remote model but does not impose a reasoning
effort. The Agent-advertised choices become profile capabilities; fixed Agent
bindings and participant attachment choose an explicit effort later.

### Provider selectors exposed by Caelis

Caelis publishes configured provider models as `provider[@endpoint]/model` in
AppServer presentation, ACP model configuration options, and `/model` completion.
Only the literal `default` endpoint is omitted: `deepseek/deepseek-flash`,
`xiaomi@api-cn/mimo-v2.5-pro`, and
`xiaomi@token-plan-cn/mimo-v2.5-pro` name distinct routes. Adding another endpoint
never changes a route's selector. Custom model aliases retain their fully
qualified configuration ID so multiple configurations of one upstream model
remain distinct.

Control's `modelconfig` package owns selector generation and resolution. Model
options and their current value use the same public selector; selection resolves
to the existing internal configuration and ModelProfile identities before any
Session write. Credentials, durable bindings, and upstream model IDs are not
renamed. Remote model IDs owned by external ACP Agents are unaffected.

Selection first matches an exact internal ID, then a public selector, and only
then an unambiguous historical alias. A historical alias cannot shadow a public
selector: `deepseek/model` names the `default` route when that route exists,
while `deepseek@office/model` names the `office` route even if both configurations
share an alias. Without a matching public selector, ambiguous aliases such as
`xiaomi/model` are rejected rather than resolved through the Host default.

Compatibility aliases are input-only, not additional options. Historical alias
resolution is not a stable route binding when the catalog changes; clients that
need one must use an advertised selector or an exact internal ID. Alias support
remains while historical client references are supported and can be removed
only through an explicit breaking migration. A public selector colliding with
another configuration's public selector or exact internal ID is still a catalog
error; shared historical aliases alone do not invalidate the catalog.

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
| Draft Session notices | The bridge accepts `session/update` with `sessionUpdate: "notice"`, required `severity` and non-empty `title`, and optional `description` and `_meta` | Replace the bridge decoder with the SDK Notice variant when available |
| Codex Notice transport | The client advertises `_meta.session_notice: true`; the adapter sends `_session/notice` with the same `{sessionId, update}` payload as the draft standard notification | The pinned ACP SDK can encode the Notice variant; switch the adapter to `session/update` and remove the capability and extension method |
| Codex MCP display identity | `_meta["codex/mcp_tool"]` names server `caelis-collaboration` and tool `SendMessage`, with no conflicting standard kind or existing exact display name | ACP supplies a standard structured MCP identity that replaces the provider hint |

The [Session notices draft](https://github.com/agentclientprotocol/agent-client-protocol/pull/2004)
defines advisory live events outside Session history. The standard update needs
no capability negotiation; only the temporary Codex transport does. Unsupported
clients may ignore notices. Unknown severity strings remain presentation hints,
and malformed optional fields are ignored. The bridge projects notices through
the existing transient Notice event, never reasoning, model input, public child
results, or approval authority.

Codex MCP compatibility belongs to the built-in adapter: `arguments` becomes
standard `rawInput`, and supported `result.content` blocks become standard ACP
tool content. Complete provider results, including `structuredContent` and
unknown content blocks, remain in `rawOutput`. The Host-private bridge consumes
the structured MCP display hint; it never derives a tool name from a title.
Generic tools render standard input even without a recognized display profile.

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
