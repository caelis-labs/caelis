// Package file provides a durable file-backed runtime store. Canonical session
// events are stored in JSONL logs, session/state documents use atomic rename,
// and compound event/document updates use a fsynced write-ahead transaction
// record recovered before any later read or write. SQLite stores secondary
// session metadata and task control indexes.
package file
