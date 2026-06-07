// Package kernel owns the active turn registry, gateway request
// validation, approval routing, binding state, session replay,
// session-to-gateway projection, and participant/control-plane
// orchestration.
//
// This is a Layer 3 sub-package of gateway/. It may import gateway/,
// runner/, session/, agent/, model/, and policy/. It must not import
// acp/, tui/, or app/.
//
// Phase 1: skeleton only. No behavior.
package kernel
