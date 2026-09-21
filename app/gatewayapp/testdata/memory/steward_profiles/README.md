# Frozen historical Steward profiles

Verbatim copies of Memory's immutable released `memory-default` policy
snapshots, from
`github.com/caelis-labs/memory@v0.6.1/sdk/go/memory/stewardworker/testdata/released_profiles`.

`v0.5.2` and `v0.6.0` shipped different prompt content under the same version
`1`; `v0.6.1` keeps the `v0.6.0` prompt but assigns version `2`. These snapshots
are release contracts independent of `BuiltInProfile()`: never edit one, and
allocate a new Memory profile version plus a new snapshot when the policy
changes.

`memory_steward_upgrade_test.go` seeds one of these profiles into a real Host
database through the public owner API to reproduce a consumer that stored an
older built-in policy before upgrading, then checks that the current Host binds
the current version, retains the historical one, drains the old pending Job, and
stays idempotent across restart. This is a consumer profile-upgrade regression,
not a Memory storage/schema-migration test: there is no SQL, no storage
internals, and no hand-written compatibility policy. Upstream owns the real
pre-`v0.6.1` database fixture test.
