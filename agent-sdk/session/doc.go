// Package session defines session persistence, event, and replay contracts for
// the Agent SDK.
//
// This package owns the public session-domain contracts. The in-memory store
// implementation lives in
// agent-sdk/session/memory; file-backed storage lives in
// agent-sdk/session/file. ProtocolUpdate and related Protocol types are the
// normalized ACP-compatible semantic owner shared by built-in and external
// Agents; product wire schemas and codecs must depend on these contracts, not
// the reverse.
package session
