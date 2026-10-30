# Session profiles and client extensions

Bot, TUI, headless and ACP are Surfaces of the same Control Host. They use the
same authenticated principal, native Session owner, permission policy and
operation namespace. A Session can use ordinary Host configuration or an explicit
execution profile. That choice controls assembly; it does not create another
client identity or permission system.

The maintained HTTP contract is
[`api/control/v1/openapi.json`](../api/control/v1/openapi.json), with generated
[Go](../control/appserver/wirev1/generated/control_v1.gen.go) and
[TypeScript](../clients/typescript/control-v1.gen.ts) types and a supported
[HTTP client](../control/appserver/httpclient/session_configuration.go).
Surfaces must not import Host-private packages or read the Store directly.

## Protocol and authentication

`GET /api/control/v1/initialize` reports protocol, Envelope and API versions,
capabilities, persistent `store_id`, transient `instance_id`, and build identity.
Protocol acceptance follows consumed semantics and capabilities. Build identity
selects the executable lifecycle; it is not a protocol version allowlist.

Use the Host bearer token or the same trusted in-process principal through
`BindAppServerClients`. There is no application enrollment, connection credential,
Worker grant or client lease. Request bodies and model-generated text cannot
choose a principal. Native Session ownership remains enforced, including for
Task, terminal, participant and approval routes.

Explicit profiles require `session-profiles-v1`. Optional functions advertise
`session-hot-configuration-v1`, `session-native-execution-v1`,
`session-workspace-binding-v1`, `session-background-activation-v1`,
`session-resource-transfer-v1`, `session-model-capabilities-v1`,
`session-tool-result-content-v1`, `session-media-resources-v1`,
`session-guardian-review-v1`, `runtime-agent-team-v1`, and
`session-atomic-capabilities-v1`.
`session-profile-control-v1` and `session-profile-children-v1` identify the
implemented profile-aware observation, native control and child paths.
Require the capability consumed by a feature before using it.

Paths below are relative to `/api/control/v1`:

| Operation | Route |
| --- | --- |
| Create ordinary or explicit-profile Session | `POST /sessions` |
| Prompt, steer, cancel, close, approve | Existing `/sessions/{session_id}` commands |
| Immutable creation evidence | `GET /sessions/{session_id}/profile` |
| Desired configuration | `GET/POST /sessions/{session_id}/configuration` |
| Model, reviewer and extension health | `GET /sessions/{session_id}/model-capabilities`, `/reviewer-state`, `/mcp-status` |
| Original command result | `GET /operations/{operation_id}` |
| Background authorization | `GET/POST /sessions/{session_id}/background-grants`; `GET .../{grant_id}`; `POST .../{grant_id}/revoke` |
| Callback observation | `GET /sessions/{session_id}/tool-calls`, optionally `?wait=true`; `GET .../{call_id}` |
| Callback claim and completion | `POST /sessions/{session_id}/tool-calls/{call_id}/claim` and `/result` |
| Immutable resource upload | `POST /sessions/{session_id}/resources` |
| Resource descriptor and bytes | `GET /sessions/{session_id}/resources/{resource_id}` and `/content` |

There are no `/application/*` routes. Old application credentials are not accepted.
Callbacks have their own native call/result identity, not a second Session command
namespace. An empty claim body is `{}`; background revocation is a command with a
stable operation ID.

## Operation ownership and recovery

Persist exact request bytes and a globally unique operation ID before dispatch.
Writes use matching `operation_id` and `Idempotency-Key`; native revision fences
also use `If-Match` where specified. IDs are shared across actions for one
principal. A different payload, target or action under a retained ID conflicts.

The common Control ledger records intent before dispatch and the original result
afterward. Configuration, resource and background-grant mutations commit their
domain change and command receipt in one SQLite transaction. Their typed results
are `CommandResult.configuration`, `.session_resource` and `.background_grant`.
A no-op also returns a retained receipt. Creation, native execution and closure
retain their original Session/Turn identities and conservative unknown outcomes
when durable completion cannot be proven.

`GET /operations/{id}` observes the original result without dispatch or retention
refresh. It can reconcile incomplete native commands only from their original,
exact durable evidence. Current Session state is not a substitute for an original
receipt. Missing or expired evidence does not prove no effect occurred and never
authorizes replay or a fresh ID. Known rejection is a retained result too; reading
it does not report a committed configuration or refresh its retention.

New commands use the common ledger retention policy. Incomplete and unknown work
is retained conservatively. Imported historical permanent receipts remain
permanent, including after later proof resolves an unknown. Never turn admission
(`accepted` or `committed`) into a claim that the Turn or external effect finished.

## Runtime Agent Team and legacy reviewers

