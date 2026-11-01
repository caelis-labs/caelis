/*
Package applicationv3 contains the Store v1/v2 startup import and the bounded
schema-3 to schema-4 format fence for native configuration adjuncts.

The caller must hold the Store's exclusive Host ownership and stop all producers
before Run. A successful transaction raises app_schema to 4, which intentionally
prevents older Hosts from reopening the upgraded Store. Binary rollback
therefore cannot imply Store downgrade; an upgrade coordinator must inspect the
format before attempting to restart an older Host.

Original native Session, child, callback, provider-call, resource, grant and
source identities survive. The old credential/connection and Worker-grant owners
are discarded. Immutable original records remain in retired_application_archive
for offline audit only; runtime code must never consult it for admission,
configuration, ownership, recovery or results. Resource bytes have one live owner
and are not duplicated in the archive.

Old operation IDs were connection-local, and uploads had another namespace.
OperationID and ResourceOperationID are the exact deterministic address mapping
used by the Bot state importer. Imported operations have the retired.application
action, so no current dispatcher/reconciler can interpret a missing result as
permission to retry. Where a historical command ledger supplies a result, it is
merged only after checking the original wrapped request digest and exact receipt.
Permanent receipts remain permanent in the common ledger.

The schema-3 transition changes no retained profile or receipt bytes; its marker
prevents old writers from interpreting native configuration as an execution
Profile or ignoring an explicit empty plugin override.

Removal boundary: after the minimum accepted on-disk Store format is v4, delete
this entire package and its startup branches. Reject older formats with an
explicit upgrade/export requirement. Keep the small app_schema downgrade refusal
marker while old binaries can still be pointed at the Store. Archive disposal is
an independent explicit data-retention decision and never a runtime dependency.
*/
package applicationv3
