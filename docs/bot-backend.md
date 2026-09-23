# Bot backend contract

The authenticated Caelis Control API owns Bot work and desktop action authority.
This is the backend contract for a desktop adapter; consumer-side Go interfaces
in another repository are not an HTTP specification. The public source is
[`api/control/v1/openapi.json`](../api/control/v1/openapi.json). Generated Go wire
and TypeScript declarations live in
[`wirev1/generated`](../control/appserver/wirev1/generated/control_v1.gen.go) and
[`clients/typescript`](../clients/typescript/control-v1.gen.ts). The typed Go
client exposes `BotWorkClient` and `BotDesktopClient` through
[`httpclient`](../control/appserver/httpclient/appserver.go).

## Capability negotiation and configuration

Call `GET /api/control/v1/initialize` after authentication. Preserve `store_id`
(the persisted Control store), `instance_id` (this Host lifetime), protocol,
envelope, and API versions. Discovery and initialize expose the same Host
identity and assembled capabilities. Capabilities describe server support;
Bot configuration and client grants still decide authorization.

| Capability | Supported boundary |
| --- | --- |
| `bot-mode-v1` | One durable Bot conversation and Bot configuration |
| `bot-private-files-v1` | Model file tools in that Bot's private area |
| `bot-managed-work-v1` | Owned work, source and operation recovery, completion acknowledgement |
| `bot-desktop-actions-v1` | Revocable client lease, fixed action mailbox and one-time dispatch claims |
| `bot-reminder-grants-v1` | Persisted reminder authorization and finite scheduled occurrences |
| `bot-image-input-v1` | Existing bounded text/image `content_parts`; selected model must support images |
| `bot-text-results-v1` | Work result text bounded to 64 KiB and native Task output streams |

Arbitrary file upload, binary artifact download, existing-project adoption,
external controller workers, Windows worker read isolation, and arbitrary desktop
command execution are unsupported. Image input is not a file-transfer API.
The sandbox backend rejects a managed worker if it cannot enforce the read
ceiling. macOS uses Seatbelt; Linux uses Bubblewrap. No desktop windows,
notifications, characters, or reminder scheduler are implemented by Control. Work,
completion, and reminder-occurrence lists currently return the whole owned
catalog; pagination and archival are not provided. Dispatch anchors are retained
to prevent expired shared receipts from admitting duplicate effects.

Use existing `POST /bots/create` and `POST /sessions/{bot_session}/bots/update`
for the complete Bot configuration. Relative paths in this document are under
`/api/control/v1`. Writes use `Idempotency-Key`; revision-guarded configuration
uses the current decimal-string `expected_revision` and matching `If-Match`.
`managed_work` and `desktop_actions` default to false. `work_permission` accepts
only `workspace-write`; omission uses that policy. Model, effort, and Fast use
this Bot path, never generic Session configuration. Unsupported values fail.
A worker snapshots its Bot configuration at creation; changing the Bot does not
alter an existing worker's policy. Disabling managed work prevents new create,
continue, or steer actions; observation and exact cancellation remain available.

The file area is `bots/<Bot ID>/files/`. There is no notebook migration or legacy
enablement mode. Notebook contents do not grant capabilities. The current TUI
settings form exposes identity/model fields; desktop integrations set capability
flags through the Bot API.

## Owned work and request sources

| Method and route | Meaning |
| --- | --- |
| `GET /bots/{bot_id}/requests/{operation_id}` | Resolve a persisted authenticated Bot prompt source |
| `GET /bots/{bot_id}/work` | List Bot-owned work |
| `GET /bots/{bot_id}/work/{work_id}` | Read work, latest native execution and bounded result |
| `GET /bots/{bot_id}/work-operations/{operation_id}` | Read the permanent dispatch anchor, including unknown outcome |
| `POST /bots/{bot_id}/work/create` | Allocate one independent work directory and Session |
| `POST /bots/{bot_id}/work/continue` | Continue an idle work handle with an authorized source |
| `POST /bots/{bot_id}/work/steer` | Submit the actual user source to the exact active execution |
| `POST /bots/{bot_id}/work/cancel` | Interrupt the exact current execution |
| `GET /bots/{bot_id}/completions` | Read durable completion notifications |
| `POST /bots/{bot_id}/work/acknowledge` | Acknowledge one notification for presentation |

`WriteBase.session_id` addresses the main Bot conversation. `work_id` is the
long-lived work handle; the command result's `session_id` addresses its native
Session. The current implementation uses equal work and Session ID strings, but
clients keep the fields separate. Each `execution` contains `instance_id`,
`session_id`, `handle_id`, `run_id`, and `turn_id`. Run and handle IDs are scoped
to their Session/runtime and must not be used alone. Native Task/Job IDs identify
execution resources, not the long-lived work.

Create and continue require `source_id` and `assignment`. Only authenticated
Bot prompt admission or a claimed authorized reminder can create a source.
Settings, descriptions, files, assignments, and results cannot do so. A source
without confirmed execution admission cannot delegate. The original user text
and image parts enter work history as user input; the Bot assignment enters as
Agent communication. Steering forwards the original user source, not assignment
text promoted to a user message. Model-facing delegation tools do not expose
source IDs or operation IDs as model-selected arguments.