`runtime-agent-team-v1` makes Runtime AppConfig the single model authority for
breeze, orbit, zenith, tool-search, guardian and guardian-screen, including their
effort and speed. TUI Team and Control agent binding commands edit that same
configuration. Main conversation/Worker model selection remains Session-local;
`self` uses that main selection. An unbound Guardian or tool-search uses the
existing Session main-model fallback; resetting a delegation role removes it.

New auto-review profiles omit `reviewer`. Existing valid `reviewer` values,
including Bot-generated MiMo overrides and explicitly supplied historical
Application overrides, no longer select execution models. They remain byte-for-
byte creation evidence for request digests and receipt recovery. This is a read-
time semantic migration: no database edits, new Session, Task recreation, Dream,
or background maintenance Turn is required. Manual approval mode and all sandbox,
permission, source and ownership boundaries remain unchanged.

`reviewer-state` reports the current Runtime Guardian (or current Session main
fallback), not the historical profile field. `ready` checks local assembly only.
Clients must negotiate `runtime-agent-team-v1` and must not compare the reported
model to a legacy creation reviewer. Older clients that enforce that equality
need a client upgrade; Core does not preserve a second fixed-reviewer mode.
No history/receipt schema is removed, so legacy creation evidence has no scheduled
removal while those identities remain recoverable.

Team changes use the existing configuration revision and cache invalidation.
An already selected model request keeps its model/effort/speed; a review also
freezes the optional classifier and deferred Guardian before screening. Completed
approvals are never revisited and the underlying action is not replayed. Empty
Guardian assessment recovery is a separate concern, unchanged by this contract.

## Configuration and native execution

An ordinary Session samples ordinary Host configuration at Runtime activation;
Agent Team roles are resolved from the current Runtime configuration on each call.
An explicit profile pins its creation identity and execution boundaries, and
samples its desired configuration at each model request. These are two assembly
choices in one Session owner. Updating an ignored native model field on an
explicit-profile Session is rejected; use its configuration patch.

Creation requires `version`, `model`, `tools_version` and `execution`. It may
supply instructions, callback definitions, selected MCP servers and Skill roots,
workspace access, permissions and process configuration. Explicit
profiles do not inherit workspace instructions, ambient business tools. A Bot can provide its private memory through an explicit MCP,
Skill or callback integration. Control still owns history, tools, approvals,
sandbox policy and recovery.

`execution: tools-only` exposes selected callbacks and extensions without native
file/command tools. `workspace-write` uses the ordinary native sandbox and
approval owner. `permissions.mode: danger-full-access` selects the ordinary Host
execution route; it does not auto-approve callbacks, MCP, OS privacy access or
external effects. Workspace CWD/access, execution, process environment, permissions
are creation-bound. Canonical directories are checked at activation
and before effects; redirection cannot expand a bound directory.

Omitted native tool selection uses the default native catalog. Explicit `[]`
selects none. Names and callback collisions are validated. Native command/file
execution uses the SDK Runtime lifecycle; closing that Runtime rejects delayed
launches, filesystem effects and interactive input. No independent client lease
can revive it. See [Execution Configuration](execution-configuration.md) for
shell/environment behavior.

`POST .../configuration` requires `expected_configuration_revision` and `patch`.
Instructions, model, effort, tier, callback catalog/version, native tools, MCP
servers and Skill selections can change. Omitted fields preserve values, explicit
empty values clear supported selections, and null is rejected. Workspace,
execution and permissions cannot be changed through a patch. The legacy reviewer
field is retained as immutable creation evidence and cannot be patched.

Configuration revision is separate from native history revision. The immutable
creation profile/revision remains evidence; `configuration.profile` is the sole
current desired configuration. `last_request` records the revision and native
request/Turn identity actually admitted. Rejected updates alter neither desired
configuration nor an already issued request. A successful update affects the next
model request, including a continuation in the same Turn. It does not replace
in-flight callback names, arguments, catalog version or permission decisions.

MCP and Skills use standard assembly and progressive disclosure. Selection does
not call remote tools or load Skill bodies. Metadata is discoverable first;
ToolSearch/Skill loading exposes bodies on demand. Standard MCP grant ownership
is shared across Surfaces using the same Host/workspace/server configuration.
There is no Bot-specific MCP permission owner. Status reads do not activate
Runtime, start servers or scan new directories. Failed selected roots/servers
remain visible rather than being silently replaced by ambient capabilities.

## Sources, children and approvals

Prompt sources are `user`, `application_summary`, `external_material` and
`authorized_background`. Collected user input is user authority; summaries and
external material are not. Core records background authorization, while the
Surface owns its timer or scheduler. Create a grant with `source` and
`authorization_operation_id`, then supply `grant_id` only for an authorized
background prompt. Core derives the authorized source from the grant and checks
its Session/principal and revocation. A model cannot mint or widen a grant.

