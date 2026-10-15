# Control process interoperability

Run `python3 scripts/control_compatibility/run.py` with Go and Python 3 to build
the current Host and public client, execute controlled work, then restart the
Host against the same isolated Store. No production Store, credentials or model
service is used. `--output` selects a new private evidence directory.

An optional `--peer-ref` selects a local Git ref implementing the unified Session
protocol and a compatible Store format. This adds both client/Host directions
and a stopped-Store current/peer round trip. No pre-unification release is a
compatibility baseline; those Stores are covered by the one-time importer tests.
The peer source is extracted with `git archive` (tar is required).

A loopback synthetic Responses provider drives real Host processes and SQLite
Stores. The public probe verifies initialization, common-owner ordinary and
explicit-profile Sessions, configuration CAS and original command receipts,
callback claim/effect/result, immutable resource bytes, background grants and
SSE transcript recovery. Every restart verifies the original identities and
refuses a second callback claim. Source file hashes include uncommitted and new
files, and binary hashes and per-phase evidence remain in the output directory.

This is isolated process evidence with a deterministic provider. It does not
replace native Bot UI, real provider-account or platform acceptance.
