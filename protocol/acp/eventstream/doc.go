// Package eventstream defines the Caelis v1 client event protocol.
//
// Envelope is the stable stream container consumed by TUI, GUI, app-server,
// headless, and compatibility bridges. It carries standard ACP
// session/update and request_permission payloads plus Caelis extension events
// for lifecycle, participant state, approval review, and notices. Usage is a
// standard ACP session/update usage_update; the legacy caelis/usage envelope is
// accepted only as a compatibility input.
//
// This package is a client protocol boundary, not the durable session model.
// Durable replay input is ports/session.Event: model-visible messages live in
// Event.Message and durable tool execution state lives in Event.Tool. ACP
// updates in an Envelope are projections of those canonical facts, or live
// transient trace events when Scope/visibility identify subagent or UI-only
// streams.
//
// Transport mapping rules:
//   - SSE uses Envelope.Cursor as the event id and serializes the full envelope
//     as the event data.
//   - WebSocket transports serialize the full envelope directly.
//   - ACP stdio maps standard payloads to JSON-RPC session/update and
//     session/request_permission messages; Caelis extension events remain
//     extension envelopes on non-stdio streams.
//
// Cursor values are opaque and monotonic only within one stream. Final marks a
// completed semantic projection for the scoped actor/message, while lifecycle
// terminal states close the turn stream. Caelis-specific display hints belong
// under _meta.caelis and must not be the only copy of model-critical data.
package eventstream