Native participants/Tasks create independent child Sessions inheriting the
explicit parent profile and pinned native model placement. Durable relations keep
root Session, parent Session, Task and original input source. Child preparation
uses the common configuration command. Child prompts cannot relabel background
work as a fresh user request. Provider and approval waits recheck root lifecycle
and authorization before collaboration effects. Nested delegation remains subject
to the native participant rules.

Approvals retain the exact native Session, handle, run, Turn, canonical item and
request IDs and original option IDs. A Boolean is not a substitute for native
choices. Reviewer readiness is observational and creation-bound. Only the first
valid decision for the current approval head settles it; other Surfaces observe
the same settlement. Provider call IDs may repeat and are not canonical item IDs.

## Callback effects and content

A callback binds principal, Session, Turn, canonical item, provider call ID,
configuration revision, tools version and original source. Its opaque `id` is the
HTTP receipt address. Preserve it and the exact request before claiming.

1. Read/wait for calls and record the original native identity.
2. Claim once; only a successful claim authorizes a new business effect.
3. Execute through the tool's durable effect owner.
4. Submit the exact succeeded, failed or unknown result.

A lost claim response stays uncertain. A query saying `claimed` is not another
grant. A lost result response permits observing or resending only the same result
bytes; conflicting results fail. Core does not replay unknown external effects or
infer success from prose. Host restart/close cancels abandoned pending calls and
marks claimed calls unknown. Closing a Session seals new claims and execution
while retaining observations and resources. Detaching a UI does not close work.

The default result content is opaque JSON. Tools opting into `result_format:
content-v1` use declared text, image and resource-link blocks and may declare an
output schema for `structuredContent`. Empty structured objects remain present.
Validate size, format, image dimensions, MIME type and resource hash before model
admission. Media capability comes from the selected/frozen model; unknown image
support is not permission to inject an image. See OpenAPI for exact block limits.

`turn_complete: true` is valid only for succeeded results. It records the original
callback result then finishes that Turn before another serial tool effect or model
request. Unexecuted siblings gain no claim. It never moves queued input or grants
a new Session. Failed/unknown results cannot complete a Turn as success.

## Resources

Uploads retain immutable bytes, descriptor, SHA-256, size and original expiry.
The common upload receipt preserves the original descriptor even after expiry;
recovery does not renew it. Reads verify Session/resource identity, size and hash.
Expiry prevents fresh byte reads but does not erase already delivered snapshots.
The existing opaque `application-resource:app-resource-...` URI representation is
stored content, not an Application protocol endpoint or credential namespace.

Native ReadResource/PublishArtifact use the same resource owner and bounded
workspace file handling. Paths and symlinks cannot escape the bound artifact
workspace. The public content response transfers base64 bytes; clients must
verify integrity. Closing a Session retains immutable history/resources. Resource
deletion and arbitrary outside-workspace publication are not supported.

## Persistence compatibility

Store schema 3 has one profile/configuration, callback, resource, grant and common
operation owner. The isolated importer
[`control/internal/storemigration/applicationv3`](../control/internal/storemigration/applicationv3/doc.go)
transactionally imports schema 1/2 data, validates identity/digests, then drops
retired `app_*` tables. It preserves original Session/child/resource/callback IDs,
bytes, provenance and known/unknown outcomes without replay. Schema-1 desired
configuration retains its original native tool selection. No current execution
path reads the retired schema.

Historical connection-scoped operation IDs map deterministically into the common
principal namespace. The importer owns this mapping; clients migrating journals
must preserve original provenance and use the mapped receipt ID for observation.
The inert archive retains original records for audit, not execution authority.
Imported permanent receipts remain permanent. Conflicting or corrupt evidence
aborts the complete transaction rather than inventing completion.

The `app_schema` floor marker rejects old writers after migration. Binary rollback
alone cannot downgrade a Store; restore a complete stopped-Host backup when a
schema-incompatible rollback is required. Remove the importer once the minimum
accepted Store format is 3; archive retention/deletion is an explicit separate
decision. Keep the rejection marker while old binaries may target the directory.
Protocol compatibility, Store readability and Host replacement are separate gates.

## Verification

`make client-protocol-check` verifies generated outputs against the single schema.
Owning tests cover the Store importer, command transaction/recovery, public wire,
real HTTP Host with deterministic providers/MCP, and independent public clients.
The [process interoperability runner](../scripts/control_compatibility/README.md)
checks restart and optional compatible peers without a legacy release baseline.
Fixture/process evidence does not establish native Bot UI or live-account
acceptance. See [Testing](testing.md) and [Release](release.md).
