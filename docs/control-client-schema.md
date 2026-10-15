# Control client compatibility

TUI and application clients share `api/control/v1/openapi.json`, the Control
HTTP/SSE client and the existing `/api/control/v1` routes. `/initialize` reports
protocol, Envelope and API versions plus capabilities. Executable versions do
not select protocol behavior. Clients check their required capabilities before
using them; an unknown optional capability does not prevent the handshake.

## Additive object fields

Schemas marked `x-caelis-ignore-unknown-fields` admit optional object extensions.
The public decoder ignores unrecognized properties while retaining validation
and defaults for known fields. This covers the shared command request families,
Application configuration patches and receipts, initialization, Session state and
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

A new reader can consume the release initialization response without requiring
new headers or routes. New Hosts keep the release response shape for existing
clients: released strict readers cannot be made tolerant by updating the Host's
schema declaration. New response fields or effectful request options therefore
need an explicit supported capability before they are sent to such peers.

Field tolerance alone does not establish full cross-version compatibility. Live
and replayed display replacement, exact approvals, operation receipts and unknown
effects must retain their semantics. Wire compatibility does not authorize an
older executable to write a newer Store format.

## Reproducible release matrix

The [release interoperability runner](../scripts/control_compatibility/README.md)
builds frozen and current production clients and Hosts. It checks both directions
against isolated Stores, including original mutation receipts, configuration,
callback effects, resources and SSE recovery after Host replacement. The evidence
is source-built binary acceptance; its synthetic provider is not a live-account
or native Bot UI test.
