# Model connections over HTTP

User-facing settings can use the same Host configuration commands as `/connect`,
`/disconnect`, `/model` and `/team`. They require the user's Host credential.
An enrolled application's credential cannot change Host models, credentials or
Agent bindings. All paths below are relative to `/api/control/v1`.

Use the current catalog and configuration revision. Model connection uses
`POST /configuration/connect-model`; selection uses `/configuration/use-model`,
and removal uses `/configuration/delete-model`. Agent bindings and named sets
use the `/agents/*` endpoints in the [OpenAPI contract](../api/control/v1/openapi.json).
These operations update the ordinary Host configuration shared with the TUI.
They do not select a private team for each Worker.

`POST /agents/binding-status?include=eligible_profile_ids` returns the current
profile catalog in `Targets` and each handle's allowed choices in
`eligible_profile_ids`. Resolve those IDs against `Targets`; an empty array
means no eligible model. Without that query selection, the Host omits the new
field so strict v1 clients can keep reading their original response shape.
The Go HTTP client's `AgentBindingStatus` method requests the expanded shape.
An older Host may ignore the query and omit eligibility; do not infer candidates
locally when the field is absent. Refresh the projection after configuration
changes and submit an explicit supported effort with a binding. The Host
revalidates eligibility and the configuration revision on writes.

## Connection presets and model discovery

The native presets are `codex`, `grok`, `deepseek` and `xiaomi` (MiMo).
`magpie` and `ollama` discover models from the selected local gateway.
`custom` offers one protocol choice: Responses, Chat Completions, Anthropic
Messages or System One. External ACP Agent onboarding remains separate.

The TUI `/connect` menu lists these connections directly. Authentication follows
the selected connection. Its Magpie model menu includes chat and judgment models;
model type is confirmed after model selection. Type follows the advertised
protocol rather than an arbitrary user classification. The existing `judgment`
Profile field and role eligibility remain the public contract.

`control/modelconfig` owns connection policy, discovery and assembly. The TUI
and HTTP commands use the same `AssembleConnect` path. Accepted model settings
become the ordinary provider configuration and ModelProfile catalog; Runtime
uses that complete configuration without fetching another model directory.
A failed gateway discovery fails the connection instead of publishing a guessed
catalog. An existing configuration remains readable while its gateway is offline.

### Maintaining native models

[The native catalog](../control/modelcatalog/catalog.json) is the single editable
source for recommendation order, limits, image input, reasoning controls and
request-speed choices. Entries use the connection namespace (`openai-codex`,
`xai`, `deepseek` or `xiaomi`) and the upstream model ID. The most specific model
prefix wins within that namespace. Custom and gateway endpoints do not inherit
metadata from an upstream vendor name.

Edit this JSON directly to add or update a native model; no generator, remote
models.dev feed or overlay is involved. Keep `default_max_output` distinct from
the provider's `max_output` ceiling, and declare only supported reasoning levels.
Run `go test ./control/modelcatalog ./control/modelconfig` after an edit. The
catalog test rejects duplicate entries, unsupported namespaces, missing limits
and default efforts outside the declared set.

Connection assembly pins accepted context, output budget, image input and
reasoning controls. Directory edits affect new connections; reconnect a saved
model to accept refreshed defaults. Explicit model settings and Runtime context
window overrides retain their precedence. The context window is not a compaction
threshold: Runtime subtracts its output reserve and safety margin first.

### Magpie

The default Host-side address is `http://127.0.0.1:3425/v1`. The local preset
requires no user-entered API key; a non-default authenticated gateway requires its
own gateway key. The address is resolved on the Caelis Host, which must be able
to reach the running Magpie instance.

Chat models are discovered through `GET /v1/models` and use the existing
Responses adapter. Preserve the full returned ID, such as `xiaomi/mimo-v2.5` or
`group/daily`, in the request. Magpie owns upstream providers, credentials and
routing. Caelis stores its own endpoint/profile identity separately.

Unified discovery adds `X-Magpie-Deciders: 1`; entries marked `kind: "decision"`
use System One. One connection may select both chat and judgment models, each
retaining its protocol-specific endpoint identity. Custom System One endpoints
remain available through the custom protocol choice.

The judgment command spelling remains `magpie-judgment`; HTTP clients may use
`provider: "magpie"` with `endpoint_id: "judgment"`. These produce an ordinary
judgment ModelProfile and use the existing `/v1/systemone` client; they are not
chat choices in `/model`.

Model labels use the upstream ID, such as `codex/gpt-6-luna`, without a Magpie
prefix. Selection menus show concise route notes to distinguish direct and
gateway connections. Labels never replace public selectors, durable model IDs,
Profile IDs or credential references. Saved connections use the same labels
without contacting the gateway. The original `connect-model:` completion keeps
its chat-only menu for existing clients; `connect-models:` is the unified TUI
menu. Both use the same discovery and assembly owner.

