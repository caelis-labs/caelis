// Package gojaexec implements a disposable, continuous JavaScript environment.
// Call WorkerMain at the executable's early worker entry point. NewEnvironment
// reexecutes that binary lazily with an empty environment except GOMAXPROCS=1
// and fixed race-detector shutdown settings. Runtime retains one environment
// per active Session and releases it with the host's execution resources.
// Standalone Executor.Execute creates and closes a single-use environment.
//
// Sequential scripts share top-level declarations and assignments, with await
// and return at the script boundary. Successful tool calls return a Body with
// data (explicit structured output), text (joined text blocks), and media
// (original opaque references). Successful search returns plain objects instead.
// Returning a Body directly preserves the original blocks and raw JSON tokens;
// selected values carry only explicitly returned original media identities.
// Decoded values use JavaScript Number semantics. BigInt must be returned as a
// string. Only failed diagnostic Bodies retain the text() and json() readers:
// text() includes raw JSON blocks, and json() parses the whole diagnostic text.
//
// There are no default fs, network, shell, environment or module APIs. The worker
// inherits no application handles or credentials. This is capability isolation,
// not an OS sandbox against engine exploits. Goja has no hard per-VM heap limit;
// process RSS is an observation, not a rejection condition or enforcement claim.
package gojaexec
