// Package controlclient owns authenticated, request-scoped product Control
// commands, their idempotency outcomes, and the durable operation ledger.
// It is transport-neutral; surfaces and transport adapters map onto these
// contracts without defining a second command path.
//
// The durable ledger is co-located because its intent-before-effect and
// result-after-effect protocol defines command idempotency. Session state and
// feed projection remain outside this package.
package controlclient
