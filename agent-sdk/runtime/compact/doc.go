// Package compact defines compaction contracts and replay helpers for the
// Agent SDK runtime.
//
// It owns usage snapshots, compaction requests and results, compact event
// metadata, and prompt replay helpers. Prompt overlays retain Compact/System
// provenance; provider-role compatibility projection belongs to runtime/chat.
package compact
