# Application Runtime

Caelis applications own their product identity, notes, Memory, business tools and
scheduling. Control owns canonical Sessions, Turns, tool history, native approvals,
sandbox policy and recovery. The SDK has no application or Bot product dependency.
Ordinary CLI/TUI, Workspace Memory, MCP and ACP remain separate capabilities.

## Protocol and authentication

The public HTTP contract is
[`api/control/v1/openapi.json`](../api/control/v1/openapi.json), with generated
[Go wire types](../control/appserver/wirev1/generated/control_v1.gen.go) and
[TypeScript types](../clients/typescript/control-v1.gen.ts). The supported Go HTTP
client is [`control/appserver/httpclient`](../control/appserver/httpclient/application.go).
No sibling repository or private Store access is needed.

`GET /api/control/v1/initialize` returns `api_version: "v1"`, protocol and envelope
versions, `store_id`, `instance_id`, build identity and capabilities. Require
`application-runtime-v1`; do not fall back to an ordinary coding Session if absent.
Feature capabilities refine the baseline: `application-hot-configuration-v1`,
`application-native-execution-v1`, `application-workspace-binding-v1`,
`application-background-activation-v1` and `application-resource-transfer-v1`.
Require the capability guarding the feature you need instead of probing with
destructive trial calls.
Store identity persists across Host replacement; instance identity does not.

An authenticated Host principal enrolls an application using
`POST /applications/register`. Generate and persist a secret consisting of
`app-client-` followed by 64 cryptographically random lowercase hexadecimal digits
**before** enrollment. Send `operation_id`, `name`, `credential` and matching
`Idempotency-Key`. Identical enrollment returns the original connection; changed
parameters under the same operation conflict. Only the credential hash is stored.
Never send this credential to a model, in query strings, or to a renderer.

Use a separate HTTP client with the application credential as Bearer thereafter.
The server derives principal/application/connection scope from authentication.
Request metadata, names, absolute paths and Session IDs do not grant ownership.
Application credentials cannot enroll other applications, stop the shared Host,
modify Host credentials/settings, or use ordinary Session creation/steering.

The following paths are relative to `/api/control/v1`:

| Method and path | Contract |
| --- | --- |
| `POST /applications/register` | Host-authenticated enrollment |
| `GET /application/connection` | Read connection and lease, including after revocation |
| `POST /application/connection/renew` | Renew the ten-minute tool-host lease; body `{}` |
| `POST /application/connection/revoke` | Permanently revoke; body `{}`; does not stop Host |
| `POST /application/sessions` | Create a Session with a creation-bound explicit profile |
| `GET /application/sessions` | List this exact application connection's bindings |
| `GET /application/sessions/{session_id}` | Read creation-bound profile, ownership and archive state |
| `POST /application/sessions/{session_id}/prompt` | Admit source-typed input |
| `GET /application/sessions/{session_id}/configuration` | Read the latest desired configuration and revision |
| `POST /application/sessions/{session_id}/configuration` | Compare-and-swap update; returns the committed configuration |
| `GET /application/configuration-operations/{operation_id}` | Exact committed configuration update result |
| `GET /application/sessions/{session_id}/background-grants` | List this connection's background grants |
| `POST /application/sessions/{session_id}/background-grants` | Record a background activation grant |
| `GET /application/sessions/{session_id}/background-grants/{grant_id}` | Read one background grant |
| `POST /application/sessions/{session_id}/background-grants/{grant_id}/revoke` | Permanently revoke; body `{}` |
| `POST /application/sessions/{session_id}/archive` | Close canonical admission, retain history and resources |
| `GET /application/operations/{operation_id}` | Permanent mutation intent/receipt |
| `GET /application/sessions/{session_id}/calls` | Durable callback snapshot; `?wait=true` waits for pending calls |
| `GET /application/sessions/{session_id}/calls/{call_id}` | Recover one opaque callback receipt |
| `POST /application/sessions/{session_id}/calls/{call_id}/claim` | Claim effect once, body `{}` |
| `POST /application/sessions/{session_id}/calls/{call_id}/result` | Idempotently submit `outcome` and JSON `content` |
| `POST /application/sessions/{session_id}/resources` | Upload owned immutable bytes |
| `GET /application/sessions/{session_id}/resources/{resource_id}` | Read resource descriptor |
| `GET /application/sessions/{session_id}/resources/{resource_id}/content` | Read descriptor and base64 bytes |

