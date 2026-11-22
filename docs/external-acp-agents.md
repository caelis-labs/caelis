# External ACP Agents

This document owns the product contract for local external ACP Agent onboarding,
authentication, model selection, input, disconnect, and endpoint compatibility.
For collaboration setup and workspace controls, see [Participants](participants.md).
Layer ownership lives in [Architecture](architecture.md).
For Host settings clients and model-provider OAuth, see
[Model Connections](model-connections.md).

## Protocol versions

The default public ACP endpoint and unselected third-party processes use stable
ACP v1. Stable initialization negotiates v1 when a client requests version 1 or
newer; version 0 is invalid. A stable outbound client requires the negotiated
response to be exactly v1 before Session work. The AppServer user API and
Application integrations keep their existing stable types independently of the
ACP wire profile.

Repo-owned subprocess children use the explicitly selected
`schema-v2.0.0-beta.3` profile. The hosted Codex adapter keeps stable v1 by
default. Direct native main/child delegation stays
inside Control and Runtime and does not acquire an ACP transport. User-provided
process overrides remain stable unless their link explicitly selects a draft.
The Host-owned adapter descriptor, one-use channel grant and helper invocation
carry the same exact schema. An unsupported schema fails before granting or
serving the channel; the selected draft never falls back to v1.

`caelis acp --acp-draft=schema-v2.0.0-beta.3` serves product Sessions through the
existing principal-bound Control clients. Initialization negotiates v2 from a
client offering protocol version 2 or a newer major, using the v2 `info` shape.
The controlled product channel requires an exact beta.3
`_meta["caelis/acp_schema"]` tag. Responses advertise the implemented Session
capabilities and the conditional-prompt profile described below. Initialization
may repeat before any other request; concurrent attempts and
repeats after another request are rejected by the SDK connection. Unsupported
versions return `-32022` with the requested and supported versions. Unknown
methods and unimplemented optional
features fail explicitly. Only actual image/MCP/configuration support is
advertised; native ACP Sessions do not accept lifecycle MCP server declarations
or additional directories that their existing owner cannot apply.

The separate Core initialization/capability probe (`ServeDraftStdio`) also
negotiates newer offered majors down to v2. Its schema tag is optional in the
request; when explicitly provided it must match beta.3. The response always
identifies the pinned schema. This probe advertises no Session or auth work;
omitting its tag does not relax the controlled product channel's requirements.

Draft connections reject a JSON-RPC array as one invalid request before any
element is dispatched. Stable v1 retains its batch response and notification
ordering. Draft prompts, including steering, require at least one content block;
an empty text block remains valid. Invalid select controls with no remaining
values are omitted from recoverable display lists; an explicit empty control
list still clears the previous list.

Draft usage cost is a per-Session patch: omission retains the reported cost,
null clears it, and a value replaces it. The ingress adapter materializes full
snapshots for the shared live and durable context gauge; units such as credits
remain opaque. Background work snapshots and per-entity cancellation capabilities
do not grant Caelis Task or cancellation authority. Product and Codex endpoints
advertise no per-entity cancellation support; unsupported cancellation requests
fail explicitly. Foreground receipts still settle only through their matching
Activity state, independently of any reported background work.

Entity upsert `_meta` objects patch by top-level key: omitted keys remain,
null-valued keys are removed, and nested values replace rather than merge.
An empty object is a no-op; top-level null clears entity metadata. Message-chunk
metadata annotates only that contribution and does not patch the entity's
metadata. Numeric tokens in these opaque annotations are preserved through
aggregation, canonical events, and live/replay projection. A draft diff needs
at least one valid file change. An empty content replacement clears the prior
tool content.

