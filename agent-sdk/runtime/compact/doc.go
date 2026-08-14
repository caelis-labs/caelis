// Package compact defines compaction contracts and replay helpers for the
// Agent SDK runtime.
//
// This package owns the public compact-domain contracts migrated from
// ports/compact: usage snapshots, compaction requests/results, compact event
// metadata, and prompt replay helpers. Prompt overlays retain Compact/System
// provenance; provider-role compatibility projection belongs to runtime/chat
// and does not change checkpoint authority.
package compact
