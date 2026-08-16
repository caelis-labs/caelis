// Package appserver owns the transport-neutral product boundary exposed to
// presentation surfaces. It defines principal-bound clients and services for
// request-scoped commands, Session list and reconnect state, the resumable
// Session feed, approval recovery, and the durable operation ledger. Surfaces
// bind this complete boundary without defining a second command, replay, or
// permission path.
//
// The durable ledger is co-located because its intent-before-effect and
// result-after-effect protocol defines command idempotency. Recoverable
// commands hold one process-local exact-operation gate through receipt
// completion so observational recovery cannot race the active Host creator;
// restart recovery relies on durable intent plus domain receipts. The
// Session feed uses protocol/acp projection and eventstream packages for the shared
// surface-facing Envelope vocabulary; those packages do not own Control
// authorization, state assembly, replay coordination, or broker lifecycle.
package appserver
