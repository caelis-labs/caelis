// Package gateway owns the surface-facing service contract: session
// lifecycle, turn lifecycle, active turn submission/cancellation,
// control-plane operations, gateway events, approvals, usage, and event
// metadata.
//
// This is a Layer 3 (Control) package. It imports session/, model/, and
// stdlib. It must not import runner/, tool/, sandbox/, policy/, acp/,
// tui/, or app/.
//
// Sub-packages:
//   - gateway/kernel/ — turn registry, approval routing, projection
//
// Phase 1: types and interfaces only. No behavior.
package gateway
