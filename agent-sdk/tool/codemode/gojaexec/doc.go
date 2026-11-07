// Package gojaexec implements the fresh-process JavaScript executor.
// Call WorkerMain at the executable's early
// worker entry point; Execute reexecutes that same binary, with an empty environment
// except GOMAXPROCS=1 and fixed race-detector shutdown settings; it inherits no
// application handles or credentials.
//
// Each Execute has a new heap. Scripts are async function bodies: await
// tools.call(name, object), tools.search/describe, output.text/image, and JSON return.
// Reply JSON numbers use JavaScript Number semantics; reply.raw_json retains the
// exact original Host reply JSON, including integers beyond 2^53-1. BigInt must
// be returned as a string. There are no default fs, network, shell, env or module
// APIs. This is capability isolation, not an OS sandbox against engine exploits.
//
// Goja has no hard per-VM heap limit. Process RSS is an observation, not a hard
// limit or a default rejection condition. No GOMEMLIMIT/RSS polling claim is made.
package gojaexec