Control allocates `bots/<Bot ID>/work/<Work ID>/files/`. The request cannot name
a directory or adopt an existing Session. Project trust does not admit project
configuration or plugins here. File tools are rooted; commands use the native
sandbox with reads limited to their own directory and system runtime files,
writes limited to that directory, and network disabled. Native workspace policy and manual
approvals remain active inside this mandatory ceiling. A Host/full-access
approval cannot remove it. The command environment contains no inherited
credential values, Host Bearer, or desktop dispatch credentials.

Two work Sessions can execute independently while the main Bot accepts a new
message. Use each native Session's existing atomic reconnect/bootstrap and SSE
stream independently. A disconnected observer does not interrupt execution.
Read native Task directories and streams through the existing Session Task
routes. Do not create a second execution owner in the desktop adapter.

## Receipts, approval targets, and completion

Allocate and persist an operation ID before create or continue. Reuse the exact
request and ID for an acknowledged retry. A changed request digest conflicts;
client identity participates in the digest. Permanent work anchors outlive the
shared operation ledger's terminal retention. An existing unknown anchor is
queried and reconciled, never dispatched again. It also fences new work mutations
from that source, so changing assignment text or operation ID cannot bypass it. After a lost reply, query the
anchor and work/native Session before deciding what the user should do. Do not
invent a new ID to replay an unknown request.

Bot prompt sources also survive shared receipt expiry. A recorded prompt is never
readmitted solely because its transient receipt is gone; recover its bound execution.

An example Go adapter sequence, with error handling required at each call:

```go
// Persist these IDs and request bytes in the desktop request journal first.
prompt := appserver.PromptRequest{
    WriteBase: appserver.WriteBase{OperationID: "user-42", SessionID: botSessionID},
    Input: "Prepare the report and verify its output.",
}
accepted, err := client.Prompt(ctx, prompt)
source, err := client.GetBotRequest(ctx, botID, prompt.OperationID)
request := appserver.BotWorkRequest{
    WriteBase: appserver.WriteBase{OperationID: "work-42-a", SessionID: botSessionID},
    BotID: botID, SourceID: source.ID, Assignment: "Prepare and verify the report.",
}
started, err := client.CreateBotWork(ctx, request)
workID := started.Resource.Ref
work, err := client.GetBotWork(ctx, botID, workID)
observation, err := client.Reconnect(ctx, appserver.ReconnectRequest{
    SessionID: work.SessionID, Cursor: persistedCursor,
})
```

Apply the bootstrap atomically and persist each successfully applied native
cursor. A cursor is opaque and scoped to its stream; desktop action cursors are
a separate namespace. Close a subscription to detach it. On Host replacement,
compare `instance_id`, discard stale live targets, and bootstrap again. A changed
`store_id` is a different authority store.

For approval, use `SessionState.approval.active`: retain its native request ID,
`target`, `scope`, `parent_tool`, tool item identity, and all `permission.options`
unchanged. Submit the chosen native option through the existing
`POST /sessions/{work_session}/approvals/{request_id}/resolve`. Match the current
head and target after reconnect; do not translate options into a generic Boolean
or apply an old choice to a newer head. The server rejects stale targets.

Completion IDs are stable per work/run/turn. Completion persistence is separate
from native terminal execution. Control claims each report before admitting one
bounded secretary turn (up to four notices); it does not poll an idle model.
`report_state` is `pending`, `claimed`, `admitted`, or `suppressed`. Claimed with
no confirmed report execution is an unknown dispatch and is not replayed after
restart. Read state remains available for explicit recovery. Cancelled or
interrupted work suppresses automatic reports. Reading or acknowledging a notice
does not start a report or a worker. Acknowledgement suppresses a report that
has not yet been claimed.

Acknowledgement uses `BotWorkRequest.work_id` as the completion ID on the
`work/acknowledge` route, with its own stable operation ID. `acknowledged` records
presentation consumption, not proof that the user saw an OS notification. The
desktop owns notification delivery and deduplicates by completion ID.

## Desktop connection and reminders

An authenticated Host client enrolls a connection with
`POST /bots/{bot_id}/clients/register`, a stable operation ID, and `actions`
selected from `clock`, `reminders`, and `gesture`. The result returns one native
credential. Store it outside model context. The credential is not returned again;
an uncertain enrollment leaves an inactive record and requires deliberate new
enrollment, never guessing a replacement credential.

Use that credential as Bearer for the remaining Bot routes. It is limited to its
Bot and owned native Sessions; shared Host settings, creation of other Bots,
plugins, and shutdown are denied. Call `POST /bots/{bot_id}/client/activate` with
`{}` and renew with `POST /bots/{bot_id}/client/renew` before the ten-minute lease
expires. Activation returns the current `activation_id`. This HTTP credential
boundary is not accepted by the unrestricted embedded AppServer facade.

