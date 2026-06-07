// Package app is the composition root. It assembles the Agent Runtime
// by wiring session, model, tool, sandbox, policy, skill, runner,
// and gateway dependencies.
//
// This is a Layer 3 (Control) package. It imports Layer 4 domain
// packages, gateway/, and gateway/kernel/. It must not import tui/,
// headless/, or acp/server/.
//
// Sub-packages:
//   - app/commands/ — shared command semantics
//
// Phase 1: skeleton only. No behavior.
package app
