// Package taskstream exposes Control-owned, Session-authorized Task output
// delivery. Child history recovery and live producer events enter one bounded
// asynchronous recorder and disposable file spool. ACP child Sessions remain
// authoritative; failed replay never falls back to a final answer. Commands
// retain their durable final-result fallback. Opening an exact child origin
// reads bounded append pages without collecting the whole history. Stale child
// cursors require a complete replacement before following the new spool cursor.
package taskstream