| Route | Native consumer behavior |
| --- | --- |
| `GET /bots/{bot_id}/client` | Query lifecycle state, including after exit |
| `GET /bots/{bot_id}/client/actions` | Snapshot pending actions and cursor |
| `GET /bots/{bot_id}/client/actions/events` | SSE `bot.desktop.snapshot`, including a cursor matching its SSE ID |
| `GET /bots/{bot_id}/client/actions/{action_id}` | Recover an owned action record, including an older activation |
| `POST /bots/{bot_id}/client/actions/{action_id}/claim` | Send `{}`; obtain one native dispatch token exactly once |
| `POST /bots/{bot_id}/client/actions/{action_id}/result` | Send that token and bounded JSON result; equal receipt is idempotent |
| `GET /bots/{bot_id}/client/reminders` | Read persisted grant versions for native scheduler recovery |
| `GET /bots/{bot_id}/client/reminder-occurrences` | Recover pending, claimed, admitted or suppressed occurrences by grant/version/due without firing again |
| `POST /bots/{bot_id}/client/reminders/fire` | Report exact `grant_id`, `version`, and RFC3339 `due`, with an operation ID |

A snapshot is observation only. Persist action identity and the claim before
native execution. Only a successful claim grants execution. A lost claim reply
cannot be claimed again; recover any native receipt without repeating the
action. A disconnect after execution requires receipt reconciliation. `result`
is untrusted tool output, at most 64 KiB JSON. Old activation receipts and
conflicting receipts are rejected. The model never sees a dispatch token.

Clock has no arguments. Gestures are `attention`, `nod`, or `celebrate`.
Reminders support `list`, `remove` by stable native ID, and `save` with ID, label,
prompt and exactly one schedule: RFC3339 `at`, `every_minutes` from 1 to 10080, or
HH:MM `daily` with IANA `time_zone`. Only a direct user request can save or remove
a reminder; scheduled prompts cannot create more schedule authority. Successful
native save/remove receipts update the grant atomically. Unknown native changes
keep the previous grant and prevent a second unresolved change of the same ID.
Exit or activation replacement atomically suppresses unclaimed actions and
releases their reminder IDs. Suppressed actions remain queryable and cannot be
claimed. Claimed actions retain their unknown outcome and continue to fence
conflicting reminder changes; a new activation does not authorize replay.

Desktop owns timers and persists native receipts. Control validates the exact
saved version and due time; it accepts no prompt in a fire request. The original
user request remains the authorization source. Accepted occurrences are durable,
coalesced on catch-up, and claimed once. An idle secretary receives a finite
wake; busy conversations defer it until completion. Explicit main-Turn cancellation
pauses pending wakes until the next accepted user message. Daily occurrences use whole
minutes; an interval's first eligible occurrence is `created_at + interval`.
The desktop should use returned Control grant timing for reconciliation.
`last_occurrence` records the consumed scheduled time; `coalesced_through` records
the receipt-time cutoff for missed occurrences. A daily catch-up before today's
scheduled time does not consume today's future occurrence. Interval scheduling
uses the cutoff plus the interval for the next eligible occurrence. Grant version,
active status, ownership, and client lease are checked atomically when an
occurrence is claimed. Revocation prevents later claims without retroactively
cancelling an already admitted claim.

## Exit and recovery

Connection loss leaves a lease and accepted work intact; it does not mean exit.
An expired lease denies new desktop dispatch until explicit reactivation.
`POST /bots/{bot_id}/client/exit` requires the exact `activation_id` and an
operation ID. It revokes the connection. With `cancel_owned_work: true`, it also
interrupts only work created for that client and reports stopped and unknown
work IDs. It does not close the shared Host or another client's work. Repeating
the same exit request recovers its receipt; lifecycle reads remain allowed after
exit. Reactivation after exit creates a new activation ID and excludes schedule
occurrences from the stopped period. Pending occurrences from before that activation
are suppressed. A stale exit cannot revoke that activation.

Host restart changes the instance ID and invalidates live connections. Explicit
activation is required again. Native terminal journals rebuild work outcomes and
completion records; unknown work, report, reminder, and native action dispatches
are never automatically replayed. Stored grants permit one eligible catch-up
occurrence after an ordinary disconnect or restart. The desktop must reconcile
its native scheduler and action journal before resuming observation.

## Validation

```bash
CAELIS_TEST_BOT_NATIVE=1 go test ./app/gatewayapp -run \
'^TestBot(ManagedWorkParallelAndSourceIsolation|WorkNativeCredentialAndPathCeiling|NativeHostHTTPWorkApprovalReportAndRecovery|NativeModelDelegationUsesBoundRequest)$' \
-count=1 -v
```

This runs controlled-provider tests with a real temporary Host, HTTP and
native sandbox. It requires permission to initialize Seatbelt or Bubblewrap.
Ordinary Bot tests cover file behavior, durable authority, desktop HTTP/SSE,
revocation and reminder provenance. These are controlled model fixtures, not
real-model or desktop-adapter acceptance. See [Testing](testing.md).
