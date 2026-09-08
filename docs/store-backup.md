# Store backup and recovery

Caelis backup is an offline Host lifecycle operation. The CLI first stops the
managed Host, then a newly opened Host closes admission and drains active
producers before it creates the archive:

```text
caelis backup --store-dir <store> --output <archive.zip>
caelis restore --store-dir <store> --input <archive.zip>
caelis upgrade prepare --store-dir <store>
caelis upgrade commit --store-dir <store>
caelis upgrade rollback --store-dir <store>
```

The backup archive uses `caelis.store-backup.v1` and records the source layout
floor, the last writer of that layout, the minimum reader capability, the
Caelis version, the quiesce boundary, each component's size and SHA-256, and
the explicit statement that its components are independent snapshots under one
quiesce boundary. The source floor and last writer are storage facts; the
reader capability is a format contract, not a guessed Caelis release. It does
not claim a cross-database transaction.

The archive contains only the following durable authorities:

| Component | Authority and restore rule |
| --- | --- |
| `config` | Host AppConfig document; an unknown AppConfig schema is rejected by the normal config owner before a writer starts. |
| `control` | Host-owned `control/control.sqlite`; its WAL is checkpointed after quiesce. |
| `session-canonical` | Canonical Session JSONL and documents under `sessions/`; the SDK-owned `.sessions.index.sqlite` is captured as a quiesced lookup cache while canonical documents remain the source of truth, and delivery spool is discarded. A non-empty Session index `-wal`, `-shm`, or `-journal` sidecar rejects the backup until the owner recovers it; Caelis never drops a sidecar and claims success. |
| `memory-owner-snapshot` | A consistent image created by the embedded Memory public owner API; Caelis never opens or copies `memory.db`. |

Provider credentials, Memory management and Steward credentials, Control
tokens and cursor keys, runtime locks, logs, and the disposable Control spool
are excluded. References to excluded credentials remain in configuration only
when they are opaque and safe to recreate; the backup never makes those bytes
portable.

The destination archive must be outside the Store directory. Restore is an
offline operation against an existing or separately provisioned Store owner:
the target must already have the owner credentials required by Memory, because
those credentials are intentionally excluded from the archive. The target
credential must authorize the restored Memory generation; operators must
provision that identity through the Memory owner lifecycle before restore.

Restore stages and verifies every listed component in a private directory. It
uses a `.caelis-restore.json` journal and a rollback directory while replacing
Config, Control, and the complete Session directory. The journal records a
pending rename and component digests before each destructive step, and records
durable per-component rollback completion. If a process stops after a rollback
rename but before the journal update, the next owner-held recovery verifies the
original digest and completes the journal instead of deleting the only
restored copy. Missing or mismatched evidence leaves the recovery journal in
place and refuses uncertain removal. A process interruption can therefore
restore a prior target even when it stopped between two filesystem renames.
The live product Host authority lock is required throughout restore; a free
lock probe is not used as restore authorization.

Memory restore, Memory commit, and Memory rollback remain Memory-owner
operations. Caelis installs its independent components, asks Memory to install
its snapshot as a pending generation, and commits that generation last. If
commit fails, the owner rollback is requested and the Caelis journal restores
the previous Config, Control, and Session state. If finalization is
interrupted, the journal retries the owner commit or retains the recovery
state for an explicit operator retry. A successful restore is complete only
after the Memory owner has accepted its generation and the Caelis journal and
rollback directory have been removed.

`upgrade prepare` performs a read-only Config owner validation, Control owner
schema/integrity/record validation, and canonical Session owner validation of
document, event-log, and transaction schemas. Future versions, malformed JSON,
and unknown Control tables or columns are rejected before the Store journal or
Memory prepare can be written. It then records the stopped Memory generation,
preflight digest, and target writer capability in a Store-owned upgrade journal.
The current writer refuses to open while that journal is pending, so Config
migration and Control/Session writes cannot run before the owner barrier is
committed. The new writer must repeat the same read-only preflight and match
the recorded digest before `upgrade commit`; `upgrade rollback` restores the
owner generation and removes the journal before a writer is admitted. The
journal is understood by writers carrying this contract; pre-contract writers
do not magically parse it, so operators must keep the old process stopped after
prepare. Unknown archive capabilities, future reader capabilities, unknown
component kinds, malformed paths, invalid digests, and unsupported persisted
schemas fail closed before any target component is replaced. Feature flags do
not reverse a durable migration.

The source floor for this contract is `caelis.store-layout.v0`; `v0.51.2` is
the last writer of that source layout. The first release carrying the reader
capability is intentionally left unassigned until the release process records
it. A release that does not implement `caelis.store-backup.v1` must reject the
archive before writing. The migration sequence is: create and verify an
external backup, stop the managed Host, run `upgrade prepare`, install the new
writer, allow its owner-controlled schema checks to pass, and run
`upgrade commit`. If the new writer does not become healthy, stop it and run
`upgrade rollback`; if any independent Config, Control, or Session step fails,
restore the external archive and let Memory rollback remain owner-controlled.
No old and new process may write the Store concurrently, and a feature flag is
never a rollback for a durable schema change.

After a successful restore, opening a Host reads the restored canonical Session
documents and Control operation records. The normal Session admission and
model-context reconstruction paths rebuild the accepted work context; the
backup feature does not introduce a second transcript or memory authority.
