// Package controlclient owns the product-facing Control client: authenticated
// request-scoped commands, Session list and reconnect state, the resumable
// Session feed, approval recovery, and the durable operation ledger. Surfaces
// and transport adapters map onto these contracts without defining a second
// command, replay, or permission path.
//
// The durable ledger is co-located because its intent-before-effect and
// result-after-effect protocol defines command idempotency. The Session feed
// uses protocol/acp projection and eventstream packages for the shared
// surface-facing Envelope vocabulary; those packages do not own Control
// authorization, state assembly, replay coordination, or broker lifecycle.
package controlclient