Discovery imports declared context/output limits, image modality and reasoning
levels. Missing metadata remains unknown and the wizard collects advanced
settings. A reasoning model with no declared effort levels exposes no invented
selector and leaves the gateway's reasoning default unchanged. Output defaults
use a conservative budget bounded by the declared
maximum. Chat and judgment endpoints have separate protocol identities.

### Custom and retained configuration

A custom connection selects `custom-responses`, `custom-chat`, `custom-messages`
or `custom-judgment`. Protocol and address participate in endpoint identity,
so the same model ID on two endpoints or protocols remains two distinct profiles.
The endpoint stores its explicit protocol and authentication; model records
store accepted capabilities. Credentials remain in the existing credential store.

Retired providers remain usable and removable through `/disconnect` and the Host
delete command, but cannot create new connections. Control reads their saved
protocol, address, credentials and model settings using a small configuration
adapter. Omitted fields use protocol defaults or current native metadata; exact
historical model limits and recommendations are not maintained. Gemini's native
protocol retains image input without depending on a vendor namespace lookup.
Loading preserves IDs, credential references and bindings; the next ordinary
save persists the resolved fields. Remove this adapter when the minimum supported
upgrade source persists explicit endpoint and capability fields. SDK wire
adapters remain usable by saved configurations and independent SDK consumers.

## Interactive provider authentication

Hosts advertising `model-auth-stream-v1` accept `Accept: text/event-stream` on
`POST /configuration/connect-model`. The JSON body, `Idempotency-Key` and
`If-Match` remain identical to the ordinary command. Without that Accept value,
the response remains the synchronous JSON command result.

The stream emits `event: model_authentication` with a complete
`ModelAuthenticationSnapshot` as JSON in `data`. `operation_id` identifies the
original command; `sequence` is a decimal uint64 string that increases within
this connection. Snapshots
coalesce progress, so gaps in sequence are normal. There is no replay cursor.
This stream presents one interactive request; it does not subscribe to or replace
any Session or Worker observation connection.

`phase` reports `starting`, provider progress such as `opening_browser`,
`waiting_for_browser`, `requesting_device_code`, `waiting_for_device` or
`authenticated`, and finally `finished`. Treat provider authentication and
configuration commit separately: only the terminal `result.outcome` describes
the configuration command's outcome. HTTP 200 alone does not imply success.
`result.revision`, like every Control uint64 revision, is a decimal JSON string.

`verification_url` and `user_code` are for the initiating user. The normal
provider flow still owns browser launch, loopback callbacks, device login and
token validation. If `challenge_id` and `prompt` appear, collect the requested
value without storing it in history and submit:

```http
POST /api/control/v1/configuration/operations/CONNECT_OPERATION_ID/auth-input
Authorization: Bearer HOST_TOKEN
Content-Type: application/json

{"challenge_id":"CURRENT_CHALLENGE_ID","input":"USER_SUPPLIED_RESPONSE"}
```

This endpoint consumes one live challenge rather than creating a command. It
does not take a new operation ID, revision or idempotency header. The principal
must match the original command. Exactly one competing response can succeed;
an expired, canceled, already consumed or foreign challenge returns 409.
An application principal is rejected with 403. Input is limited to 16 KiB of
UTF-8 bytes, is never echoed, and does not enter the command ledger, Session
history or diagnostics. A 200 response acknowledges delivery, not authentication
success. Loopback completion can invalidate a manual input challenge first.

Closing the HTTP response cancels the interactive request. A missing terminal
snapshot leaves the caller's outcome unknown: credentials or configuration may
already have changed. Keep the exact original request and operation ID. An
identical submission uses the normal command ledger to recover a retained result
or return unknown; it does not resume the old stream or its challenge. Never
automatically retry with a new ID, changed request or updated revision. Concurrent
streams for the same principal and operation receive 409 until the current
interactive request closes. After the ledger's retention window, an old ID is
not a recovery mechanism; reconcile current Host configuration instead of
submitting an uncertain operation again.

## External Agents

Use the catalog's launcher choices: `hosted` for a Caelis-managed built-in
adapter, `installed` for a provider's local runtime, and `command` for a custom
ACP command. The catalog and preparation response supply provider-specific setup,
authentication methods, model choices and configuration options. Legacy launcher
values remain readable but should not be offered as new choices.

The ACP prepare, authenticate and connect commands remain separate from model
provider OAuth. In particular, Antigravity runtime preparation and installation
follow its advertised setup steps. See [External ACP Agents](external-acp-agents.md)
for the existing preparation, authentication, terminal-login and disconnect
contracts. Clients must not infer that an arbitrary Agent supports browser or
terminal authentication from its display name.
