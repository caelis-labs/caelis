// Package sandbox owns backend-neutral command execution, filesystem
// access, constraints, descriptors, setup/status, and platform-specific
// enforcement.
//
// This is a Layer 4 (Infrastructure / Agent Core) package. It imports
// only stdlib and platform libs in backends. It must not import session/,
// model/, tool/, agent/, runner/, gateway/, acp/, tui/, or app/.
//
// Sub-packages:
//   - sandbox/host/   — no-sandbox host execution
//   - sandbox/darwin/  — macOS seatbelt
//   - sandbox/linux/   — Linux bubblewrap / Landlock
//   - sandbox/windows/ — Windows restricted-token
//
// Phase 1: types and interfaces only. No behavior.
package sandbox
