# Control client compatibility

TUI and Bot clients share `api/control/v1/openapi.json`, the Control
HTTP/SSE client and the existing `/api/control/v1` routes. `/initialize` reports
protocol, Envelope and API versions plus capabilities. Executable versions do
not select protocol behavior. Clients check their required capabilities before
using them; an unknown optional capability does not prevent the handshake.

## Additive object fields

Schemas marked `x-caelis-ignore-unknown-fields` admit optional object extensions.
The public decoder ignores unrecognized properties while retaining validation
and defaults for known fields. This covers the shared command request families,
Session configuration patches and receipts, initialization, Session state and
status, presentation, completion/catalog responses, Task directories and feed
delivery containers, and UI preferences, including their marked nested objects. Other typed formats
retain their declared validation; the marker is not a global relaxation of all
Control objects.

Known revisions remain decimal strings without floating-point conversion.
Absent, false, null and empty collections keep their existing meanings. A patch
cannot use an extension to change a known creation-bound field, including
workspace or permissions. MCP credential/environment fields without a supported
private-secret contract remain rejected. Opaque tool schemas and application
content retain their data; ignoring protocol extensions does not strip keys
inside those payloads or change the selected tool-result format.

The Go generator retains declared fields for objects with this marker. Their
readers may discard unknown properties; these are different from vendor objects
whose additional keys are data and must survive round trips. TypeScript types
permit the marked objects' additional properties without changing declared types.

## Peer versions

The unified Session protocol is the current baseline. Pre-unification Application
routes and credentials are removed; old clients need an update. This breaking
cutover does not discard their durable Stores: the isolated Store importer owns
that migration, separately from current client negotiation.

Compatible versions keep consumed semantics and optional fields additive. New
response fields or effectful options require an explicit capability when an older
compatible peer cannot safely consume them. There is one maintained schema and
no frozen legacy protocol implementation.

Field tolerance alone does not establish full cross-version compatibility. Live
and replayed display replacement, exact approvals, operation receipts and unknown
effects must retain their semantics. Wire compatibility does not authorize an
older executable to write a newer Store format.

## Reproducible release matrix

The [release interoperability runner](../scripts/control_compatibility/README.md)
builds the current production client and Host, with optional compatible peer refs.
The peer mode checks both directions
against isolated Stores, including original mutation receipts, configuration,
callback effects, resources and SSE recovery after Host replacement. The evidence
is source-built binary acceptance; its synthetic provider is not a live-account
or native Bot UI test.

## Managed Host replacement

`host-idle-shutdown-v1` adds `expected_instance_id` and `if_idle` to the existing
`POST /host/shutdown`. The Host atomically rejects new mutation/Runtime admission
only when existing work is idle. A conflict leaves the old Host usable. The
lifecycle manager owns executable selection, exclusive Store ownership, readiness
and rollback; subscriptions never select or start a Host during reconnection.
Explicit `service stop` and `service restart` retain their interrupting semantics.

For the one-time pre-unification upgrade, a Host without this capability cannot
prove conditional shutdown. `service start` reports `CAELIS_HOST_RESTART_REQUIRED`;
finish shared work and explicitly run `caelis service restart --store-dir <Store>`.
This is a lifecycle-only rejection, not an old protocol adapter. Once the minimum
supported installed Host has the capability, that diagnostic branch can be removed.
No timeout silently forces a busy or unknown Host to stop.