Canonical observation and control use the existing scoped paths:
`GET /sessions/{session_id}/state`, `/reconnect`, `/events`, `/subscribe`,
`POST /sessions/{session_id}/cancel`, and
`POST /sessions/{session_id}/approvals/{approval_request_id}/resolve`.
Reconnect atomically restores state and the existing Envelope feed; `history_turns`
and `history_before` page canonical history. Closing an observation is not Cancel.
Archive is not deletion. A completion sentence is not a native Turn terminal.

Create, prompt, archive, upload, configuration update and background grant
create require matching `Idempotency-Key` and `operation_id`. Existing
`expected_revision` and `expected_configuration_revision` fields use decimal
strings, not JSON numbers. Approval and cancellation retain the exact native
Session/handle/run/turn identity and approval options. Do not convert an
approval into a general Boolean.

## Persistence compatibility

The application Store accepts schema 1 and transactionally upgrades it to schema
2. Existing bindings, Session identities, operation digests, callback receipts and
resource bytes are retained. Each existing binding receives desired configuration
revision 1; schema-1 native Sessions explicitly retain `RunCommand` and `Task`
plus the resource bridge, rather than silently receiving the expanded file-tool
catalog. They can select additional native tools through a configuration update,
provided the selected names do not collide with application callbacks. New
Sessions with omitted `native_tools` use the complete default native set.

