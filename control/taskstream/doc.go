// Package taskstream exposes Control-owned, Session-authorized Task output
// delivery. Normalized producer events are recorded in a disposable file
// spool; durable Task results and ACP child Sessions remain authoritative
// fallbacks. A valid cursor prefers exact bytes; cache loss is delivered as an
// explicit bounded replacement transaction rather than making spool
// availability authoritative.
package taskstream
