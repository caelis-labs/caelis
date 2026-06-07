// Package session owns durable session identity, state, events, visibility
// rules, and the model-context reconstruction contract.
//
// This is a Layer 4 (Infrastructure / Agent Core) leaf package. It imports
// only model/ and stdlib. It must not import tool/, agent/, runner/, gateway/,
// tui/, or app/.
//
// Key capabilities:
//   - Typed semantic event payloads (User, Assistant, ToolCall, ToolResult, Plan, etc.)
//   - Visibility rules (canonical, mirror, overlay, ui_only)
//   - Model context reconstruction from durable events
//   - Controller/participant bindings for multi-agent sessions
//   - Structured state storage
//   - File-backed durable store with JSONL event log
//
// ACP projection is owned by acp/ package, not session/.
//
// Sub-packages:
//   - session/file/ — file-backed durable store
package session
