// Package tui owns the Bubble Tea terminal UI: state management, input
// handling, transcript rendering, command dispatch, completion, theme,
// layout, and visual policy.
//
// This is a Layer 2 (Presentation) package. It imports gateway/, acp/
// types, and model/ types if needed. It must not import session/ stores,
// runner/, sandbox/, policy/, or concrete providers.
//
// Sub-packages:
//   - tui/transcript/     — session record rendering
//   - tui/commands/       — slash command syntax and dispatch
//   - tui/input/          — input and completion
//   - tui/theme/          — theme and colors
//   - tui/controladapter/ — gateway event to TUI control adaptation
//   - tui/tuikit/         — shared TUI primitives
//
// Phase 1: skeleton only. No behavior.
package tui