The schema-1 reader is owned by `control/application` and remains necessary while
schema 1 is a supported upgrade source. Old creation requests preserve their
original serialized digest and recover their stored receipt without redispatch.
An older schema-1 binary cannot open schema 2. Preserve a complete stopped-Host
Store backup before an upgrade when rollback is required; downgrading a binary
alone does not downgrade stored data. This is independent of the embedded
Memory database migration described in [Release](release.md#embedded-memory-upgrades-and-recovery).
Installing a binary does not upgrade an already-running Host; activation follows
the [managed Host contract](architecture.md#product-host-and-clients).

## Execution profile and dynamic configuration

Example creation body (also available as a
[machine-readable fixture](../api/control/v1/fixtures/application-create.json)):

```json
{
  "operation_id": "create-example-1",
  "profile": {
    "version": "example-role/1",
    "instructions": "Use the example application tool to answer the request.",
    "model": "configured-provider-model-id",
    "tools_version": "example-tools/1",
    "execution": "tools-only",
    "inherit": {
      "cwd_instructions": false,
      "mcp": false,
      "skills": false,
      "workspace_memory": false
    },
    "tools": [{
      "name": "ExampleLookup",
      "description": "Read an application-owned example value.",
      "input_schema": {
        "type": "object",
        "properties": {"key": {"type": "string"}},
        "required": ["key"],
        "additionalProperties": false
      }
    }]
  }
}
```

`model` references Host-managed configuration, never a raw API key. Profiles do not
inherit CWD AGENTS, global MCP, skills, plugins or Workspace Memory. Version 1
rejects any enabled inheritance flag. A resident working directory comes from
authenticated application configuration (`workspace.cwd`); when omitted, Control
allocates the execution directory. No model-selected root is accepted.

A creation profile has two lifetimes:

- **Creation-bound**: `version`, `execution`, `inherit`, `workspace` and
  `permissions` are fixed at Session creation. A configuration update that
  carries them is rejected; changing them requires a new Session.
- **Revisioned desired configuration**: `instructions`, `model`,
  `reasoning_effort`, `service_tier`, `tools_version`, `tools` and
  `native_tools` are read from and updated through
  `GET/POST /application/sessions/{session_id}/configuration`.

### Workspace and permissions (creation-bound)

`workspace.access` names additional absolute directories with mode `read-only`
or `read-write`. Ordinary `workspace-write` execution has broad ambient
filesystem reads, so a `read-only` entry grants no additional write access; the
CWD and explicit `read-write` roots are writable. `permissions.mode` defaults
when omitted to `workspace-write`; `danger-full-access` is an explicit opt-in
that selects the Host without a native sandbox and is accepted only when the
platform's native agent implements it — the Host registers that mode on opt-in,
never silently. `permissions.approval_mode` defaults to `manual`; manual is the
only currently supported mode. Ordinary per-call `require_escalated` requests
still surface Host approval.

### Dynamic configuration update

The update body is [the fixture](../api/control/v1/fixtures/application-configuration-update.json):

```json
{
  "operation_id": "update-example-1",
  "expected_configuration_revision": "1",
  "patch": {"model": "configured-provider-model-id", "instructions": ""}
}
```

Send it with a matching `Idempotency-Key`. Patch semantics:

| Patch field | Absent | Explicit value |
| --- | --- | --- |
| `instructions` | preserve | replaces; `""` clears role instructions |
| `model` | preserve | nonempty Host-managed model reference |
| `reasoning_effort` | preserve | `""` restores the model default effort |
| `service_tier` | preserve | `""` restores the provider default tier; `priority` is the only explicit value and requires model support |
| `tools_version` | preserve | nonempty catalog version |
| `tools` | preserve | replaces the callback catalog; `[]` clears it |
| `native_tools` | preserve (including the default set) | replaces the native selection; `[]` selects no native tools |

Explicit JSON `null` in a patch field is invalid, as are unknown fields —
`workspace` and `permissions` are creation-bound and rejected here. Readback
distinguishes the two catalogs: profile reads omit `tools` when it is empty
(absent and `[]` mean the same empty catalog), while a cleared `native_tools`
selection is echoed as `[]` and omission means the default native set —
`native_tools: null` is invalid at creation too, never a silent default. An
unsupported effort or service-tier combination returns HTTP 400 with code
`unsupported`; the message identifies the model and rejected field/value. An
unconfigured or ambiguous model selector returns HTTP 400 `invalid_argument`.
Selections are never silently ignored or downgraded. These rejections do not
change desired configuration, revision or any issued request. Internal failures
and provider unavailability retain their 5xx classification. A no-op or same-value update commits a durable
operation receipt without creating a new revision; concurrent writers are
ordered by the revision compare-and-swap.

`GET .../configuration` returns `ApplicationConfiguration`: `session_id`,
decimal-string `revision`, the full `profile`, and optional `last_request`
(`revision`, `request_id`, `turn_id`, plus the resolved `model`,
`reasoning_effort`, `service_tier` and `tools_version` selectors when recorded;
pre-upgrade records omit them) marking the latest request admitted across
desired-configuration updates; its own `revision` field identifies the revision
that request actually ran under. The creation `Binding.profile` remains the immutable
creation snapshot; the configuration read is the sole current desired profile.
`GET /application/configuration-operations/{operation_id}` returns the exact
committed result for that operation, including a no-op — never a later mutable
latest configuration. Use it to reconcile a lost update response; the same
operation ID never dispatches twice. The generic
`GET /application/operations/{operation_id}` deliberately rejects
configuration update IDs with `unsupported` so a typed configuration receipt is
never misread as a `CommandResult`; always read configuration update receipts
through the typed route.

Validation rejections occur before the atomic configuration/operation commit and
create no successful operation receipt. A received 400 is a definite rejection;
a lost response or transport failure is not. After a lost response, query the
original operation ID. A 404 means no committed receipt is visible, not proof
that an outstanding request can never commit; retain the original request and ID
when reconciling rather than submitting a different operation.

An accepted update takes effect at the next not-yet-issued model request,
including later tool rounds of the same Turn; already-issued requests complete
against their old snapshot. Distinguish committed-but-not-yet-executed
(POST response revision) from used-by-execution (`last_request.revision`). Hot
updates never touch `workspace` or `permissions`, so they cannot reset user
approval selections.

`tools-only` admits only the declared callback tools; omitting `tools` declares an
empty callback catalog. `workspace-write` adds confined native
filesystem/command tools and resource transfer tools, enforced by the platform's
ordinary native sandbox (for example Seatbelt on macOS) together with the
creation-bound workspace bindings. A platform without a supported native
backend rejects this mode before Session creation and again before activation;
an unsupported native sandbox fails closed, not through an unrestricted
fallback. Native platform acceptance requires more than compilation.

Native commands do not inherit Host environment values or arbitrary installed
toolchains; system runtime access is platform-defined. `workspace-write` is the
ordinary broad-read workspace sandbox: the CWD and explicit `read-write` roots
are writable, and an ordinary per-call `require_escalated` request can seek
explicit Host approval for that call — approval is per-call, not a standing
bypass. `danger-full-access` selects the Host without a native sandbox.
Application-owned callback approval does not authorize a shell command,
arbitrary MCP tool, or external write.

Only the desired-configuration fields above are hot-updatable; `version`,
`execution`, `inherit`, `workspace` and `permissions` stay the immutable
creation binding. Application notes and Memory change through ordinary new tool
results, not by rewriting committed model prefixes or triggering implicit
compaction. Bot embeds its own Memory store and exposes its own tools; Workspace
Memory is not admitted.

## Source and callback authority

Prompt body example:

```json
{"operation_id":"prompt-example-1","source_kind":"user","input":"Look up the example value."}
```

`source_kind` is `user`, `application_summary`, `external_material`, or
`authorized_background` (with `grant_id`, see below). The
application credential is trusted to distinguish actual collected user input from
application evidence. Summaries and external material enter as non-user
communication, not additional authorization. Never relabel model-generated task
instructions as a user request.

### Background activation grants

Core records authorization, not schedules: the application owns any timer or
scheduler, creates a grant once, and activates within it.

- `POST /application/sessions/{session_id}/background-grants` with
  `{operation_id, source, authorization_operation_id}` and a matching
  `Idempotency-Key` returns a `BackgroundGrant`; a repeat with the same
  operation and changed parameters conflicts, an unchanged repeat returns the
  existing grant with its current revocation state.
- `GET .../background-grants` lists this connection's grants; a single grant is
  readable by `{grant_id}`.
- `POST .../background-grants/{grant_id}/revoke` with body `{}` permanently
  revokes; later activations under it are rejected and the response reports
  `revoked: true`.

A grant embeds server-derived `principal_id`, `application_id` and
`connection_id` plus `session_id`, the application-declared `source`, and
`authorization_operation_id`. Activate with `source_kind:
"authorized_background"` and `grant_id` required; `grant_id` is forbidden for
other kinds. Callbacks and history carry `grant_id` together with
`authorized_source`, which Control copies from the durable grant, so replay
distinguishes the authorized source without trusting model-generated text.
These prompts enter history as their own source, distinct from
user input, summaries and external material, and Core never promotes an
`application_summary` or external material into an authorized trigger. Core
holds no cron expressions, reminder lists or scheduling loop.

A callback contains server-bound principal/application/connection, Session,
Turn, canonical item, provider call ID, catalog version, configuration revision
and source operation. Callbacks dispatched under an older
`configuration_revision` keep routing by their original `tools_version`; a
catalog update does not reroute in-flight calls, and the next model request
receives the new catalog.
The callback's opaque `id` is distinct from `call_id`; address HTTP receipt routes
with `id`. Provider call IDs can repeat across Turns. Runtime binds canonical item
identity before dispatch and overwrites caller-supplied execution context; model
arguments and metadata cannot choose this binding.

The application must:

1. Read or wait for pending calls and persist the native intent and request bytes.
2. Claim `id` once. Only a successful claim grants a new effect. A query returning
   `claimed` is not another grant.
3. Execute through its own durable business-effect ledger.
4. Submit `{"outcome":"succeeded","content":{...}}`, `failed`, or `unknown`.
   A byte-equivalent receipt is idempotent; a conflicting receipt is rejected.

A lost claim response remains uncertain and cannot be reclaimed. A lost result
response can be queried or resent with the same result. Core does not promise
exactly-once external effects. It never retries an unknown effect, promotes
unknown to success, or infers success from model text. Runtime stores the canonical
call/result history; the application stores its business receipt.

Disconnecting the UI does nothing to execution or the lease. Tool-host lease
expiry/revocation prevents new claims, cancels unclaimed intents, and leaves
claimed effects unknown. Late results cannot reactivate revoked/cancelled bindings.
Host restart likewise terminalizes abandoned callbacks rather than reissuing them.
An unavailable required callback has no built-in tool fallback.

Explicit renewal may reactivate the **same** expired connection. It atomically
terminalizes expired pending/claimed callbacks before extending the lease; those
receipts never become pending again, and late claims/results remain rejected.
Renewal reauthorizes still-live canonical work: old Turn continuations, pending
native approvals, and a model response that creates a previously uncreated
callback may proceed under the current lease. Expiry is not a permanent generation
fence or implicit Turn cancellation. Cancel canonical work when that behavior is
required. Revocation is permanent, is allowed after expiry, and cannot be renewed.

Create/prompt/archive permanent intent anchors outlive the shared command receipt
retention. Known native results are recorded and remain readable under the original
scope even if the connection expires or is revoked after admission. This completion
does not restore mutation authority or relax late callback rejection.

Archive persists the original committed CloseSession receipt before sealing the
binding, with client-independent, five-second-bounded completion writes. If binding
completion fails, querying the same operation (including after revocation) or
repeating it under a live lease finishes the archive without native redispatch,
including after Host restart. A committed archive response requires the binding to
be sealed. An intent alone, an unknown receipt, or a closed Session without the
original operation's committed receipt cannot prove that archive succeeded; a crash
before that receipt is durable remains unknown.

Repeating an uncertain ID does not dispatch again. Query native state and the same
operation ID; do not invent a new ID as a retry. Application Sessions are not
ordinary workspace resume candidates.

## Resources

Upload `name`, `media_type`, base64 `data`, lowercase SHA-256 `sha256`, and the
operation fields. The limit is 8 MiB per resource. A descriptor has opaque `id`,
`session_id`, `name`, `media_type`, byte `size`, and `sha256`. Ownership is inherited
from the authenticated binding. Immutable bytes live behind the public API, not
in a guessed worker path. Reusing an upload ID with changed bytes conflicts.
There is currently no read-only lookup of a resource descriptor by upload
operation ID. If the upload response and opaque resource ID are both lost, the
resource read routes cannot recover the descriptor by operation ID; the generic
command receipt route does not supply that upload receipt. Retain confirmed
descriptors and unresolved upload intent, and do not guess IDs or retry under
a new ID to conceal an unknown result.

A `workspace-write` model receives:

- `ReadResource({"resource_id":"..."})`: verify and deliver bytes into its
  controlled `.resources` directory; return descriptor and relative path.
- `PublishArtifact({"path":"relative-output.txt","name":"output.txt","media_type":"text/plain"})`:
  snapshot an allowed regular file; return the structured descriptor.

Artifact paths must remain in the bound workspace. Absolute paths, traversal,
symlinks, nonregular files, oversize files and detected copy-time changes fail.
A model's path is not access authority. Read output through the resource content
route; the typed client verifies size and digest. Archive preserves immutable
snapshots. Resource expiry/deletion and paths outside the bound artifact workspace
are not supported; no user file cleanup is performed.

## Error and recovery semantics

HTTP errors retain the existing `error`, `code`, and optional `kind` envelope.
Command responses retain `operation_id`, `outcome`, native `session_id`, target
and resource. Distinguish mutation admission (`accepted`/`committed`) from native
execution completion.

| Category | Meaning / action |
| --- | --- |
| `invalid_argument` | Invalid schema, source, digest, size or conflicting header; correct request before admission |
| `unauthenticated` / `permission_denied` | Invalid credential or ownership; never fall back to a broader credential |
| `not_found` | No record in the authenticated scope |
| `conflict` | ID payload conflict, already-claimed effect, stale native target or revision |
| `failed_precondition` | Lease expired or binding revoked; inspect lifecycle |
| `unsupported` | HTTP 400: unsupported model effort/tier, inheritance `true`, generic operation lookup for a configuration update, or a platform without the requested native capability |
| `unknown_outcome` / command `unknown` | Intent/effect cannot be proven; reconcile without redispatch |

## Public client and isolated Host

The [compilable Go client example](../control/appserver/httpclient/application_example_test.go)
uses only supported public packages. It checks the capability before creation and
queries a lost receipt without redispatch. The [Host HTTP test](../app/gatewayapp/application_host_http_test.go)
exercises the same client through enrollment, callback claim/result, native Turn
observation, resource transfer and restart, using a controlled provider.

To start a disposable Host without using the ordinary Store, credentials or CWD:

```sh
DEMO=$(mktemp -d /tmp/caelis-application.XXXXXX)
mkdir -p "$DEMO/home" "$DEMO/config" "$DEMO/work"
go build -o "$DEMO/caelis" ./cmd/caelis
(
  cd "$DEMO/work"
  env -i PATH="$PATH" HOME="$DEMO/home" XDG_CONFIG_HOME="$DEMO/config" \
    "$DEMO/caelis" serve --store-dir "$DEMO/store" \
    --control-token-file "$DEMO/host.token" --listen 127.0.0.1:7777
)
```

The foreground process stops with Ctrl-C. Keep the temporary directory if its
Sessions or credentials are needed; no command above cleans up an existing Store.
Choose another loopback port if 7777 is occupied. A fresh Host has no configured
model. Enrollment and capability checks need none; creation requires a model
configured in that isolated Host, and prompting can incur provider calls.

For an existing isolated Host, a public HTTP enrollment example is:

```python
import json, os, pathlib, secrets, urllib.request

root = pathlib.Path(os.environ["DEMO"])
base = "http://127.0.0.1:7777/api/control/v1"
# Persist once before dispatch. Do not overwrite this file to retry enrollment.
credential_file = root / "application.token"
fd = os.open(credential_file, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
credential = "app-client-" + secrets.token_hex(32)
with os.fdopen(fd, "w") as output:
    output.write(credential)
body = {"operation_id": "enroll-example-1", "name": "example", "credential": credential}
request = urllib.request.Request(
    base + "/applications/register", json.dumps(body).encode(), method="POST",
    headers={"Authorization": "Bearer " + (root / "host.token").read_text().strip(),
             "Content-Type": "application/json", "Idempotency-Key": body["operation_id"]})
with urllib.request.urlopen(request) as response:
    connection = json.load(response)
print(connection["application_id"], connection["connection_id"])
```

Export `DEMO` to the Python process. After uncertain enrollment, reuse the saved
credential and exact registration body/ID instead of generating another identity.
For creation, use the JSON fixture above with a configured model and matching
`Idempotency-Key`; authenticate with `application.token`, not `host.token`.

## Verification entry points

Use isolated temporary directories, synthetic data and controlled models:

```sh
go test ./control/application ./control/appserver ./control/appserver/httpclient ./app/controlserver
go test ./app/gatewayapp -run 'TestApplication|TestRetired|TestControlHostIdentity' -count=1
go test ./agent-sdk/runtime -run 'Test.*Invocation|Test.*Recovery' -count=1
make client-protocol-check
```

On macOS, the opt-in SDK probes distinguish the ordinary sandbox filesystem
ceiling from the stronger same-user process-credential boundary, which
application native execution does not claim:

```sh
CAELIS_TEST_APPLICATION_NATIVE=1 go test ./agent-sdk/sandbox/seatbelt -run '^TestExplicit(ReadCeiling|ProcessIsolation)Native$' -count=1 -v
```

The public HTTP application path has a separate native acceptance test for file
and command effects, a bound Notebook directory, and resource upload-to-artifact
byte and digest verification:

```sh
CAELIS_TEST_APPLICATION_NATIVE=1 go test ./app/gatewayapp -run '^TestApplicationNativeHTTPB01B02B10$' -count=1 -timeout=3m -v
```

`TestExplicitProcessIsolationNative` is a known failing security probe on current
macOS: it uses only an exact test-owned process with a synthetic environment and
requires positive controls before checking denial. It is not an application
acceptance gate and does not gate `workspace-write` execution, which uses the
ordinary workspace sandbox — a filesystem ceiling, not same-user credential
isolation. Do not cite the filesystem probe as evidence of a credential
boundary, in either direction.

A containing sandbox can reject `sandbox_apply`; run only in an environment
explicitly authorized for native sandbox construction, not with a product bypass.
Protocol tests with a controlled provider do not establish real-model, native
platform or release acceptance. Ordinary product gates are selected from
[Testing](testing.md).

Legacy Bot Mode, its TUI and dedicated APIs/tools/scheduler are not supported.
Historical Bot data is retained, not imported, executed or automatically deleted.
Normal workspace Sessions, credentials and Memory data are not migrated into the
application namespace.
