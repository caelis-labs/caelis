// Package tool owns tool declaration, tool call, tool result, toolset,
// registry, observer, errors, truncation contracts, and pure conversion
// helpers.
//
// This is a Layer 4 (Infrastructure / Agent Core) package. It imports
// model/ and stdlib. It must not import agent/, runner/, gateway/, acp/,
// tui/, or app/.
//
// Sub-packages:
//   - tool/builtin/ — built-in tool implementations
//
// Phase 1: types and interfaces only. No behavior.
package tool
