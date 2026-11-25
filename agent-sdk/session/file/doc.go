// Package file provides a durable file-backed runtime store. Canonical session
// events are stored in JSONL logs, session/state documents use atomic rename,
// and compound event/document updates use a fsynced write-ahead transaction
// record. Before each operation the Store finishes recoverable transactions.
// An unreadable transaction stays at its original path and blocks its Session.
// SQLite indexes Session documents by identity and summary and reserves the
// physical addresses of unreadable transactions without a document index row.
// The optional Task store owns durable Task records in the same database.
//
// Forward JSONL paging uses bounded in-memory sequence checkpoints, and active
// append preparation may reuse a bounded decoded-history cache. Both are
// derived accelerators: a seek validates file identity and an anchor record,
// while truncate, rollback, replacement, or an invalid anchor falls back to
// canonical disk reconstruction. On-demand provenance indexes persist sparse
// sequence locations and journal identities beside a selected log. They retain
// no transcript payloads, validate source size/time and an anchor, and rebuild
// after cache loss or invalidation. Consecutive equal provenance coalesces; Run
// and pause records remain individually addressable. Run-state reads decode only
// the selected journal payloads. Cache persistence is best effort, and neither
// listing Sessions nor opening the Store prewarms these indexes.
package file
