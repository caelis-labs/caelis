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
