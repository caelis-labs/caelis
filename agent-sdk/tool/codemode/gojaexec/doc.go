// Package gojaexec implements a disposable, continuous JavaScript environment.
// Call WorkerMain at the executable's early worker entry point. NewEnvironment
// reexecutes that binary lazily with an empty environment except GOMAXPROCS=1
// and fixed race-detector shutdown settings. Runtime retains one environment
// per active Session and releases it with the host's execution resources.
// Standalone Executor.Execute creates and closes a single-use environment.
//
// Sequential scripts share top-level declarations and assignments, with await
// and return at the script boundary. Every tool entry returns the same Body:
// text() reads all textual/JSON entries, json() uses validated structured content
// or parses that entire text, and media holds original opaque references.
// Reading is repeatable. Returning a Body preserves the complete original reply;
// selected values carry only explicitly returned original media identities.
// text() and whole Body returns preserve exact JSON tokens; parsed values use
// JavaScript Number semantics. BigInt must be returned as a string.
//
// There are no default fs, network, shell, environment or module APIs. The worker
// inherits no application handles or credentials. This is capability isolation,
// not an OS sandbox against engine exploits. Goja has no hard per-VM heap limit;
// process RSS is an observation, not a rejection condition or enforcement claim.
package gojaexec
