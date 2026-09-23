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
| `POST /application/sessions` | Create a Session with an immutable explicit profile |
| `GET /application/sessions` | List this exact application connection's bindings |
| `GET /application/sessions/{session_id}` | Read immutable profile, ownership and archive state |
| `POST /application/sessions/{session_id}/prompt` | Admit source-typed input |
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

Create, prompt, archive and upload require matching `Idempotency-Key` and
`operation_id`. Existing `expected_revision` fields use decimal strings, not JSON
numbers. Approval and cancellation retain the exact native Session/handle/run/turn
identity and approval options. Do not convert an approval into a general Boolean.

## Explicit execution profile

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
rejects any enabled inheritance flag. No model-selected or client-provided Host
root is accepted. Control allocates the execution directory.

`tools-only` admits only the declared callback tools; omitting `tools` declares an
empty callback catalog. `workspace-write` adds
confined native filesystem/command tools and resource transfer tools, but requires
Linux with a native backend enforcing the explicit read ceiling. Other platforms
reject this mode before Session creation and again before activation. Linux native
execution still requires platform acceptance; compilation alone is not that evidence.

macOS application native execution is unsupported: Seatbelt filesystem isolation
does not prevent direct same-user process environment access through numeric
`KERN_PROCARGS2`, even with an explicit named `sysctl` deny. A filesystem-only
success is not a credential boundary. This restriction does not change ordinary
CLI/TUI sandbox behavior or application `tools-only` execution.

Native commands do not inherit Host environment values or arbitrary installed
toolchains; system runtime access is platform-defined. Execution has a mandatory
filesystem/network ceiling; an approval cannot grant Host execution or remove it.
Application-owned callback approval does not authorize a shell command, arbitrary
MCP tool, or external write. An unsupported native sandbox fails closed, not
through an unrestricted fallback.

The profile and catalog versions are immutable for a Session. A changed profile
requires a new Session in version 1; in-place profile upgrade is unsupported.
Application notes and Memory change through ordinary new tool results, not by
rewriting committed model prefixes or triggering implicit compaction. Bot embeds
its own Memory store and exposes its own tools; Workspace Memory is not admitted.

## Source and callback authority

Prompt body example:

```json
{"operation_id":"prompt-example-1","source_kind":"user","input":"Look up the example value."}
```

`source_kind` is `user`, `application_summary`, or `external_material`. The
application credential is trusted to distinguish actual collected user input from
application evidence. Summaries and external material enter as non-user
communication, not additional authorization. Background grants/triggers are not
implemented in version 1 and are rejected. Never relabel model-generated task
instructions as a user request.

A callback contains server-bound principal/application/connection, Session,
Turn, canonical item, provider call ID, catalog version and source operation.
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
retention. Repeating an uncertain ID does not dispatch again. Query native state
and the same operation ID; do not invent a new ID as a retry. Application Sessions
are not ordinary workspace resume candidates.

## Resources

Upload `name`, `media_type`, base64 `data`, lowercase SHA-256 `sha256`, and the
operation fields. The limit is 8 MiB per resource. A descriptor has opaque `id`,
`session_id`, `name`, `media_type`, byte `size`, and `sha256`. Ownership is inherited
from the authenticated binding. Immutable bytes live behind the public API, not
in a guessed worker path. Reusing an upload ID with changed bytes conflicts.

A `workspace-write` model receives:

- `ReadResource({"resource_id":"..."})`: verify and deliver bytes into its
  controlled `.resources` directory; return descriptor and relative path.
- `PublishArtifact({"path":"relative-output.txt","name":"output.txt","media_type":"text/plain"})`:
  snapshot an allowed regular file; return the structured descriptor.

Paths must remain in the allocated workspace. Absolute paths, traversal,
symlinks, nonregular files, oversize files and detected copy-time changes fail.
A model's path is not access authority. Read output through the resource content
route; the typed client verifies size and digest. Archive preserves immutable
snapshots. Resource expiry/deletion and arbitrary host-directory adoption are not
supported; no user file cleanup is performed.

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
| `unsupported` | Inheritance, background grant, profile mode or platform not implemented |
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

On macOS, the opt-in SDK probes distinguish the filesystem ceiling from the
stronger process-credential boundary required by application native execution:

```sh
CAELIS_TEST_APPLICATION_NATIVE=1 go test ./agent-sdk/sandbox/seatbelt -run '^TestExplicit(ReadCeiling|ProcessIsolation)Native$' -count=1 -v
```

`TestExplicitProcessIsolationNative` is a known failing security probe on current
macOS: it uses only an exact test-owned process with a synthetic environment and
requires positive controls before checking denial. It is not a passing application
acceptance gate. The product instead rejects application native execution on this
platform. The filesystem probe alone must not be used to enable it.

A containing sandbox can reject `sandbox_apply`; run only in an environment
explicitly authorized for native sandbox construction, not with a product bypass.
Protocol tests with a controlled provider do not establish real-model, native
platform or release acceptance. Ordinary product gates are selected from
[Testing](testing.md).

Legacy Bot Mode, its TUI and dedicated APIs/tools/scheduler are not supported.
Historical Bot data is retained, not imported, executed or automatically deleted.
Normal workspace Sessions, credentials and Memory data are not migrated into the
application namespace.
