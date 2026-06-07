// Package policy owns tool authorization input/output, mode options,
// policy profiles, decisions (allow/deny/approval-request), and metadata
// keys.
//
// This is a Layer 4 (Infrastructure / Agent Core) package. It imports
// tool/, sandbox/, and stdlib. It must not import agent/, runner/,
// gateway/, acp/, tui/, or app/.
//
// Sub-packages:
//   - policy/presets/ — built-in policy profiles
//
// Phase 1: types and interfaces only. No behavior.
package policy