The Codex draft channel requires native app-server 0.160.0 or newer with
`clientUserMessageId` correlation. The adapter assigns a private submission token
and acknowledges only the corresponding actual native user item. Older or
unverifiable native versions fail this selected channel without a stable
fallback. Codex authentication remains Host-owned; neither product draft endpoint
advertises remote authentication or elicitation without an implementing owner.
It serves standard v2 lifecycle and active-only insertion as an explicit
experimental channel, but advertises `idleOnly: false`: native multi-agent input
can start an owned Thread independently, and native `turn/start` lacks atomic
start-only-if-idle admission. The controlled Caelis client rejects this missing
capability during initialization. Safe default migration needs that native
admission contract; it does not disable native multi-agent behavior or silently
fall back after selecting a draft.

Draft `session/prompt` acknowledges insertion with a real `messageId`, echoed in
a live user-message update and kept unchanged when retained and replayed. This
acknowledgement is distinct from queue acceptance and foreground completion.
`state_update` reports running, requires-action and idle; only the matching idle
state settles the internal synchronous producer. `session/cancel` requests
cancellation and still waits for the actual state. Each Session has independent
foreground state, receipt and permission ownership. Reconnect uses standard
`session/resume`, with `replayFrom: {type: "start"}` when replay is needed, and
emits `replay_complete` before live updates resume. The bridge finishes replay
at that marker, with the ordered response as a fallback for peers that omit it.
Draft endpoints reject prompts carrying an idempotency key before insertion.
Transport loss before settlement remains `unknown_outcome`. Caelis does not
automatically resubmit the prompt.

Native permission requests keep the same original options and resolution owner.
Where the selected draft cannot express completeness, sensitivity or per-option
scope, the bounded public approval subject travels in `_meta["caelis/approval_subject"]`.
The receiver stores it as a reported display hint, joins only the original
option identities and marks remote claims in presentation. Guardian continues
using the canonical standard subject and private native evidence; the hint does
not grant authority or change resolution.

Full message/tool/terminal updates retain absent versus explicit null/empty
patch semantics. Message replacements reuse their message identity, terminal
chunks append only their decoded bytes, and terminal snapshots replace existing
output, including an empty snapshot. These are normalized into the same live,
canonical and recovery projections used by stable clients.

Exact retained narrative chunks remain deltas during `session/resume` catch-up.
Each replay epoch resets an identified narrative once before its first retained
delta; it does not replace the message with each chunk. Repeating a resume on
the same client therefore reconstructs the same text without duplicate appends.
Session message ownership survives Activity boundaries: repairing a message from
an earlier Activity updates its display without taking the new Activity's result.

The explicit Codex draft channel preserves native skill and mention references
by their supplied name and path without reading those paths. Native negative
command exit sentinels produce an exit status without an exit code.
If a route fails before native completion is confirmed, the selected draft cannot express
an Agent-side Session closure. The affected ACP connection closes observably,
pending native requests are cancelled and the owner attempts interruption of
the actual native Turn. Sessions on that connection become unknown outcomes;
other ACP connections and the shared native backend remain independent. Cleanup
does not manufacture a successful idle state.

The selected draft retains message identities for later repairs and settled
Activity identities to reject reuse. Its private display cache has a 32 MiB
serialized budget per Session and 128 MiB per connection, with at most 4,096
message, terminal and settled Activity records of each kind per Session and
1,024 Sessions per connection. Exhaustion is explicit and closes the channel;
it does not evict old identities and reinterpret a repair as new work. Accepted
work then stays `unknown_outcome`. Already projected terminal bytes remain with
the canonical terminal owner; the private cache buffers only output awaiting its
first tool association, then releases those bytes.

## Connect

The `/connect` overlay, including authentication method choices, supports
keyboard and mouse navigation. Hovering a
catalog row selects it without activating it; hovering a form field highlights
it without moving the editing cursor. Buttons have brackets and highlight on
hover. A click activates only when the left mouse button is released on the
same control. Read-only text supports drag-to-copy.

The `/connect` menu shows the ACP presets whose commands Caelis can find on
its Host's `PATH`, alongside the provider presets and a custom ACP command.
Opening the menu checks executable availability. Selecting an ACP entry starts
preparation and verifies the protocol. The guided flow:

