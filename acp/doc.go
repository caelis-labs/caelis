// Package acp is the ACP protocol core for the Caelis Agent SDK.
//
// It owns:
//   - ACP wire schema types (ContentChunk, ToolCallUpdate, PlanUpdate, etc.)
//   - Session event → ACP projection (ProjectEvent)
//   - External ACP event normalization (NormalizeExternalEvent)
//   - Terminal lifecycle types
//   - Permission request/response types
//   - JSON-RPC client transport (acp/client)
//   - JSON-RPC handler and IO server primitives (Handler, Serve)
//
// Design principles:
//   - Model-critical data lives in canonical session events, never only in _meta
//   - Caelis-specific hints are nested under _meta.caelis
//   - Projection is computed on demand, never stored
//   - Standard ACP wire format uses camelCase JSON tags
//
// This is a Layer 4 (Infrastructure / ACP Protocol) package.
// It imports only session/ and stdlib. It must not import
// gateway/, runner/, tui/, or app/.
//
// Sub-packages:
//   - acp/client/ — JSON-RPC stdio client transport
package acp
