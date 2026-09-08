# Internal Store recovery primitives

Store snapshots are Host-private building blocks for temporary upgrade recovery,
not a user-facing backup, export, import, or machine-migration feature. The CLI
exposes neither archive commands nor manual Store prepare/commit/rollback
commands. The raw and npm updaters do not currently coordinate these primitives;
updating the executable does not imply automatic Store rollback protection.

## Ownership and scope

A recovery point belongs to one stopped Store owner. Its purpose is to protect
state that an upgrade may change, not to maintain a history of user backups.
A lifecycle caller must remove its recovery point after verified success or
verified rollback. Interrupted or uncertain recovery must retain its evidence
and keep writers out until the owning operation is resolved. A timeout is not
permission to delete the only recovery material.

The internal archive records independent component snapshots under one
quiesce boundary; it does not claim a cross-database transaction:

| Component | Owner and snapshot rule |
| --- | --- |
| Config | Host AppConfig document, validated by the config owner. |
| Control | Host-owned `control/control.sqlite`, checkpointed after quiesce. |
| Session | Canonical documents and JSONL under `sessions/`, with the SDK-owned index as a lookup cache. Session index SQLite sidecars require owner recovery before capture. |
| Memory | A consistent image from the embedded Memory owner API. Caelis never opens or copies the live Memory database. |

Credentials, Control tokens and cursor keys, runtime locks, logs, and the
Control spool are excluded. Recovery requires the existing Memory owner
credentials to authorize the restored generation. An archive alone is not a
portable identity or a complete disaster-recovery artifact. Reconstructed model
context comes from canonical Session state, not a second transcript in the
snapshot or the disposable spool.

`Stack.WriteStoreBackup` permanently quiesces its Stack, checkpoints Control,
and delegates Memory capture to its owner. The caller must hold Store ownership
and close the Stack afterwards. The lower-level writer requires an already
quiesced Store. A caller owns the archive destination and its cleanup; the
snapshot writer does not publish to a user-selected path. Writer and reader
share a 1 GiB compressed archive budget and a 512 MiB uncompressed entry budget.
Oversized captures fail before publication; readers reject excess bytes rather
than accepting truncated input. Capture stages the ZIP in a private temporary
file and removes that staging file on return. Restore likewise stages its input
ZIP on disk rather than retaining the compressed archive in memory.

## Restore safety

Restore holds the product Host authority lock throughout recovery. A free lock
probe is never authorization. It stages and verifies the component digests and
Config, Control, and Session schemas before replacing any authority. Target
Control SQLite sidecars require owner recovery; they cannot be left next to a
replacement main database or silently discarded.

Restored files and their directory entries must be durable before Memory commit
and before original data can be removed. The `.caelis-restore.json` journal
records each pending rename, original and installed digests, and completed
rollback steps. Re-entry verifies evidence rather than deleting an original
whose rollback rename may already have succeeded. Missing or mismatched
evidence leaves the recovery journal in place and refuses uncertain cleanup.
The Windows directory-sync adapter is currently a no-op; file flushing does not
establish a power-loss-safe directory-rename guarantee on that platform. These
reserved primitives are not a substitute for native-platform recovery evidence.

Memory install, commit, and rollback remain Memory-owner operations. A restore
is complete only after the accepted Memory generation, installed independent
components, and removal of its temporary journal and rollback directory are
confirmed. Failed cleanup is not successful completion.

## Generation barrier

The retained Store upgrade primitive validates Config, Control schema and
records, and canonical Session documents, events, and recovery transactions
without opening a writer. It records their digest, the Memory generation, and
the target writer capability in `.caelis-upgrade.json`. An ordinary Host refuses
to open while that journal is pending. Commit repeats the preflight and requires
the same digest; rollback restores the Memory generation. These primitives do
not migrate or roll back Config, Control, or Session on their own, and do not
restore an executable or npm installation.

An owner commit error retains `committing`: it may follow an irreversible owner
effect and must not advertise rollback availability. Only an idempotent commit
retry can resolve it. A successful terminal operation removes its journal;
unresolved state remains fenced. Writers that predate this contract do not
understand the journal and must remain stopped under external lifecycle control.

The archive capability `caelis.store-backup.v1`, source floor
`caelis.store-layout.v0`, and source last writer `v0.51.2` describe the retained
internal reader contract, not a published portable backup format. Unknown
capabilities, component kinds, unsafe paths, invalid digests, and unsupported
persisted schemas are rejected before installation.