1. prepares the selected local command and discovers its capabilities;
2. completes one declared authentication method when required;
3. selects one or more advertised remote models, or Agent default when the
   Agent advertises no models;
4. discovers each selected model's configuration in its own temporary Session
   and commits the selected ModelProfiles against one Host configuration revision.

A batch connect uses the existing ACP connect route with the unselected roster
preparation and the exact ready preparation for each selected model. It uses
each model's discovered defaults and returns every committed profile in selection
order. A Host that only supports single-model connect rejects the unselected
roster as a single-model request.

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

## Speed and service tiers

ModelProfiles expose a typed speed capability independently of reasoning effort.
Each choice maps a product value to the exact backend wire value; ACP profiles
also retain the advertised config ID. `/model` and `/team` show Fast only when
both standard and Fast are available. Provider capabilities retain the official
endpoint checks. A binding's empty `speed` preserves an explicitly configured
profile default in `SessionDefaults`, or inherits the backend default when none
is configured;
`standard` and `fast` are explicit selections. Binding sets retain that choice.
Host and Session model commands retain their explicit Fast boolean: false selects
standard on a capable profile. Guardian remains provider-only.

The Codex adapter reads `model/list.serviceTiers` and `defaultServiceTier` and
publishes `service_tier`. Every catalog-known model exposes the protocol baseline
`default`, including models without additional tiers. Additional choices use
catalog IDs (currently `priority` for Fast). Missing or empty catalogs do not
advertise Fast; there is no `additionalSpeedTiers` or model-name fallback.
Thread start/resume responses supply the effective tier. Missing/null tier stays
inherited internally, even when the selector displays its discovered default.
Control sends explicitly configured values even when they equal that display;
only an omitted selection inherits. When a combined model/tier selection requests
Standard, Control applies it before switching models if the current model advertises
it, then confirms the requested defaults against the destination configuration.
Changing a model validates an explicit tier against the destination capability.
Explicit standard (`default`) remains valid without additional tiers; an
incompatible Fast selection fails without silently downgrading it.

