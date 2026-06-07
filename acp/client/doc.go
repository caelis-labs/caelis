// Package client implements a JSON-RPC 2.0 client for ACP agents.
//
// It manages stdio transport, session lifecycle, incoming request
// dispatch, and notification handling. The client is designed to be
// used by Gateway or test harnesses to communicate with external
// ACP agents.
//
// Key features:
//   - JSON-RPC 2.0 over stdio with unbounded line reading
//   - Supports both number and string IDs
//   - Incoming requests dispatched in goroutines (non-blocking)
//   - Typed lifecycle, terminal, and filesystem methods
//   - session/update notification parsing into acp.Update values
package client
