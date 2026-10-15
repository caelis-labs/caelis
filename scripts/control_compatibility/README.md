# Control release interoperability

Run `python3 scripts/control_compatibility/run.py` from a checkout with Go,
Python 3, tar and the local `v0.71.1` tag. `--baseline` selects a supported local
release ref; `--output` selects a new evidence directory. No production Store,
credentials or model service is used.

The runner freezes release source with `git archive`, builds its Host and the
probe against its production Control client, then builds the current pair. A
loopback synthetic Responses provider drives real Host processes and SQLite
Stores. The probe verifies initialization, ordinary Session creation, APP scope,
configuration CAS and original receipts, callback claim/effect/result, resource
bytes, background grants and SSE transcript recovery.

The old client runs against the new Host before and after restart. The new
client runs against the release Host, upgrades its stopped Store to the new Host,
and reopens the same Store with the release Host. Every restart verifies the
original receipts and refuses a second callback claim. Source IDs, binary hashes,
provider requests and per-phase evidence remain in the output directory.

This is source-built release-client/Host evidence with a deterministic provider.
It does not replace native Bot UI, real provider-account or platform acceptance.
