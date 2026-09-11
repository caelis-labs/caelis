// Package taskstream exposes Control-owned, Session-authorized Task output
// delivery. Child history recovery and live producer events enter one bounded
// asynchronous recorder and disposable file spool. ACP child Sessions remain
// authoritative; failed replay never falls back to a final answer. Commands
// retain their durable final-result fallback. Clients commit complete replacement
// transactions before following their opaque spool cursors.
package taskstream