Set-config validates the current catalog and stages the next Turn selection.
The next `turn/start.serviceTier` applies it to that and subsequent Turns;
`serviceTierForTurn` is not used. Configuration cannot change during an active
Turn. A rejected start retains the complete staged model, effort and tier so a
retry sends the same selection; it does not combine a new model with an old tier.
An ambiguous start closes the route rather than claiming a successful change.
A newly opened Codex thread cannot be resumed until it has persisted its first
Turn, so set-config
does not fabricate a resume as an acknowledgement. Resume reads actual backend
state again. See the [Codex app-server protocol](https://developers.openai.com/codex/app-server).

Explicit speed is sealed in the existing Placement: provider `service_tier` or
ACP `session_config_values`. It contributes to the placement fingerprint.
Rebinding changes future work; active participants keep their frozen selection.
Session overrides do not mutate Codex's global configuration or another thread.

## Input and collaborator Sessions

An idle external collaborator receives `session/prompt` on its existing ACP
Session. On the controlled draft links, running input also uses standard
`session/prompt`. Initialization negotiates
`_meta["caelis/conditional_prompt"] = {version: 1, activeOnly: true, idleOnly: true}`. State
updates carry the owner-assigned Activity identity in
`_meta["caelis/activity_id"]`; running-only input sends
`_meta["caelis/active_only"] = {activityId: "observed identity"}`. The peer
atomically checks the actual active native Turn before insertion. Initial
foreground calls send `_meta["caelis/active_only"] = {idleOnly: true}` instead;
the actual owner admits them only while idle, so another caller starting work
cannot turn the initial prompt into mid-turn injection. Both conditions in one
guard are invalid. An unmet condition returns the profile's structured
`promptRequired` no-effect error. Callers do not silently retry it as a different
operation. Ordinary unguarded external v2 prompts keep standard insertion
behavior. The selected draft has no equivalent running-only or idle-only admission/CAS
field, so this minimal negotiated metadata remains. Pure
commands may use their durable interaction identity as an opaque Activity ID;
that ID never authorizes steering into a native Turn.

Each successful controlled-draft `PromptResponse` also carries
`_meta["caelis/activity_id"]`: the actual admitted native Turn, or the durable
interaction identity for a command that creates no Turn. Its standard
`messageId` identifies the canonical inserted input and the mandatory same-ID
live user echo. Callers bind completion to this receipt's Activity and its true
terminal state, including when state notifications arrive before the response.
Another caller's running/idle pair cannot complete the request. Missing receipt
identity is an unknown outcome; callers do not infer it from ambient Session
state or retry the input.

Stable v1 collaborators receive `_session/steering` only when they advertised
`_meta.steering.supported`. That maintained v1 extension remains available for
third-party peers; direct running input stays unsupported without it. StartThread
reports the effective link capability as `supports_steering`. Product mailbox,
input queue, receipt and Task lifecycles remain Control-owned: a v2 inserted
message receipt does not replace their separate queued/sent/applied or durable
Task states.

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
Session. Children expose `ReadMessages`, `ListThreads`, and `SendMessage`; the
controller also has `StartThread`, `ReadThread`, and `WaitThread`. Built-in children and child MCP servers
register only their tool set. `ListThreads` discovers all collaborators.

`SendMessage {to, message, reply_to?}` atomically commits a message to the shared
log and recipient's persistent mailbox, then independently takes up to 32 messages from the caller's
own mailbox. The result contains `{id, status:"queued"}` and optional `messages`;
it never echoes the outgoing body. Empty or failed inbox checks omit `messages`
and do not change the successful send acknowledgement. A failed send does not
take inbox messages. Incoming batches are bounded by encoded JSON size within
the shared 4 MiB response limit; remaining mail stays queued. Automatic delivery,
SendMessage replies, and controller waits share the atomic read-and-delete path:
a message is consumed by only one path. There are no automatic retries if a
response is lost or a dispatch outcome is unknown. Message text is limited to
65,536 bytes; `reply_to`, when present, is a canonical message UUID.

`ReadMessages {limit?, cursor?}` reads shared explicit collaboration messages.
The default page contains at most 32 messages; the limit can be 1–128 and the
encoded response is bounded by 4 MiB. Each entry includes sequence, message ID,
sender, directed recipient and optional reply ID. `@handle` and `handle` address
the same recipient. Public results, private reasoning, tool traces, original
controller conversation and direct user input are not copied into this log.
New members may read messages sent before they joined, within retention.

Control commits default read progress before returning. Concurrent reads from
one member serialize; a cancelled transaction does not advance progress. A lost
response may therefore lose a page from the unread view. Supplying an earlier
`cursor` explicitly replays retained bodies without changing saved progress.
Use the returned cursor to continue; `has_more` reports remaining entries at the
read snapshot. Messages appended later remain available on subsequent reads.
These are observation positions, separate from ReadThread cursors and delivery.
Reading never drains a mailbox or starts execution. A message read from the
group log can still arrive later through its pending directed delivery.

Successful sends, mailbox takes and automatic dispatch claims mark
individual IDs known to that reader. Default group reads skip those IDs while
scanning intervening unseen records in order; they never jump over unread gaps.
Admission and server-side response completion do not prove model consumption.
An ambiguous or failed dispatch removes its suppression marker without
rewinding an already advanced reader. Its body remains available through explicit
replay even though its directed queue entry was consumed. None of these paths
promises exactly-once delivery.

Reader identity includes the immutable Task/participant/attachment generation,
or the parent's controller epoch. Credential renewal and reconnect within that
instance retain progress. Handle reuse, participant replacement and controller
handoff start a new reader. Progress survives Host restart for unchanged
instances; a newly activated controller epoch starts afresh. Old credentials
cannot read as a replacement. Control retains the newest 4,096 messages per
work Session; `truncated_before` reports when an older cursor crosses discarded
history. Session closure discards the log, read positions and setup markers along
with pending mail. Existing mailbox rows are not backfilled into shared history.

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
with admission waits bound to Host service cancellation rather than a short
polling deadline. No mailbox read lock is held while waiting for a remote input
boundary. Runtime validates exact attachment generations and pins only the
involved bindings before releasing its topology lock. Removal or replacement of
those bindings reports a conflict while admission is pending; other participants
can attach or receive input. Child input preparation remains serial per endpoint
with cancellable queue waits.
Each attempt atomically claims the pending messages that fit the encoded batch budget and submits them as one
input admission, preserving their order and individual source identities. The
same rule applies to the parent: a running local controller accepts the batch
at its next safe model boundary, and a running ACP controller receives one
negotiated running-input request. Local safe boundaries follow a completed model
response or tool step; mail does not cancel an in-flight tool. Accepted local
batches are committed atomically before entering model context. A final empty
drain closes input admission so late mail can select the next Turn without
being acknowledged into a completed Run.
The 32-message pull limit does not split automatic delivery; messages beyond the
encoded budget remain queued. An unconfirmed additional message affects only
that delivery: it neither settles the existing Activity nor closes its output
connection. A definite terminal result for the original Activity closes its
steering admission and cancels any outstanding additional-message receipt/echo
wait before publishing completion. Only that unconfirmed message is uncertain;
no remote Activity cancellation is sent. The sender can wait or explicitly send another message, which may
duplicate a delivery whose outcome remains unknown. No uncertain send is
replayed automatically. Actual connection loss remains an execution observation
failure; a receipt alone cannot prove remote execution has stopped.
Agents without steering can take mail through MCP during their current turn;
otherwise the Host waits for a known terminal activity before submitting the
queued batch as one prompt on the existing Session. An unresolved execution
does not qualify as idle. Confirmed Session closure discards its pending mail,
including mail recovered after Host restart; transient discovery failures do
not authorize cleanup.

Native tools and the `caelis collaboration mcp --stdio` bridge call the same
AppServer service. The bridge uses Host-issued `CAELIS_COLLABORATION_URL` and
`CAELIS_COLLABORATION_TOKEN` environment values, never general Host credentials.
External controller and child creation/load/resume inject this stdio server through ACP
`mcpServers` when the Host has a child Control endpoint. The built-in Codex
adapter translates stdio MCP declarations into per-thread app-server overrides;
HTTP and SSE injection are not supported by that adapter. Bridge exit does not
cancel queued messages.

Controller MCP declarations include the activation's configured delegation names
and role descriptions from the same catalog as native StartThread. The Host
passes these bounded definitions in `CAELIS_COLLABORATION_TOOLS`, so tool search
works during ACP setup before the grant is bound. Definitions describe tools;
each invocation still requires the current authenticated controller grant.

The controller's first work-bearing prompt includes a lightweight maintainer
instruction with the stable `caelis-collaboration` search key. A durable marker
belongs to the work Session and exact remote Session, so ordinary turns,
credential renewal, same-remote resume and Host restart do not repeat it. A new
remote Session gets its own instruction. A proven non-submission releases the
marker; an ambiguous submission retains it. The prompt grants no authority.

Controller grants bind the current controller epoch and exact remote Session.
Before prompting a replacement remote Session, Runtime commits its binding under
the admitted Turn's fence. A fresh remote starts at checkpoint zero and receives
full context; only successful prompt completion acknowledges context delivery.
Restoring a controller can replace its epoch within an admitted Turn. Bound
Turn approval, steering and cancellation use the exact Handle/Run/Turn target;
new commands still validate the controller epoch observed before admission.
StartThread reuses Runtime's native Spawn, permission, journal, Task and sealed
placement owners under the live controller Turn's execution fence. Participants
cannot obtain this authority through tool arguments or an MCP role flag. Hosts
embedded without a child Control endpoint cannot inject collaboration MCP;
endpoint and transport failures are reported by Session setup or tool invocation.
The Codex adapter forwards explicit empty-form MCP tool approvals through ACP
permission requests. It does not answer arbitrary elicitation forms or URLs.

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
display body remain separate from this text. Accepted input is projected as ACP `session/update` with `user_message_chunk`
on v1 and the corresponding canonical `user_message` on the controlled draft; display-only
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
mail syntax across retained history (`session/load` on v1 and `session/resume` on v2). The child model receives the envelope; the
Task transcript decodes it once and displays the original text with a user
source. Images remain ordinary ACP content blocks. The envelope carries no
principal identifier and grants no authority. Replay joins fragmented envelope
text before decoding and never interprets its decoded body as mail or setup.
Unmarked retained history still uses the legacy display parser; an already
stored ambiguous message has no reliable evidence for retroactive attribution.

A stable v1 `session/prompt` remains open until its execution reaches a Turn
terminal. A draft prompt acknowledges insertion earlier; the internal producer
still observes its matching foreground settlement separately. If ACP forwarding fails, the bridge can no longer reliably service
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
Collaborating participants' gauges remain on their Tasks and contribute once to
Session totals.

## Disconnect

`/disconnect` opens one searchable list of connected models. Rows backed by an
external Agent carry an `ACP` label. Use Space to select models and Enter to
apply the selection. The Host commits the complete selection against one
configuration revision. If the connected Host lacks `delete-models-v1`, the TUI
asks you to upgrade the Host before using `/disconnect`.

Removing an ACP model preserves its Agent connection for later use. Control
revokes the selected profile from live Runtime catalogs, detaches participants
using that profile, and repairs affected main-controller bindings. A
post-commit repair warning reports a committed removal. The Host Agent API
also supports removing a whole Agent and all of its dependent profiles.

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
| `antigravity` | `agy acp` |
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

Installed commands are discovered on the Host process PATH. Caelis persists
commands and arguments. Use Custom for other ACP stdio commands.

Executable discovery does not make an Agent available as a collaborator. Open
`/team` to choose its participant profile and binding in the Team overlay.

### Antigravity

The preset uses the native [Antigravity CLI ACP server](https://antigravity.google/docs/cli/acp).
Select Google Antigravity in `/connect` to discover the models advertised by
the installed server. The model roster comes from the ACP server.
A model selectable through the standalone CLI may be absent from that roster.

The Host preserves saved standalone Antigravity profiles and Session history.
Reconnect Google Antigravity through `/connect` to replace the standalone
launcher. Remove obsolete profiles through `/disconnect`. The Host can retire
this launcher guard once stores with standalone Antigravity connections are
outside the supported upgrade window.

## Compatibility

Compatibility stays inside the Host-private ACP bridge and is selected from
observed message shape or advertised capabilities, never a guessed peer version.

| Path | Enabled condition | Removal event |
| --- | --- | --- |
| Flat Session configuration options | Standard options fail normalization; standard shapes always win | Supported peers and upgrade fixtures no longer emit the flat shape |
| Legacy `models` and `session/set_model` | No standard model option is advertised and the requested model exists in the legacy catalog | Every supported selectable peer uses standard model configuration and no fixture needs the legacy channel |
| Legacy tool names | Standard `name` is absent or null; projection reads retained tool-name metadata, and permission conversion falls back to metadata, title, then kind. The main-controller bridge uses raw output/input names, then kind, when only a title/kind fallback is available | Supported peers and retained histories all supply standard tool names |
| Prompt image `name` | Standard image content is valid and a non-empty top-level name supplies display metadata only | Supported peers use standard image URI/reference metadata |
| Codex MCP display identity | `_meta["codex/mcp_tool"]` names server `caelis-collaboration` and tool `SendMessage`, with no conflicting standard kind or existing exact display name | ACP supplies a standard structured MCP identity that replaces the provider hint |

Standard ACP v1 Session notices use `session/update` and are sent only when the
client advertises `clientCapabilities.session.notices`. The SDK validates the
wire shape. Unknown severity strings remain presentation hints, and malformed
optional fields are ignored. The bridge projects notices through the existing
transient Notice event, never reasoning, model input, public child results, or
approval authority.

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
